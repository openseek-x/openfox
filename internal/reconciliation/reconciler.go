package reconciliation

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Reconciler is a deterministic in-memory seam for the same state transitions
// enforced by the PostgreSQL store. It is useful for fault injection without
// pretending that an in-memory map is restart recovery.
type Reconciler struct {
	mu        sync.Mutex
	clock     func() time.Time
	nextID    uint64
	cases     map[string]Case
	active    map[string]string
	evidence  map[string]Evidence
	manifests map[string]Manifest
}

func NewReconciler(clock func() time.Time) *Reconciler {
	if clock == nil {
		clock = time.Now
	}
	return &Reconciler{
		clock:     clock,
		cases:     make(map[string]Case),
		active:    make(map[string]string),
		evidence:  make(map[string]Evidence),
		manifests: make(map[string]Manifest),
	}
}

func (r *Reconciler) Open(ctx context.Context, request OpenRequest) (Case, error) {
	if err := contextError(ctx); err != nil {
		return Case{}, err
	}
	if err := request.Validate(); err != nil {
		return Case{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := caseKey(request.OrganizationID, request.RunID, request.Generation)
	if existingID, ok := r.active[key]; ok {
		return r.cases[existingID], nil
	}
	r.nextID++
	item := Case{
		OrganizationID: request.OrganizationID,
		ID:             fmt.Sprintf("00000000-0000-0000-0000-%012x", r.nextID),
		RunID:          request.RunID,
		Generation:     request.Generation,
		ReasonCode:     request.ReasonCode,
		FailureCode:    request.FailureCode,
		Status:         CaseOpen,
		SafeDetail:     request.SafeDetail,
		OpenedAt:       request.Now.UTC(),
		Version:        1,
	}
	if item.OpenedAt.IsZero() {
		item.OpenedAt = r.clock().UTC()
	}
	r.cases[item.ID] = item
	r.active[key] = item.ID
	r.manifests[item.ID] = request.Manifest
	return item, nil
}

func (r *Reconciler) SubmitEvidence(ctx context.Context, evidence Evidence) (Evidence, error) {
	if err := contextError(ctx); err != nil {
		return Evidence{}, err
	}
	if err := evidence.Validate(); err != nil {
		return Evidence{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.evidence[evidence.ID]; ok {
		if existing.PayloadHash != evidence.PayloadHash {
			return Evidence{}, ErrEvidenceConflict
		}
		return existing, nil
	}
	item, ok := r.cases[evidence.CaseID]
	if !ok || item.OrganizationID != evidence.OrganizationID || item.RunID != evidence.RunID || item.Generation != evidence.Generation {
		return Evidence{}, ErrNotFound
	}
	if item.Status != CaseOpen {
		return Evidence{}, fmt.Errorf("%w: case is not open", ErrVersionConflict)
	}
	if err := VerifyEvidence(r.manifests[evidence.CaseID], evidence); err != nil {
		evidence.AcceptedState = "STALE"
	} else {
		evidence.AcceptedState = "ACCEPTED"
	}
	evidence.CreatedAt = r.clock().UTC()
	if evidence.ObservedAt.IsZero() {
		evidence.ObservedAt = evidence.CreatedAt
	}
	r.evidence[evidence.ID] = evidence
	return evidence, nil
}

func (r *Reconciler) Resolve(ctx context.Context, request ResolveRequest) (Case, error) {
	if err := contextError(ctx); err != nil {
		return Case{}, err
	}
	if err := request.Validate(); err != nil {
		return Case{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.cases[request.CaseID]
	if !ok || item.OrganizationID != request.OrganizationID || item.RunID != request.RunID {
		return Case{}, ErrNotFound
	}
	if item.Status != CaseOpen {
		return item, nil
	}
	if item.Version != request.ExpectedVersion {
		return Case{}, ErrVersionConflict
	}
	evidence, ok := r.evidence[request.EvidenceID]
	if !ok {
		return Case{}, ErrNotFound
	}
	if err := ResolveWithEvidence(request, evidence); err != nil {
		return Case{}, err
	}
	now := request.Now.UTC()
	if now.IsZero() {
		now = r.clock().UTC()
	}
	item.Status = CaseResolved
	item.Resolution = request.Resolution
	item.ResolutionEvidenceID = request.EvidenceID
	item.ResolvedAt = &now
	item.ResolvedBy = request.ActorID
	item.SafeDetail = request.SafeDetail
	item.Version++
	r.cases[item.ID] = item
	delete(r.active, caseKey(item.OrganizationID, item.RunID, item.Generation))
	return item, nil
}

func (r *Reconciler) Get(ctx context.Context, organizationID, caseID string) (Case, error) {
	if err := contextError(ctx); err != nil {
		return Case{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.cases[caseID]
	if !ok || item.OrganizationID != organizationID {
		return Case{}, ErrNotFound
	}
	return item, nil
}

func caseKey(organizationID, runID string, generation int64) string {
	return fmt.Sprintf("%s/%s/%d", organizationID, runID, generation)
}
