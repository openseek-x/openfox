package workdir

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Manager struct {
	policy   GuardPolicy
	locks    *LockManager
	bindings *BindingRegistry
	git      GitRunner

	prepareMu sync.Mutex
	repoMu    sync.Mutex
	repos     map[string]*repositoryLock
}

func NewManager(policy GuardPolicy) (*Manager, error) {
	return NewManagerWithDependencies(policy, NewLockManager(), NewBindingRegistry(), execGitRunner{})
}

func NewManagerWithDependencies(policy GuardPolicy, locks *LockManager, bindings *BindingRegistry, git GitRunner) (*Manager, error) {
	if locks == nil || bindings == nil || git == nil {
		return nil, fmt.Errorf("%w: workdir dependencies are required", ErrInvalidRequest)
	}
	normalized, err := policy.normalize()
	if err != nil {
		return nil, err
	}
	return &Manager{
		policy:   normalized,
		locks:    locks,
		bindings: bindings,
		git:      git,
		repos:    make(map[string]*repositoryLock),
	}, nil
}

func (m *Manager) Bindings() *BindingRegistry {
	if m == nil {
		return nil
	}
	return m.bindings
}

func (m *Manager) Prepare(ctx context.Context, req Request) (*PreparedWorkspace, error) {
	if m == nil {
		return nil, fmt.Errorf("%w: manager is nil", ErrInvalidRequest)
	}
	if err := req.validate(); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}

	m.prepareMu.Lock()
	defer m.prepareMu.Unlock()
	if req.Shared {
		return m.prepareShared(ctx, req)
	}
	if existing, ok := m.bindings.Lookup(req.Key); ok {
		// 同一模式下沿用首次绑定的目录（默认路径变化不迁移）。
		if existing.Mode == req.Mode {
			return m.reopen(ctx, existing)
		}
		// 模式变化（例如 Agent 从 NONE 改为 EXPLICIT）时旧目录不再适用：解除旧绑定并按新请求准备。
		// 旧目录保留在磁盘上，不做删除。
		if err := m.bindings.Unbind(existing.Key, existing.Version); err != nil {
			return nil, err
		}
	}
	workspace, err := m.prepareFresh(ctx, req)
	if err != nil {
		return nil, err
	}
	binding := Binding{
		Key:            req.Key,
		Mode:           req.Mode,
		ConfiguredPath: firstNonEmpty(req.RequestedPath, req.SourcePath),
		ActualPath:     workspace.Path,
		RepositoryRoot: workspace.RepositoryRoot,
		RepositoryURL:  strings.TrimSpace(req.RepositoryURL),
		BaseRef:        effectiveBase(req.BaseRef),
		Branch:         workspace.Branch,
		Owned:          workspace.Owned,
		IsGit:          workspace.IsGit,
	}
	bound, created, err := m.bindings.Bind(binding)
	if err != nil {
		_ = workspace.Release()
		return nil, err
	}
	if !created {
		_ = workspace.Release()
		return m.reopen(ctx, bound)
	}
	workspace.Binding = bound
	return &workspace, nil
}

func (m *Manager) prepareFresh(ctx context.Context, req Request) (PreparedWorkspace, error) {
	switch req.Mode {
	case ModeTempGit, ModeSpecifiedGit:
		workspace, err := m.prepareGit(ctx, req)
		if err != nil {
			return PreparedWorkspace{}, err
		}
		lease, err := m.acquire(ctx, workspace.Path, req.Key)
		if err != nil {
			return PreparedWorkspace{}, err
		}
		workspace.lease = lease
		return workspace, nil
	case ModeDirect:
		return m.prepareDirect(ctx, req)
	case ModeNoRepo:
		return m.prepareNoRepo(ctx, req)
	default:
		return PreparedWorkspace{}, fmt.Errorf("%w: unsupported mode %q", ErrInvalidRequest, req.Mode)
	}
}

func (m *Manager) prepareDirect(ctx context.Context, req Request) (PreparedWorkspace, error) {
	if err := contextErr(ctx); err != nil {
		return PreparedWorkspace{}, err
	}
	info, err := os.Stat(req.RequestedPath)
	if err != nil {
		return PreparedWorkspace{}, &PrepareError{Diagnostics: []Diagnostic{{
			Code: "DIRECT_PATH_UNAVAILABLE", Operation: "stat", Message: "direct path is unavailable", Detail: err.Error(), Retriable: false,
		}}, Err: fmt.Errorf("%w: %v", ErrRepositoryUnavailable, err)}
	}
	if !info.IsDir() {
		return PreparedWorkspace{}, fmt.Errorf("%w: direct path is not a directory", ErrInvalidRequest)
	}
	resolved, err := m.policy.check(req.RequestedPath)
	if err != nil {
		return PreparedWorkspace{}, err
	}
	isGit := false
	if _, err := m.git.Run(ctx, resolved, "rev-parse", "--show-toplevel"); err == nil {
		isGit = true
	}
	if !isGit && !m.policy.AllowNonGit {
		return PreparedWorkspace{}, fmt.Errorf("%w: non-Git direct paths are disabled", ErrNotGitRepository)
	}
	lease, err := m.acquire(ctx, resolved, req.Key)
	if err != nil {
		return PreparedWorkspace{}, err
	}
	return PreparedWorkspace{Path: resolved, IsGit: isGit, Owned: false, lease: lease}, nil
}

func (m *Manager) prepareNoRepo(ctx context.Context, req Request) (PreparedWorkspace, error) {
	root, err := m.managedRoot()
	if err != nil {
		return PreparedWorkspace{}, err
	}
	path := root
	if strings.TrimSpace(req.RequestedPath) != "" {
		path = req.RequestedPath
		resolved, checkErr := m.policy.check(path)
		if checkErr != nil {
			return PreparedWorkspace{}, checkErr
		}
		path = resolved
		if !pathContains(root, path) {
			return PreparedWorkspace{}, fmt.Errorf("%w: no-repo path must be under the managed root", ErrPathOutsideAllowed)
		}
	}
	if path == root {
		path = filepath.Join(root, "office-"+m.nextID())
	}
	if err := ensureDirectory(path, true); err != nil {
		return PreparedWorkspace{}, err
	}
	resolved, err := m.policy.check(path)
	if err != nil {
		return PreparedWorkspace{}, err
	}
	lease, err := m.acquire(ctx, resolved, req.Key)
	if err != nil {
		return PreparedWorkspace{}, err
	}
	return PreparedWorkspace{Path: resolved, Owned: true, lease: lease}, nil
}

func (m *Manager) reopen(ctx context.Context, binding Binding) (*PreparedWorkspace, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	info, err := os.Stat(binding.ActualPath)
	if err != nil || !info.IsDir() {
		return nil, &PrepareError{Diagnostics: []Diagnostic{{
			Code: "BOUND_PATH_UNAVAILABLE", Operation: "stat", Message: "bound workspace is unavailable", Detail: errString(err), Retriable: true,
		}}, Err: ErrBindingUnavailable}
	}
	resolved, err := m.policy.check(binding.ActualPath)
	if err != nil {
		return nil, err
	}
	if resolved != binding.ActualPath {
		return nil, fmt.Errorf("%w: canonical path changed from %s to %s", ErrBindingUnavailable, binding.ActualPath, resolved)
	}
	lease, err := m.acquire(ctx, resolved, binding.Key)
	if err != nil {
		return nil, err
	}
	return &PreparedWorkspace{
		Binding:        binding,
		Path:           resolved,
		RepositoryRoot: binding.RepositoryRoot,
		Branch:         binding.Branch,
		IsGit:          binding.IsGit,
		Owned:          binding.Owned,
		lease:          lease,
	}, nil
}

func (m *Manager) acquire(ctx context.Context, path string, key BindingKey) (*Lease, error) {
	return m.locks.Acquire(ctx, path, LockOwner{
		OrganizationID: key.OrganizationID,
		SpaceID:        key.SpaceID,
		AgentID:        key.AgentID,
		ConversationID: key.ConversationID,
	})
}

func (m *Manager) managedRoot() (string, error) {
	if strings.TrimSpace(m.policy.ManagedRoot) == "" {
		return "", ErrManagedRootRequired
	}
	if err := ensureDirectory(m.policy.ManagedRoot, true); err != nil {
		return "", fmt.Errorf("%w: %v", ErrManagedRootRequired, err)
	}
	resolved, err := m.policy.check(m.policy.ManagedRoot)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func (m *Manager) newManagedPath(branch string) (string, error) {
	root, err := m.managedRoot()
	if err != nil {
		return "", err
	}
	safe := strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(branch)
	path := filepath.Join(root, "git-"+safe+"-"+m.nextID())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		if err == nil {
			return "", fmt.Errorf("%w: generated workspace already exists", ErrPathConflict)
		}
		return "", err
	}
	return path, nil
}

func (m *Manager) nextID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return "local"
}

func (m *Manager) repoLock(ctx context.Context, repo string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.repoMu.Lock()
	lock := m.repos[repo]
	if lock == nil {
		lock = newRepositoryLock()
		m.repos[repo] = lock
	}
	m.repoMu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-lock.token:
		return nil
	}
}

func (m *Manager) releaseRepoLock(repo string) {
	m.repoMu.Lock()
	lock := m.repos[repo]
	m.repoMu.Unlock()
	if lock != nil {
		lock.token <- struct{}{}
	}
}

func (m *Manager) CommitResult(ctx context.Context, workspace *PreparedWorkspace, req CommitRequest) (string, error) {
	return m.commitResult(ctx, workspace, req)
}

func effectiveBase(ref string) string {
	if strings.TrimSpace(ref) == "" {
		return "HEAD"
	}
	return strings.TrimSpace(ref)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return "path is not a directory"
	}
	return err.Error()
}
