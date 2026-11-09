// Package invitations implements the invitation lifecycle and its database
// transaction boundary.
package invitations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

const (
	DefaultExpiry = 7 * 24 * time.Hour

	StatusPending  Status = "PENDING"
	StatusAccepted Status = "ACCEPTED"
	StatusRevoked  Status = "REVOKED"
	StatusExpired  Status = "EXPIRED"
)

type Status string

var (
	ErrInvalidRequest         = errors.New("invitations: invalid request")
	ErrNotFound               = errors.New("invitations: not found")
	ErrConflict               = errors.New("invitations: conflict")
	ErrForbidden              = errors.New("invitations: action forbidden")
	ErrAuthenticationRequired = errors.New("invitations: authentication required")
	ErrEmailMismatch          = errors.New("invitations: email mismatch")
	ErrPasswordRequired       = errors.New("invitations: password required")
	ErrParameterConflict      = errors.New("invitations: parameter conflict")
	ErrAlreadyMember          = errors.New("invitations: already a member")
	ErrCooldown               = errors.New("invitations: resend cooldown")
	ErrSecretsUnavailable     = errors.New("invitations: secrets key is not configured")
)

type Invitation struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	Email          string     `json:"email"`
	Role           string     `json:"role"`
	Status         Status     `json:"status"`
	IssuedAt       time.Time  `json:"issued_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	ConsumedAt     *time.Time `json:"consumed_at,omitempty"`
	// SpaceIDs 是接受后加入的空间；DeliveryStatus 为 PENDING / SENT / FAILED。
	SpaceIDs       []string   `json:"space_ids"`
	DeliveryStatus string     `json:"delivery_status"`
	LastSentAt     *time.Time `json:"last_sent_at"`
}

const invitationColumns = `id::text, organization_id::text, normalized_email, role, status,
	issued_at, expires_at, consumed_at, space_ids::text[], delivery_status, last_sent_at`

func (i *Invitation) scanTargets() []any {
	return []any{&i.ID, &i.OrganizationID, &i.Email, &i.Role, &i.Status, &i.IssuedAt, &i.ExpiresAt,
		&i.ConsumedAt, &i.SpaceIDs, &i.DeliveryStatus, &i.LastSentAt}
}

type CreateInput struct {
	Email    string
	Role     string
	SpaceIDs []string
}

// Delivery 是发送邀请邮件所需的信息（链接令牌只在这里出现，不进入 HTTP 响应）。
type Delivery struct {
	Token            string
	OrganizationName string
	Locale           string
	InviterEmail     string
}

type Acceptance struct {
	UserID         string `json:"user_id"`
	OrganizationID string `json:"organization_id"`
	Role           string `json:"role"`
	Status         Status `json:"status"`
}

type Service struct {
	Pool           *postgres.Pool
	PasswordHasher identity.Argon2idHasher
}

func NormalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func NewToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate invitation token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

func HashToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

func (s Service) Create(ctx context.Context, selection tenancy.OrganizationSelection, actorRole string, input CreateInput) (Invitation, Delivery, error) {
	if s.Pool == nil {
		return Invitation{}, Delivery{}, postgres.ErrNilPool
	}
	email, role, err := validateCreateInput(input)
	if err != nil {
		return Invitation{}, Delivery{}, err
	}
	spaceIDs, err := normalizeSpaceIDs(input.SpaceIDs)
	if err != nil {
		return Invitation{}, Delivery{}, err
	}
	if !canManageRole(actorRole, role) {
		return Invitation{}, Delivery{}, ErrForbidden
	}
	token, tokenHash, err := NewToken()
	if err != nil {
		return Invitation{}, Delivery{}, err
	}

	var invitation Invitation
	delivery := Delivery{Token: token}
	err = s.runForOrganization(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkInviteSpaces(ctx, tx, actorRole, spaceIDs); err != nil {
			return err
		}
		var member bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.organization_members m JOIN openfox.users u ON u.id = m.user_id
			 WHERE m.organization_id = $1::uuid AND u.normalized_email = $2 AND m.status = 'ACTIVE')`,
			selection.OrganizationID, email).Scan(&member); err != nil {
			return err
		}
		if member {
			return ErrAlreadyMember
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.invitations
			   SET status = 'EXPIRED'
			 WHERE organization_id = $1::uuid
			   AND normalized_email = $2
			   AND status = 'PENDING'
			   AND expires_at <= clock_timestamp()
		`, selection.OrganizationID, email); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO openfox.invitations (
				organization_id, normalized_email, role, created_by, token_hash, space_ids
			) VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6::uuid[])
			ON CONFLICT (organization_id, normalized_email) WHERE status = 'PENDING'
			DO NOTHING
			RETURNING `+invitationColumns+`
		`, selection.OrganizationID, email, role, selection.ActorID, tokenHash, spaceIDs).Scan(invitation.scanTargets()...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if delivery, err = loadDelivery(ctx, tx, selection, token); err != nil {
			return err
		}
		return recordAudit(ctx, tx, selection.OrganizationID, selection.ActorID,
			"invitation.created", invitation.ID, selection.AuthorizationVersion,
			map[string]string{"status": string(invitation.Status), "grant_scope": "role:" + invitation.Role})
	})
	if err != nil {
		return Invitation{}, Delivery{}, normalizeDatabaseError(err)
	}
	return invitation, delivery, nil
}

// ResendCooldown 是同一邀请两次发送之间的最短间隔。
const ResendCooldown = time.Minute

// Resend 为待接受的邀请换一个新令牌（旧邮件里的链接失效）并顺延有效期；冷却期内返回 ErrCooldown。
func (s Service) Resend(ctx context.Context, selection tenancy.OrganizationSelection, actorRole, invitationID string) (Invitation, Delivery, error) {
	if s.Pool == nil {
		return Invitation{}, Delivery{}, postgres.ErrNilPool
	}
	token, tokenHash, err := NewToken()
	if err != nil {
		return Invitation{}, Delivery{}, err
	}
	var invitation Invitation
	var delivery Delivery
	var expired bool
	err = s.runForOrganization(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		var cooling bool
		err := tx.QueryRow(ctx, `
			SELECT `+invitationColumns+`, expires_at <= clock_timestamp(),
			       COALESCE(last_sent_at > clock_timestamp() - $3::integer * interval '1 second', false)
			  FROM openfox.invitations
			 WHERE organization_id = $1::uuid AND id = $2::uuid
			 FOR UPDATE
		`, selection.OrganizationID, invitationID, int(ResendCooldown.Seconds())).Scan(
			append(invitation.scanTargets(), &expired, &cooling)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if invitation.Status != StatusPending {
			return ErrConflict
		}
		if expired {
			return expirePending(ctx, tx, selection.OrganizationID, actorRole, invitation)
		}
		if !canManageRole(actorRole, invitation.Role) {
			return ErrForbidden
		}
		if cooling {
			return ErrCooldown
		}
		if err := tx.QueryRow(ctx, `
			UPDATE openfox.invitations
			   SET token_hash = $3, expires_at = clock_timestamp() + interval '7 days', delivery_status = 'PENDING'
			 WHERE organization_id = $1::uuid AND id = $2::uuid
			RETURNING `+invitationColumns+`
		`, selection.OrganizationID, invitation.ID, tokenHash).Scan(invitation.scanTargets()...); err != nil {
			return err
		}
		if delivery, err = loadDelivery(ctx, tx, selection, token); err != nil {
			return err
		}
		return recordAudit(ctx, tx, selection.OrganizationID, selection.ActorID,
			"invitation.resent", invitation.ID, selection.AuthorizationVersion,
			map[string]string{"status": string(invitation.Status)})
	})
	if err != nil {
		return Invitation{}, Delivery{}, normalizeDatabaseError(err)
	}
	if expired {
		return Invitation{}, Delivery{}, ErrConflict
	}
	return invitation, delivery, nil
}

// RecordDelivery 记录一次发送尝试的结果（SENT / FAILED）与时间；邀请已不再待接受时不改动。
func (s Service) RecordDelivery(ctx context.Context, selection tenancy.OrganizationSelection, invitation *Invitation, sent bool) error {
	status := "FAILED"
	if sent {
		status = "SENT"
	}
	return s.runForOrganization(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE openfox.invitations
			   SET delivery_status = $3, last_sent_at = clock_timestamp()
			 WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'PENDING'
			RETURNING `+invitationColumns+`
		`, selection.OrganizationID, invitation.ID, status).Scan(invitation.scanTargets()...)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
}

func loadDelivery(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, token string) (Delivery, error) {
	delivery := Delivery{Token: token}
	err := tx.QueryRow(ctx, `
		SELECT o.name, o.default_locale, u.email
		  FROM openfox.organizations o, openfox.users u
		 WHERE o.id = $1::uuid AND u.id = $2::uuid`, selection.OrganizationID, selection.ActorID).Scan(
		&delivery.OrganizationName, &delivery.Locale, &delivery.InviterEmail)
	return delivery, err
}

// MaxInviteSpaces 是一次邀请可选的空间上限（与迁移 000037 的约束一致）。
const MaxInviteSpaces = 50

func normalizeSpaceIDs(ids []string) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if !validUUID(id) {
			return nil, ErrInvalidRequest
		}
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	if len(result) > MaxInviteSpaces {
		return nil, ErrInvalidRequest
	}
	return result, nil
}

// checkInviteSpaces 要求所选空间都是活跃的、且邀请人自己能读到（不能把人加进自己看不到的私密空间）。
func checkInviteSpaces(ctx context.Context, tx pgx.Tx, actorRole string, spaceIDs []string) error {
	if len(spaceIDs) == 0 {
		return nil
	}
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.spaces s
		 WHERE s.organization_id = openfox_private.current_organization_id() AND s.id = ANY($2::uuid[])
		   AND s.status = 'ACTIVE' AND `+collaboration.SpaceReadableSQL,
		strings.ToUpper(actorRole), spaceIDs).Scan(&count); err != nil {
		return err
	}
	if count != len(spaceIDs) {
		return ErrInvalidRequest
	}
	return nil
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (s Service) List(ctx context.Context, selection tenancy.OrganizationSelection, actorRole string) ([]Invitation, error) {
	if s.Pool == nil {
		return nil, postgres.ErrNilPool
	}
	if !canManageRole(actorRole, "") {
		return nil, ErrForbidden
	}
	var invitations []Invitation
	err := s.runForOrganization(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.invitations
			   SET status = 'EXPIRED'
			 WHERE organization_id = $1::uuid
			   AND status = 'PENDING'
			   AND expires_at <= clock_timestamp()
		`, selection.OrganizationID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT `+invitationColumns+`
			  FROM openfox.invitations
			 WHERE organization_id = $1::uuid
			 ORDER BY issued_at, id
		`, selection.OrganizationID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var invitation Invitation
			if err := rows.Scan(invitation.scanTargets()...); err != nil {
				return err
			}
			invitations = append(invitations, invitation)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, normalizeDatabaseError(err)
	}
	if invitations == nil {
		invitations = []Invitation{}
	}
	return invitations, nil
}

// The caller holds the invitation row lock. Commit physical expiry (and release reservations)
// before returning the same conflict as a replay of an already terminal invitation.
func expirePending(ctx context.Context, tx pgx.Tx, orgID, actorRole string, invitation Invitation) error {
	if !canManageRole(actorRole, invitation.Role) {
		return ErrConflict
	}
	result, err := tx.Exec(ctx, `UPDATE openfox.invitations SET status='EXPIRED'
		WHERE organization_id=$1::uuid AND id=$2::uuid AND status='PENDING'`, orgID, invitation.ID)
	if err == nil && result.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}

func (s Service) Revoke(ctx context.Context, selection tenancy.OrganizationSelection, actorRole, invitationID string) (Invitation, error) {
	if s.Pool == nil {
		return Invitation{}, postgres.ErrNilPool
	}
	if strings.TrimSpace(invitationID) == "" {
		return Invitation{}, ErrNotFound
	}
	var invitation Invitation
	var expired bool
	err := s.runForOrganization(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT `+invitationColumns+`,
			       (expires_at <= clock_timestamp())
			  FROM openfox.invitations
			 WHERE organization_id = $1::uuid AND id = $2::uuid
			 FOR UPDATE
		`, selection.OrganizationID, invitationID).Scan(append(invitation.scanTargets(), &expired)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if invitation.Status != StatusPending {
			return ErrConflict
		}
		if expired {
			return expirePending(ctx, tx, selection.OrganizationID, actorRole, invitation)
		}
		if !canManageRole(actorRole, invitation.Role) {
			return ErrForbidden
		}
		if err := tx.QueryRow(ctx, `
			UPDATE openfox.invitations
			   SET status = 'REVOKED'
			 WHERE organization_id = $1::uuid AND id = $2::uuid AND status = 'PENDING'
			RETURNING `+invitationColumns+`
		`, selection.OrganizationID, invitation.ID).Scan(invitation.scanTargets()...); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrConflict
			}
			return err
		}
		return recordAudit(ctx, tx, selection.OrganizationID, selection.ActorID,
			"invitation.revoked", invitation.ID, selection.AuthorizationVersion,
			map[string]string{"status": string(invitation.Status)})
	})
	if err != nil {
		return Invitation{}, normalizeDatabaseError(err)
	}
	if expired {
		return Invitation{}, ErrConflict
	}
	return invitation, nil
}

func (s Service) Accept(ctx context.Context, invitationID, token string, actorID *string, password string) (Acceptance, error) {
	if s.Pool == nil {
		return Acceptance{}, postgres.ErrNilPool
	}
	if err := validateAcceptInput(invitationID, token, actorID, password); err != nil {
		return Acceptance{}, err
	}
	var passwordHash any
	if password != "" {
		hash, err := s.PasswordHasher.Hash(password)
		if err != nil {
			return Acceptance{}, err
		}
		passwordHash = hash
	}
	var actor any
	if actorID != nil {
		actor = *actorID
	}
	var accepted Acceptance
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT user_id::text, organization_id::text, role, status
			  FROM openfox_private.accept_invitation($1::uuid, $2::bytea, $3::uuid, $4::text)
		`, invitationID, HashToken(token), actor, passwordHash).Scan(
			&accepted.UserID,
			&accepted.OrganizationID,
			&accepted.Role,
			&accepted.Status,
		)
		if err != nil {
			return err
		}
		var authorizationVersion int64
		if err := tx.QueryRow(ctx, `
			SELECT authorization_version
			  FROM openfox.organization_members
			 WHERE organization_id = $1::uuid AND user_id = $2::uuid
		`, accepted.OrganizationID, accepted.UserID).Scan(&authorizationVersion); err != nil {
			return err
		}
		return recordAudit(ctx, tx, accepted.OrganizationID, accepted.UserID,
			"invitation.accepted", invitationID, uint64(authorizationVersion),
			map[string]string{"status": string(accepted.Status), "grant_scope": "role:" + accepted.Role})
	})
	if err != nil {
		return Acceptance{}, normalizeDatabaseError(err)
	}
	return accepted, nil
}

func (s Service) runForOrganization(ctx context.Context, selection tenancy.OrganizationSelection, fn func(context.Context, pgx.Tx) error) error {
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
	return s.Pool.RunForOrganization(ctx, selection.OrganizationID, tenancy.SourceUserRequest, selector, fn)
}

func validateCreateInput(input CreateInput) (string, string, error) {
	email := NormalizeEmail(input.Email)
	if !validEmail(email) {
		return "", "", ErrInvalidRequest
	}
	role := strings.ToUpper(strings.TrimSpace(input.Role))
	if !validRole(role) {
		return "", "", ErrInvalidRequest
	}
	return email, role, nil
}

func validateAcceptInput(invitationID, token string, actorID *string, password string) error {
	if strings.TrimSpace(invitationID) == "" || strings.TrimSpace(token) == "" || len(token) > 512 {
		return ErrInvalidRequest
	}
	if actorID != nil && strings.TrimSpace(*actorID) == "" {
		return ErrInvalidRequest
	}
	if len(password) > 1024 {
		return ErrInvalidRequest
	}
	return nil
}

func validEmail(value string) bool {
	if value == "" || len(value) > 320 || strings.Count(value, "@") != 1 {
		return false
	}
	if strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return false
	}
	at := strings.IndexByte(value, '@')
	return at > 0 && at < len(value)-1
}

func validRole(role string) bool {
	switch role {
	case "OWNER", "ADMIN", "MEMBER", "GUEST":
		return true
	default:
		return false
	}
}

func canManageRole(actorRole, targetRole string) bool {
	switch strings.ToUpper(strings.TrimSpace(actorRole)) {
	case "OWNER":
		return targetRole == "" || validRole(targetRole)
	case "ADMIN":
		return targetRole == "" || targetRole == "MEMBER" || targetRole == "GUEST"
	default:
		return false
	}
}

type acceptanceState struct {
	InvitationStatus Status
	ExpiresAt        time.Time
	TargetExists     bool
	ActorPresent     bool
	ActorMatches     bool
	PasswordPresent  bool
}

func validateAcceptance(state acceptanceState, now time.Time) error {
	if state.InvitationStatus != StatusPending || !now.Before(state.ExpiresAt) {
		return ErrConflict
	}
	if state.TargetExists {
		if !state.ActorPresent {
			return ErrAuthenticationRequired
		}
		if !state.ActorMatches {
			return ErrEmailMismatch
		}
		if state.PasswordPresent {
			return ErrParameterConflict
		}
		return nil
	}
	if state.ActorPresent {
		return ErrEmailMismatch
	}
	if !state.PasswordPresent {
		return ErrPasswordRequired
	}
	return nil
}

func normalizeDatabaseError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInvalidRequest) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) ||
		errors.Is(err, ErrForbidden) || errors.Is(err, ErrAuthenticationRequired) || errors.Is(err, ErrEmailMismatch) ||
		errors.Is(err, ErrPasswordRequired) || errors.Is(err, ErrParameterConflict) ||
		errors.Is(err, ErrAlreadyMember) || errors.Is(err, ErrCooldown) || errors.Is(err, ErrSecretsUnavailable) {
		return err
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	if pgErr.Code == "22P02" {
		return ErrNotFound
	}
	if pgErr.Code == "P0002" {
		return ErrNotFound
	}
	if pgErr.Code == "P0001" {
		switch pgErr.Message {
		case "invitation_auth_required":
			return ErrAuthenticationRequired
		case "invitation_email_mismatch":
			return ErrEmailMismatch
		case "invitation_password_required":
			return ErrPasswordRequired
		case "invitation_parameter_conflict":
			return ErrParameterConflict
		default:
			return ErrConflict
		}
	}
	return err
}

func recordAudit(ctx context.Context, tx pgx.Tx, organizationID, actorID, action, resourceID string, authorizationVersion uint64, metadata map[string]string) error {
	eventID, err := newUUID()
	if err != nil {
		return err
	}
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{
		ID:                   eventID,
		OrganizationID:       organizationID,
		ActorID:              actorID,
		Action:               action,
		Decision:             "allow",
		Reason:               "allowed",
		Resource:             audit.ResourceRef{Kind: "invitation", OrganizationID: organizationID, ID: resourceID},
		RequestID:            eventID,
		AuthorizationVersion: authorizationVersion,
		SafeMetadata:         metadata,
		CreatedAt:            time.Now().UTC(),
	})
}

func newUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
