package reconciliation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LocalJournal is the daemon-side persistence-neutral file boundary. It only
// writes typed manifests and append-only facts; it never deletes a run to
// recover from a corrupt or incomplete journal.
type LocalJournal struct {
	Root string
	mu   sync.Mutex
}

type JournalEntry struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	RunID          string         `json:"run_id"`
	Generation     int64          `json:"generation"`
	Kind           EvidenceKind   `json:"kind"`
	Source         EvidenceSource `json:"source"`
	ProcessState   ProcessState   `json:"process_state"`
	ProcessExited  bool           `json:"process_exited"`
	ExitCode       *int           `json:"exit_code,omitempty"`
	ObservedAt     time.Time      `json:"observed_at"`
	SafeDetail     string         `json:"safe_detail,omitempty"`
}

func NewLocalJournal(root string) (*LocalJournal, error) {
	root = strings.TrimSpace(root)
	if root == "" || strings.ContainsRune(root, '\x00') {
		return nil, fmt.Errorf("%w: journal root is invalid", ErrInvalidRequest)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create journal root: %w", err)
	}
	return &LocalJournal{Root: root}, nil
}

func (j *LocalJournal) WriteManifest(ctx context.Context, manifest Manifest) (string, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	if j == nil || j.Root == "" {
		return "", fmt.Errorf("%w: journal is not configured", ErrInvalidRequest)
	}
	hash, err := manifest.Hash()
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')

	j.mu.Lock()
	defer j.mu.Unlock()
	directory, err := j.runDirectory(manifest.OrganizationID, manifest.RunID, manifest.Generation)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create run journal directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".manifest-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create manifest temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("protect manifest: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("write manifest: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("sync manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close manifest: %w", err)
	}
	path := filepath.Join(directory, "manifest.json")
	if err := os.Rename(temporaryName, path); err != nil {
		return "", fmt.Errorf("publish manifest: %w", err)
	}
	return hash, nil
}

func (j *LocalJournal) ReadManifest(ctx context.Context, organizationID, runID string, generation int64) (Manifest, error) {
	if err := contextError(ctx); err != nil {
		return Manifest{}, err
	}
	if j == nil || j.Root == "" || generation <= 0 {
		return Manifest{}, fmt.Errorf("%w: journal identity is invalid", ErrInvalidRequest)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	directory, err := j.runDirectory(organizationID, runID, generation)
	if err != nil {
		return Manifest{}, err
	}
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Manifest{}, ErrNotFound
		}
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if manifest.OrganizationID != organizationID || manifest.RunID != runID || manifest.Generation != generation {
		return Manifest{}, ErrStaleEvidence
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (j *LocalJournal) Append(ctx context.Context, entry JournalEntry) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if j == nil || j.Root == "" || entry.ID == "" || entry.ObservedAt.IsZero() {
		return fmt.Errorf("%w: journal entry is invalid", ErrInvalidRequest)
	}
	if !validEvidenceKind(entry.Kind) || !validSource(entry.Source) || !validProcessState(entry.ProcessState) || len(entry.SafeDetail) > 4096 {
		return fmt.Errorf("%w: journal entry is invalid", ErrInvalidRequest)
	}
	if entry.ProcessExited && entry.Kind != EvidenceProcessExited && entry.Kind != EvidenceProcessNotFound && entry.Kind != EvidenceResultConfirmed {
		return fmt.Errorf("%w: journal entry exit fact is invalid", ErrInvalidRequest)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	j.mu.Lock()
	defer j.mu.Unlock()
	directory, err := j.runDirectory(entry.OrganizationID, entry.RunID, entry.Generation)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create journal directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(directory, "journal.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("append journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync journal: %w", err)
	}
	return nil
}

func (j *LocalJournal) runDirectory(organizationID, runID string, generation int64) (string, error) {
	if generation <= 0 || !safePathPart(organizationID) || !safePathPart(runID) {
		return "", fmt.Errorf("%w: journal path identity is invalid", ErrInvalidRequest)
	}
	return filepath.Join(j.Root, "runs", organizationID, runID, fmt.Sprint(generation)), nil
}

func safePathPart(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00")
}

func contextError(ctx context.Context) error {
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

func JournalPayloadHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
