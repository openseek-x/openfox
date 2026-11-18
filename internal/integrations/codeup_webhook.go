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
	"openfox/internal/integrations/codeup"
	"openfox/internal/integrations/gitapi"
)

var ErrCodeupSignature = errors.New("codeup: invalid signature")

type CodeupEvent struct {
	Event        string
	Repository   string
	RepositoryID string
	Number       string
	Before       string
	Head         string
	Ref          string
	Commits      []codeup.Commit
	Total        int
	Unsupported  bool
	access       CodeupAccess
	key          string
	duplicate    bool
}

func (e CodeupEvent) IsPush() bool    { return e.Event == "Push Hook" }
func (e CodeupEvent) Duplicate() bool { return e.duplicate }
func (s Service) PrepareCodeupEvent(ctx context.Context, tx pgx.Tx, lookup GitWebhookLookup, token string, header http.Header, body []byte) (CodeupEvent, error) {
	a, err := s.loadCodeup(ctx, tx, lookup.OrganizationID, lookup.ConnectionID, false)
	if err != nil {
		return CodeupEvent{}, err
	}
	if a.Status != StatusActive || a.credentials.WebhookToken == "" || !hmac.Equal([]byte(token), []byte(a.credentials.WebhookToken)) {
		return CodeupEvent{}, ErrNotFound
	}
	if a.credentials.WebhookSecret == "" || !hmac.Equal([]byte(header.Get("X-Codeup-Token")), []byte(a.credentials.WebhookSecret)) {
		return CodeupEvent{}, ErrCodeupSignature
	}
	var p struct {
		Kind      string    `json:"object_kind"`
		Version   string    `json:"version"`
		ProjectID codeup.ID `json:"project_id"`
		Before    string    `json:"before"`
		After     string    `json:"after"`
		Ref       string    `json:"ref"`
		Total     int       `json:"total_commits_count"`
		Commits   []struct {
			ID      string `json:"id"`
			Message string `json:"message"`
			URL     string `json:"url"`
			Author  struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"commits"`
		Object struct {
			ProjectID codeup.ID `json:"project_id"`
			TargetID  codeup.ID `json:"target_project_id"`
			LocalID   int       `json:"local_id"`
			IID       int       `json:"iid"`
		} `json:"object_attributes"`
	}
	e := CodeupEvent{Event: header.Get("Codeup-Event"), access: a}
	if json.Unmarshal(body, &p) != nil {
		return e, ErrInvalidRequest
	}
	switch e.Event {
	case "Push Hook":
		if p.Kind != "push" || !strings.HasPrefix(p.Ref, "refs/heads/") || len(p.Ref) > 255 || !codeup.ValidSHA(p.Before) || !codeup.ValidSHA(p.After) {
			return e, ErrInvalidRequest
		}
		e.RepositoryID = string(p.ProjectID)
		e.Before = p.Before
		e.Head = p.After
		e.Ref = p.Ref
		e.Total = p.Total
		for _, v := range p.Commits {
			e.Commits = append(e.Commits, codeup.Commit{SHA: v.ID, Message: v.Message, URL: v.URL, Author: v.Author.Name})
		}
	case "Merge Request Hook":
		if p.Kind != "merge_request" {
			return e, ErrInvalidRequest
		}
		e.RepositoryID = string(p.Object.TargetID)
		if e.RepositoryID == "" {
			e.RepositoryID = string(p.Object.ProjectID)
		}
		if p.Version != "new" {
			e.Unsupported = true
		} else if p.Object.LocalID <= 0 || int64(p.Object.LocalID) > 2147483647 || string(p.Object.ProjectID) != e.RepositoryID {
			return e, ErrInvalidRequest
		}
		e.Number = strconv.Itoa(p.Object.LocalID)
	default:
		return e, codeup.ErrUnsupported
	}
	for _, repo := range a.Repositories {
		if repo.ID == e.RepositoryID {
			e.Repository = repo.Path
		}
	}
	if e.Repository == "" {
		return e, ErrInvalidRepository
	}
	hash := sha256.Sum256(append([]byte(e.Event+"\x00"), body...))
	e.key = hex.EncodeToString(hash[:])
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_webhook_deliveries WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND delivery_id=$2 AND status='PROCESSED')`, a.ID, e.key).Scan(&e.duplicate); err != nil {
		return e, err
	}
	return e, nil
}
func (e CodeupEvent) HydrateCodeupPush(ctx context.Context) (codeup.Repository, []codeup.Commit, error) {
	r, err := e.access.Client.Repository(ctx, e.access.credentials.Secret, e.access.credentials.CompanyID, e.RepositoryID)
	if err != nil {
		return r, nil, err
	}
	if r.Path != e.Repository {
		return r, nil, ErrInvalidRepository
	}
	if strings.Trim(e.Head, "0") == "" {
		return r, nil, nil
	}
	if strings.Trim(e.Before, "0") == "" {
		v, err := e.access.Client.Commit(ctx, e.access.credentials.Secret, e.access.credentials.CompanyID, e.RepositoryID, e.Head)
		if err != nil {
			return r, nil, err
		}
		v.URL = codeup.CommitURL(r.Path, v.SHA)
		return r, []codeup.Commit{v}, nil
	}
	if e.Total < 0 || e.Total > codeup.MaxCommits || len(e.Commits) != e.Total {
		return r, nil, codeup.ErrCommitLimit
	}
	seen := map[string]bool{}
	for _, v := range e.Commits {
		if !codeup.ValidSHA(v.SHA) || seen[strings.ToLower(v.SHA)] || (!codeup.ValidURL(v.URL, r.Path, "/commits/"+v.SHA) && !codeup.ValidURL(v.URL, r.Path, "/commit/"+v.SHA)) {
			return r, nil, codeup.ErrCommitLimit
		}
		seen[strings.ToLower(v.SHA)] = true
	}
	return r, e.Commits, nil
}

// CompleteCodeupEvent rechecks connection version/selection under the first lock.
// PR hydration and backlink posting are owned by the durable SyncWorker phase.
func (s Service) CompleteCodeupEvent(ctx context.Context, tx pgx.Tx, org string, e CodeupEvent, repo codeup.Repository, commits []codeup.Commit, hydrateErr error) (bool, error) {
	a, err := s.loadCodeup(ctx, tx, org, e.access.ID, true)
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
		 VALUES(openfox_private.current_organization_id(),$1::uuid,'codeup',$2,$3,'','valid','PROCESSED') RETURNING id::text`, a.ID, e.key, e.Event).Scan(&deliveryID)
		if err != nil {
			return false, err
		}
	}
	status, detail, linked := deliveryProcessed, "accepted; awaiting PR verification", 0
	if e.Unsupported {
		status, detail = deliveryIgnored, "legacy_merge_request_unsupported"
	} else if hydrateErr != nil {
		status, detail = deliveryFailed, codeupErrorCode(hydrateErr)
	} else if e.IsPush() {
		if string(repo.ID) != e.RepositoryID || repo.Path != e.Repository || !repo.Valid() {
			return false, ErrInvalidRepository
		}
		values, _, _, err := loadSettingsValues(ctx, tx, ProviderCodeup)
		if err != nil {
			return false, err
		}
		gc := &gitContext{organizationID: org, connectionID: a.ID, deliveryID: deliveryID, createdBy: a.CreatedBy, settings: values}
		payload := ghPayload{Ref: e.Ref, Repository: ghRepository{FullName: repo.Path, Private: repo.Private()}}
		for _, commit := range commits {
			item := ghCommit{ID: commit.SHA, Message: commit.Message, URL: commit.URL}
			item.Author.Username = commit.Author
			payload.Commits = append(payload.Commits, item)
		}
		s.Git = ProviderCodeup
		status, detail, linked, err = s.handlePush(ctx, tx, gc, payload)
		if err != nil {
			return false, err
		}
	} else {
		number, _ := strconv.Atoi(e.Number)
		// A different connection can wake the same business PR, but cannot replace
		// the current claimant's credentials. Inactive takeover is explicit config.
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_codeup_mr_work(organization_id,connection_id,cloud_organization_id,repository_id,repository_path,number)
		 VALUES(openfox_private.current_organization_id(),$1::uuid,$5,$2,$3,$4)
		 ON CONFLICT(organization_id,cloud_organization_id,repository_id,number) DO UPDATE SET requested_at=clock_timestamp(),
 next_check_at=CASE WHEN integration_codeup_mr_work.consecutive_failures>=8 THEN NULL
 WHEN integration_codeup_mr_work.last_error_code<>'' AND integration_codeup_mr_work.next_check_at>clock_timestamp() THEN integration_codeup_mr_work.next_check_at ELSE clock_timestamp() END,
 terminal=false,updated_at=clock_timestamp()`, a.ID, e.RepositoryID, e.Repository, number, a.credentials.CompanyID)
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
		err = s.MarkCodeupTokenRevoked(ctx, tx, a.ID, a.Version)
	}
	return false, err
}
