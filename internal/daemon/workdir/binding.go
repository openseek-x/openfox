package workdir

import (
	"fmt"
	"sync"
	"time"
)

type BindingRegistry struct {
	mu     sync.RWMutex
	nextID uint64
	byKey  map[BindingKey]Binding
	byID   map[string]BindingKey
	clock  func() time.Time
}

func NewBindingRegistry() *BindingRegistry {
	return &BindingRegistry{
		byKey: make(map[BindingKey]Binding),
		byID:  make(map[string]BindingKey),
		clock: time.Now,
	}
}

func (r *BindingRegistry) Lookup(key BindingKey) (Binding, bool) {
	if r == nil {
		return Binding{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	binding, ok := r.byKey[key]
	return binding, ok
}

func (r *BindingRegistry) Bind(candidate Binding) (Binding, bool, error) {
	if r == nil || !candidate.Key.valid() {
		return Binding{}, false, fmt.Errorf("%w: binding key is incomplete", ErrInvalidRequest)
	}
	if candidate.ActualPath == "" || candidate.Mode == "" {
		return Binding{}, false, fmt.Errorf("%w: binding path and mode are required", ErrInvalidRequest)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.byKey[candidate.Key]; ok {
		return existing, false, nil
	}
	for _, existing := range r.byKey {
		if pathContains(existing.ActualPath, candidate.ActualPath) || pathContains(candidate.ActualPath, existing.ActualPath) {
			return Binding{}, false, &PathConflictError{
				RequestedPath: candidate.ActualPath,
				HeldPath:      existing.ActualPath,
				Owner: LockOwner{
					OrganizationID: existing.Key.OrganizationID,
					SpaceID:        existing.Key.SpaceID,
					AgentID:        existing.Key.AgentID,
					ConversationID: existing.Key.ConversationID,
				},
			}
		}
	}
	if candidate.ID == "" {
		r.nextID++
		candidate.ID = fmt.Sprintf("binding-%d", r.nextID)
	}
	if _, exists := r.byID[candidate.ID]; exists {
		return Binding{}, false, fmt.Errorf("%w: binding id %q already exists", ErrBindingConflict, candidate.ID)
	}
	if candidate.LockedAt.IsZero() {
		candidate.LockedAt = r.clock()
	}
	if candidate.Version == 0 {
		candidate.Version = 1
	}
	r.byKey[candidate.Key] = candidate
	r.byID[candidate.ID] = candidate.Key
	return candidate, true, nil
}

func (r *BindingRegistry) Unbind(key BindingKey, version uint64) error {
	if r == nil {
		return fmt.Errorf("%w: registry is nil", ErrInvalidRequest)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	binding, ok := r.byKey[key]
	if !ok {
		return ErrBindingUnavailable
	}
	if version != binding.Version {
		return fmt.Errorf("%w: expected %d, got %d", ErrBindingConflict, binding.Version, version)
	}
	delete(r.byKey, key)
	delete(r.byID, binding.ID)
	return nil
}

func (r *BindingRegistry) UpdateVersion(key BindingKey, version uint64) (Binding, error) {
	if r == nil {
		return Binding{}, fmt.Errorf("%w: registry is nil", ErrInvalidRequest)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	binding, ok := r.byKey[key]
	if !ok {
		return Binding{}, ErrBindingUnavailable
	}
	if version != binding.Version {
		return Binding{}, fmt.Errorf("%w: expected %d, got %d", ErrBindingConflict, binding.Version, version)
	}
	binding.Version++
	r.byKey[key] = binding
	return binding, nil
}
