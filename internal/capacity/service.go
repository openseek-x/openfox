package capacity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	defaultOfferTTL = 15 * time.Second
	minOfferTTL     = 5 * time.Second
	maxOfferTTL     = 60 * time.Second
)

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewReservationID() string
	NewOfferID() string
}

type Options struct {
	OfferTTL    time.Duration
	Clock       Clock
	IDGenerator IDGenerator
}

type Service struct {
	repository Repository
	clock      Clock
	offerTTL   time.Duration
	ids        IDGenerator
}

func NewService(repository Repository, options Options) (*Service, error) {
	if repository == nil {
		return nil, fmt.Errorf("%w: repository is nil", ErrInvalidRequest)
	}
	if options.OfferTTL == 0 {
		options.OfferTTL = defaultOfferTTL
	}
	if options.OfferTTL < minOfferTTL || options.OfferTTL > maxOfferTTL {
		return nil, fmt.Errorf("%w: offer TTL must be between %s and %s", ErrInvalidRequest, minOfferTTL, maxOfferTTL)
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	if options.IDGenerator == nil {
		options.IDGenerator = randomIDGenerator{}
	}
	return &Service{
		repository: repository,
		clock:      options.Clock,
		offerTTL:   options.OfferTTL,
		ids:        options.IDGenerator,
	}, nil
}

func (s *Service) Reserve(ctx context.Context, request ReserveRequest) (ReserveResult, error) {
	resources, computerID, err := validateReserveRequest(request)
	if err != nil {
		return ReserveResult{}, err
	}
	key := IdempotencyKey{
		Operation:      OperationReserve,
		OrganizationID: request.OrganizationID,
		RunID:          request.RunID,
		Generation:     request.Generation,
		ResourceKey:    resourceKey(resources),
		Value:          request.IdempotencyKey,
	}
	fingerprint, err := fingerprint(struct {
		OrganizationID string
		RunID          string
		Generation     int64
		GrantID        string
		GrantVersion   int64
		Resources      []Resource
	}{request.OrganizationID, request.RunID, request.Generation, request.GrantID, request.GrantVersion, resources})
	if err != nil {
		return ReserveResult{}, err
	}

	var result ReserveResult
	err = s.repository.WithinTransaction(ctx, func(tx Transaction) error {
		if existing, found, err := tx.FindIdempotency(ctx, key); err != nil {
			return err
		} else if found {
			if existing.Fingerprint != fingerprint {
				return ErrIdempotencyConflict
			}
			return s.loadReserveResult(ctx, tx, existing, false, &result)
		}

		if _, found, err := tx.FindEffectiveOffer(ctx, request.OrganizationID, request.RunID, request.Generation); err != nil {
			return err
		} else if found {
			return ErrOfferAlreadyExists
		}

		capacities, err := tx.LockCapacities(ctx, resources)
		if err != nil {
			return err
		}
		for i := range capacities {
			if err := capacities[i].Reserve(resources[i].Quantity); err != nil {
				return err
			}
		}
		for _, capacity := range capacities {
			if err := tx.SaveCapacity(ctx, capacity); err != nil {
				return err
			}
		}

		now := s.clock.Now()
		reservation := Reservation{
			ID:             s.ids.NewReservationID(),
			OrganizationID: request.OrganizationID,
			RunID:          request.RunID,
			Generation:     request.Generation,
			Resources:      cloneResources(resources),
			Status:         ReservationReserved,
			CreatedAt:      now,
		}
		offer := Offer{
			ID:               s.ids.NewOfferID(),
			ReservationID:    reservation.ID,
			OrganizationID:   request.OrganizationID,
			RunID:            request.RunID,
			Generation:       request.Generation,
			TargetComputerID: computerID,
			GrantID:          request.GrantID,
			GrantVersion:     request.GrantVersion,
			ExpiresAt:        now.Add(s.offerTTL),
			Status:           OfferLive,
		}
		if err := tx.CreateReservation(ctx, reservation); err != nil {
			return err
		}
		if err := tx.CreateOffer(ctx, offer); err != nil {
			return err
		}
		if err := tx.SaveIdempotency(ctx, IdempotencyRecord{
			Key:           key,
			Fingerprint:   fingerprint,
			ReservationID: reservation.ID,
			OfferID:       offer.ID,
			Changed:       true,
		}); err != nil {
			return err
		}
		result = ReserveResult{Reservation: reservation, Offer: offer, Created: true}
		return nil
	})
	if err != nil {
		return ReserveResult{}, err
	}
	return result, nil
}

func (s *Service) Claim(ctx context.Context, request ClaimRequest) (ClaimResult, error) {
	if err := validateClaimRequest(request); err != nil {
		return ClaimResult{}, err
	}
	key := IdempotencyKey{
		Operation:      OperationClaim,
		OrganizationID: request.OrganizationID,
		RunID:          request.RunID,
		Generation:     request.Generation,
		ResourceKey:    request.OfferID,
		Value:          request.IdempotencyKey,
	}
	fingerprint, err := fingerprint(request)
	if err != nil {
		return ClaimResult{}, err
	}

	var result ClaimResult
	var operationErr error
	err = s.repository.WithinTransaction(ctx, func(tx Transaction) error {
		if existing, found, err := tx.FindIdempotency(ctx, key); err != nil {
			return err
		} else if found {
			if existing.Fingerprint != fingerprint {
				return ErrIdempotencyConflict
			}
			return s.loadClaimResult(ctx, tx, existing, &result)
		}

		offer, found, err := tx.FindOffer(ctx, request.OfferID)
		if err != nil {
			return err
		}
		if !found || offer.OrganizationID != request.OrganizationID || offer.RunID != request.RunID {
			return ErrNotFound
		}
		if offer.Generation != request.Generation {
			return ErrStaleGeneration
		}
		if offer.TargetComputerID != request.ComputerID {
			return ErrWrongTarget
		}
		if offer.GrantID != request.GrantID || offer.GrantVersion != request.GrantVersion {
			return ErrGrantMismatch
		}

		reservation, found, err := tx.FindReservation(ctx, offer.ReservationID)
		if err != nil {
			return err
		}
		if !found || reservation.OrganizationID != request.OrganizationID || reservation.RunID != request.RunID || reservation.Generation != request.Generation {
			return fmt.Errorf("%w: offer reservation mismatch", ErrInvariantViolation)
		}
		capacities, err := tx.LockCapacities(ctx, reservation.Resources)
		if err != nil {
			return err
		}
		offer, err = tx.LockOffer(ctx, request.OfferID)
		if err != nil {
			return err
		}
		if offer.OrganizationID != request.OrganizationID || offer.RunID != request.RunID {
			return ErrNotFound
		}
		if offer.Generation != request.Generation {
			return ErrStaleGeneration
		}
		if offer.Status != OfferLive {
			return s.claimNonLiveError(offer.Status)
		}

		now := s.clock.Now()
		if !now.Before(offer.ExpiresAt) {
			if _, _, err := s.expireLockedOffer(ctx, tx, capacities, offer, now); err != nil {
				return err
			}
			// The expiry is committed, but the caller must not treat a failed
			// claim as a successful claim or automatically retry it.
			operationErr = ErrOfferExpired
			return nil
		}

		grant, found, err := tx.FindGrant(ctx, request.GrantID)
		if err != nil {
			return err
		}
		if !found || grant.Status != GrantActive {
			return ErrGrantRevoked
		}
		if grant.OrganizationID != request.OrganizationID || grant.ComputerID != request.ComputerID || grant.ID != request.GrantID || grant.Version != request.GrantVersion {
			return ErrGrantMismatch
		}

		reservation, err = tx.LockReservation(ctx, offer.ReservationID)
		if err != nil {
			return err
		}
		if reservation.Status != ReservationReserved {
			return fmt.Errorf("%w: claim requires reserved reservation", ErrInvariantViolation)
		}
		if !reservation.Status.canTransitionTo(ReservationClaimed) || !offer.Status.canTransitionTo(OfferClaimed) {
			return fmt.Errorf("%w: illegal claim transition", ErrInvariantViolation)
		}
		reservation.Status = ReservationClaimed
		reservation.ClaimedAt = timePointer(now)
		offer.Status = OfferClaimed
		offer.ClaimedAt = timePointer(now)
		if err := tx.SaveReservation(ctx, reservation); err != nil {
			return err
		}
		if err := tx.SaveOffer(ctx, offer); err != nil {
			return err
		}
		if err := tx.SaveIdempotency(ctx, IdempotencyRecord{
			Key:           key,
			Fingerprint:   fingerprint,
			ReservationID: reservation.ID,
			OfferID:       offer.ID,
			Changed:       true,
		}); err != nil {
			return err
		}
		result = ClaimResult{Reservation: reservation, Offer: offer, Changed: true}
		return nil
	})
	if err != nil {
		return ClaimResult{}, err
	}
	if operationErr != nil {
		return ClaimResult{}, operationErr
	}
	return result, nil
}

func (s *Service) Release(ctx context.Context, request ReleaseRequest) (ReleaseResult, error) {
	if err := validateReleaseRequest(request); err != nil {
		return ReleaseResult{}, err
	}
	key := IdempotencyKey{
		Operation:      OperationRelease,
		OrganizationID: request.OrganizationID,
		RunID:          request.RunID,
		Generation:     request.Generation,
		ResourceKey:    request.ReservationID,
		Value:          request.IdempotencyKey,
	}
	fingerprint, err := fingerprint(request)
	if err != nil {
		return ReleaseResult{}, err
	}

	var result ReleaseResult
	err = s.repository.WithinTransaction(ctx, func(tx Transaction) error {
		if existing, found, err := tx.FindIdempotency(ctx, key); err != nil {
			return err
		} else if found {
			if existing.Fingerprint != fingerprint {
				return ErrIdempotencyConflict
			}
			return s.loadReleaseResult(ctx, tx, existing, &result)
		}

		reservation, found, err := tx.FindReservation(ctx, request.ReservationID)
		if err != nil {
			return err
		}
		if !found || reservation.OrganizationID != request.OrganizationID || reservation.RunID != request.RunID || reservation.Generation != request.Generation {
			return ErrNotFound
		}
		capacities, err := tx.LockCapacities(ctx, reservation.Resources)
		if err != nil {
			return err
		}
		reservation, err = tx.LockReservation(ctx, request.ReservationID)
		if err != nil {
			return err
		}
		if reservation.Status == ReservationReleased {
			if err := tx.SaveIdempotency(ctx, IdempotencyRecord{Key: key, Fingerprint: fingerprint, ReservationID: reservation.ID, Changed: false}); err != nil {
				return err
			}
			result = ReleaseResult{Reservation: reservation, Changed: false}
			return nil
		}
		if reservation.Status != ReservationReserved && reservation.Status != ReservationClaimed {
			return ErrReservationNotReleasable
		}
		if !reservation.Status.canTransitionTo(ReservationReleased) {
			return fmt.Errorf("%w: illegal release transition", ErrInvariantViolation)
		}
		for i := range capacities {
			if err := capacities[i].Release(reservation.Resources[i].Quantity); err != nil {
				return err
			}
		}
		for _, capacity := range capacities {
			if err := tx.SaveCapacity(ctx, capacity); err != nil {
				return err
			}
		}
		now := s.clock.Now()
		reservation.Status = ReservationReleased
		reservation.ReleasedAt = timePointer(now)
		if err := tx.SaveReservation(ctx, reservation); err != nil {
			return err
		}
		if err := tx.SaveIdempotency(ctx, IdempotencyRecord{Key: key, Fingerprint: fingerprint, ReservationID: reservation.ID, Changed: true}); err != nil {
			return err
		}
		result = ReleaseResult{Reservation: reservation, Changed: true}
		return nil
	})
	if err != nil {
		return ReleaseResult{}, err
	}
	return result, nil
}

func (s *Service) Reap(ctx context.Context, request ReapRequest) (ReapResult, error) {
	if err := validateReapRequest(request); err != nil {
		return ReapResult{}, err
	}
	key := IdempotencyKey{
		Operation:      OperationReap,
		OrganizationID: request.OrganizationID,
		RunID:          request.RunID,
		Generation:     request.Generation,
		ResourceKey:    request.OfferID,
		Value:          request.IdempotencyKey,
	}
	fingerprint, err := fingerprint(request)
	if err != nil {
		return ReapResult{}, err
	}

	var result ReapResult
	err = s.repository.WithinTransaction(ctx, func(tx Transaction) error {
		if existing, found, err := tx.FindIdempotency(ctx, key); err != nil {
			return err
		} else if found {
			if existing.Fingerprint != fingerprint {
				return ErrIdempotencyConflict
			}
			return s.loadReapResult(ctx, tx, existing, &result)
		}

		offer, found, err := tx.FindOffer(ctx, request.OfferID)
		if err != nil {
			return err
		}
		if !found || offer.OrganizationID != request.OrganizationID || offer.RunID != request.RunID || offer.Generation != request.Generation {
			return ErrNotFound
		}
		reservation, found, err := tx.FindReservation(ctx, offer.ReservationID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: offer reservation missing", ErrInvariantViolation)
		}
		capacities, err := tx.LockCapacities(ctx, reservation.Resources)
		if err != nil {
			return err
		}
		offer, err = tx.LockOffer(ctx, request.OfferID)
		if err != nil {
			return err
		}
		if offer.Status != OfferLive {
			if offer.Status == OfferExpired || offer.Status == OfferClaimed || offer.Status == OfferCanceled {
				if err := tx.SaveIdempotency(ctx, IdempotencyRecord{Key: key, Fingerprint: fingerprint, ReservationID: offer.ReservationID, OfferID: offer.ID, Changed: false}); err != nil {
					return err
				}
				reservation, found, err := tx.FindReservation(ctx, offer.ReservationID)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("%w: offer reservation missing", ErrInvariantViolation)
				}
				result = ReapResult{Reservation: reservation, Offer: offer, Changed: false}
				return nil
			}
			return ErrOfferNotLive
		}
		now := s.clock.Now()
		if now.Before(offer.ExpiresAt) {
			return ErrOfferNotExpired
		}
		reservation, offer, err = s.expireLockedOffer(ctx, tx, capacities, offer, now)
		if err != nil {
			return err
		}
		if err := tx.SaveIdempotency(ctx, IdempotencyRecord{Key: key, Fingerprint: fingerprint, ReservationID: reservation.ID, OfferID: offer.ID, Changed: true}); err != nil {
			return err
		}
		result = ReapResult{Reservation: reservation, Offer: offer, Changed: true}
		return nil
	})
	if err != nil {
		return ReapResult{}, err
	}
	return result, nil
}

func (s *Service) expireLockedOffer(ctx context.Context, tx Transaction, capacities []Capacity, offer Offer, now time.Time) (Reservation, Offer, error) {
	reservation, err := tx.LockReservation(ctx, offer.ReservationID)
	if err != nil {
		return Reservation{}, Offer{}, err
	}
	if reservation.Status != ReservationReserved {
		return Reservation{}, Offer{}, fmt.Errorf("%w: expired live offer requires reserved reservation", ErrInvariantViolation)
	}
	if !offer.Status.canTransitionTo(OfferExpired) {
		return Reservation{}, Offer{}, fmt.Errorf("%w: illegal expiry transition", ErrInvariantViolation)
	}
	for i := range capacities {
		if err := capacities[i].Release(reservation.Resources[i].Quantity); err != nil {
			return Reservation{}, Offer{}, err
		}
	}
	for _, capacity := range capacities {
		if err := tx.SaveCapacity(ctx, capacity); err != nil {
			return Reservation{}, Offer{}, err
		}
	}
	reservation.Status = ReservationReleased
	reservation.ReleasedAt = timePointer(now)
	offer.Status = OfferExpired
	offer.ExpiredAt = timePointer(now)
	if err := tx.SaveReservation(ctx, reservation); err != nil {
		return Reservation{}, Offer{}, err
	}
	if err := tx.SaveOffer(ctx, offer); err != nil {
		return Reservation{}, Offer{}, err
	}
	return reservation, offer, nil
}

func (s *Service) loadReserveResult(ctx context.Context, tx Transaction, record IdempotencyRecord, created bool, result *ReserveResult) error {
	reservation, found, err := tx.FindReservation(ctx, record.ReservationID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: idempotency reservation missing", ErrInvariantViolation)
	}
	offer, found, err := tx.FindOffer(ctx, record.OfferID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: idempotency offer missing", ErrInvariantViolation)
	}
	*result = ReserveResult{Reservation: reservation, Offer: offer, Created: created}
	return nil
}

func (s *Service) loadClaimResult(ctx context.Context, tx Transaction, record IdempotencyRecord, result *ClaimResult) error {
	reservation, found, err := tx.FindReservation(ctx, record.ReservationID)
	if err != nil {
		return err
	}
	offer, offerFound, err := tx.FindOffer(ctx, record.OfferID)
	if err != nil {
		return err
	}
	if !found || !offerFound {
		return fmt.Errorf("%w: idempotency claim target missing", ErrInvariantViolation)
	}
	*result = ClaimResult{Reservation: reservation, Offer: offer, Changed: record.Changed}
	return nil
}

func (s *Service) loadReleaseResult(ctx context.Context, tx Transaction, record IdempotencyRecord, result *ReleaseResult) error {
	reservation, found, err := tx.FindReservation(ctx, record.ReservationID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: idempotency release target missing", ErrInvariantViolation)
	}
	*result = ReleaseResult{Reservation: reservation, Changed: record.Changed}
	return nil
}

func (s *Service) loadReapResult(ctx context.Context, tx Transaction, record IdempotencyRecord, result *ReapResult) error {
	reservation, found, err := tx.FindReservation(ctx, record.ReservationID)
	if err != nil {
		return err
	}
	offer, offerFound, err := tx.FindOffer(ctx, record.OfferID)
	if err != nil {
		return err
	}
	if !found || !offerFound {
		return fmt.Errorf("%w: idempotency reap target missing", ErrInvariantViolation)
	}
	*result = ReapResult{Reservation: reservation, Offer: offer, Changed: record.Changed}
	return nil
}

func (s *Service) claimNonLiveError(status OfferStatus) error {
	switch status {
	case OfferExpired:
		return ErrOfferExpired
	case OfferClaimed, OfferCanceled:
		return ErrOfferNotLive
	default:
		return fmt.Errorf("%w: unknown offer status %q", ErrInvariantViolation, status)
	}
}

func validateReserveRequest(request ReserveRequest) ([]Resource, string, error) {
	if strings.TrimSpace(request.OrganizationID) == "" || strings.TrimSpace(request.RunID) == "" || request.Generation <= 0 || strings.TrimSpace(request.GrantID) == "" || request.GrantVersion <= 0 || strings.TrimSpace(request.IdempotencyKey) == "" {
		return nil, "", fmt.Errorf("%w: incomplete reserve request", ErrInvalidRequest)
	}
	resources, err := normalizeResources(request.Resources)
	if err != nil {
		return nil, "", err
	}
	var organizationID, computerID string
	kindCounts := make(map[ResourceKind]int)
	for _, resource := range resources {
		kindCounts[resource.Kind]++
		switch resource.Kind {
		case ResourceOrganization:
			organizationID = resource.ID
		case ResourceComputer:
			computerID = resource.ID
		}
	}
	if kindCounts[ResourceOrganization] != 1 || kindCounts[ResourceComputer] != 1 || kindCounts[ResourceAgent] != 1 || organizationID != request.OrganizationID {
		return nil, "", fmt.Errorf("%w: reserve requires one organization, computer, and agent resource", ErrInvalidRequest)
	}
	return resources, computerID, nil
}

func validateClaimRequest(request ClaimRequest) error {
	if strings.TrimSpace(request.OrganizationID) == "" || strings.TrimSpace(request.RunID) == "" || request.Generation <= 0 || strings.TrimSpace(request.OfferID) == "" || strings.TrimSpace(request.ComputerID) == "" || strings.TrimSpace(request.GrantID) == "" || request.GrantVersion <= 0 || strings.TrimSpace(request.IdempotencyKey) == "" {
		return fmt.Errorf("%w: incomplete claim request", ErrInvalidRequest)
	}
	return nil
}

func validateReleaseRequest(request ReleaseRequest) error {
	if strings.TrimSpace(request.OrganizationID) == "" || strings.TrimSpace(request.RunID) == "" || request.Generation <= 0 || strings.TrimSpace(request.ReservationID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return fmt.Errorf("%w: incomplete release request", ErrInvalidRequest)
	}
	return nil
}

func validateReapRequest(request ReapRequest) error {
	if strings.TrimSpace(request.OrganizationID) == "" || strings.TrimSpace(request.RunID) == "" || request.Generation <= 0 || strings.TrimSpace(request.OfferID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return fmt.Errorf("%w: incomplete reap request", ErrInvalidRequest)
	}
	return nil
}

func normalizeResources(resources []Resource) ([]Resource, error) {
	if len(resources) == 0 {
		return nil, fmt.Errorf("%w: resources must not be empty", ErrInvalidRequest)
	}
	copyResources := cloneResources(resources)
	for _, resource := range copyResources {
		if !validResourceKind(resource.Kind) || strings.TrimSpace(resource.ID) == "" || resource.Quantity <= 0 {
			return nil, fmt.Errorf("%w: invalid resource", ErrInvalidRequest)
		}
	}
	sort.Slice(copyResources, func(i, j int) bool {
		leftRank := resourceRank(copyResources[i].Kind)
		rightRank := resourceRank(copyResources[j].Kind)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return copyResources[i].ID < copyResources[j].ID
	})
	for i := 1; i < len(copyResources); i++ {
		if copyResources[i-1].Kind == copyResources[i].Kind && copyResources[i-1].ID == copyResources[i].ID {
			return nil, fmt.Errorf("%w: duplicate resource", ErrInvalidRequest)
		}
	}
	return copyResources, nil
}

func resourceRank(kind ResourceKind) int {
	switch kind {
	case ResourceOrganization:
		return 0
	case ResourceComputer:
		return 1
	case ResourceAgent:
		return 2
	default:
		return 99
	}
}

func resourceKey(resources []Resource) string {
	var builder strings.Builder
	for _, resource := range resources {
		fmt.Fprintf(&builder, "%s:%s:%d|", resource.Kind, resource.ID, resource.Quantity)
	}
	return builder.String()
}

func cloneResources(resources []Resource) []Resource {
	return append([]Resource(nil), resources...)
}

func fingerprint(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: fingerprint request: %v", ErrInvalidRequest, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func timePointer(value time.Time) *time.Time {
	copyValue := value
	return &copyValue
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type randomIDGenerator struct{}

func (randomIDGenerator) NewReservationID() string { return newRandomID("reservation") }
func (randomIDGenerator) NewOfferID() string       { return newRandomID("offer") }

func newRandomID(prefix string) string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		// crypto/rand failure must not silently create a collision-prone ID.
		panic(fmt.Sprintf("capacity: generate %s id: %v", prefix, err))
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}
