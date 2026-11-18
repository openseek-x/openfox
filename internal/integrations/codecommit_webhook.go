package integrations

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/codecommit"
)

const CodeCommitEnvelopeLimit = 256 << 10

type CodeCommitBridgeLookup struct{ OrganizationID, ConnectionID, RepositoryID string }
type CodeCommitReceipt struct {
	Accepted  bool `json:"accepted"`
	Duplicate bool `json:"duplicate"`
}
type codeCommitEnvelope struct {
	Version   string    `json:"version"`
	ID        string    `json:"id"`
	Type      string    `json:"detail-type"`
	Source    string    `json:"source"`
	Account   string    `json:"account"`
	Region    string    `json:"region"`
	Time      time.Time `json:"time"`
	Resources []string  `json:"resources"`
	Detail    struct {
		Event             string   `json:"event"`
		RepositoryName    string   `json:"repositoryName"`
		RepositoryID      string   `json:"repositoryId"`
		RepositoryNames   []string `json:"repositoryNames"`
		ReferenceType     string   `json:"referenceType"`
		ReferenceName     string   `json:"referenceName"`
		ReferenceFullName string   `json:"referenceFullName"`
		CommitID          string   `json:"commitId"`
		OldCommitID       string   `json:"oldCommitId"`
		PullRequestID     string   `json:"pullRequestId"`
	} `json:"detail"`
}

func codeCommitText(s string, max int) bool {
	return s != "" && utf8.ValidString(s) && len([]rune(s)) <= max && strings.IndexFunc(s, unicode.IsControl) < 0
}

// Reject ambiguous duplicate keys before the typed decoder. Unknown AWS fields
// remain accepted but are never persisted. The whole envelope is already bounded.
func codeCommitJSON(d *json.Decoder, depth int) error {
	if depth > 16 {
		return ErrInvalidRequest
	}
	t, e := d.Token()
	if e != nil {
		return ErrInvalidRequest
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return ErrInvalidRequest
			}
			s, ok := k.(string)
			if !ok || seen[s] || len(seen) >= 1000 {
				return ErrInvalidRequest
			}
			seen[s] = true
			if e = codeCommitJSON(d, depth+1); e != nil {
				return e
			}
		}
		t, e = d.Token()
		if e != nil || t != json.Delim('}') {
			return ErrInvalidRequest
		}
	case '[':
		n := 0
		for d.More() {
			n++
			if n > 1000 {
				return ErrInvalidRequest
			}
			if e = codeCommitJSON(d, depth+1); e != nil {
				return e
			}
		}
		t, e = d.Token()
		if e != nil || t != json.Delim(']') {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}
func parseCodeCommitEnvelope(body []byte) (codeCommitEnvelope, error) {
	var v codeCommitEnvelope
	if len(body) == 0 || len(body) > CodeCommitEnvelopeLimit || !utf8.Valid(body) {
		return v, ErrInvalidRequest
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if codeCommitJSON(d, 0) != nil {
		return v, ErrInvalidRequest
	}
	if _, e := d.Token(); e != io.EOF {
		return v, ErrInvalidRequest
	}
	if json.Unmarshal(body, &v) != nil || v.Version != "0" || !codeCommitText(v.ID, 128) || v.Source != "aws.codecommit" || !codecommit.ValidAccount(v.Account) || !codecommit.SupportedRegion(v.Region) || v.Time.IsZero() || len(v.Resources) != 1 {
		return v, ErrInvalidRequest
	}
	return v, nil
}
func validateCodeCommitEnvelope(v codeCommitEnvelope, a CodeCommitAccess, r CodeCommitRepository) (kind, key, ref, oldSHA, newSHA string, number *int32, e error) {
	invalid := func() (string, string, string, string, string, *int32, error) {
		return "", "", "", "", "", nil, ErrInvalidRequest
	}
	if v.Account != a.settings.Account || v.Region != a.settings.Region || v.Resources[0] != r.ARN {
		return invalid()
	}
	d := v.Detail
	switch d.Event {
	case "pullRequestCreated", "pullRequestSourceBranchUpdated", "pullRequestStatusChanged", "pullRequestMergeStatusUpdated":
		// AWS's PR event carries repositoryNames, not repositoryId. Stable ID is
		// checked again through GetRepository before any worker business write.
		if v.Type != "CodeCommit Pull Request State Change" || len(d.RepositoryNames) != 1 || d.RepositoryNames[0] != r.Name || (d.RepositoryID != "" && d.RepositoryID != r.RepositoryID) {
			return invalid()
		}
		n, err := codecommit.PullRequestNumber(d.PullRequestID)
		if err != nil {
			return invalid()
		}
		return "PR", d.PullRequestID, "", "", "", &n, nil
	case "referenceCreated", "referenceUpdated", "referenceDeleted":
		if v.Type != "CodeCommit Repository State Change" || d.RepositoryID != r.RepositoryID || d.RepositoryName != r.Name {
			return invalid()
		}
		if d.ReferenceType == "tag" {
			return "IGNORE", "", "", "", "", nil, nil
		}
		if d.ReferenceType != "branch" || !codeCommitText(d.ReferenceName, 256) || d.ReferenceFullName != "refs/heads/"+d.ReferenceName {
			return invalid()
		}
		switch d.Event {
		case "referenceCreated":
			if !codecommit.ValidSHA(d.CommitID) || d.OldCommitID != "" {
				return invalid()
			}
		case "referenceUpdated":
			if !codecommit.ValidSHA(d.CommitID) || !codecommit.ValidSHA(d.OldCommitID) {
				return invalid()
			}
		case "referenceDeleted":
			if d.CommitID != "" || !codecommit.ValidSHA(d.OldCommitID) {
				return invalid()
			}
		}
		return "PUSH", codeCommitDigest(v.ID), d.ReferenceName, d.OldCommitID, d.CommitID, nil, nil
	default:
		return invalid()
	}
}
func (s Service) ReceiveCodeCommit(ctx context.Context, tx pgx.Tx, lookup CodeCommitBridgeLookup, bridge, token string, body []byte) (CodeCommitReceipt, error) {
	var out CodeCommitReceipt
	a, e := s.readCodeCommit(ctx, tx, lookup.OrganizationID, lookup.ConnectionID, true, false)
	if e != nil {
		return out, e
	}
	if a.Status != StatusActive {
		return out, ErrNotFound
	}
	r, e := loadCodeCommitRepository(ctx, tx, a.ID, lookup.RepositoryID, true)
	if e != nil {
		return out, e
	}
	var hash []byte
	if e = tx.QueryRow(ctx, `SELECT bridge_token_hash FROM openfox.integration_codecommit_repositories WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.ID).Scan(&hash); e != nil {
		return out, e
	}
	if !r.Enabled || r.BridgeID != bridge || len(hash) != 32 || len(token) != 43 || subtle.ConstantTimeCompare(hash, TokenHash(token)) != 1 {
		return out, ErrCodeCommitBridgeUnauthorized
	}
	v, e := parseCodeCommitEnvelope(body)
	if e != nil {
		return out, e
	}
	kind, key, ref, oldSHA, newSHA, _, e := validateCodeCommitEnvelope(v, a, r)
	if e != nil {
		return out, e
	}
	fingerprint := codeCommitDigest(string(body))
	deliveryKey := codeCommitDigest(r.ID + ":" + strconv.FormatUint(r.Generation, 10) + ":" + v.ID)
	var previous string
	e = tx.QueryRow(ctx, `SELECT coalesce(payload_sha256,'') FROM openfox.integration_webhook_deliveries WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND delivery_id=$2`, a.ID, deliveryKey).Scan(&previous)
	if e == nil {
		if previous != fingerprint {
			return out, ErrVersionConflict
		}
		return CodeCommitReceipt{Accepted: true, Duplicate: true}, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	status, detail := "QUEUED", "accepted_for_processing"
	if kind == "IGNORE" {
		status, detail = "IGNORED", "unsupported_reference_type"
	}
	// EventBridge timestamps may have only second precision. Accept the
	// activation second; otherwise a valid immediate event could be discarded.
	if v.Time.Before(r.ActivatedAt.Truncate(time.Second)) {
		kind, status, detail = "IGNORE", "IGNORED", "before_activation"
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_webhook_deliveries(organization_id,connection_id,provider,delivery_id,event,action,signature_status,status,detail,payload_sha256) VALUES(openfox_private.current_organization_id(),$1::uuid,'codecommit',$2,$3,'','BRIDGE_BEARER',$4,$5,$6)`, a.ID, deliveryKey, v.Detail.Event, status, detail, fingerprint)
	if e != nil {
		return out, e
	}
	switch kind {
	case "PR":
		e = queueCodeCommitPR(ctx, tx, a, r, key, fingerprint, false)
	case "PUSH":
		e = queueCodeCommitPush(ctx, tx, a, r, key, ref, oldSHA, newSHA, fingerprint)
	}
	if e != nil {
		return out, e
	}

	_, e = tx.Exec(ctx, `UPDATE openfox.integration_codecommit_repositories SET last_event_at=clock_timestamp(),updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.ID)
	return CodeCommitReceipt{Accepted: true}, e
}

var ErrCodeCommitBridgeUnauthorized = errors.New("codecommit: bridge authentication failed")
