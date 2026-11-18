package workdir

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

type LockOwner struct {
	OrganizationID string
	SpaceID        string
	AgentID        string
	ConversationID string
	RunID          string
}

type heldLock struct {
	path  string
	owner LockOwner
	lease *Lease
}

type LockManager struct {
	mu     sync.Mutex
	byPath map[string]*heldLock
}

type Lease struct {
	manager *LockManager
	path    string
	owner   LockOwner

	once sync.Once
	err  error
}

func NewLockManager() *LockManager {
	return &LockManager{byPath: make(map[string]*heldLock)}
}

func (m *LockManager) Acquire(ctx context.Context, path string, owner LockOwner) (*Lease, error) {
	if m == nil {
		return nil, fmt.Errorf("%w: lock manager is nil", ErrInvalidRequest)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	resolved, err := canonicalPath(path)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for heldPath, held := range m.byPath {
		if pathContains(heldPath, resolved) || pathContains(resolved, heldPath) {
			return nil, &PathConflictError{RequestedPath: resolved, HeldPath: heldPath, Owner: held.owner}
		}
	}
	lease := &Lease{manager: m, path: resolved, owner: owner}
	m.byPath[resolved] = &heldLock{path: resolved, owner: owner, lease: lease}
	return lease, nil
}

func (l *Lease) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func (l *Lease) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.manager == nil {
			return
		}
		l.manager.mu.Lock()
		defer l.manager.mu.Unlock()
		held, ok := l.manager.byPath[l.path]
		if !ok || held.lease != l {
			l.err = fmt.Errorf("%w: lease is not held", ErrPathConflict)
			return
		}
		delete(l.manager.byPath, l.path)
	})
	return l.err
}

type PathConflictError struct {
	RequestedPath string
	HeldPath      string
	Owner         LockOwner
}

func (e *PathConflictError) Error() string {
	if e == nil {
		return ErrPathConflict.Error()
	}
	return fmt.Sprintf("%s: requested %s overlaps held path %s", ErrPathConflict, e.RequestedPath, e.HeldPath)
}

func (e *PathConflictError) Unwrap() error { return ErrPathConflict }

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func ownerKey(owner LockOwner) string {
	return strings.Join([]string{owner.OrganizationID, owner.SpaceID, owner.AgentID, owner.ConversationID, owner.RunID}, "/")
}
