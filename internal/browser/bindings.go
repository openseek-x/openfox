// Package browser defines the authenticated local browser scope. The center
// stores only binding metadata; it has no page-control or page-proxy endpoint.
package browser

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/membership"
)

var (
	ErrInvalid     = errors.New("browser: invalid request")
	ErrUnavailable = errors.New("browser: binding unavailable")
	ErrConflict    = errors.New("browser: live binding exists")
)

const ClaimTTL = 60 * time.Second
const LeaseTTL = 30 * time.Second

type Binding struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ConversationID string    `json:"conversation_id"`
	OwnerUserID    string    `json:"owner_user_id"`
	DeviceID       string    `json:"device_id"`
	HostInstanceID string    `json:"host_instance_id"`
	HostEpoch      int64     `json:"host_epoch"`
	Revision       int64     `json:"revision"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

type CreateInput struct {
	DeviceID       string `json:"device_id"`
	HostInstanceID string `json:"host_instance_id"`
	HostEpoch      int64  `json:"host_epoch"`
	Replace        bool   `json:"replace"`
}

type CreateResult struct {
	Binding
	ClaimSecret string `json:"claim_secret"`
}

type Identity struct {
	HostInstanceID string `json:"host_instance_id"`
	HostEpoch      int64  `json:"host_epoch"`
	Revision       int64  `json:"revision"`
}

func (i Identity) Valid() bool {
	return membership.ValidateUUID(i.HostInstanceID) && i.HostEpoch > 0 && i.Revision > 0
}

const columns = `id::text,organization_id::text,conversation_id::text,owner_user_id::text,device_id::text,host_instance_id::text,host_epoch,revision,lease_expires_at`

func scan(row pgx.Row) (Binding, error) {
	var b Binding
	err := row.Scan(&b.ID, &b.OrganizationID, &b.ConversationID, &b.OwnerUserID, &b.DeviceID, &b.HostInstanceID, &b.HostEpoch, &b.Revision, &b.LeaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrUnavailable
	}
	return b, err
}

// ownerScope is checked on create, claim and heartbeat. Organization admin
// privileges never substitute for physical device ownership.
func ownerScope(ctx context.Context, tx pgx.Tx, org, conversation, owner, device string) error {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.physical_devices d
 JOIN openfox.device_grants g ON g.device_id=d.id AND g.organization_id=$1::uuid
 WHERE d.id=$2::uuid AND d.owner_user_id=$3::uuid AND d.status='ACTIVE' AND g.status='ACTIVE')`, org, device, owner).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrUnavailable
	}
	_, err = (conversations.Service{}).AuthorizeInvocation(ctx, tx, org, conversation, owner)
	if errors.Is(err, conversations.ErrNotFound) || errors.Is(err, conversations.ErrForbidden) {
		return ErrUnavailable
	}
	return err
}

func Create(ctx context.Context, tx pgx.Tx, org, conversation, owner string, input CreateInput) (CreateResult, error) {
	for _, id := range []string{org, conversation, owner, input.DeviceID, input.HostInstanceID} {
		if !membership.ValidateUUID(id) {
			return CreateResult{}, ErrInvalid
		}
	}
	if input.HostEpoch < 1 {
		return CreateResult{}, ErrInvalid
	}
	if err := ownerScope(ctx, tx, org, conversation, owner, input.DeviceID); err != nil {
		return CreateResult{}, err
	}
	id, err := membership.NewID()
	if err != nil {
		return CreateResult{}, err
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return CreateResult{}, err
	}
	secret := hex.EncodeToString(raw)
	b, err := scan(tx.QueryRow(ctx, `INSERT INTO openfox.browser_bindings AS b
 (organization_id,id,conversation_id,owner_user_id,device_id,host_instance_id,host_epoch,status,claim_hash,lease_expires_at)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7,'PENDING',$8,clock_timestamp()+interval '60 seconds')
 ON CONFLICT(organization_id,conversation_id,owner_user_id,device_id) DO UPDATE
 SET id=EXCLUDED.id,host_instance_id=EXCLUDED.host_instance_id,host_epoch=EXCLUDED.host_epoch,
 revision=b.revision+1,status='PENDING',claim_hash=EXCLUDED.claim_hash,lease_expires_at=EXCLUDED.lease_expires_at,updated_at=clock_timestamp()
 WHERE b.status='REVOKED' OR b.lease_expires_at<=clock_timestamp() OR $9
 RETURNING `+columns, org, id, conversation, owner, input.DeviceID, input.HostInstanceID, input.HostEpoch, digest(secret), input.Replace))
	if errors.Is(err, ErrUnavailable) {
		return CreateResult{}, ErrConflict
	}
	return CreateResult{Binding: b, ClaimSecret: secret}, err
}

func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func Claim(ctx context.Context, tx pgx.Tx, org, device, id, secret string, identity Identity) (Binding, error) {
	if !identity.Valid() || !membership.ValidateUUID(id) || len(secret) != 64 {
		return Binding{}, ErrInvalid
	}
	if _, err := hex.DecodeString(secret); err != nil {
		return Binding{}, ErrInvalid
	}
	b, err := scan(tx.QueryRow(ctx, `UPDATE openfox.browser_bindings SET status='ACTIVE',claim_hash=NULL,
 lease_expires_at=clock_timestamp()+interval '30 seconds',updated_at=clock_timestamp()
 WHERE organization_id=$1::uuid AND device_id=$2::uuid AND id=$3::uuid AND status='PENDING'
 AND claim_hash=$4 AND lease_expires_at>clock_timestamp() AND host_instance_id=$5::uuid AND host_epoch=$6 AND revision=$7
 RETURNING `+columns, org, device, id, digest(secret), identity.HostInstanceID, identity.HostEpoch, identity.Revision))
	if err != nil {
		return Binding{}, err
	}
	if err = ownerScope(ctx, tx, org, b.ConversationID, b.OwnerUserID, device); err != nil {
		return Binding{}, err
	}
	return b, nil
}

func Heartbeat(ctx context.Context, tx pgx.Tx, org, device, id string, identity Identity) (Binding, error) {
	if !identity.Valid() || !membership.ValidateUUID(id) {
		return Binding{}, ErrInvalid
	}
	b, err := scan(tx.QueryRow(ctx, `UPDATE openfox.browser_bindings SET lease_expires_at=clock_timestamp()+interval '30 seconds',updated_at=clock_timestamp()
 WHERE organization_id=$1::uuid AND device_id=$2::uuid AND id=$3::uuid AND status='ACTIVE'
 AND lease_expires_at>clock_timestamp() AND host_instance_id=$4::uuid AND host_epoch=$5 AND revision=$6
 RETURNING `+columns, org, device, id, identity.HostInstanceID, identity.HostEpoch, identity.Revision))
	if err != nil {
		return Binding{}, err
	}
	if err = ownerScope(ctx, tx, org, b.ConversationID, b.OwnerUserID, device); err != nil {
		return Binding{}, err
	}
	return b, nil
}

func Active(ctx context.Context, tx pgx.Tx, org, device, id string, identity Identity) (Binding, error) {
	if !identity.Valid() || !membership.ValidateUUID(id) {
		return Binding{}, ErrInvalid
	}
	return scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM openfox.browser_bindings
 WHERE organization_id=$1::uuid AND device_id=$2::uuid AND id=$3::uuid AND status='ACTIVE'
 AND lease_expires_at>clock_timestamp() AND host_instance_id=$4::uuid AND host_epoch=$5 AND revision=$6 FOR SHARE`, org, device, id, identity.HostInstanceID, identity.HostEpoch, identity.Revision))
}

func Revoke(ctx context.Context, tx pgx.Tx, org, conversation, owner, id string) error {
	if !membership.ValidateUUID(id) || !membership.ValidateUUID(conversation) {
		return ErrInvalid
	}
	result, err := tx.Exec(ctx, `UPDATE openfox.browser_bindings SET status='REVOKED',claim_hash=NULL,revision=revision+1,updated_at=clock_timestamp()
 WHERE organization_id=$1::uuid AND conversation_id=$2::uuid AND owner_user_id=$3::uuid AND id=$4::uuid`, org, conversation, owner, id)
	if err == nil && result.RowsAffected() != 1 {
		return ErrUnavailable
	}
	return err
}
