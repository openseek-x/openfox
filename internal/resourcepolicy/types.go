// Package resourcepolicy owns typed organization runtime limits and the safe
// cleanup workflow. It intentionally has no generic configuration map.
package resourcepolicy

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultQueueMax                       = 1000
	DefaultActiveMax                      = 10
	DefaultQueueTimeoutHours              = 24
	DefaultSpoolMaxBytes            int64 = 2 * 1024 * 1024 * 1024
	DefaultDiskMinFreeBytes         int64 = 2 * 1024 * 1024 * 1024
	DefaultRunMaxBytes              int64 = 20 * 1024 * 1024
	DefaultDetailedRetentionDays          = 14
	DefaultArtifactFileMaxBytes     int64 = 20 * 1024 * 1024
	DefaultArtifactRunMaxBytes      int64 = 100 * 1024 * 1024
	DefaultArtifactRetentionDays          = 90
	DefaultAuditRetentionDays             = 180
	DefaultIdempotencyRetentionDays       = 30
	PreviewTTL                            = 15 * time.Minute
	MaxPreviewTTL                         = time.Hour
	MaxCleanupItems                       = 1000
)

var (
	ErrInvalidPolicy      = errors.New("resourcepolicy: invalid policy")
	ErrConfigConflict     = errors.New("resourcepolicy: config version conflict")
	ErrNotFound           = errors.New("resourcepolicy: resource not found")
	ErrPreviewExpired     = errors.New("resourcepolicy: cleanup preview expired")
	ErrPreviewConflict    = errors.New("resourcepolicy: cleanup preview is not confirmable")
	ErrStorageUnavailable = errors.New("resourcepolicy: storage is unavailable")
	ErrLimitExceeded      = errors.New("resourcepolicy: resource limit exceeded")
)

type BackpressureReason string

const (
	BackpressureQueueFull  BackpressureReason = "QUEUE_FULL"
	BackpressureActiveFull BackpressureReason = "ACTIVE_LIMIT"
	BackpressureDiskLow    BackpressureReason = "DISK_LOW"
	BackpressureRunFull    BackpressureReason = "RUN_OUTPUT_LIMIT"
)

type CapacityDecision struct {
	Allowed   bool
	Reason    BackpressureReason
	Current   int64
	Requested int64
	Limit     int64
	Retryable bool
}

func (p Policy) QueueDecision(queued, active int) CapacityDecision {
	if queued < 0 || active < 0 {
		return CapacityDecision{Reason: BackpressureQueueFull, Retryable: true}
	}
	if active >= p.ActiveMax {
		return CapacityDecision{Reason: BackpressureActiveFull, Current: int64(active), Limit: int64(p.ActiveMax), Retryable: true}
	}
	if queued >= p.QueueMax {
		return CapacityDecision{Reason: BackpressureQueueFull, Current: int64(queued), Limit: int64(p.QueueMax), Retryable: true}
	}
	return CapacityDecision{Allowed: true}
}

func (p Policy) DiskDecision(freeBytes, requestedBytes uint64) CapacityDecision {
	if freeBytes < requestedBytes || freeBytes-requestedBytes < uint64(p.DiskMinFreeBytes) {
		return CapacityDecision{
			Reason: BackpressureDiskLow, Current: int64(freeBytes), Requested: int64(requestedBytes),
			Limit: p.DiskMinFreeBytes, Retryable: true,
		}
	}
	return CapacityDecision{Allowed: true, Requested: int64(requestedBytes), Limit: p.DiskMinFreeBytes}
}

// Policy is the persisted, typed organization runtime policy. Changes to this
// struct require a migration and a contract update; callers cannot add keys.
type Policy struct {
	QueueMax                 int   `json:"queue_max"`
	ActiveMax                int   `json:"active_max"`
	QueueTimeoutHours        int   `json:"queue_timeout_hours"`
	SpoolMaxBytes            int64 `json:"spool_max_bytes"`
	DiskMinFreeBytes         int64 `json:"disk_min_free_bytes"`
	RunMaxBytes              int64 `json:"run_max_bytes"`
	DetailedRetentionDays    int   `json:"detailed_retention_days"`
	ArtifactFileMaxBytes     int64 `json:"artifact_file_max_bytes"`
	ArtifactRunMaxBytes      int64 `json:"artifact_run_max_bytes"`
	ArtifactRetentionDays    int   `json:"artifact_retention_days"`
	AuditRetentionDays       int   `json:"audit_retention_days"`
	IdempotencyRetentionDays int   `json:"idempotency_retention_days"`
}

func DefaultPolicy() Policy {
	return Policy{
		QueueMax: DefaultQueueMax, ActiveMax: DefaultActiveMax,
		QueueTimeoutHours: DefaultQueueTimeoutHours,
		SpoolMaxBytes:     DefaultSpoolMaxBytes, DiskMinFreeBytes: DefaultDiskMinFreeBytes,
		RunMaxBytes: DefaultRunMaxBytes, DetailedRetentionDays: DefaultDetailedRetentionDays,
		ArtifactFileMaxBytes: DefaultArtifactFileMaxBytes, ArtifactRunMaxBytes: DefaultArtifactRunMaxBytes,
		ArtifactRetentionDays: DefaultArtifactRetentionDays, AuditRetentionDays: DefaultAuditRetentionDays,
		IdempotencyRetentionDays: DefaultIdempotencyRetentionDays,
	}
}

func (p Policy) Validate() error {
	checks := []struct {
		name string
		ok   bool
	}{
		{"queue_max", p.QueueMax >= 1 && p.QueueMax <= 10000},
		{"active_max", p.ActiveMax >= 1 && p.ActiveMax <= 100},
		{"queue_timeout_hours", p.QueueTimeoutHours >= 1 && p.QueueTimeoutHours <= 168},
		{"spool_max_bytes", p.SpoolMaxBytes >= 256*1024*1024},
		{"disk_min_free_bytes", p.DiskMinFreeBytes >= 512*1024*1024},
		{"run_max_bytes", p.RunMaxBytes >= 1024*1024 && p.RunMaxBytes <= 100*1024*1024},
		{"detailed_retention_days", p.DetailedRetentionDays >= 1 && p.DetailedRetentionDays <= 90},
		{"artifact_file_max_bytes", p.ArtifactFileMaxBytes >= 1024*1024 && p.ArtifactFileMaxBytes <= 100*1024*1024},
		{"artifact_run_max_bytes", p.ArtifactRunMaxBytes >= p.ArtifactFileMaxBytes},
		{"artifact_retention_days", p.ArtifactRetentionDays >= 1 && p.ArtifactRetentionDays <= 730},
		{"audit_retention_days", p.AuditRetentionDays >= 30 && p.AuditRetentionDays <= 730},
		{"idempotency_retention_days", p.IdempotencyRetentionDays >= 30},
	}
	for _, check := range checks {
		if !check.ok {
			return fmt.Errorf("%w: %s is outside its supported range", ErrInvalidPolicy, check.name)
		}
	}
	return nil
}

type Config struct {
	OrganizationID  string     `json:"organization_id"`
	ConfigVersion   int64      `json:"config_version"`
	AppliedVersion  int64      `json:"applied_version"`
	RestartRequired bool       `json:"restart_required"`
	Policy          Policy     `json:"policy"`
	UpdatedAt       time.Time  `json:"updated_at"`
	AppliedAt       *time.Time `json:"applied_at,omitempty"`
}

type ConfigUpdate struct {
	ExpectedVersion int64  `json:"expected_version"`
	Policy          Policy `json:"policy"`
	RestartRequired bool   `json:"restart_required"`
}

type ConfigAck struct {
	AppliedVersion  int64 `json:"applied_version"`
	RestartRequired bool  `json:"restart_required"`
}

type RunSnapshot struct {
	OrganizationID string `json:"organization_id"`
	RunID          string `json:"run_id"`
	ConfigVersion  int64  `json:"config_version"`
	Policy
}

type CleanupPreview struct {
	OrganizationID string     `json:"organization_id"`
	ID             string     `json:"id"`
	RequestedBy    string     `json:"requested_by"`
	ConfigVersion  int64      `json:"config_version"`
	Status         string     `json:"status"`
	ExpiresAt      time.Time  `json:"expires_at"`
	CandidateCount int        `json:"candidate_count"`
	CandidateBytes int64      `json:"candidate_bytes"`
	CreatedAt      time.Time  `json:"created_at"`
	ConfirmedAt    *time.Time `json:"confirmed_at,omitempty"`
	ConfirmedBy    string     `json:"confirmed_by,omitempty"`
	Version        int64      `json:"version"`
}

type CleanupItem struct {
	OrganizationID         string     `json:"organization_id"`
	ID                     string     `json:"id"`
	PreviewID              string     `json:"preview_id"`
	ArtifactID             string     `json:"artifact_id"`
	StorageKey             string     `json:"-"`
	SizeBytes              int64      `json:"size_bytes"`
	ExpectedVersion        int64      `json:"expected_version"`
	ExpectedSHA256         string     `json:"expected_sha256"`
	ExpectedRetentionUntil *time.Time `json:"expected_retention_until,omitempty"`
	Status                 string     `json:"status"`
	FailureCode            string     `json:"failure_code,omitempty"`
}

func normalizePreviewTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return PreviewTTL
	}
	if ttl > MaxPreviewTTL {
		return MaxPreviewTTL
	}
	return ttl
}

func validIdentifier(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func validStorageKey(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 512 && !strings.ContainsRune(value, '\x00')
}
