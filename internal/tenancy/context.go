package tenancy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrMissingTenantContext     = errors.New("tenancy: tenant context is missing")
	ErrInvalidTenantContext     = errors.New("tenancy: tenant context is invalid")
	ErrInvalidOrganizationID    = errors.New("tenancy: organization id is invalid")
	ErrInvalidActorID           = errors.New("tenancy: actor id is invalid")
	ErrInvalidAuthorization     = errors.New("tenancy: authorization version is invalid")
	ErrInvalidSource            = errors.New("tenancy: source is invalid")
	ErrCrossOrganizationContext = errors.New("tenancy: cross-organization context")
	ErrTenantContextConflict    = errors.New("tenancy: tenant context conflict")
	ErrOrganizationSelection    = errors.New("tenancy: organization selection failed")
	ErrNilContext               = errors.New("tenancy: nil context")
)

// Source identifies the authenticated pathway that selected a tenant.
type Source string

const (
	SourceUserRequest        Source = "user_request"
	SourceDeviceRequest      Source = "device_request"
	SourceOrganizationWorker Source = "organization_worker"
)

func (s Source) valid() bool {
	switch s {
	case SourceUserRequest, SourceDeviceRequest, SourceOrganizationWorker:
		return true
	default:
		return false
	}
}

// OrganizationSelection is the result of validating a requested organization
// against the authenticated actor. It is not itself a tenant context and does
// not authorize database access until a Runner establishes a transaction.
type OrganizationSelection struct {
	OrganizationID       string
	ActorID              string
	AuthorizationVersion uint64
}

// OrganizationSelector validates an untrusted organization choice. The
// implementation must obtain actor identity and membership from trusted
// authentication state, rather than from the request's organization header or
// body.
type OrganizationSelector interface {
	SelectOrganization(context.Context, string) (OrganizationSelection, error)
}

// OrganizationSelectorFunc adapts a function to OrganizationSelector.
type OrganizationSelectorFunc func(context.Context, string) (OrganizationSelection, error)

func (f OrganizationSelectorFunc) SelectOrganization(ctx context.Context, organizationID string) (OrganizationSelection, error) {
	if f == nil {
		return OrganizationSelection{}, ErrOrganizationSelection
	}
	return f(ctx, organizationID)
}

// TenantContext is the trusted, transaction-scoped identity used by tenant
// repositories. Its fields are private so callers cannot inject a context by
// constructing or mutating the value directly.
type TenantContext struct {
	organizationID       string
	actorID              string
	authorizationVersion uint64
	source               Source
}

// OrganizationID returns the canonical organization UUID.
func (c TenantContext) OrganizationID() string { return c.organizationID }

// ActorID returns the authenticated user or device identifier.
func (c TenantContext) ActorID() string { return c.actorID }

// AuthorizationVersion returns the membership/authorization version checked
// before this transaction started.
func (c TenantContext) AuthorizationVersion() uint64 { return c.authorizationVersion }

// Source returns the authenticated pathway that selected this context.
func (c TenantContext) Source() Source { return c.source }

// DatabaseArgs returns the values accepted by the trusted database context
// setter. The context remains immutable and validated before these values are
// exposed to the platform adapter.
func (c TenantContext) DatabaseArgs() (organizationID, actorID string, authorizationVersion uint64, source Source, err error) {
	if err := c.valid(); err != nil {
		return "", "", 0, "", err
	}
	return c.organizationID, c.actorID, c.authorizationVersion, c.source, nil
}

func (c TenantContext) valid() error {
	if _, err := normalizeOrganizationID(c.organizationID); err != nil {
		return fmt.Errorf("%w: organization_id: %w", ErrInvalidTenantContext, err)
	}
	if _, err := normalizeActorID(c.actorID); err != nil {
		return fmt.Errorf("%w: actor_id: %w", ErrInvalidTenantContext, err)
	}
	if c.authorizationVersion == 0 || c.authorizationVersion > uint64(1<<63-1) {
		return fmt.Errorf("%w: %w", ErrInvalidTenantContext, ErrInvalidAuthorization)
	}
	if !c.source.valid() {
		return fmt.Errorf("%w: %w", ErrInvalidTenantContext, ErrInvalidSource)
	}
	return nil
}

type tenantContextKey struct{}

// FromContext returns the trusted context established by Runner. Arbitrary
// values stored by callers under their own context keys are ignored.
func FromContext(ctx context.Context) (TenantContext, bool) {
	if ctx == nil {
		return TenantContext{}, false
	}
	value := ctx.Value(tenantContextKey{})
	tenant, ok := value.(TenantContext)
	if !ok || tenant.valid() != nil {
		return TenantContext{}, false
	}
	return tenant, true
}

// RequireOrganization checks that the active trusted context belongs to the
// requested organization. It is intended for repository boundaries and never
// establishes or changes tenant context.
func RequireOrganization(ctx context.Context, organizationID string) error {
	tenant, ok := FromContext(ctx)
	if !ok {
		return ErrMissingTenantContext
	}
	requested, err := normalizeOrganizationID(organizationID)
	if err != nil {
		return err
	}
	if tenant.organizationID != requested {
		return ErrCrossOrganizationContext
	}
	return nil
}

func withTenantContext(ctx context.Context, tenant TenantContext) context.Context {
	return context.WithValue(ctx, tenantContextKey{}, tenant)
}

// WithTenantContext attaches a context created by NewTenantContext. It is
// exported for platform adapters that own a transaction boundary.
func WithTenantContext(ctx context.Context, tenant TenantContext) context.Context {
	if ctx == nil || tenant.valid() != nil {
		return ctx
	}
	return withTenantContext(ctx, tenant)
}

// NewTenantContext validates an organization selection before a platform
// adapter establishes the database transaction-local context.
func NewTenantContext(selection OrganizationSelection, source Source) (TenantContext, error) {
	return newTenantContext(selection, source)
}

func newTenantContext(selection OrganizationSelection, source Source) (TenantContext, error) {
	organizationID, err := normalizeOrganizationID(selection.OrganizationID)
	if err != nil {
		return TenantContext{}, err
	}
	actorID, err := normalizeActorID(selection.ActorID)
	if err != nil {
		return TenantContext{}, err
	}
	if selection.AuthorizationVersion == 0 || selection.AuthorizationVersion > uint64(1<<63-1) {
		return TenantContext{}, ErrInvalidAuthorization
	}
	if !source.valid() {
		return TenantContext{}, ErrInvalidSource
	}
	return TenantContext{
		organizationID:       organizationID,
		actorID:              actorID,
		authorizationVersion: selection.AuthorizationVersion,
		source:               source,
	}, nil
}

func normalizeOrganizationID(value string) (string, error) {
	if len(value) != 36 {
		return "", ErrInvalidOrganizationID
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return "", ErrInvalidOrganizationID
			}
			continue
		}
		if !isHex(character) {
			return "", ErrInvalidOrganizationID
		}
	}
	return strings.ToLower(value), nil
}

func normalizeActorID(value string) (string, error) {
	if value == "" || len(value) > 255 || value != strings.TrimSpace(value) || !utf8.ValidString(value) {
		return "", ErrInvalidActorID
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", ErrInvalidActorID
		}
	}
	return value, nil
}

func isHex(character rune) bool {
	return character >= '0' && character <= '9' ||
		character >= 'a' && character <= 'f' ||
		character >= 'A' && character <= 'F'
}
