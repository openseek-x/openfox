package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"openfox/internal/capacity"
)

type schedulerCapacityFake struct {
	mu           sync.Mutex
	limit        int
	used         int
	reservations map[string]capacity.Reservation
	offers       map[string]capacity.Offer
	nextID       int
}

func newSchedulerCapacityFake(limit int) *schedulerCapacityFake {
	return &schedulerCapacityFake{
		limit:        limit,
		reservations: make(map[string]capacity.Reservation),
		offers:       make(map[string]capacity.Offer),
	}
}

func (f *schedulerCapacityFake) Reserve(_ context.Context, request capacity.ReserveRequest) (capacity.ReserveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, reservation := range f.reservations {
		if reservation.OrganizationID == request.OrganizationID && reservation.RunID == request.RunID && reservation.Generation == request.Generation && reservation.Status != capacity.ReservationReleased {
			return capacity.ReserveResult{Reservation: reservation, Offer: f.offersForReservationLocked(reservation.ID), Created: false}, nil
		}
	}
	if f.used >= f.limit {
		return capacity.ReserveResult{}, capacity.ErrCapacityUnavailable
	}
	f.nextID++
	reservationID := fmt.Sprintf("reservation-%d", f.nextID)
	f.nextID++
	offerID := fmt.Sprintf("offer-%d", f.nextID)
	now := time.Now().UTC()
	reservation := capacity.Reservation{
		ID:             reservationID,
		OrganizationID: request.OrganizationID,
		RunID:          request.RunID,
		Generation:     request.Generation,
		Resources:      append([]capacity.Resource(nil), request.Resources...),
		Status:         capacity.ReservationReserved,
		CreatedAt:      now,
	}
	offer := capacity.Offer{
		ID:               offerID,
		ReservationID:    reservationID,
		OrganizationID:   request.OrganizationID,
		RunID:            request.RunID,
		Generation:       request.Generation,
		TargetComputerID: resourceID(request.Resources, capacity.ResourceComputer),
		GrantID:          request.GrantID,
		GrantVersion:     request.GrantVersion,
		ExpiresAt:        now.Add(15 * time.Second),
		Status:           capacity.OfferLive,
	}
	f.reservations[reservationID] = reservation
	f.offers[offerID] = offer
	f.used++
	return capacity.ReserveResult{Reservation: reservation, Offer: offer, Created: true}, nil
}

func (f *schedulerCapacityFake) Claim(_ context.Context, request capacity.ClaimRequest) (capacity.ClaimResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	offer, found := f.offers[request.OfferID]
	if !found {
		return capacity.ClaimResult{}, capacity.ErrNotFound
	}
	reservation := f.reservations[offer.ReservationID]
	if offer.Status == capacity.OfferClaimed {
		return capacity.ClaimResult{Reservation: reservation, Offer: offer, Changed: false}, nil
	}
	if offer.Status != capacity.OfferLive {
		return capacity.ClaimResult{}, capacity.ErrOfferNotLive
	}
	if offer.TargetComputerID != request.ComputerID || offer.GrantID != request.GrantID || offer.GrantVersion != request.GrantVersion {
		return capacity.ClaimResult{}, capacity.ErrGrantMismatch
	}
	if time.Now().UTC().After(offer.ExpiresAt) {
		offer.Status = capacity.OfferExpired
		reservation.Status = capacity.ReservationReleased
		f.offers[offer.ID] = offer
		f.reservations[reservation.ID] = reservation
		f.used--
		return capacity.ClaimResult{}, capacity.ErrOfferExpired
	}
	now := time.Now().UTC()
	offer.Status = capacity.OfferClaimed
	offer.ClaimedAt = &now
	reservation.Status = capacity.ReservationClaimed
	reservation.ClaimedAt = &now
	f.offers[offer.ID] = offer
	f.reservations[reservation.ID] = reservation
	return capacity.ClaimResult{Reservation: reservation, Offer: offer, Changed: true}, nil
}

func (f *schedulerCapacityFake) Release(_ context.Context, request capacity.ReleaseRequest) (capacity.ReleaseResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reservation, found := f.reservations[request.ReservationID]
	if !found {
		return capacity.ReleaseResult{}, capacity.ErrNotFound
	}
	if reservation.Status == capacity.ReservationReleased {
		return capacity.ReleaseResult{Reservation: reservation, Changed: false}, nil
	}
	if reservation.OrganizationID != request.OrganizationID || reservation.RunID != request.RunID || reservation.Generation != request.Generation {
		return capacity.ReleaseResult{}, capacity.ErrNotFound
	}
	now := time.Now().UTC()
	reservation.Status = capacity.ReservationReleased
	reservation.ReleasedAt = &now
	f.reservations[reservation.ID] = reservation
	f.used--
	return capacity.ReleaseResult{Reservation: reservation, Changed: true}, nil
}

func (f *schedulerCapacityFake) Reap(_ context.Context, request capacity.ReapRequest) (capacity.ReapResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	offer, found := f.offers[request.OfferID]
	if !found {
		return capacity.ReapResult{}, capacity.ErrNotFound
	}
	reservation := f.reservations[offer.ReservationID]
	if offer.Status != capacity.OfferLive || time.Now().UTC().Before(offer.ExpiresAt) {
		return capacity.ReapResult{Reservation: reservation, Offer: offer, Changed: false}, nil
	}
	offer.Status = capacity.OfferExpired
	reservation.Status = capacity.ReservationReleased
	f.offers[offer.ID] = offer
	f.reservations[reservation.ID] = reservation
	f.used--
	return capacity.ReapResult{Reservation: reservation, Offer: offer, Changed: true}, nil
}

func (f *schedulerCapacityFake) offersForReservationLocked(reservationID string) capacity.Offer {
	for _, offer := range f.offers {
		if offer.ReservationID == reservationID {
			return offer
		}
	}
	return capacity.Offer{}
}

func resourceID(resources []capacity.Resource, kind capacity.ResourceKind) string {
	for _, resource := range resources {
		if resource.Kind == kind {
			return resource.ID
		}
	}
	return ""
}

func TestSchedulerLastSlotClaimAndTerminalRelease(t *testing.T) {
	engine := newTestEngine()
	broker := newSchedulerCapacityFake(1)
	scheduler, err := NewScheduler(engine, broker)
	if err != nil {
		t.Fatalf("NewScheduler() error = %v", err)
	}
	firstRequest := validCreateRequest()
	first, err := engine.Create(context.Background(), firstRequest)
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	secondRequest := validCreateRequest()
	secondRequest.ConversationID = "conversation-b"
	secondRequest.IdempotencyKey = "create-b"
	secondRequest.InputID = "input-b"
	secondRequest.InputBody = "second"
	second, err := engine.Create(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}

	reserved, err := scheduler.Reserve(context.Background(), ReserveScheduleRequest{OrganizationID: "org-a", RunID: first.Run.ID, ExpectedVersion: first.Run.Version})
	if err != nil || !reserved.Reserved || reserved.Run.ReservationID == "" {
		t.Fatalf("first Reserve() = %#v, error = %v", reserved, err)
	}
	waiting, err := scheduler.Reserve(context.Background(), ReserveScheduleRequest{OrganizationID: "org-a", RunID: second.Run.ID, ExpectedVersion: second.Run.Version})
	if err != nil || waiting.Run.WaitReason != WaitComputerCapacity || waiting.Reserved {
		t.Fatalf("second Reserve() = %#v, error = %v", waiting, err)
	}

	preparing, err := scheduler.Claim(context.Background(), ClaimScheduleRequest{
		OrganizationID:  "org-a",
		RunID:           first.Run.ID,
		ExpectedVersion: reserved.Run.Version,
		OfferID:         reserved.OfferID,
		ComputerID:      "computer-a",
		GrantID:         "grant-a",
		GrantVersion:    3,
		IdempotencyKey:  "claim-a",
	})
	if err != nil || preparing.Run.State != StatePreparing {
		t.Fatalf("Claim() = %#v, error = %v", preparing, err)
	}
	duplicate, err := scheduler.Claim(context.Background(), ClaimScheduleRequest{
		OrganizationID:  "org-a",
		RunID:           first.Run.ID,
		ExpectedVersion: preparing.Run.Version,
		OfferID:         preparing.OfferID,
		ComputerID:      "computer-a",
		GrantID:         "grant-a",
		GrantVersion:    3,
		IdempotencyKey:  "claim-a-replay",
	})
	if err != nil || duplicate.Run.State != StatePreparing || duplicate.Changed {
		t.Fatalf("duplicate Claim() = %#v, error = %v", duplicate, err)
	}
	running, err := scheduler.StartRunning(context.Background(), StartRequest{OrganizationID: "org-a", RunID: first.Run.ID, ExpectedVersion: preparing.Run.Version})
	if err != nil || running.State != StateRunning {
		t.Fatalf("StartRunning() = %#v, error = %v", running, err)
	}
	completed, err := scheduler.Complete(context.Background(), CompleteRequest{OrganizationID: "org-a", RunID: first.Run.ID, ExpectedVersion: running.Version, Succeeded: true})
	if err != nil || completed.Run.State != StateSucceeded || completed.Reserved {
		t.Fatalf("Complete() = %#v, error = %v", completed, err)
	}

	retried, err := scheduler.Reserve(context.Background(), ReserveScheduleRequest{OrganizationID: "org-a", RunID: second.Run.ID, ExpectedVersion: waiting.Run.Version})
	if err != nil || !retried.Reserved {
		t.Fatalf("retry Reserve() = %#v, error = %v", retried, err)
	}
	if broker.used != 1 {
		t.Fatalf("broker used slots = %d, want 1", broker.used)
	}
}

func TestSchedulerOfferExpiryReturnsRunToWaitingQueue(t *testing.T) {
	engine := newTestEngine()
	broker := newSchedulerCapacityFake(1)
	scheduler, err := NewScheduler(engine, broker)
	if err != nil {
		t.Fatalf("NewScheduler() error = %v", err)
	}
	created, err := engine.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	reserved, err := scheduler.Reserve(context.Background(), ReserveScheduleRequest{OrganizationID: "org-a", RunID: created.Run.ID, ExpectedVersion: created.Run.Version})
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	broker.mu.Lock()
	offer := broker.offers[reserved.OfferID]
	offer.ExpiresAt = time.Now().UTC().Add(-time.Second)
	broker.offers[reserved.OfferID] = offer
	broker.mu.Unlock()
	reaped, err := scheduler.Reap(context.Background(), ReapScheduleRequest{
		OrganizationID:  "org-a",
		RunID:           created.Run.ID,
		ExpectedVersion: reserved.Run.Version,
		OfferID:         reserved.OfferID,
		IdempotencyKey:  "reap-a",
	})
	if err != nil || reaped.Run.WaitReason != WaitOfferExpired || reaped.Run.ReservationID != "" {
		t.Fatalf("Reap() = %#v, error = %v", reaped, err)
	}
}

func TestSchedulerRejectsMissingDependencies(t *testing.T) {
	if _, err := NewScheduler(nil, newSchedulerCapacityFake(1)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("nil engine error = %v", err)
	}
}
