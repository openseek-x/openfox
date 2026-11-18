package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/cnb"
	"openfox/internal/integrations/gitapi"
)

var ErrCNBSignature = errors.New("cnb: invalid signature")

type CNBEvent struct {
	Event        string `json:"event"`
	Repository   string `json:"repository"`
	RepositoryID string `json:"repository_id"`
	BuildID      string `json:"build_id"`
	Number       string `json:"number"`
	Before       string `json:"before"`
	Head         string `json:"head"`
	access       CNBAccess
	key          string
	duplicate    bool
}

func (e CNBEvent) IsPush() bool    { return e.Event == "push" }
func (e CNBEvent) Duplicate() bool { return e.duplicate }

func (s Service) PrepareCNBEvent(ctx context.Context, tx pgx.Tx, lookup GitWebhookLookup, token string, header http.Header, body []byte) (CNBEvent, error) {
	a, err := s.loadCNB(ctx, tx, lookup.OrganizationID, lookup.ConnectionID, false)
	if err != nil {
		return CNBEvent{}, err
	}
	if a.Status != StatusActive || a.credentials.WebhookToken == "" || !hmac.Equal([]byte(token), []byte(a.credentials.WebhookToken)) {
		return CNBEvent{}, ErrNotFound
	}
	mac := hmac.New(sha256.New, []byte(a.credentials.WebhookSecret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if a.credentials.WebhookSecret == "" || !hmac.Equal([]byte(header.Get("X-CNB-Signature")), []byte(expected)) {
		return CNBEvent{}, ErrCNBSignature
	}
	var e CNBEvent
	if json.Unmarshal(body, &e) != nil || len(e.BuildID) == 0 || len(e.BuildID) > 255 || !a.Selected(e.RepositoryID, e.Repository) {
		return e, ErrInvalidRequest
	}
	switch e.Event {
	case "pull_request.target", "pull_request.merged":
		n, err := strconv.ParseInt(e.Number, 10, 32)
		if err != nil || n <= 0 {
			return e, ErrInvalidRequest
		}
		e.Number = strconv.FormatInt(n, 10)
	case "push":
		if !cnb.ValidSHA(e.Before) || !cnb.ValidSHA(e.Head) {
			return e, ErrInvalidRequest
		}
	default:
		return e, ErrInvalidRequest
	}
	canonical, _ := json.Marshal([]string{e.RepositoryID, e.Event, e.BuildID, e.Number, e.Before, e.Head})
	hash := sha256.Sum256(canonical)
	e.key = hex.EncodeToString(hash[:])
	e.access = a
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_webhook_deliveries WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND delivery_id=$2 AND status='PROCESSED')`, a.ID, e.key).Scan(&e.duplicate); err != nil {
		return e, err
	}
	return e, nil
}

// HydrateCNBPush performs all provider reads outside the database transaction.
func (e CNBEvent) HydrateCNBPush(ctx context.Context) (cnb.Repository, []cnb.Commit, error) {
	r, err := e.access.Client.Repository(ctx, e.access.credentials.Secret, e.Repository)
	if err != nil {
		return r, nil, err
	}
	if r.ID != e.RepositoryID {
		return r, nil, ErrInvalidRepository
	}
	if strings.Trim(e.Head, "0") == "" {
		return r, nil, nil
	}
	commits, err := e.access.Client.Commits(ctx, e.access.credentials.Secret, e.Repository, e.Before, e.Head)
	return r, commits, err
}

// CompleteCNBEvent rechecks connection version/selection under the first lock.
// PR hydration and backlink posting are owned by the durable SyncWorker phase.
func (s Service) CompleteCNBEvent(ctx context.Context, tx pgx.Tx, org string, e CNBEvent, repo cnb.Repository, commits []cnb.Commit, hydrateErr error) (bool, error) {
	a, err := s.loadCNB(ctx, tx, org, e.access.ID, true)
	if err != nil {
		return false, err
	}
	if a.Status != StatusActive {
		return false, ErrNotFound
	}
	if a.Version != e.access.Version || !a.Selected(e.RepositoryID, e.Repository) {
		return false, ErrVersionConflict
	}
	var deliveryID, status string
	err = tx.QueryRow(ctx, `SELECT id::text,status FROM openfox.integration_webhook_deliveries WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND delivery_id=$2`, a.ID, e.key).Scan(&deliveryID, &status)
	if err == nil && status == deliveryProcessed {
		return true, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if deliveryID == "" {
		err = tx.QueryRow(ctx, `INSERT INTO openfox.integration_webhook_deliveries(organization_id,connection_id,provider,delivery_id,event,action,signature_status,status)
		 VALUES(openfox_private.current_organization_id(),$1::uuid,'cnb',$2,$3,'','valid','PROCESSED') RETURNING id::text`, a.ID, e.key, e.Event).Scan(&deliveryID)
		if err != nil {
			return false, err
		}
	}
	status, detail, linked := deliveryProcessed, "accepted; awaiting PR verification", 0
	if hydrateErr != nil {
		status, detail = deliveryFailed, cnbErrorCode(hydrateErr)
	} else if e.IsPush() {
		if repo.ID != e.RepositoryID || repo.Path != e.Repository || !repo.Valid() {
			return false, ErrInvalidRepository
		}
		values, _, _, err := loadSettingsValues(ctx, tx, ProviderCNB)
		if err != nil {
			return false, err
		}
		gc := &gitContext{organizationID: org, connectionID: a.ID, deliveryID: deliveryID, createdBy: a.CreatedBy, settings: values}
		payload := ghPayload{Repository: ghRepository{FullName: repo.Path, Private: repo.Private()}}
		for _, commit := range commits {
			item := ghCommit{ID: commit.SHA, Message: commit.Commit.Message, URL: cnb.CommitURL(repo.Path, commit.SHA)}
			item.Author.Username = commit.Author.Username
			payload.Commits = append(payload.Commits, item)
		}
		s.Git = ProviderCNB
		status, detail, linked, err = s.handlePush(ctx, tx, gc, payload)
		if err != nil {
			return false, err
		}
	} else {
		number, _ := strconv.Atoi(e.Number)
		// A different connection can wake the same business PR, but cannot replace
		// the current claimant's credentials. Inactive takeover is explicit config.
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_cnb_pr_work(organization_id,connection_id,repository_id,repository_path,number)
		 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4)
		 ON CONFLICT(organization_id,repository_id,number) DO UPDATE SET requested_at=clock_timestamp(),next_check_at=clock_timestamp(),terminal=false,updated_at=clock_timestamp()`, a.ID, e.RepositoryID, e.Repository, number)
		if err != nil {
			return false, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_webhook_deliveries SET status=$2,detail=$3,linked_count=$4 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, deliveryID, status, detail, linked)
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET webhook_last_delivery_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID)
	if err == nil && errors.Is(hydrateErr, gitapi.ErrTokenRevoked) {
		err = s.MarkCNBTokenRevoked(ctx, tx, a.ID, a.Version)
	}
	return false, err
}
