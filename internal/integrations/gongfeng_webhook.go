package integrations

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/gongfeng"
)

type GongfengReceipt struct {
	Accepted  bool `json:"accepted"`
	Duplicate bool `json:"duplicate"`
	Ignored   bool `json:"ignored"`
	Test      bool `json:"test"`
}

// ReceiveGongfeng performs no upstream I/O. Receipt and coalesced business job
// commit together; accepted describes durability, never business completion.
func (s Service) ReceiveGongfeng(ctx context.Context, tx pgx.Tx, lookup GitWebhookLookup, pathToken string, headers http.Header, body []byte) (GongfengReceipt, error) {
	out := GongfengReceipt{}
	a, err := s.loadGongfeng(ctx, tx, lookup.OrganizationID, lookup.ConnectionID, true)
	if err != nil {
		return out, err
	}
	if subtle.ConstantTimeCompare([]byte(pathToken), []byte(a.credentials.WebhookToken)) != 1 {
		return out, gongfeng.ErrAuthentication
	}
	event, test, err := gongfeng.AuthenticateEvent(headers, a.credentials.WebhookSecret)
	if err != nil {
		return out, err
	}
	if test {
		out.Test = true
		out.Accepted = true
		return out, nil
	}
	if a.Status != StatusActive {
		return out, ErrNeedsReauth
	}
	parsed, err := gongfeng.ParseEvent(event, body)
	if err != nil {
		return out, err
	}
	repo, err := scanGongfengRepository(tx.QueryRow(ctx, `SELECT `+gongfengRepoColumns+` FROM openfox.integration_gongfeng_repositories
 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND remote_project_id=$2 AND removed_at IS NULL FOR UPDATE`, a.ID, parsed.ProjectID))
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(body)
	key := strconv.FormatInt(a.CredentialGeneration, 10) + ":" + event + ":" + hex.EncodeToString(sum[:])
	tag, err := tx.Exec(ctx, `INSERT INTO openfox.integration_gongfeng_work(organization_id,connection_id,repository_id,kind,object_key,status,next_attempt_at)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,'RECEIPT',$3,'DONE',NULL)
 ON CONFLICT(organization_id,repository_id,kind,object_key) DO NOTHING`, a.ID, repo.ID, key)
	if err != nil {
		return out, err
	}
	out.Accepted = true
	if tag.RowsAffected() == 0 {
		out.Duplicate = true
		return out, nil
	}
	out.Ignored = parsed.Ignored || parsed.Kind == "MR_REFRESH" && !repo.MREnabled || parsed.Kind == "PUSH" && !repo.CommitEnabled
	if !out.Ignored {
		if err = enqueueGongfengWork(ctx, tx, repo, parsed.Kind, parsed.ObjectKey, parsed.ParentID, parsed.Payload); err != nil {
			return GongfengReceipt{}, err
		}
	}
	tag, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET webhook_last_delivery_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id()
 AND id=$1::uuid AND version=$2 AND status='ACTIVE'`, a.ID, a.Version)
	if err != nil {
		return GongfengReceipt{}, err
	}
	if tag.RowsAffected() != 1 {
		return GongfengReceipt{}, ErrVersionConflict
	}
	return out, nil
}
