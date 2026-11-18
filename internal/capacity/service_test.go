package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

// memoryRepository is a transactional test double only. It serializes the
// whole transaction and rolls back by snapshot replacement; it is not a
// substitute for PostgreSQL row locks, RLS, unique constraints, or capacity
// guarantees in production.
type memoryRepository struct {
	mu               sync.Mutex
	state            memoryState
	transactionCount int
	returnUnknown    bool
}

type memoryState struct {
	capacities   map[string]Capacity
	reservations map[string]Reservation
	offers       map[string]Offer
	grants       map[string]Grant
	idempotency  map[string]IdempotencyRecord
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{state: memoryState{
		capacities:   make(map[string]Capacity),
		reservations: make(map[string]Reservation),
		offers:       make(map[string]Offer),
		grants:       make(map[string]Grant),
		idempotency:  make(map[string]IdempotencyRecord),
	}}
}

func (r *memoryRepository) WithinTransaction(ctx context.Context, fn func(Transaction) error) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transactionCount++
	working := r.state.clone()
	tx := &memoryTransaction{state: &working}
	if err := fn(tx); err != nil {
		return err
	}
	if r.returnUnknown {
		return ErrUnknownResult
	}
	r.state = working
	return nil
}

func (r *memoryRepository) capacity(kind ResourceKind, id string) Capacity {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.capacities[capacityKey(kind, id)]
}

func (r *memoryRepository) setGrant(grant Grant) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.grants[grant.ID] = grant
}

func (r *memoryRepository) setCapacity(capacity Capacity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state.capacities[capacityKey(capacity.Kind, capacity.ID)] = capacity
}

func (r *memoryRepository) offer(id string) Offer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.offers[id]
}

func (r *memoryRepository) reservation(id string) Reservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.reservations[id]
}

func (r *memoryRepository) revokeGrant(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	grant := r.state.grants[id]
	grant.Status = GrantRevoked
	r.state.grants[id] = grant
}

type memoryTransaction struct {
	state            *memoryState
	capacitiesLocked bool
}

func (tx *memoryTransaction) FindIdempotency(_ context.Context, key IdempotencyKey) (IdempotencyRecord, bool, error) {
	record, found := tx.state.idempotency[idempotencyMapKey(key)]
	return record, found, nil
}

func (tx *memoryTransaction) SaveIdempotency(_ context.Context, record IdempotencyRecord) error {
	mapKey := idempotencyMapKey(record.Key)
	if existing, found := tx.state.idempotency[mapKey]; found && existing.Fingerprint != record.Fingerprint {
		return ErrIdempotencyConflict
	}
	tx.state.idempotency[mapKey] = record
	return nil
}

func (tx *memoryTransaction) FindEffectiveOffer(_ context.Context, organizationID, runID string, generation int64) (Offer, bool, error) {
	ids := make([]string, 0, len(tx.state.offers))
	for id, offer := range tx.state.offers {
		if offer.OrganizationID == organizationID && offer.RunID == runID && offer.Generation == generation && (offer.Status == OfferLive || offer.Status == OfferClaimed) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return Offer{}, false, nil
	}
	return cloneOffer(tx.state.offers[ids[0]]), true, nil
}

func (tx *memoryTransaction) FindReservation(_ context.Context, reservationID string) (Reservation, bool, error) {
	reservation, found := tx.state.reservations[reservationID]
	return cloneReservation(reservation), found, nil
}

func (tx *memoryTransaction) LockReservation(_ context.Context, reservationID string) (Reservation, error) {
	if !tx.capacitiesLocked {
		return Reservation{}, fmt.Errorf("%w: reservation lock before capacity lock", ErrInvariantViolation)
	}
	reservation, found := tx.state.reservations[reservationID]
	if !found {
		return Reservation{}, ErrNotFound
	}
	return cloneReservation(reservation), nil
}

func (tx *memoryTransaction) SaveReservation(_ context.Context, reservation Reservation) error {
	if _, found := tx.state.reservations[reservation.ID]; !found {
		return ErrNotFound
	}
	tx.state.reservations[reservation.ID] = cloneReservation(reservation)
	return nil
}

func (tx *memoryTransaction) FindOffer(_ context.Context, offerID string) (Offer, bool, error) {
	offer, found := tx.state.offers[offerID]
	return cloneOffer(offer), found, nil
}

func (tx *memoryTransaction) LockOffer(_ context.Context, offerID string) (Offer, error) {
	if !tx.capacitiesLocked {
		return Offer{}, fmt.Errorf("%w: offer lock before capacity lock", ErrInvariantViolation)
	}
	offer, found := tx.state.offers[offerID]
	if !found {
		return Offer{}, ErrNotFound
	}
	return cloneOffer(offer), nil
}

func (tx *memoryTransaction) SaveOffer(_ context.Context, offer Offer) error {
	if _, found := tx.state.offers[offer.ID]; !found {
		return ErrNotFound
	}
	tx.state.offers[offer.ID] = cloneOffer(offer)
	return nil
}

func (tx *memoryTransaction) LockCapacities(_ context.Context, resources []Resource) ([]Capacity, error) {
	if !resourcesSorted(resources) {
		return nil, fmt.Errorf("%w: capacity resources are not in protocol order", ErrInvariantViolation)
	}
	capacities := make([]Capacity, 0, len(resources))
	for _, resource := range resources {
		capacity, found := tx.state.capacities[capacityKey(resource.Kind, resource.ID)]
		if !found {
			return nil, ErrNotFound
		}
		capacities = append(capacities, capacity)
	}
	tx.capacitiesLocked = true
	return capacities, nil
}

func (tx *memoryTransaction) SaveCapacity(_ context.Context, capacity Capacity) error {
	if !tx.capacitiesLocked {
		return fmt.Errorf("%w: capacity save before capacity lock", ErrInvariantViolation)
	}
	if err := capacity.validate(); err != nil {
		return err
	}
	tx.state.capacities[capacityKey(capacity.Kind, capacity.ID)] = capacity
	return nil
}

func (tx *memoryTransaction) FindGrant(_ context.Context, grantID string) (Grant, bool, error) {
	grant, found := tx.state.grants[grantID]
	return grant, found, nil
}

func (tx *memoryTransaction) CreateReservation(_ context.Context, reservation Reservation) error {
	if _, found := tx.state.reservations[reservation.ID]; found {
		return fmt.Errorf("%w: duplicate reservation", ErrInvariantViolation)
	}
	tx.state.reservations[reservation.ID] = cloneReservation(reservation)
	return nil
}

func (tx *memoryTransaction) CreateOffer(_ context.Context, offer Offer) error {
	if _, found := tx.state.offers[offer.ID]; found {
		return fmt.Errorf("%w: duplicate offer", ErrInvariantViolation)
	}
	if _, found, err := tx.FindEffectiveOffer(context.Background(), offer.OrganizationID, offer.RunID, offer.Generation); err != nil {
		return err
	} else if found {
		return ErrOfferAlreadyExists
	}
	tx.state.offers[offer.ID] = cloneOffer(offer)
	return nil
}

func (s memoryState) clone() memoryState {
	clone := memoryState{
		capacities:   make(map[string]Capacity, len(s.capacities)),
		reservations: make(map[string]Reservation, len(s.reservations)),
		offers:       make(map[string]Offer, len(s.offers)),
		grants:       make(map[string]Grant, len(s.grants)),
		idempotency:  make(map[string]IdempotencyRecord, len(s.idempotency)),
	}
	for key, value := range s.capacities {
		clone.capacities[key] = value
	}
	for key, value := range s.reservations {
		clone.reservations[key] = cloneReservation(value)
	}
	for key, value := range s.offers {
		clone.offers[key] = cloneOffer(value)
	}
	for key, value := range s.grants {
		clone.grants[key] = value
	}
	for key, value := range s.idempotency {
		clone.idempotency[key] = value
	}
	return clone
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

type fakeIDs struct {
	mu     sync.Mutex
	nextID int
}

func (g *fakeIDs) next(prefix string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nextID++
	return fmt.Sprintf("%s-%d", prefix, g.nextID)
}

func (g *fakeIDs) NewReservationID() string { return g.next("reservation") }
func (g *fakeIDs) NewOfferID() string       { return g.next("offer") }

func newTestService(t *testing.T) (*Service, *memoryRepository, *fakeClock) {
	t.Helper()
	repository := newMemoryRepository()
	clock := &fakeClock{now: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)}
	service, err := NewService(repository, Options{OfferTTL: 15 * time.Second, Clock: clock, IDGenerator: &fakeIDs{}})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, repository, clock
}

func addCapacity(repository *memoryRepository, kind ResourceKind, id string, limit int64) {
	repository.setCapacity(Capacity{Kind: kind, ID: id, ConfiguredLimit: limit})
}

func addGrant(repository *memoryRepository, organizationID, computerID, grantID string, version int64) {
	repository.setGrant(Grant{ID: grantID, OrganizationID: organizationID, ComputerID: computerID, Version: version, Status: GrantActive})
}

func reserveRequest(organizationID, runID, agentID, computerID, grantID, key string) ReserveRequest {
	return ReserveRequest{
		OrganizationID: organizationID,
		RunID:          runID,
		Generation:     1,
		GrantID:        grantID,
		GrantVersion:   1,
		Resources: []Resource{
			{Kind: ResourceAgent, ID: agentID, Quantity: 1},
			{Kind: ResourceComputer, ID: computerID, Quantity: 1},
			{Kind: ResourceOrganization, ID: organizationID, Quantity: 1},
		},
		IdempotencyKey: key,
	}
}

func claimRequest(reservation Reservation, offer Offer, key string) ClaimRequest {
	return ClaimRequest{
		OrganizationID: reservation.OrganizationID,
		RunID:          reservation.RunID,
		Generation:     reservation.Generation,
		OfferID:        offer.ID,
		ComputerID:     offer.TargetComputerID,
		GrantID:        offer.GrantID,
		GrantVersion:   offer.GrantVersion,
		IdempotencyKey: key,
	}
}

func TestReserveLastSharedSlotAllowsOnlyOneOrganization(t *testing.T) {
	service, repository, _ := newTestService(t)
	addCapacity(repository, ResourceComputer, "computer-1", 1)
	for _, organizationID := range []string{"org-a", "org-b"} {
		addCapacity(repository, ResourceOrganization, organizationID, 1)
		addCapacity(repository, ResourceAgent, "agent-"+organizationID, 1)
		addGrant(repository, organizationID, "computer-1", "grant-"+organizationID, 1)
	}

	requests := []ReserveRequest{
		reserveRequest("org-a", "run-a", "agent-org-a", "computer-1", "grant-org-a", "reserve-a"),
		reserveRequest("org-b", "run-b", "agent-org-b", "computer-1", "grant-org-b", "reserve-b"),
	}
	type outcome struct {
		result ReserveResult
		err    error
	}
	outcomes := make(chan outcome, len(requests))
	var waitGroup sync.WaitGroup
	for _, request := range requests {
		request := request
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := service.Reserve(context.Background(), request)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	waitGroup.Wait()
	close(outcomes)

	successes := 0
	for outcome := range outcomes {
		if outcome.err == nil {
			successes++
			continue
		}
		if !errors.Is(outcome.err, ErrCapacityUnavailable) {
			t.Fatalf("Reserve() error = %v, want capacity unavailable", outcome.err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful reservations = %d, want 1", successes)
	}
	if got := repository.capacity(ResourceComputer, "computer-1").ReservedCount; got != 1 {
		t.Fatalf("shared computer reserved count = %d, want 1", got)
	}
}

func TestNewServiceRejectsOfferTTLOutsideContractRange(t *testing.T) {
	repository := newMemoryRepository()
	for _, ttl := range []time.Duration{time.Second, 61 * time.Second} {
		if _, err := NewService(repository, Options{OfferTTL: ttl}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("NewService(%s) error = %v, want invalid request", ttl, err)
		}
	}
}

func TestClaimLiveOfferAllowsOnlyOneWorker(t *testing.T) {
	service, repository, _ := newTestService(t)
	addCapacity(repository, ResourceOrganization, "org-a", 1)
	addCapacity(repository, ResourceComputer, "computer-1", 1)
	addCapacity(repository, ResourceAgent, "agent-a", 1)
	addGrant(repository, "org-a", "computer-1", "grant-a", 1)
	reserved, err := service.Reserve(context.Background(), reserveRequest("org-a", "run-a", "agent-a", "computer-1", "grant-a", "reserve-a"))
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}

	requests := []ClaimRequest{
		claimRequest(reserved.Reservation, reserved.Offer, "claim-a"),
		claimRequest(reserved.Reservation, reserved.Offer, "claim-b"),
	}
	type outcome struct {
		result ClaimResult
		err    error
	}
	outcomes := make(chan outcome, len(requests))
	var waitGroup sync.WaitGroup
	for _, request := range requests {
		request := request
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := service.Claim(context.Background(), request)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	waitGroup.Wait()
	close(outcomes)

	successes := 0
	for outcome := range outcomes {
		if outcome.err == nil {
			successes++
			if outcome.result.Offer.Status != OfferClaimed {
				t.Fatalf("claimed offer status = %q", outcome.result.Offer.Status)
			}
			continue
		}
		if !errors.Is(outcome.err, ErrOfferNotLive) {
			t.Fatalf("Claim() error = %v, want offer not live", outcome.err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful claims = %d, want 1", successes)
	}
}

func TestExpiredClaimAndReapCompeteForOneTransition(t *testing.T) {
	service, repository, clock := newTestService(t)
	addCapacity(repository, ResourceOrganization, "org-a", 1)
	addCapacity(repository, ResourceComputer, "computer-1", 1)
	addCapacity(repository, ResourceAgent, "agent-a", 1)
	addGrant(repository, "org-a", "computer-1", "grant-a", 1)
	reserved, err := service.Reserve(context.Background(), reserveRequest("org-a", "run-a", "agent-a", "computer-1", "grant-a", "reserve-a"))
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	clock.Advance(16 * time.Second)
	claim := claimRequest(reserved.Reservation, reserved.Offer, "claim-expired")
	reap := ReapRequest{OrganizationID: "org-a", RunID: "run-a", Generation: 1, OfferID: reserved.Offer.ID, IdempotencyKey: "reap-expired"}

	claimErrors := make(chan error, 1)
	reapResults := make(chan ReapResult, 1)
	reapErrors := make(chan error, 1)
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		_, err := service.Claim(context.Background(), claim)
		claimErrors <- err
	}()
	go func() {
		defer waitGroup.Done()
		result, err := service.Reap(context.Background(), reap)
		reapResults <- result
		reapErrors <- err
	}()
	waitGroup.Wait()
	close(claimErrors)
	close(reapResults)
	close(reapErrors)

	claimErr := <-claimErrors
	if !errors.Is(claimErr, ErrOfferExpired) {
		t.Fatalf("expired Claim() error = %v, want offer expired", claimErr)
	}
	if err := <-reapErrors; err != nil {
		t.Fatalf("Reap() error = %v", err)
	}
	<-reapResults
	if got := repository.capacity(ResourceComputer, "computer-1").ReservedCount; got != 0 {
		t.Fatalf("computer reserved count after expiry = %d, want 0", got)
	}
	if got := repository.offer(reserved.Offer.ID).Status; got != OfferExpired {
		t.Fatalf("offer status after expiry race = %q, want expired", got)
	}
	if got := repository.reservation(reserved.Reservation.ID).Status; got != ReservationReleased {
		t.Fatalf("reservation status after expiry race = %q, want released", got)
	}
}

func TestDuplicateReleaseAndReapAreIdempotentAndNonNegative(t *testing.T) {
	service, repository, clock := newTestService(t)
	addCapacity(repository, ResourceOrganization, "org-a", 1)
	addCapacity(repository, ResourceComputer, "computer-1", 1)
	addCapacity(repository, ResourceAgent, "agent-a", 1)
	addGrant(repository, "org-a", "computer-1", "grant-a", 1)
	reserved, err := service.Reserve(context.Background(), reserveRequest("org-a", "run-a", "agent-a", "computer-1", "grant-a", "reserve-release"))
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	release := ReleaseRequest{OrganizationID: "org-a", RunID: "run-a", Generation: 1, ReservationID: reserved.Reservation.ID, IdempotencyKey: "release-once"}
	if result, err := service.Release(context.Background(), release); err != nil || !result.Changed {
		t.Fatalf("first Release() = %#v, error = %v", result, err)
	}
	if result, err := service.Release(context.Background(), release); err != nil || !result.Changed {
		t.Fatalf("idempotent Release() = %#v, error = %v", result, err)
	}
	if result, err := service.Release(context.Background(), ReleaseRequest{OrganizationID: "org-a", RunID: "run-a", Generation: 1, ReservationID: reserved.Reservation.ID, IdempotencyKey: "release-replay"}); err != nil || result.Changed {
		t.Fatalf("duplicate Release() with a new key = %#v, error = %v", result, err)
	}
	for _, kind := range []ResourceKind{ResourceOrganization, ResourceComputer, ResourceAgent} {
		if got := repository.capacity(kind, capacityID(kind)).ReservedCount; got != 0 {
			t.Fatalf("%s reserved count = %d, want 0", kind, got)
		}
	}

	reserved, err = service.Reserve(context.Background(), reserveRequest("org-a", "run-b", "agent-a", "computer-1", "grant-a", "reserve-reap"))
	if err != nil {
		t.Fatalf("second Reserve() error = %v", err)
	}
	clock.Advance(16 * time.Second)
	reap := ReapRequest{OrganizationID: "org-a", RunID: "run-b", Generation: 1, OfferID: reserved.Offer.ID, IdempotencyKey: "reap-once"}
	if result, err := service.Reap(context.Background(), reap); err != nil || !result.Changed {
		t.Fatalf("first Reap() = %#v, error = %v", result, err)
	}
	if result, err := service.Reap(context.Background(), reap); err != nil || !result.Changed {
		t.Fatalf("idempotent Reap() = %#v, error = %v", result, err)
	}
	if result, err := service.Reap(context.Background(), ReapRequest{OrganizationID: "org-a", RunID: "run-b", Generation: 1, OfferID: reserved.Offer.ID, IdempotencyKey: "reap-replay"}); err != nil || result.Changed {
		t.Fatalf("duplicate Reap() with a new key = %#v, error = %v", result, err)
	}
	if got := repository.capacity(ResourceComputer, "computer-1").ReservedCount; got != 0 {
		t.Fatalf("computer reserved count after duplicate reap = %d, want 0", got)
	}
}

func TestReleaseAfterClaimDecrementsCapacityOnce(t *testing.T) {
	service, repository, _ := newTestService(t)
	addCapacity(repository, ResourceOrganization, "org-a", 1)
	addCapacity(repository, ResourceComputer, "computer-1", 1)
	addCapacity(repository, ResourceAgent, "agent-a", 1)
	addGrant(repository, "org-a", "computer-1", "grant-a", 1)
	reserved, err := service.Reserve(context.Background(), reserveRequest("org-a", "run-claimed", "agent-a", "computer-1", "grant-a", "reserve-claimed"))
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if _, err := service.Claim(context.Background(), claimRequest(reserved.Reservation, reserved.Offer, "claim-claimed")); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	release := ReleaseRequest{
		OrganizationID: "org-a",
		RunID:          "run-claimed",
		Generation:     1,
		ReservationID:  reserved.Reservation.ID,
		IdempotencyKey: "release-claimed",
	}
	result, err := service.Release(context.Background(), release)
	if err != nil || !result.Changed || result.Reservation.Status != ReservationReleased {
		t.Fatalf("Release() after claim = %#v, error = %v", result, err)
	}
	replay, err := service.Release(context.Background(), ReleaseRequest{
		OrganizationID: "org-a",
		RunID:          "run-claimed",
		Generation:     1,
		ReservationID:  reserved.Reservation.ID,
		IdempotencyKey: "release-claimed-replay",
	})
	if err != nil || replay.Changed {
		t.Fatalf("Release() replay after claim = %#v, error = %v", replay, err)
	}
	for _, kind := range []ResourceKind{ResourceOrganization, ResourceComputer, ResourceAgent} {
		if got := repository.capacity(kind, capacityID(kind)).ReservedCount; got != 0 {
			t.Fatalf("%s reserved count = %d, want 0", kind, got)
		}
	}
}

func TestClaimRejectsStaleGenerationAndRevokedGrant(t *testing.T) {
	service, repository, _ := newTestService(t)
	addCapacity(repository, ResourceOrganization, "org-a", 1)
	addCapacity(repository, ResourceComputer, "computer-1", 1)
	addCapacity(repository, ResourceAgent, "agent-a", 1)
	addGrant(repository, "org-a", "computer-1", "grant-a", 1)
	reserved, err := service.Reserve(context.Background(), reserveRequest("org-a", "run-a", "agent-a", "computer-1", "grant-a", "reserve-claim-guards"))
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	stale := claimRequest(reserved.Reservation, reserved.Offer, "claim-stale-generation")
	stale.Generation = 2
	if _, err := service.Claim(context.Background(), stale); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale generation Claim() error = %v, want stale generation", err)
	}
	repository.revokeGrant("grant-a")
	if _, err := service.Claim(context.Background(), claimRequest(reserved.Reservation, reserved.Offer, "claim-revoked-grant")); !errors.Is(err, ErrGrantRevoked) {
		t.Fatalf("revoked grant Claim() error = %v, want grant revoked", err)
	}
	if got := repository.capacity(ResourceComputer, "computer-1").ReservedCount; got != 1 {
		t.Fatalf("computer reserved count after rejected claims = %d, want 1", got)
	}
}

func TestIdempotencyParameterConflictIsNotRetriedAsSuccess(t *testing.T) {
	service, repository, _ := newTestService(t)
	addCapacity(repository, ResourceOrganization, "org-a", 2)
	addCapacity(repository, ResourceComputer, "computer-1", 2)
	addCapacity(repository, ResourceAgent, "agent-a", 2)
	addGrant(repository, "org-a", "computer-1", "grant-a", 1)
	request := reserveRequest("org-a", "run-a", "agent-a", "computer-1", "grant-a", "same-key")
	if _, err := service.Reserve(context.Background(), request); err != nil {
		t.Fatalf("first Reserve() error = %v", err)
	}
	request.GrantVersion = 2
	if _, err := service.Reserve(context.Background(), request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting Reserve() error = %v, want idempotency conflict", err)
	}
}

func TestUnknownTransactionResultIsNotRetried(t *testing.T) {
	service, repository, _ := newTestService(t)
	repository.returnUnknown = true
	addCapacity(repository, ResourceOrganization, "org-a", 1)
	addCapacity(repository, ResourceComputer, "computer-1", 1)
	addCapacity(repository, ResourceAgent, "agent-a", 1)
	addGrant(repository, "org-a", "computer-1", "grant-a", 1)
	if _, err := service.Reserve(context.Background(), reserveRequest("org-a", "run-a", "agent-a", "computer-1", "grant-a", "unknown-result")); !errors.Is(err, ErrUnknownResult) {
		t.Fatalf("Reserve() error = %v, want unknown result", err)
	}
	if repository.transactionCount != 1 {
		t.Fatalf("transaction attempts = %d, want 1", repository.transactionCount)
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidRequest)
	}
	return ctx.Err()
}

func idempotencyMapKey(key IdempotencyKey) string {
	encoded, _ := json.Marshal(key)
	return string(encoded)
}

func capacityKey(kind ResourceKind, id string) string {
	return string(kind) + "\x00" + id
}

func capacityID(kind ResourceKind) string {
	switch kind {
	case ResourceOrganization:
		return "org-a"
	case ResourceComputer:
		return "computer-1"
	case ResourceAgent:
		return "agent-a"
	default:
		return ""
	}
}

func resourcesSorted(resources []Resource) bool {
	for i := 1; i < len(resources); i++ {
		if resourceRank(resources[i-1].Kind) > resourceRank(resources[i].Kind) || (resources[i-1].Kind == resources[i].Kind && resources[i-1].ID >= resources[i].ID) {
			return false
		}
	}
	return true
}

func cloneReservation(reservation Reservation) Reservation {
	reservation.Resources = cloneResources(reservation.Resources)
	if reservation.ClaimedAt != nil {
		reservation.ClaimedAt = timePointer(*reservation.ClaimedAt)
	}
	if reservation.ReleasedAt != nil {
		reservation.ReleasedAt = timePointer(*reservation.ReleasedAt)
	}
	return reservation
}

func cloneOffer(offer Offer) Offer {
	if offer.ClaimedAt != nil {
		offer.ClaimedAt = timePointer(*offer.ClaimedAt)
	}
	if offer.ExpiredAt != nil {
		offer.ExpiredAt = timePointer(*offer.ExpiredAt)
	}
	if offer.CanceledAt != nil {
		offer.CanceledAt = timePointer(*offer.CanceledAt)
	}
	return offer
}
