package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"openfox/internal/capacity"
)

// CapacityBroker is the narrow boundary between Run scheduling and the
// organization/device/Agent reservation service. It deliberately exposes no
// general device or grant mutation operations.
type CapacityBroker interface {
	Reserve(context.Context, capacity.ReserveRequest) (capacity.ReserveResult, error)
	Claim(context.Context, capacity.ClaimRequest) (capacity.ClaimResult, error)
	Release(context.Context, capacity.ReleaseRequest) (capacity.ReleaseResult, error)
	Reap(context.Context, capacity.ReapRequest) (capacity.ReapResult, error)
}

type Scheduler struct {
	engine   *Engine
	capacity CapacityBroker
}

type ReserveScheduleRequest struct {
	OrganizationID  string
	RunID           string
	ExpectedVersion int64
}

type ClaimScheduleRequest struct {
	OrganizationID  string
	RunID           string
	ExpectedVersion int64
	OfferID         string
	ComputerID      string
	GrantID         string
	GrantVersion    int64
	IdempotencyKey  string
}

type ReapScheduleRequest struct {
	OrganizationID  string
	RunID           string
	ExpectedVersion int64
	OfferID         string
	IdempotencyKey  string
}

func NewScheduler(engine *Engine, broker CapacityBroker) (*Scheduler, error) {
	if engine == nil || broker == nil {
		return nil, fmt.Errorf("%w: scheduler requires engine and capacity broker", ErrInvalidRequest)
	}
	return &Scheduler{engine: engine, capacity: broker}, nil
}

// Reserve performs the center-side reservation/offer step. Capacity
// exhaustion is a normal queue outcome, not a failed Run; the wait reason is
// persisted on the Run so a later wake can retry it.
func (s *Scheduler) Reserve(ctx context.Context, request ReserveScheduleRequest) (SchedulerResult, error) {
	if err := validateScheduleIdentity(request.OrganizationID, request.RunID, request.ExpectedVersion); err != nil {
		return SchedulerResult{}, err
	}
	run, err := s.engine.Get(ctx, request.OrganizationID, request.RunID)
	if err != nil {
		return SchedulerResult{}, err
	}
	if run.Version != request.ExpectedVersion {
		return SchedulerResult{}, ErrVersionConflict
	}
	if run.State != StateQueued {
		return SchedulerResult{}, fmt.Errorf("%w: reservation requires queued run", ErrInvalidTransition)
	}
	if run.ReservationID != "" || run.OfferID != "" {
		return SchedulerResult{Run: run, ReservationID: run.ReservationID, OfferID: run.OfferID, Reserved: true}, nil
	}

	result, err := s.capacity.Reserve(ctx, capacity.ReserveRequest{
		OrganizationID: request.OrganizationID,
		RunID:          run.ID,
		Generation:     run.Generation,
		GrantID:        run.GrantID,
		GrantVersion:   run.GrantVersion,
		Resources: []capacity.Resource{
			{Kind: capacity.ResourceOrganization, ID: run.OrganizationID, Quantity: 1},
			{Kind: capacity.ResourceComputer, ID: run.ComputerID, Quantity: 1},
			{Kind: capacity.ResourceAgent, ID: run.AgentID, Quantity: 1},
		},
		IdempotencyKey: "run:" + run.ID + ":generation:" + fmt.Sprint(run.Generation),
	})
	if err != nil {
		if errors.Is(err, capacity.ErrCapacityUnavailable) {
			waitReason := WaitComputerCapacity
			var unavailable *capacity.CapacityUnavailableError
			if errors.As(err, &unavailable) && unavailable.Kind == capacity.ResourceAgent {
				waitReason = WaitAgentConcurrency
			} else if errors.As(err, &unavailable) && unavailable.Kind == capacity.ResourceOrganization {
				waitReason = WaitOrganizationCapacity
			}
			waited, waitErr := s.engine.SetWaitReason(ctx, run.OrganizationID, run.ID, run.Version, waitReason, err.Error())
			if waitErr != nil {
				return SchedulerResult{}, waitErr
			}
			return SchedulerResult{Run: waited, Changed: true, Reserved: false}, nil
		}
		return SchedulerResult{}, err
	}

	attached, err := s.engine.AttachOffer(ctx, run.OrganizationID, run.ID, run.Version, result.Reservation.ID, result.Offer.ID)
	if err != nil {
		releaseErr := s.releaseReservation(ctx, run, result.Reservation.ID)
		if releaseErr != nil {
			if current, getErr := s.engine.Get(ctx, run.OrganizationID, run.ID); getErr == nil {
				_, _ = s.engine.MarkReconciliation(ctx, current.OrganizationID, current.ID, current.Version, "capacity reserved but Run offer attach failed: "+releaseErr.Error())
			}
			return SchedulerResult{}, fmt.Errorf("%w: attach offer: %v; release: %v", ErrReconciliationRequired, err, releaseErr)
		}
		return SchedulerResult{}, err
	}
	return SchedulerResult{Run: attached, ReservationID: result.Reservation.ID, OfferID: result.Offer.ID, Changed: result.Created, Reserved: true}, nil
}

func (s *Scheduler) Claim(ctx context.Context, request ClaimScheduleRequest) (SchedulerResult, error) {
	if err := validateScheduleIdentity(request.OrganizationID, request.RunID, request.ExpectedVersion); err != nil {
		return SchedulerResult{}, err
	}
	if strings.TrimSpace(request.OfferID) == "" || strings.TrimSpace(request.ComputerID) == "" || strings.TrimSpace(request.GrantID) == "" || request.GrantVersion <= 0 || strings.TrimSpace(request.IdempotencyKey) == "" {
		return SchedulerResult{}, fmt.Errorf("%w: incomplete offer claim request", ErrInvalidRequest)
	}
	run, err := s.engine.Get(ctx, request.OrganizationID, request.RunID)
	if err != nil {
		return SchedulerResult{}, err
	}
	if run.Version != request.ExpectedVersion {
		return SchedulerResult{}, ErrVersionConflict
	}
	if (run.State == StatePreparing || run.State == StateRunning) && run.OfferID == request.OfferID {
		return SchedulerResult{Run: run, ReservationID: run.ReservationID, OfferID: run.OfferID, Reserved: true}, nil
	}
	if run.State != StateQueued || run.OfferID != request.OfferID {
		return SchedulerResult{}, fmt.Errorf("%w: claim requires the current queued offer", ErrInvalidTransition)
	}
	claim, err := s.capacity.Claim(ctx, capacity.ClaimRequest{
		OrganizationID: request.OrganizationID,
		RunID:          run.ID,
		Generation:     run.Generation,
		OfferID:        request.OfferID,
		ComputerID:     request.ComputerID,
		GrantID:        request.GrantID,
		GrantVersion:   request.GrantVersion,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		if errors.Is(err, capacity.ErrOfferExpired) {
			cleared, clearErr := s.engine.ClearOffer(ctx, run.OrganizationID, run.ID, run.Version, WaitOfferExpired, "dispatch offer expired")
			if clearErr != nil {
				return SchedulerResult{}, clearErr
			}
			return SchedulerResult{Run: cleared, Changed: true, Reserved: false}, nil
		}
		return SchedulerResult{}, err
	}
	preparing, err := s.engine.StartPreparing(ctx, StartRequest{
		OrganizationID:  request.OrganizationID,
		RunID:           run.ID,
		ExpectedVersion: run.Version,
		Now:             offerClaimedAt(claim.Offer),
	}, claim.Reservation.ID, claim.Offer.ID)
	if err != nil {
		releaseErr := s.releaseReservation(ctx, run, claim.Reservation.ID)
		if releaseErr != nil {
			if current, getErr := s.engine.Get(ctx, run.OrganizationID, run.ID); getErr == nil {
				_, _ = s.engine.MarkReconciliation(ctx, current.OrganizationID, current.ID, current.Version, "capacity claim succeeded but Run preparation failed: "+releaseErr.Error())
			}
			return SchedulerResult{}, fmt.Errorf("%w: prepare Run: %v; release: %v", ErrReconciliationRequired, err, releaseErr)
		}
		return SchedulerResult{}, err
	}
	return SchedulerResult{Run: preparing, ReservationID: claim.Reservation.ID, OfferID: claim.Offer.ID, Changed: claim.Changed, Reserved: true}, nil
}

func (s *Scheduler) StartRunning(ctx context.Context, request StartRequest) (Run, error) {
	return s.engine.StartRunning(ctx, request)
}

// Cancel releases a queued offer immediately. A preparing/running Run only
// records CANCEL_REQUESTED; the reservation stays held until the runtime
// reports a terminal fact and Complete performs the release.
func (s *Scheduler) Cancel(ctx context.Context, request CancelRequest) (SchedulerResult, error) {
	run, err := s.engine.Cancel(ctx, request)
	if err != nil {
		return SchedulerResult{}, err
	}
	if run.State != StateCanceled || run.ReservationID == "" {
		return SchedulerResult{Run: run, ReservationID: run.ReservationID, OfferID: run.OfferID, Changed: true, Reserved: run.ReservationID != ""}, nil
	}
	if err := s.releaseReservation(ctx, run, run.ReservationID); err != nil {
		marked, markErr := s.engine.MarkReconciliation(ctx, run.OrganizationID, run.ID, run.Version, "queued Run canceled but capacity release failed: "+err.Error())
		if markErr == nil {
			run = marked
		}
		return SchedulerResult{Run: run, ReservationID: run.ReservationID, OfferID: run.OfferID, Changed: true, Reserved: true}, fmt.Errorf("%w: %v", ErrReconciliationRequired, err)
	}
	return SchedulerResult{Run: run, ReservationID: run.ReservationID, OfferID: run.OfferID, Changed: true, Reserved: false}, nil
}

func (s *Scheduler) Complete(ctx context.Context, request CompleteRequest) (SchedulerResult, error) {
	run, err := s.engine.Complete(ctx, request)
	if err != nil {
		return SchedulerResult{}, err
	}
	if run.ReservationID == "" {
		return SchedulerResult{Run: run, Changed: true}, nil
	}
	if err := s.releaseReservation(ctx, run, run.ReservationID); err != nil {
		marked, markErr := s.engine.MarkReconciliation(ctx, run.OrganizationID, run.ID, run.Version, "Run terminal but capacity release failed: "+err.Error())
		if markErr == nil {
			run = marked
		}
		return SchedulerResult{Run: run, ReservationID: run.ReservationID, Changed: true, Reserved: true}, fmt.Errorf("%w: %v", ErrReconciliationRequired, err)
	}
	return SchedulerResult{Run: run, ReservationID: run.ReservationID, Changed: true, Reserved: false}, nil
}

func (s *Scheduler) Reap(ctx context.Context, request ReapScheduleRequest) (SchedulerResult, error) {
	if err := validateScheduleIdentity(request.OrganizationID, request.RunID, request.ExpectedVersion); err != nil {
		return SchedulerResult{}, err
	}
	if strings.TrimSpace(request.OfferID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return SchedulerResult{}, fmt.Errorf("%w: incomplete offer reap request", ErrInvalidRequest)
	}
	run, err := s.engine.Get(ctx, request.OrganizationID, request.RunID)
	if err != nil {
		return SchedulerResult{}, err
	}
	if run.Version != request.ExpectedVersion {
		return SchedulerResult{}, ErrVersionConflict
	}
	reaped, err := s.capacity.Reap(ctx, capacity.ReapRequest{
		OrganizationID: request.OrganizationID,
		RunID:          run.ID,
		Generation:     run.Generation,
		OfferID:        request.OfferID,
		IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		return SchedulerResult{}, err
	}
	if !reaped.Changed {
		return SchedulerResult{Run: run, ReservationID: run.ReservationID, OfferID: run.OfferID, Reserved: run.ReservationID != ""}, nil
	}
	cleared, err := s.engine.ClearOffer(ctx, run.OrganizationID, run.ID, run.Version, WaitOfferExpired, "dispatch offer expired")
	if err != nil {
		return SchedulerResult{}, err
	}
	return SchedulerResult{Run: cleared, ReservationID: reaped.Reservation.ID, OfferID: reaped.Offer.ID, Changed: true, Reserved: false}, nil
}

func (s *Scheduler) releaseReservation(ctx context.Context, run Run, reservationID string) error {
	if strings.TrimSpace(reservationID) == "" {
		return ErrReservationMissing
	}
	_, err := s.capacity.Release(ctx, capacity.ReleaseRequest{
		OrganizationID: run.OrganizationID,
		RunID:          run.ID,
		Generation:     run.Generation,
		ReservationID:  reservationID,
		IdempotencyKey: "release:run:" + run.ID + ":generation:" + fmt.Sprint(run.Generation) + ":reservation:" + reservationID,
	})
	return err
}

func validateScheduleIdentity(organizationID, runID string, expectedVersion int64) error {
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(runID) == "" || expectedVersion <= 0 {
		return fmt.Errorf("%w: organization, run and expected version are required", ErrInvalidRequest)
	}
	return nil
}

func offerClaimedAt(offer capacity.Offer) time.Time {
	if offer.ClaimedAt != nil {
		return offer.ClaimedAt.UTC()
	}
	return time.Now().UTC()
}
