package integrations

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

type ProviderTokenInput struct {
	Method    string `json:"method"`
	Account   string `json:"account,omitempty"`
	Secret    string `json:"secret"`
	CompanyID string `json:"company_id,omitempty"`
}

type providerIdentity struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Username    string `json:"username"`
	Nick        string `json:"nick"`
}

var companyPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

var tapdCompanyPattern = regexp.MustCompile(`^[0-9]{1,32}$`)

func (in ProviderTokenInput) validate(provider string) bool {
	if in.Secret == "" || len(in.Secret) > 4096 || strings.ContainsAny(in.Secret, "\r\n") || len(in.Account) > 200 {
		return false
	}
	switch provider {
	case "teambition":
		return in.Method == "app" && companyPattern.MatchString(in.Account) && companyPattern.MatchString(in.CompanyID)
	case "tapd":
		if in.Method == "basic" {
			return in.Account != "" && !strings.ContainsAny(in.Account, ":\r\n") && tapdCompanyPattern.MatchString(in.CompanyID)
		}
		return in.Method == "token" && in.Account == "" && in.CompanyID == ""
	case "pingcode":
		return in.Method == "token" && companyPattern.MatchString(in.CompanyID) && in.Account == ""
	case "cnb", "shortcut":
		return in.Method == "token" && in.CompanyID == "" && in.Account == ""
	}
	return false
}

func (s Service) CheckProviderToken(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, provider string, input ProviderTokenInput) error {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return err
	}
	if !CanManage(actor.Role) {
		return ErrForbidden
	}
	if !input.validate(provider) {
		return ErrInvalidRequest
	}
	if s.Box == nil {
		return ErrSecretsUnavailable
	}
	return nil
}

// Contact only the provider's official origin. A redirect must never carry a credential.
func VerifyProviderToken(ctx context.Context, provider string, in ProviderTokenInput) (string, string, error) {
	if !in.validate(provider) {
		return "", "", ErrInvalidRequest
	}
	source := ProjectSource{Provider: provider, auth: in}
	var identity providerIdentity
	if provider == "teambition" || provider == "tapd" && in.Method == "basic" {
		if _, err := source.Projects(ctx); err != nil {
			return "", "", err
		}
		return in.CompanyID, in.Account, nil
	}
	if provider == "pingcode" {
		var err error
		identity, err = source.pingCompany(ctx)
		if err != nil {
			return "", "", err
		}
	} else if provider == "shortcut" {
		var member struct {
			Workspace providerIdentity `json:"workspace2"`
		}
		if err := source.get(ctx, "/member", nil, &member); err != nil {
			return "", "", err
		}
		identity = member.Workspace
	} else {
		path := map[string]string{"tapd": "/users/info", "cnb": "/user"}[provider]
		if err := source.get(ctx, path, nil, &identity); err != nil {
			return "", "", err
		}
	}
	if identity.ID == "" || len(identity.ID) > 255 {
		return "", "", ErrUpstreamUnavailable
	}
	for _, name := range []string{identity.Username, identity.Name, identity.Nick, identity.ID} {
		if name != "" && len(name) <= 200 {
			return identity.ID, name, nil
		}
	}
	return "", "", ErrUpstreamUnavailable
}

func (s Service) SaveProviderToken(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, provider string, in ProviderTokenInput, externalID, name string) (Connection, error) {
	if err := s.CheckProviderToken(ctx, tx, org, actor, provider, in); err != nil {
		return Connection{}, err
	}
	id, err := membership.NewID()
	if err != nil {
		return Connection{}, err
	}
	plain, err := json.Marshal(in)
	if err != nil {
		return Connection{}, err
	}
	blob, key, err := s.seal(plain, connectionAAD(org, id))
	if err != nil {
		return Connection{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,credentials_sealed,credentials_key_id,settings,created_by)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,'ORGANIZATION',$3,$4,$5,$6,jsonb_build_object('auth_method',$7::text,'company_id',$8::text),$9::uuid)`, id, provider, externalID, name, blob, key, in.Method, in.CompanyID, actor.UserID)
	if isUniqueViolation(err) {
		return Connection{}, ErrVersionConflict
	}
	if err != nil {
		return Connection{}, err
	}
	return loadConnection(ctx, tx, id)
}
