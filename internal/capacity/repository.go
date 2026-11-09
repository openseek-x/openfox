package capacity

import "context"

// Repository provides the transaction boundary for the application service.
// A PostgreSQL adapter must implement real row locks, unique constraints for
// effective offers/idempotency keys, and atomic commit. This package does not
// provide a production memory-backed capacity guarantee.
type Repository interface {
	// WithinTransaction commits only when fn returns nil. If commit status is
	// unknown, the adapter must return ErrUnknownResult; callers must reconcile
	// instead of retrying the operation as if it succeeded.
	WithinTransaction(ctx context.Context, fn func(Transaction) error) error
}

// Transaction is intentionally narrow so capacity cannot become a generic
// tenant or device administration API. LockCapacities must acquire rows in
// the supplied protocol order. LockOffer is called only after capacities.
type Transaction interface {
	FindIdempotency(ctx context.Context, key IdempotencyKey) (IdempotencyRecord, bool, error)
	SaveIdempotency(ctx context.Context, record IdempotencyRecord) error

	FindEffectiveOffer(ctx context.Context, organizationID, runID string, generation int64) (Offer, bool, error)

	FindReservation(ctx context.Context, reservationID string) (Reservation, bool, error)
	LockReservation(ctx context.Context, reservationID string) (Reservation, error)
	SaveReservation(ctx context.Context, reservation Reservation) error

	FindOffer(ctx context.Context, offerID string) (Offer, bool, error)
	LockOffer(ctx context.Context, offerID string) (Offer, error)
	SaveOffer(ctx context.Context, offer Offer) error

	LockCapacities(ctx context.Context, resources []Resource) ([]Capacity, error)
	SaveCapacity(ctx context.Context, capacity Capacity) error

	FindGrant(ctx context.Context, grantID string) (Grant, bool, error)

	CreateReservation(ctx context.Context, reservation Reservation) error
	CreateOffer(ctx context.Context, offer Offer) error
}
