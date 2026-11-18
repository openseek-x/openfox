package integrations

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/azuredevops"
)

// Only envelope IDs enter durable work. Business fields, URLs, identities,
// state and text from a signed webhook are never used as authoritative data.
const AzureWebhookBodyLimit = 2 << 20

type azureEvent struct {
	ID, SubscriptionID, EventType, ResourceVersion, AccountID, ProjectID, RepositoryID, Kind string
	ObjectID                                                                                 int64
}

func parseAzureEvent(body []byte) (azureEvent, error) {
	var out azureEvent
	if len(body) == 0 || len(body) > AzureWebhookBodyLimit {
		return out, ErrInvalidRequest
	}
	var wire struct {
		ID                 string `json:"id"`
		SubscriptionID     string `json:"subscriptionId"`
		EventType          string `json:"eventType"`
		PublisherID        string `json:"publisherId"`
		ResourceVersion    string `json:"resourceVersion"`
		ResourceContainers struct {
			Account struct {
				ID string `json:"id"`
			} `json:"account"`
			Project struct {
				ID string `json:"id"`
			} `json:"project"`
		} `json:"resourceContainers"`
		Resource struct {
			ID            int64 `json:"id"`
			PushID        int64 `json:"pushId"`
			PullRequestID int64 `json:"pullRequestId"`
			Repository    struct {
				ID      string `json:"id"`
				Project struct {
					ID string `json:"id"`
				} `json:"project"`
			} `json:"repository"`
		} `json:"resource"`
	}
	if json.Unmarshal(body, &wire) != nil || !azuredevops.ValidUUID(wire.ID) || wire.SubscriptionID != "" && !azuredevops.ValidUUID(wire.SubscriptionID) || wire.PublisherID != "tfs" || azureEventVersion(wire.EventType) == "" || wire.ResourceVersion != azureEventVersion(wire.EventType) || !azuredevops.ValidUUID(wire.ResourceContainers.Account.ID) || !azuredevops.ValidUUID(wire.ResourceContainers.Project.ID) {
		return out, ErrInvalidRequest
	}
	out = azureEvent{ID: strings.ToLower(wire.ID), SubscriptionID: strings.ToLower(wire.SubscriptionID), EventType: wire.EventType, ResourceVersion: wire.ResourceVersion, AccountID: strings.ToLower(wire.ResourceContainers.Account.ID), ProjectID: strings.ToLower(wire.ResourceContainers.Project.ID)}
	switch wire.EventType {
	case "git.push":
		out.Kind = "PUSH"
		out.ObjectID = wire.Resource.PushID
	case "git.pullrequest.created", "git.pullrequest.updated":
		out.Kind = "PULL_REQUEST"
		out.ObjectID = wire.Resource.PullRequestID
	default:
		out.Kind = "WORK_ITEM"
		out.ObjectID = wire.Resource.ID
	}
	if !azuredevops.ValidObjectID(out.ObjectID) {
		return azureEvent{}, ErrInvalidRequest
	}
	if out.Kind != "WORK_ITEM" {
		if !azuredevops.ValidUUID(wire.Resource.Repository.ID) || !strings.EqualFold(wire.Resource.Repository.Project.ID, out.ProjectID) {
			return azureEvent{}, ErrInvalidRequest
		}
		out.RepositoryID = strings.ToLower(wire.Resource.Repository.ID)
	}
	return out, nil
}
func azureBasicMatches(id, secret, user, password string) bool {
	wantUser, gotUser := sha256.Sum256([]byte(azureHookUsername(id))), sha256.Sum256([]byte(user))
	wantSecret, gotSecret := sha256.Sum256([]byte(secret)), sha256.Sum256([]byte(password))
	return subtle.ConstantTimeCompare(wantUser[:], gotUser[:])&subtle.ConstantTimeCompare(wantSecret[:], gotSecret[:]) == 1
}
func azureEventMatchesHook(event azureEvent, h azureHookRecord, account string) bool {
	return (event.SubscriptionID == "" || event.SubscriptionID == h.SubscriptionID) && event.AccountID == account && event.ProjectID == h.ProjectID && event.RepositoryID == h.RepositoryID && event.EventType == h.EventType && event.ResourceVersion == h.ResourceVersion
}

// Resolve_azure_hook only selects a tenant. Authentication and all current
// fences are repeated inside that tenant transaction before accepting work.
func (s Service) ReceiveAzureHook(ctx context.Context, tx pgx.Tx, org, id string, generation uint64, user, password string, body []byte) (GitWebhookOutcome, error) {
	var h azureHookRecord
	e := readAzureHook(ctx, tx, id, false, &h)
	if errors.Is(e, pgx.ErrNoRows) {
		return GitWebhookOutcome{}, nil
	}
	if e != nil {
		return GitWebhookOutcome{}, e
	}
	if !h.Desired || h.Generation != generation || h.State != "ACTIVE" || len(h.secret) == 0 {
		return GitWebhookOutcome{}, nil
	}
	secret, e := s.open(h.secret, azureHookAAD(org, id))
	if e != nil {
		return GitWebhookOutcome{}, e
	}
	if !azureBasicMatches(id, string(secret), user, password) {
		return GitWebhookOutcome{Found: true, Status: http.StatusUnauthorized}, nil
	}
	access, e := LockAzureAccess(ctx, tx, h.ConnectionID, h.ConnectionVersion, h.GrantGeneration)
	if e != nil {
		if errors.Is(e, ErrNotFound) || errors.Is(e, ErrVersionConflict) {
			return GitWebhookOutcome{}, nil
		}
		return GitWebhookOutcome{}, e
	}
	if e = readAzureHook(ctx, tx, id, true, &h); e != nil {
		return GitWebhookOutcome{}, e
	}
	if !h.Desired || h.Generation != generation || h.RemoteGeneration != generation || h.State != "ACTIVE" || h.ConnectionVersion != access.ConnectionVersion || h.GrantGeneration != access.GrantGeneration {
		return GitWebhookOutcome{}, nil
	}
	event, e := parseAzureEvent(body)
	if e != nil || !azureEventMatchesHook(event, h, access.AccountID) {
		return GitWebhookOutcome{Found: true, Status: http.StatusBadRequest}, nil
	}
	// The provider event UUID is scoped by hook and local generation. A provider
	// delivery to multiple subscriptions must not suppress independent work.
	delivery := id + ":" + strconv.FormatUint(generation, 10) + ":" + event.ID
	var deliveryID string
	e = tx.QueryRow(ctx, `INSERT INTO openfox.integration_webhook_deliveries(organization_id,connection_id,provider,delivery_id,event,signature_status,status) VALUES(openfox_private.current_organization_id(),$1::uuid,'azure_devops',$2,$3,'valid','PROCESSED') ON CONFLICT(organization_id,connection_id,delivery_id) DO NOTHING RETURNING id::text`, h.ConnectionID, delivery, event.EventType).Scan(&deliveryID)
	if errors.Is(e, pgx.ErrNoRows) {
		return GitWebhookOutcome{Found: true, Status: http.StatusOK, Duplicate: true}, nil
	}
	if e != nil {
		return GitWebhookOutcome{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_azure_work(organization_id,connection_id,hook_id,hook_generation,connection_version,grant_generation,account_id,project_id,repository_id,kind,object_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6::uuid,$7::uuid,$8::uuid,$9,$10) ON CONFLICT(organization_id,connection_id,project_id,repository_id,kind,object_id) DO UPDATE SET hook_id=EXCLUDED.hook_id,hook_generation=EXCLUDED.hook_generation,connection_version=EXCLUDED.connection_version,grant_generation=EXCLUDED.grant_generation,state='PENDING',version=openfox.integration_azure_work.version+1,lease=NULL,lease_until=NULL,retry_at=clock_timestamp(),last_error='',requested_at=clock_timestamp(),updated_at=clock_timestamp()`, h.ConnectionID, id, generation, access.ConnectionVersion, access.GrantGeneration, access.AccountID, event.ProjectID, nullableAzureID(event.RepositoryID), event.Kind, event.ObjectID)
	if e != nil {
		return GitWebhookOutcome{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET last_delivery_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	return GitWebhookOutcome{Found: true, Status: http.StatusOK}, e
}
