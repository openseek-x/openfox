// Package workdir resolves and reserves local workspaces for an execution.
// It intentionally does not own conversation or run persistence; those
// callers can replace the in-memory binding registry with a durable store.
package workdir

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Mode string

const (
	ModeTempGit      Mode = "TEMP_GIT"
	ModeSpecifiedGit Mode = "SPECIFIED_GIT"
	ModeDirect       Mode = "DIRECT"
	ModeNoRepo       Mode = "NO_REPO"
)

var (
	ErrInvalidRequest        = errors.New("workdir: invalid request")
	ErrManagedRootRequired   = errors.New("workdir: managed root is required")
	ErrPathConflict          = errors.New("workdir: path is already reserved")
	ErrPathProtected         = errors.New("workdir: path is protected")
	ErrPathOutsideAllowed    = errors.New("workdir: path is outside allowed roots")
	ErrBindingConflict       = errors.New("workdir: binding conflicts with an existing binding")
	ErrBindingUnavailable    = errors.New("workdir: bound path is unavailable")
	ErrNotGitRepository      = errors.New("workdir: path is not a Git repository")
	ErrRepositoryUnavailable = errors.New("workdir: repository is unavailable")
	ErrGitOperation          = errors.New("workdir: Git operation failed")
	ErrCommitNotAllowed      = errors.New("workdir: result commits are not allowed for this workspace")
	ErrNoChanges             = errors.New("workdir: no changes to commit")
)

type BindingKey struct {
	OrganizationID string
	SpaceID        string
	AgentID        string
	ConversationID string
}

func (k BindingKey) valid() bool {
	return strings.TrimSpace(k.OrganizationID) != "" &&
		strings.TrimSpace(k.SpaceID) != "" &&
		strings.TrimSpace(k.ConversationID) != ""
}

type Request struct {
	// Shared keeps a stable managed directory across members and daemon restarts.
	Shared        bool
	LocalBaseOnly bool
	LocalHeadPath string
	Key           BindingKey
	Mode          Mode
	SourcePath    string
	RequestedPath string
	RepositoryURL string
	BaseRef       string
	Branch        string
}

func (r Request) validate() error {
	if !r.Key.valid() {
		return fmt.Errorf("%w: organization, space and conversation are required", ErrInvalidRequest)
	}
	switch r.Mode {
	case ModeTempGit, ModeSpecifiedGit:
		if strings.TrimSpace(r.SourcePath) == "" {
			return fmt.Errorf("%w: Git source path is required for %s", ErrInvalidRequest, r.Mode)
		}
	case ModeDirect:
		if strings.TrimSpace(r.RequestedPath) == "" {
			return fmt.Errorf("%w: direct path is required", ErrInvalidRequest)
		}
	case ModeNoRepo:
	default:
		return fmt.Errorf("%w: unsupported mode %q", ErrInvalidRequest, r.Mode)
	}
	if strings.IndexByte(r.SourcePath, 0) >= 0 || strings.IndexByte(r.RequestedPath, 0) >= 0 {
		return fmt.Errorf("%w: paths must not contain NUL", ErrInvalidRequest)
	}
	if strings.IndexByte(r.Branch, 0) >= 0 || strings.IndexByte(r.BaseRef, 0) >= 0 {
		return fmt.Errorf("%w: Git refs must not contain NUL", ErrInvalidRequest)
	}
	return nil
}

type GuardPolicy struct {
	ManagedRoot    string
	AllowedRoots   []string
	ProtectedRoots []string
	AllowNonGit    bool
	MaxPathLength  int
}

type Diagnostic struct {
	Code      string
	Operation string
	Message   string
	Detail    string
	Retriable bool
}

type PrepareError struct {
	Diagnostics []Diagnostic
	Err         error
}

func (e *PrepareError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return "workdir: preparation failed"
	}
	return e.Err.Error()
}

func (e *PrepareError) Unwrap() error { return e.Err }

type Binding struct {
	ID             string
	Key            BindingKey
	Mode           Mode
	ConfiguredPath string
	ActualPath     string
	RepositoryRoot string
	RepositoryURL  string
	BaseRef        string
	Branch         string
	Owned          bool
	IsGit          bool
	LockedAt       time.Time
	Version        uint64
}

type PreparedWorkspace struct {
	Binding        Binding
	Path           string
	RepositoryRoot string
	Branch         string
	IsGit          bool
	Owned          bool
	Diagnostics    []Diagnostic

	lease *Lease
}

func (w *PreparedWorkspace) Release() error {
	if w == nil || w.lease == nil {
		return nil
	}
	err := w.lease.Release()
	w.lease = nil
	return err
}

type CommitRequest struct {
	Message string
	// AuthorName / AuthorEmail 非空时同时作为 author 与 committer（`git -c user.name/user.email`，
	// 只作用于这次 git 子进程）。
	AuthorName  string
	AuthorEmail string
}

type repositoryLock struct {
	token chan struct{}
}

func newRepositoryLock() *repositoryLock {
	lock := &repositoryLock{token: make(chan struct{}, 1)}
	lock.token <- struct{}{}
	return lock
}
