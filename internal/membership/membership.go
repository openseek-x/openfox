// Package membership contains the organization membership rules and the
// tenant-scoped PostgreSQL operations used by the membership API.
package membership

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/audit"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

var (
	ErrActionForbidden    = errors.New("membership: action forbidden")
	ErrAuthorizationStale = errors.New("membership: authorization snapshot is stale")
	ErrInvalidCursor      = errors.New("membership: invalid cursor")
	ErrInvalidPageSize    = errors.New("membership: invalid page size")
	ErrInvalidRole        = errors.New("membership: invalid role")
	ErrInvalidStatus      = errors.New("membership: invalid status")
	ErrInvalidTransition  = errors.New("membership: invalid state transition")
	ErrInvalidUserID      = errors.New("membership: invalid user id")
	ErrLastOwner          = errors.New("membership: active owner required")
	ErrMemberConflict     = errors.New("membership: member conflict")
	ErrMemberNotFound     = errors.New("membership: member not found")
	ErrSoleMember         = errors.New("membership: sole member must delete the organization")
)

const (
	DefaultPageSize = 50
	MaxPageSize     = 100

	StatusActive  Status = "ACTIVE"
	StatusInvited Status = "INVITED"
	StatusRevoked Status = "REVOKED"
)

// Role aliases the shared organization role vocabulary used by policy.
type Role = policy.OrganizationRole

type Status string

const (
	RoleOwner  = policy.OrganizationRoleOwner
	RoleAdmin  = policy.OrganizationRoleAdmin
	RoleMember = policy.OrganizationRoleMember
	RoleGuest  = policy.OrganizationRoleGuest
)

type Member struct {
	UserID string
	Email  string
	// FullName 为空串表示没有设置；AvatarUpdatedAt 为空表示没有头像。
	FullName             string
	AvatarUpdatedAt      *time.Time
	Role                 Role
	Status               Status
	AuthorizationVersion uint64
	CreatedAt            time.Time
}

type ActorSnapshot struct {
	UserID               string
	Role                 Role
	AuthorizationVersion uint64
}

type Cursor struct {
	CreatedAt time.Time
	UserID    string
}

type Page struct {
	Members    []Member
	NextCursor string
}

type MutationResult struct {
	Member         Member
	PreviousRole   Role
	PreviousStatus Status
	Operation      string
}

// ParseRole canonicalizes the database role vocabulary and rejects unknown
// values before they reach PostgreSQL.
func ParseRole(value string) (Role, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "OWNER":
		return RoleOwner, nil
	case "ADMIN":
		return RoleAdmin, nil
	case "MEMBER":
		return RoleMember, nil
	case "GUEST":
		return RoleGuest, nil
	default:
		return "", ErrInvalidRole
	}
}

func validRole(role Role) bool {
	switch role {
	case RoleOwner, RoleAdmin, RoleMember, RoleGuest:
		return true
	default:
		return false
	}
}

func ParseStatus(value string) (Status, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case string(StatusActive):
		return StatusActive, nil
	case string(StatusInvited):
		return StatusInvited, nil
	case string(StatusRevoked):
		return StatusRevoked, nil
	default:
		return "", ErrInvalidStatus
	}
}

func validStoredStatus(status Status) bool {
	return status == StatusActive || status == StatusInvited || status == StatusRevoked
}

// CanManageTarget is the member-specific part of the organization policy.
// General organization-management authority is also checked by policy.Authorize
// at the API boundary.
func CanManageTarget(actorRole, targetRole Role) error {
	if actorRole == RoleOwner {
		return nil
	}
	if actorRole == RoleAdmin && (targetRole == RoleMember || targetRole == RoleGuest) {
		return nil
	}
	return ErrActionForbidden
}

func ValidateNewMember(actorRole, desiredRole Role) error {
	if !validRole(desiredRole) {
		return ErrInvalidRole
	}
	if actorRole != RoleOwner && actorRole != RoleAdmin {
		return ErrActionForbidden
	}
	if actorRole == RoleAdmin && (desiredRole == RoleOwner || desiredRole == RoleAdmin) {
		return ErrActionForbidden
	}
	return nil
}

// ValidateChange applies the target-role matrix and verifies that a proposed
// transition cannot remove the last active owner.
func ValidateChange(actorRole, targetRole Role, targetStatus Status, desiredRole Role, desiredStatus Status, activeOwners int) error {
	if !validRole(targetRole) || !validRole(desiredRole) || !validStoredStatus(targetStatus) {
		return ErrInvalidTransition
	}
	if desiredStatus != StatusActive && desiredStatus != StatusRevoked {
		return ErrInvalidTransition
	}
	if err := CanManageTarget(actorRole, targetRole); err != nil {
		return err
	}
	if actorRole == RoleAdmin && (desiredRole == RoleOwner || desiredRole == RoleAdmin) {
		return ErrActionForbidden
	}
	if targetStatus != StatusActive && desiredStatus == targetStatus && desiredRole != targetRole {
		return ErrInvalidTransition
	}
	if targetStatus == desiredStatus && targetRole == desiredRole {
		return ErrMemberConflict
	}

	wasOwner := targetStatus == StatusActive && targetRole == RoleOwner
	willBeOwner := desiredStatus == StatusActive && desiredRole == RoleOwner
	projectedOwners := activeOwners
	if wasOwner {
		projectedOwners--
	}
	if willBeOwner {
		projectedOwners++
	}
	if projectedOwners == 0 {
		return ErrLastOwner
	}
	return nil
}

// ValidateUUID accepts the canonical UUID shape without introducing another
// dependency solely for request validation.
func ValidateUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !((character >= '0' && character <= '9') ||
			(character >= 'a' && character <= 'f') ||
			(character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func ParsePageSize(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return DefaultPageSize, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 1 || parsed > MaxPageSize {
		return 0, ErrInvalidPageSize
	}
	return parsed, nil
}

func EncodeCursor(cursor Cursor) (string, error) {
	if cursor.CreatedAt.IsZero() || !ValidateUUID(cursor.UserID) {
		return "", ErrInvalidCursor
	}
	payload, err := json.Marshal(struct {
		Version   int       `json:"v"`
		CreatedAt time.Time `json:"created_at"`
		UserID    string    `json:"user_id"`
	}{Version: 1, CreatedAt: cursor.CreatedAt.UTC(), UserID: strings.ToLower(cursor.UserID)})
	if err != nil {
		return "", ErrInvalidCursor
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func ParseCursor(value string) (Cursor, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 {
		return Cursor{}, ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	var decoded struct {
		Version   int       `json:"v"`
		CreatedAt time.Time `json:"created_at"`
		UserID    string    `json:"user_id"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.Version != 1 || decoded.CreatedAt.IsZero() || !ValidateUUID(decoded.UserID) {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{CreatedAt: decoded.CreatedAt.UTC(), UserID: strings.ToLower(decoded.UserID)}, nil
}

func NewID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}

type Service struct{}

func (Service) CheckActor(ctx context.Context, tx pgx.Tx, organizationID string, actor ActorSnapshot) error {
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return err
	}
	member, err := loadMember(ctx, tx, actor.UserID, false)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAuthorizationStale
		}
		return err
	}
	if member.Status != StatusActive || member.Role != actor.Role || member.AuthorizationVersion != actor.AuthorizationVersion {
		return ErrAuthorizationStale
	}
	return nil
}

func (Service) List(ctx context.Context, tx pgx.Tx, organizationID string, actor ActorSnapshot, limit int, cursor *Cursor) (Page, error) {
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return Page{}, err
	}
	if err := (Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Page{}, err
	}
	if limit < 1 || limit > MaxPageSize {
		return Page{}, ErrInvalidPageSize
	}

	query := `
		SELECT m.user_id::text, u.email, m.role, m.status,
		       m.authorization_version, m.created_at, u.full_name, u.avatar_updated_at
		FROM openfox.organization_members m
		JOIN openfox.users u ON u.id = m.user_id
		WHERE m.organization_id = openfox_private.current_organization_id()`
	args := []any{limit + 1}
	if cursor != nil {
		query += `
		  AND (m.created_at, m.user_id) > ($2::timestamptz, $3::uuid)`
		args = append(args, cursor.CreatedAt, cursor.UserID)
	}
	query += `
		ORDER BY m.created_at ASC, m.user_id ASC
		LIMIT $1`

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	page := Page{Members: make([]Member, 0, limit)}
	for rows.Next() {
		member, err := scanMember(rows)
		if err != nil {
			return Page{}, err
		}
		page.Members = append(page.Members, member)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Members) > limit {
		last := page.Members[limit-1]
		page.Members = page.Members[:limit]
		page.NextCursor, err = EncodeCursor(Cursor{CreatedAt: last.CreatedAt, UserID: last.UserID})
		if err != nil {
			return Page{}, err
		}
	}
	return page, nil
}

func (s Service) Add(ctx context.Context, tx pgx.Tx, organizationID string, actor ActorSnapshot, userID string, desiredRole Role) (MutationResult, error) {
	if err := ValidateUUID(userID); !err {
		return MutationResult{}, ErrInvalidUserID
	}
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return MutationResult{}, err
	}
	if err := s.lockMutationContext(ctx, tx, organizationID, actor); err != nil {
		return MutationResult{}, err
	}
	if err := ValidateNewMember(actor.Role, desiredRole); err != nil {
		return MutationResult{}, err
	}

	var email, fullName string
	var avatarUpdatedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT email, full_name, avatar_updated_at FROM openfox.users WHERE id = $1::uuid`, userID).Scan(&email, &fullName, &avatarUpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MutationResult{}, ErrMemberNotFound
		}
		return MutationResult{}, err
	}

	existing, err := loadMember(ctx, tx, userID, true)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return MutationResult{}, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		var member Member
		var version int64
		err = tx.QueryRow(ctx, `
			INSERT INTO openfox.organization_members (organization_id, user_id, role, status)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2, 'ACTIVE')
			RETURNING user_id::text, role, status, authorization_version, created_at
		`, userID, strings.ToUpper(string(desiredRole))).Scan(
			&member.UserID, &member.Role, &member.Status, &version, &member.CreatedAt)
		if err != nil {
			return MutationResult{}, mapDatabaseError(err)
		}
		member.Email = email
		member.FullName = fullName
		member.AvatarUpdatedAt = avatarUpdatedAt
		member.Role = canonicalRole(string(member.Role))
		if member.AuthorizationVersion, err = positiveVersion(version); err != nil {
			return MutationResult{}, err
		}
		return MutationResult{Member: member, Operation: "created"}, nil
	}

	if err := CanManageTarget(actor.Role, existing.Role); err != nil {
		return MutationResult{}, err
	}
	if existing.Status == StatusActive {
		return MutationResult{}, ErrMemberConflict
	}
	ownerCount, err := activeOwnerCount(ctx, tx)
	if err != nil {
		return MutationResult{}, err
	}
	if err := ValidateChange(actor.Role, existing.Role, existing.Status, desiredRole, StatusActive, ownerCount); err != nil {
		return MutationResult{}, err
	}
	var member Member
	var version int64
	err = tx.QueryRow(ctx, `
		UPDATE openfox.organization_members
		   SET role = $1, status = 'ACTIVE'
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND user_id = $2::uuid
		   AND authorization_version = $3
		RETURNING user_id::text, role, status, authorization_version, created_at
	`, strings.ToUpper(string(desiredRole)), userID, int64(existing.AuthorizationVersion)).Scan(
		&member.UserID, &member.Role, &member.Status, &version, &member.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MutationResult{}, ErrMemberConflict
	}
	if err != nil {
		return MutationResult{}, mapDatabaseError(err)
	}
	member.Email = email
	member.FullName = fullName
	member.AvatarUpdatedAt = avatarUpdatedAt
	member.Role = canonicalRole(string(member.Role))
	if member.AuthorizationVersion, err = positiveVersion(version); err != nil {
		return MutationResult{}, err
	}
	operation := "activated"
	if existing.Role != desiredRole {
		operation = "activated_role_changed"
	}
	return MutationResult{Member: member, PreviousRole: existing.Role, PreviousStatus: existing.Status, Operation: operation}, nil
}

func (s Service) Change(ctx context.Context, tx pgx.Tx, organizationID string, actor ActorSnapshot, userID string, desiredRole *Role, desiredStatus *Status) (MutationResult, error) {
	if !ValidateUUID(userID) {
		return MutationResult{}, ErrInvalidUserID
	}
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return MutationResult{}, err
	}
	if err := s.lockMutationContext(ctx, tx, organizationID, actor); err != nil {
		return MutationResult{}, err
	}
	existing, err := loadMember(ctx, tx, userID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return MutationResult{}, ErrMemberNotFound
	}
	if err != nil {
		return MutationResult{}, err
	}
	newRole := existing.Role
	if desiredRole != nil {
		newRole = *desiredRole
	}
	newStatus := existing.Status
	if desiredStatus != nil {
		newStatus = *desiredStatus
	}
	ownerCount, err := activeOwnerCount(ctx, tx)
	if err != nil {
		return MutationResult{}, err
	}
	if err := ValidateChange(actor.Role, existing.Role, existing.Status, newRole, newStatus, ownerCount); err != nil {
		return MutationResult{}, err
	}

	var member Member
	var version int64
	err = tx.QueryRow(ctx, `
		UPDATE openfox.organization_members
		   SET role = $1, status = $2
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND user_id = $3::uuid
		   AND authorization_version = $4
		RETURNING user_id::text, role, status, authorization_version, created_at
	`, strings.ToUpper(string(newRole)), string(newStatus), userID, int64(existing.AuthorizationVersion)).Scan(
		&member.UserID, &member.Role, &member.Status, &version, &member.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MutationResult{}, ErrMemberConflict
	}
	if err != nil {
		return MutationResult{}, mapDatabaseError(err)
	}
	member.Email = existing.Email
	member.FullName = existing.FullName
	member.AvatarUpdatedAt = existing.AvatarUpdatedAt
	member.Role = canonicalRole(string(member.Role))
	if member.AuthorizationVersion, err = positiveVersion(version); err != nil {
		return MutationResult{}, err
	}
	operation := "updated"
	if existing.Status != newStatus && newStatus == StatusRevoked {
		operation = "revoked"
	} else if existing.Status != newStatus && newStatus == StatusActive {
		operation = "activated"
	} else if existing.Role != newRole {
		operation = "role_changed"
	}
	return MutationResult{Member: member, PreviousRole: existing.Role, PreviousStatus: existing.Status, Operation: operation}, nil
}

// Leave 把当前用户自己的成员关系改为 REVOKED（再次访问需要重新邀请）。唯一的活跃所有者在还有其他成员时
// 返回 ErrLastOwner；只剩自己时返回 ErrSoleMember（只能删除组织）。组织行锁与其他成员变更串行。
func (s Service) Leave(ctx context.Context, tx pgx.Tx, organizationID string, actor ActorSnapshot) (MutationResult, error) {
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return MutationResult{}, err
	}
	if err := s.lockMutationContext(ctx, tx, organizationID, actor); err != nil {
		return MutationResult{}, err
	}
	var owners, members int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE role = 'OWNER'), count(*)
		  FROM openfox.organization_members
		 WHERE organization_id = openfox_private.current_organization_id() AND status = 'ACTIVE'`).Scan(&owners, &members); err != nil {
		return MutationResult{}, err
	}
	if members <= 1 {
		return MutationResult{}, ErrSoleMember
	}
	if actor.Role == RoleOwner && owners <= 1 {
		return MutationResult{}, ErrLastOwner
	}
	var member Member
	var version int64
	err := tx.QueryRow(ctx, `
		UPDATE openfox.organization_members m
		   SET status = 'REVOKED'
		  FROM openfox.users u
		 WHERE m.organization_id = openfox_private.current_organization_id()
		   AND m.user_id = $1::uuid AND m.authorization_version = $2 AND u.id = m.user_id
		RETURNING m.user_id::text, u.email, m.role, m.status, m.authorization_version, m.created_at, u.full_name, u.avatar_updated_at
	`, actor.UserID, int64(actor.AuthorizationVersion)).Scan(
		&member.UserID, &member.Email, &member.Role, &member.Status, &version, &member.CreatedAt,
		&member.FullName, &member.AvatarUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return MutationResult{}, ErrAuthorizationStale
	}
	if err != nil {
		return MutationResult{}, mapDatabaseError(err)
	}
	member.Role = canonicalRole(string(member.Role))
	if member.AuthorizationVersion, err = positiveVersion(version); err != nil {
		return MutationResult{}, err
	}
	return MutationResult{Member: member, PreviousRole: actor.Role, PreviousStatus: StatusActive, Operation: "revoked"}, nil
}

func (s Service) lockMutationContext(ctx context.Context, tx pgx.Tx, organizationID string, actor ActorSnapshot) error {
	if !ValidateUUID(actor.UserID) || actor.AuthorizationVersion == 0 || !validRole(actor.Role) {
		return ErrAuthorizationStale
	}
	var lockedOrganizationID string
	if err := tx.QueryRow(ctx, `
		SELECT id
		FROM openfox.organizations
		WHERE id = openfox_private.current_organization_id()
		FOR UPDATE`).Scan(&lockedOrganizationID); err != nil {
		return err
	}
	member, err := loadMember(ctx, tx, actor.UserID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAuthorizationStale
	}
	if err != nil {
		return err
	}
	if member.Status != StatusActive || member.Role != actor.Role || member.AuthorizationVersion != actor.AuthorizationVersion {
		return ErrAuthorizationStale
	}
	return nil
}

func activeOwnerCount(ctx context.Context, tx pgx.Tx) (int, error) {
	var count int
	err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM openfox.organization_members
		WHERE organization_id = openfox_private.current_organization_id()
		  AND role = 'OWNER' AND status = 'ACTIVE'`).Scan(&count)
	return count, err
}

func loadMember(ctx context.Context, tx pgx.Tx, userID string, forUpdate bool) (Member, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE OF m"
	}
	query := `
		SELECT m.user_id::text, u.email, m.role, m.status,
		       m.authorization_version, m.created_at, u.full_name, u.avatar_updated_at
		FROM openfox.organization_members m
		JOIN openfox.users u ON u.id = m.user_id
		WHERE m.organization_id = openfox_private.current_organization_id()
		  AND m.user_id = $1::uuid` + lock
	return scanMember(tx.QueryRow(ctx, query, userID))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMember(row rowScanner) (Member, error) {
	var member Member
	var role, status string
	var version int64
	if err := row.Scan(&member.UserID, &member.Email, &role, &status, &version, &member.CreatedAt,
		&member.FullName, &member.AvatarUpdatedAt); err != nil {
		return Member{}, err
	}
	member.Role = canonicalRole(role)
	member.Status = Status(status)
	var err error
	member.AuthorizationVersion, err = positiveVersion(version)
	if err != nil {
		return Member{}, err
	}
	return member, nil
}

func positiveVersion(value int64) (uint64, error) {
	if value <= 0 {
		return 0, fmt.Errorf("membership: invalid authorization version %d", value)
	}
	return uint64(value), nil
}

func canonicalRole(value string) Role {
	role, _ := ParseRole(value)
	return role
}

func mapDatabaseError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "23505" {
			return ErrMemberConflict
		}
		if pgErr.ConstraintName == "organization_members_owner_required" {
			return ErrLastOwner
		}
	}
	return err
}

// RecordAudit validates the shared audit event and persists it in the caller's
// tenant transaction, so a membership write and its explanation commit as one.
func RecordAudit(ctx context.Context, tx pgx.Tx, event audit.Event) error {
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, event)
}

// LoadActiveActor 读取当前组织内某成员的最新授权快照，供后台派发在没有
// 用户请求上下文时重新核验原始发起人。非 ACTIVE 成员返回 ErrAuthorizationStale。
func (Service) LoadActiveActor(ctx context.Context, tx pgx.Tx, organizationID, userID string) (ActorSnapshot, error) {
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return ActorSnapshot{}, err
	}
	if !ValidateUUID(userID) {
		return ActorSnapshot{}, ErrAuthorizationStale
	}
	member, err := loadMember(ctx, tx, userID, false)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ActorSnapshot{}, ErrAuthorizationStale
		}
		return ActorSnapshot{}, err
	}
	if member.Status != StatusActive {
		return ActorSnapshot{}, ErrAuthorizationStale
	}
	return ActorSnapshot{UserID: member.UserID, Role: member.Role, AuthorizationVersion: member.AuthorizationVersion}, nil
}
