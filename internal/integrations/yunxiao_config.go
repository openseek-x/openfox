package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/yunxiao"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

const ProviderYunxiao = "yunxiao"

var ErrYunxiaoIdentity = errors.New("integrations: Yunxiao identity must match current email")
var ErrYunxiaoHolder = errors.New("integrations: replacement PAT must have same holder")

var defaultYunxiaoClient = yunxiao.New()

func (s Service) yunxiaoClient() *yunxiao.Client {
	if s.Yunxiao != nil {
		return s.Yunxiao
	}
	return defaultYunxiaoClient
}

// VerifyYunxiao performs only external reads; callers must recheck actor and
// connection version in the saving transaction after these requests finish.
func (s Service) VerifyYunxiao(ctx context.Context, in ProviderTokenInput) (yunxiao.User, error) {
	if !in.validate(ProviderYunxiao) {
		return yunxiao.User{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	user, err := s.yunxiaoClient().Identity(ctx, in.Secret, in.CompanyID)
	if err != nil {
		return user, err
	}
	_, err = s.yunxiaoClient().Projects(ctx, in.Secret, in.CompanyID)
	return user, err
}

// Serialize final authorization with membership changes. For existing connections
// callers lock the parent first, then membership, user, and personal child.
func lockYunxiaoActor(ctx context.Context, tx pgx.Tx, actor collaboration.Actor, personal bool) error {
	if personal && actor.Role == policy.OrganizationRoleGuest || !personal && !CanManage(actor.Role) {
		return ErrForbidden
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT true FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid AND status='ACTIVE' AND role=$2 AND authorization_version=$3 FOR SHARE`, actor.UserID, strings.ToUpper(string(actor.Role)), actor.AuthorizationVersion).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	return err
}
func (s Service) SaveYunxiao(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in ProviderTokenInput, user yunxiao.User) (Connection, error) {
	if err := lockYunxiaoActor(ctx, tx, actor, false); err != nil {
		return Connection{}, err
	}
	c, err := s.SaveProviderToken(ctx, tx, org, actor, ProviderYunxiao, in, yunxiao.ConnectionIdentity(in.CompanyID), in.CompanyID)
	if err != nil {
		return c, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET settings=settings || jsonb_build_object('auth_method','yunxiao_token','verified_user_id',$2::text,'verified_at',clock_timestamp()) WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, c.ID, user.ID)
	return c, err
}

type YunxiaoAccess struct {
	ID        string
	Version   uint64
	CompanyID string
	HolderID  string
}

func (s Service) PrepareYunxiao(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, personal bool) (YunxiaoAccess, error) {
	var out YunxiaoAccess
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return out, err
	}
	if personal {
		if actor.Role == policy.OrganizationRoleGuest {
			return out, ErrForbidden
		}
	} else if !CanManage(actor.Role) {
		return out, ErrForbidden
	}
	if !membership.ValidateUUID(id) {
		return out, ErrInvalidRequest
	}
	err := tx.QueryRow(ctx, `SELECT id::text,version,settings->>'company_id',settings->>'verified_user_id' FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='yunxiao' AND scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE`, id).Scan(&out.ID, &out.Version, &out.CompanyID, &out.HolderID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil {
		err = lockYunxiaoActor(ctx, tx, actor, personal)
	}
	return out, err
}
func (s Service) UpdateYunxiao(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, access YunxiaoAccess, token string, user yunxiao.User) (Connection, error) {
	if err := s.requireManager(ctx, tx, org, actor); err != nil {
		return Connection{}, err
	}
	var version uint64
	err := tx.QueryRow(ctx, `SELECT version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='yunxiao' AND scope='ORGANIZATION' AND status='ACTIVE' FOR UPDATE`, access.ID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	if err != nil {
		return Connection{}, err
	}
	if err := lockYunxiaoActor(ctx, tx, actor, false); err != nil {
		return Connection{}, err
	}
	if version != access.Version {
		return Connection{}, ErrVersionConflict
	}
	if user.ID != access.HolderID {
		return Connection{}, ErrYunxiaoHolder
	}
	raw, _ := json.Marshal(ProviderTokenInput{Method: "token", CompanyID: access.CompanyID, Secret: token})
	sealed, key, err := s.seal(raw, connectionAAD(org, access.ID))
	if err != nil {
		return Connection{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,version=version+1,updated_at=clock_timestamp(),settings=settings || jsonb_build_object('verified_at',clock_timestamp()) WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, access.ID, sealed, key)
	if err != nil {
		return Connection{}, err
	}
	return loadConnection(ctx, tx, access.ID)
}

type YunxiaoPersonalTarget struct {
	Access YunxiaoAccess
	Email  string
}

func (s Service) PrepareYunxiaoPersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (YunxiaoPersonalTarget, error) {
	var target YunxiaoPersonalTarget
	access, err := s.PrepareYunxiao(ctx, tx, org, actor, id, true)
	if err != nil {
		return target, err
	}
	target.Access = access
	err = tx.QueryRow(ctx, `SELECT normalized_email FROM openfox.users WHERE id=$1::uuid FOR SHARE`, actor.UserID).Scan(&target.Email)
	if err == nil && strings.TrimSpace(target.Email) == "" {
		err = ErrInvalidRequest
	}
	return target, err
}
func (s Service) VerifyYunxiaoPersonal(ctx context.Context, target YunxiaoPersonalTarget, token string) (yunxiao.User, error) {
	user, err := s.yunxiaoClient().Identity(ctx, token, target.Access.CompanyID)
	if err == nil && (strings.TrimSpace(user.Email) == "" || !strings.EqualFold(strings.TrimSpace(user.Email), target.Email)) {
		err = ErrYunxiaoIdentity
	}
	return user, err
}
func (s Service) SaveYunxiaoPersonal(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, target YunxiaoPersonalTarget, user yunxiao.User) (Connection, error) {
	current, err := s.PrepareYunxiaoPersonal(ctx, tx, org, actor, target.Access.ID)
	if err != nil {
		return Connection{}, err
	}
	if current.Access.Version != target.Access.Version || current.Email != target.Email {
		return Connection{}, ErrVersionConflict
	}
	if !strings.EqualFold(strings.TrimSpace(user.Email), current.Email) || !yunxiao.ValidID(user.ID) {
		return Connection{}, ErrInvalidRequest
	}
	id, err := membership.NewID()
	if err != nil {
		return Connection{}, err
	}
	settings, _ := json.Marshal(map[string]any{"auth_method": "yunxiao_identity", "company_id": yunxiao.ConnectionIdentity(current.Access.CompanyID), "yunxiao_organization_id": current.Access.CompanyID, "external_user_id": user.ID, "parent_version": current.Access.Version, "verified_email": current.Email, "verified_at": time.Now().UTC()})
	_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,account_kind,settings,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,'yunxiao','PERSONAL',$2::uuid,$3::uuid,$4,$5,'identity',$6,$3::uuid)`, id, current.Access.ID, actor.UserID, yunxiao.UserIdentity(current.Access.CompanyID, user.ID), user.Name, settings)
	if isUniqueViolation(err) {
		return Connection{}, ErrVersionConflict
	}
	if err != nil {
		return Connection{}, err
	}
	return loadConnection(ctx, tx, id)
}
func (s Service) ReplayYunxiao(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, personal bool) (Connection, error) {
	c, err := loadConnection(ctx, tx, id)
	if err != nil {
		return c, err
	}
	if c.Provider != ProviderYunxiao || c.Status != StatusActive {
		return Connection{}, ErrNotFound
	}
	if personal {
		if c.Scope != ScopePersonal || c.OwnerUserID == nil || *c.OwnerUserID != actor.UserID || c.ParentConnectionID == nil {
			return Connection{}, ErrNotFound
		}
		var target YunxiaoPersonalTarget
		target, err = s.PrepareYunxiaoPersonal(ctx, tx, org, actor, *c.ParentConnectionID)
		if err == nil {
			var valid bool
			err = tx.QueryRow(ctx, `SELECT settings->>'parent_version'=$3 AND settings->>'verified_email'=$4 AND settings->>'company_id'=$5 FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND version=$2 AND status='ACTIVE' FOR SHARE`, c.ID, c.Version, strconv.FormatUint(target.Access.Version, 10), target.Email, yunxiao.ConnectionIdentity(target.Access.CompanyID)).Scan(&valid)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && !valid {
				err = ErrVersionConflict
			}
		}
	} else {
		_, err = s.PrepareYunxiao(ctx, tx, org, actor, id, false)
	}
	return c, err
}
