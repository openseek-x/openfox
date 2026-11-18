package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/tenancy"
)

const MaxBitbucketWebhookBytes = bitbucket.MaxWebhookBytes

// ReceiveBitbucket verifies and durably queues only routing facts. The caller
// commits before acknowledging. It performs no platform I/O or async send.
func (s Service) ReceiveBitbucket(ctx context.Context, tx pgx.Tx, lookup GitWebhookLookup, token string, header http.Header, body []byte) (GitWebhookOutcome, error) {
	out := GitWebhookOutcome{Found: true, Status: http.StatusBadRequest}
	if e := tenancy.RequireOrganization(ctx, lookup.OrganizationID); e != nil {
		return out, e
	}
	media, _, e := mime.ParseMediaType(header.Get("Content-Type"))
	if e != nil || media != "application/json" || len(body) == 0 || len(body) > MaxBitbucketWebhookBytes {
		return out, nil
	}
	var sealed []byte
	var workspace, status string
	var version, generation, hookGeneration uint64
	e = tx.QueryRow(ctx, `SELECT credentials_sealed,external_id,status,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='bitbucket' AND scope='ORGANIZATION' FOR UPDATE`, lookup.ConnectionID).Scan(&sealed, &workspace, &status, &version)
	if errors.Is(e, pgx.ErrNoRows) {
		return GitWebhookOutcome{}, nil
	}
	if e != nil {
		return out, e
	}
	if status != StatusActive {
		return GitWebhookOutcome{}, nil
	}
	creds, e := s.openCredentials(lookup.OrganizationID, lookup.ConnectionID, sealed)
	if e != nil {
		return out, e
	}
	if creds.WebhookToken == "" || !hmac.Equal([]byte(creds.WebhookToken), []byte(token)) {
		return GitWebhookOutcome{}, nil
	}
	if !bitbucket.VerifySignature(creds.WebhookSecret, body, header.Get("X-Hub-Signature")) {
		out.Status = http.StatusUnauthorized
		return out, nil
	}
	hook, e := bitbucket.UUID(header.Get("X-Hook-UUID"))
	if e != nil {
		return out, nil
	}
	request, e := bitbucket.UUID(header.Get("X-Request-UUID"))
	if e != nil {
		return out, nil
	}
	eventKey := header.Get("X-Event-Key")
	if len(eventKey) == 0 || len(eventKey) > 64 || strings.ContainsAny(eventKey, "\x00\r\n") {
		return out, nil
	}
	idRaw, _ := json.Marshal([]string{lookup.OrganizationID, lookup.ConnectionID, hook, request})
	idHash := sha256.Sum256(idRaw)
	deliveryKey := hex.EncodeToString(idHash[:])
	bodyHash := sha256.Sum256(body)
	var deliveryID string
	var previous []byte
	e = tx.QueryRow(ctx, `SELECT d.id::text,b.payload_hash FROM openfox.integration_webhook_deliveries d JOIN openfox.integration_bitbucket_deliveries b ON (b.organization_id,b.delivery_id)=(d.organization_id,d.id) WHERE d.organization_id=openfox_private.current_organization_id() AND d.connection_id=$1::uuid AND d.delivery_id=$2`, lookup.ConnectionID, deliveryKey).Scan(&deliveryID, &previous)
	if e == nil {
		out.Status = http.StatusOK
		out.Duplicate = true
		if !hmac.Equal(previous, bodyHash[:]) {
			out.Status = http.StatusConflict
			out.Duplicate = false
		}
		return out, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	known := false
	for _, key := range bitbucket.WebhookEvents {
		if eventKey == key {
			known = true
			break
		}
	}
	event := bitbucket.Event{}
	detail := "unsupported_event"
	deliveryState := deliveryIgnored
	var selection string
	var selectionVersion, priorHookGeneration uint64
	var pullRequests, commits bool
	if known {
		event, e = bitbucket.ParseEvent(eventKey, body)
		if e != nil || event.PullRequestID > 2147483647 {
			return out, nil
		}
		if event.WorkspaceID != workspace {
			out.Status = http.StatusForbidden
			return out, nil
		}
		e = tx.QueryRow(ctx, `SELECT credential_generation,hook_generation FROM openfox.integration_bitbucket_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid FOR UPDATE`, lookup.ConnectionID).Scan(&generation, &hookGeneration)
		if e != nil {
			return out, e
		}
		e = tx.QueryRow(ctx, `SELECT id::text,version,pull_requests,commits,hook_generation FROM openfox.integration_bitbucket_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND workspace_id=$2::uuid AND repository_id=$3::uuid AND status='ACTIVE' FOR UPDATE`, lookup.ConnectionID, workspace, event.RepositoryID).Scan(&selection, &selectionVersion, &pullRequests, &commits, &priorHookGeneration)
		if errors.Is(e, pgx.ErrNoRows) {
			detail = "repository_not_selected"
		} else if e != nil {
			return out, e
		} else {
			if _, e = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_repositories SET hook_generation=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, selection, hookGeneration); e != nil {
				return out, e
			}
			// A new hook generation can restore read-only reconciliation, but
			// ordinary events never reset exhausted UNKNOWN attempts.
			if priorHookGeneration != hookGeneration {
				if _, e = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_backlinks SET next_attempt_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND workspace_id=$1::uuid AND repository_id=$2::uuid AND state='UNKNOWN' AND next_attempt_at IS NULL AND lease IS NULL`, workspace, event.RepositoryID); e != nil {
					return out, e
				}
			}
			detail = "setting_disabled"
			if event.Kind == "PUSH" && commits || event.Kind != "PUSH" && pullRequests {
				deliveryState = "QUEUED"
				detail = "queued"
			}
		}
	}
	e = tx.QueryRow(ctx, `INSERT INTO openfox.integration_webhook_deliveries(organization_id,connection_id,provider,delivery_id,event,signature_status,status,detail) VALUES(openfox_private.current_organization_id(),$1::uuid,'bitbucket',$2,$3,'valid',$4,$5) RETURNING id::text`, lookup.ConnectionID, deliveryKey, eventKey, deliveryState, detail).Scan(&deliveryID)
	if e != nil {
		return out, e
	}
	var workID *string
	var requested *uint64
	if deliveryState == "QUEUED" {
		raw, _ := json.Marshal(event)
		resource := event.Kind + ":" + strconv.FormatInt(event.PullRequestID, 10)
		if event.Kind == "COMMENT" {
			resource += ":" + strconv.FormatInt(event.CommentID, 10)
		} else if event.Kind == "PUSH" {
			hash := sha256.Sum256(raw)
			resource = "PUSH:" + hex.EncodeToString(hash[:])
		}
		var id string
		var rev uint64
		e = tx.QueryRow(ctx, `INSERT INTO openfox.integration_bitbucket_work(organization_id,connection_id,selection_id,workspace_id,repository_id,kind,resource_key,payload,connection_version,credential_generation,selection_version,hook_generation)
   VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7::jsonb,$8,$9,$10,$11)
   ON CONFLICT(organization_id,selection_id,resource_key) DO UPDATE SET payload=EXCLUDED.payload,connection_version=EXCLUDED.connection_version,credential_generation=EXCLUDED.credential_generation,selection_version=EXCLUDED.selection_version,hook_generation=EXCLUDED.hook_generation,requested_version=openfox.integration_bitbucket_work.requested_version+1,state=CASE WHEN openfox.integration_bitbucket_work.state='RUNNING' THEN 'RUNNING' ELSE 'PENDING' END,next_attempt_at=clock_timestamp(),attempts=0,last_error='',updated_at=clock_timestamp() RETURNING id::text,requested_version`, lookup.ConnectionID, selection, workspace, event.RepositoryID, event.Kind, resource, raw, version, generation, selectionVersion, hookGeneration).Scan(&id, &rev)
		if e != nil {
			return out, e
		}
		workID, requested = &id, &rev
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_bitbucket_deliveries(organization_id,delivery_id,hook_id,request_id,payload_hash,work_id,requested_version) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6)`, deliveryID, hook, request, bodyHash[:], workID, requested)
	if e != nil {
		return out, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET webhook_last_delivery_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, lookup.ConnectionID)
	out.Status = http.StatusOK
	return out, e
}
