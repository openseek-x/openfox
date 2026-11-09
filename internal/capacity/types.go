package capacity

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ResourceKind is the lock-order category for a capacity row.
type ResourceKind string

const (
	ResourceOrganization ResourceKind = "organization"
	ResourceComputer     ResourceKind = "physical_computer"
	ResourceAgent        ResourceKind = "agent"
)

// Resource is one capacity row and the number of slots required from it.
// The service sorts resources by the protocol lock order before asking an
// adapter to lock them.
type Resource struct {
	Kind     ResourceKind
	ID       string
	Quantity int64
}

// Capacity is the mutable counter exposed by a repository transaction.
// ConfiguredLimit may be lower than ReservedCount after an administrative
// limit reduction; that blocks new reservations without deleting live ones.
type Capacity struct {
	Kind            ResourceKind
	ID              string
	ConfiguredLimit int64
	ReservedCount   int64
}

// CapacityUnavailableError preserves the ordered resource that blocked a
// reservation while remaining compatible with errors.Is(ErrCapacityUnavailable).
type CapacityUnavailableError struct {
	Kind ResourceKind
	ID   string
}

func (e *CapacityUnavailableError) Error() string {
	if e == nil {
		return ErrCapacityUnavailable.Error()
	}
	return fmt.Sprintf("%s: %s %s", ErrCapacityUnavailable, e.Kind, e.ID)
}

func (e *CapacityUnavailableError) Unwrap() error { return ErrCapacityUnavailable }

func (c Capacity) validate() error {
	if !validResourceKind(c.Kind) || strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("%w: invalid capacity identity", ErrInvariantViolation)
	}
	if c.ConfiguredLimit < 0 || c.ReservedCount < 0 {
		return fmt.Errorf("%w: negative capacity counter", ErrInvariantViolation)
	}
	return nil
}

// Reserve increments the counter only when the configured limit has room.
func (c *Capacity) Reserve(quantity int64) error {
	if c == nil || quantity <= 0 {
		return fmt.Errorf("%w: reservation quantity must be positive", ErrInvalidRequest)
	}
	if err := c.validate(); err != nil {
		return err
	}
	if c.ReservedCount > c.ConfiguredLimit || quantity > c.ConfiguredLimit-c.ReservedCount {
		return &CapacityUnavailableError{Kind: c.Kind, ID: c.ID}
	}
	c.ReservedCount += quantity
	return nil
}

// Release decrements the counter exactly once at the service state transition.
func (c *Capacity) Release(quantity int64) error {
	if c == nil || quantity <= 0 {
		return fmt.Errorf("%w: release quantity must be positive", ErrInvalidRequest)
	}
	if err := c.validate(); err != nil {
		return err
	}
	if quantity > c.ReservedCount {
		return fmt.Errorf("%w: release would make capacity negative", ErrInvariantViolation)
	}
	c.ReservedCount -= quantity
	return nil
}

type ReservationStatus string

const (
	ReservationReserved ReservationStatus = "reserved"
	ReservationClaimed  ReservationStatus = "claimed"
	ReservationReleased ReservationStatus = "released"
)

func (s ReservationStatus) canTransitionTo(next ReservationStatus) bool {
	if next == ReservationReleased {
		return s == ReservationReserved || s == ReservationClaimed
	}
	return s == ReservationReserved && next == ReservationClaimed
}

type OfferStatus string

const (
	OfferLive     OfferStatus = "live"
	OfferClaimed  OfferStatus = "claimed"
	OfferExpired  OfferStatus = "expired"
	OfferCanceled OfferStatus = "canceled"
)

func (s OfferStatus) canTransitionTo(next OfferStatus) bool {
	return s == OfferLive && (next == OfferClaimed || next == OfferExpired || next == OfferCanceled)
}

type GrantStatus string

const (
	GrantActive  GrantStatus = "active"
	GrantRevoked GrantStatus = "revoked"
)

type Operation string

const (
	OperationReserve Operation = "reserve"
	OperationClaim   Operation = "claim"
	OperationRelease Operation = "release"
	OperationReap    Operation = "reap"
)

// IdempotencyKey is deliberately scoped by the protocol's organization, Run,
// generation, resource, and operation dimensions. Value is the caller key.
type IdempotencyKey struct {
	Operation      Operation
	OrganizationID string
	RunID          string
	Generation     int64
	ResourceKey    string
	Value          string
}

// IdempotencyRecord stores only the durable result references. The adapter
// must persist it in the same transaction as the state transition.
type IdempotencyRecord struct {
	Key           IdempotencyKey
	Fingerprint   string
	ReservationID string
	OfferID       string
	Changed       bool
}

type Reservation struct {
	ID             string
	OrganizationID string
	RunID          string
	Generation     int64
	Resources      []Resource
	Status         ReservationStatus
	CreatedAt      time.Time
	ClaimedAt      *time.Time
	ReleasedAt     *time.Time
}

type Offer struct {
	ID               string
	ReservationID    string
	OrganizationID   string
	RunID            string
	Generation       int64
	TargetComputerID string
	GrantID          string
	GrantVersion     int64
	ExpiresAt        time.Time
	Status           OfferStatus
	ClaimedAt        *time.Time
	ExpiredAt        *time.Time
	CanceledAt       *time.Time
}

type Grant struct {
	ID             string
	OrganizationID string
	ComputerID     string
	Version        int64
	Status         GrantStatus
}

type ReserveRequest struct {
	OrganizationID string
	RunID          string
	Generation     int64
	GrantID        string
	GrantVersion   int64
	Resources      []Resource
	IdempotencyKey string
}

type ClaimRequest struct {
	OrganizationID string
	RunID          string
	Generation     int64
	OfferID        string
	ComputerID     string
	GrantID        string
	GrantVersion   int64
	IdempotencyKey string
}

type ReleaseRequest struct {
	OrganizationID string
	RunID          string
	Generation     int64
	ReservationID  string
	IdempotencyKey string
}

type ReapRequest struct {
	OrganizationID string
	RunID          string
	Generation     int64
	OfferID        string
	IdempotencyKey string
}

type ReserveResult struct {
	Reservation Reservation
	Offer       Offer
	Created     bool
}

type ClaimResult struct {
	Reservation Reservation
	Offer       Offer
	Changed     bool
}

type ReleaseResult struct {
	Reservation Reservation
	Changed     bool
}

type ReapResult struct {
	Reservation Reservation
	Offer       Offer
	Changed     bool
}

var (
	ErrInvalidRequest           = errors.New("capacity: invalid request")
	ErrNotFound                 = errors.New("capacity: not found")
	ErrCapacityUnavailable      = errors.New("capacity: capacity unavailable")
	ErrIdempotencyConflict      = errors.New("capacity: idempotency key parameter conflict")
	ErrUnknownResult            = errors.New("capacity: transaction result unknown")
	ErrInvariantViolation       = errors.New("capacity: invariant violation")
	ErrOfferAlreadyExists       = errors.New("capacity: effective offer already exists")
	ErrOfferExpired             = errors.New("capacity: offer expired")
	ErrOfferNotExpired          = errors.New("capacity: offer is not expired")
	ErrOfferNotLive             = errors.New("capacity: offer is not live")
	ErrStaleGeneration          = errors.New("capacity: stale generation")
	ErrWrongTarget              = errors.New("capacity: wrong target computer")
	ErrGrantMismatch            = errors.New("capacity: grant mismatch")
	ErrGrantRevoked             = errors.New("capacity: grant revoked or inactive")
	ErrReservationNotReleasable = errors.New("capacity: reservation is not releasable")
)

func validResourceKind(kind ResourceKind) bool {
	switch kind {
	case ResourceOrganization, ResourceComputer, ResourceAgent:
		return true
	default:
		return false
	}
}
