// Package reconciliation owns the typed evidence boundary for unknown Run
// state. It deliberately keeps process facts separate from Run lifecycle
// state: a missing heartbeat is not proof of success, failure, or exit.
package reconciliation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"openfox/internal/membership"
)

type CaseStatus string

const (
	CaseOpen     CaseStatus = "OPEN"
	CaseResolved CaseStatus = "RESOLVED"
	CaseRejected CaseStatus = "REJECTED"
)

type Resolution string

const (
	ResolutionProcessExitedFailed       Resolution = "PROCESS_EXITED_FAILED"
	ResolutionProcessNotStartedCanceled Resolution = "PROCESS_NOT_STARTED_CANCELED"
	ResolutionResultAccepted            Resolution = "RESULT_ACCEPTED"
	ResolutionManualReview              Resolution = "MANUAL_REVIEW"
	ResolutionRejected                  Resolution = "REJECTED"
)

type EvidenceKind string

const (
	EvidenceProcessStarted  EvidenceKind = "PROCESS_STARTED"
	EvidenceProcessRunning  EvidenceKind = "PROCESS_RUNNING"
	EvidenceProcessExited   EvidenceKind = "PROCESS_EXITED"
	EvidenceProcessNotFound EvidenceKind = "PROCESS_NOT_FOUND"
	EvidenceResultConfirmed EvidenceKind = "RESULT_CONFIRMED"
	EvidenceHeartbeat       EvidenceKind = "HEARTBEAT"
	EvidenceRevocation      EvidenceKind = "REVOCATION"
)

type EvidenceSource string

const (
	SourceDaemon   EvidenceSource = "DAEMON"
	SourceCenter   EvidenceSource = "CENTER"
	SourceOperator EvidenceSource = "OPERATOR"
)

type ProcessState string

const (
	ProcessIntended ProcessState = "INTENDED"
	ProcessStarted  ProcessState = "STARTED"
	ProcessRunning  ProcessState = "RUNNING"
	ProcessExited   ProcessState = "EXITED"
	ProcessNotFound ProcessState = "NOT_FOUND"
	ProcessUnknown  ProcessState = "UNKNOWN"
)

type ReasonCode string

const (
	ReasonLeaseExpired          ReasonCode = "LEASE_EXPIRED"
	ReasonDeviceOffline         ReasonCode = "DEVICE_OFFLINE"
	ReasonEpochConflict         ReasonCode = "EPOCH_CONFLICT"
	ReasonAuthorizationRevoked  ReasonCode = "AUTHORIZATION_REVOKED"
	ReasonUnknownProcess        ReasonCode = "UNKNOWN_PROCESS"
	ReasonPIDReuse              ReasonCode = "PID_REUSE"
	ReasonStartupCrash          ReasonCode = "STARTUP_CRASH"
	ReasonCancelUnconfirmed     ReasonCode = "CANCEL_UNCONFIRMED"
	ReasonResultUnconfirmed     ReasonCode = "RESULT_UNCONFIRMED"
	ReasonCapacityReleaseFailed ReasonCode = "CAPACITY_RELEASE_FAILED"
)

type FailureCode string

const (
	FailureCredentialRejected   FailureCode = "CREDENTIAL_REJECTED"
	FailureQuotaExhausted       FailureCode = "QUOTA_EXHAUSTED"
	FailureRateLimited          FailureCode = "RATE_LIMITED"
	FailureProviderOverloaded   FailureCode = "PROVIDER_OVERLOADED"
	FailureModelNotFound        FailureCode = "MODEL_NOT_FOUND"
	FailureContextTooLarge      FailureCode = "CONTEXT_TOO_LARGE"
	FailureImageTooLarge        FailureCode = "IMAGE_TOO_LARGE"
	FailureNetworkUnreachable   FailureCode = "NETWORK_UNREACHABLE"
	FailureWorktreePrepFailed   FailureCode = "WORKTREE_PREP_FAILED"
	FailureRepoUnavailable      FailureCode = "REPO_UNAVAILABLE"
	FailureRuntimeUnavailable   FailureCode = "RUNTIME_UNAVAILABLE"
	FailureQueueTimeout         FailureCode = "QUEUE_TIMEOUT"
	FailureRunTimeout           FailureCode = "RUN_TIMEOUT"
	FailureAuthorizationRevoked FailureCode = "AUTHORIZATION_REVOKED"
	FailureUnknownProcess       FailureCode = "UNKNOWN_PROCESS"
	FailureInternal             FailureCode = "INTERNAL"
)

type Manifest struct {
	OrganizationID    string    `json:"organization_id"`
	RunID             string    `json:"run_id"`
	DeviceID          string    `json:"device_id"`
	Generation        int64     `json:"generation"`
	DeviceGeneration  int64     `json:"device_generation"`
	CenterEpoch       int64     `json:"center_epoch"`
	SupervisorNonce   string    `json:"supervisor_nonce"`
	ProcessPID        int       `json:"process_pid"`
	ProcessStartedAt  time.Time `json:"process_started_at,omitempty"`
	WorkdirBindingID  string    `json:"workdir_binding_id,omitempty"`
	WorkdirPathHash   string    `json:"workdir_path_hash,omitempty"`
	LastEventSequence int64     `json:"last_event_sequence"`
	ResultID          string    `json:"result_id,omitempty"`
}

func (m Manifest) Validate() error {
	if !membership.ValidateUUID(m.OrganizationID) || !membership.ValidateUUID(m.RunID) || !membership.ValidateUUID(m.DeviceID) {
		return fmt.Errorf("%w: manifest identity is invalid", ErrInvalidRequest)
	}
	if m.Generation <= 0 || m.DeviceGeneration <= 0 || m.CenterEpoch <= 0 || len(strings.TrimSpace(m.SupervisorNonce)) < 16 || len(m.SupervisorNonce) > 128 {
		return fmt.Errorf("%w: manifest generation or nonce is invalid", ErrInvalidRequest)
	}
	if m.ProcessPID < 0 || m.LastEventSequence < 0 {
		return fmt.Errorf("%w: manifest process facts are invalid", ErrInvalidRequest)
	}
	if m.WorkdirPathHash != "" && !isSHA256(m.WorkdirPathHash) {
		return fmt.Errorf("%w: workdir path hash is invalid", ErrInvalidRequest)
	}
	if m.WorkdirBindingID != "" && !membership.ValidateUUID(m.WorkdirBindingID) {
		return fmt.Errorf("%w: workdir binding id is invalid", ErrInvalidRequest)
	}
	if m.ResultID != "" && !membership.ValidateUUID(m.ResultID) {
		return fmt.Errorf("%w: manifest result id is invalid", ErrInvalidRequest)
	}
	return nil
}

func (m Manifest) Hash() (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("hash manifest: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

type SupervisorFact struct {
	Manifest
	ManifestHash    string       `json:"manifest_hash"`
	ProcessState    ProcessState `json:"process_state"`
	ExitCode        *int         `json:"exit_code,omitempty"`
	ProcessExitedAt *time.Time   `json:"process_exited_at,omitempty"`
	ObservedAt      time.Time    `json:"observed_at"`
	Version         int64        `json:"version"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

type Case struct {
	OrganizationID       string      `json:"organization_id"`
	ID                   string      `json:"id"`
	RunID                string      `json:"run_id"`
	Generation           int64       `json:"generation"`
	ReasonCode           ReasonCode  `json:"reason_code"`
	FailureCode          FailureCode `json:"failure_code"`
	Status               CaseStatus  `json:"status"`
	SafeDetail           string      `json:"safe_detail"`
	Resolution           Resolution  `json:"resolution,omitempty"`
	ResolutionEvidenceID string      `json:"resolution_evidence_id,omitempty"`
	OpenedAt             time.Time   `json:"opened_at"`
	ResolvedAt           *time.Time  `json:"resolved_at,omitempty"`
	ResolvedBy           string      `json:"resolved_by,omitempty"`
	Version              int64       `json:"version"`
}

type Evidence struct {
	OrganizationID   string         `json:"organization_id"`
	ID               string         `json:"id"`
	CaseID           string         `json:"case_id"`
	RunID            string         `json:"run_id"`
	Generation       int64          `json:"generation"`
	DeviceGeneration int64          `json:"device_generation"`
	CenterEpoch      int64          `json:"center_epoch"`
	Kind             EvidenceKind   `json:"kind"`
	Source           EvidenceSource `json:"source"`
	DeviceID         string         `json:"device_id,omitempty"`
	SupervisorNonce  string         `json:"supervisor_nonce"`
	ProcessPID       int            `json:"process_pid,omitempty"`
	ProcessStartedAt time.Time      `json:"process_started_at,omitempty"`
	ProcessState     ProcessState   `json:"process_state"`
	ProcessExited    bool           `json:"process_exited"`
	ExitCode         *int           `json:"exit_code,omitempty"`
	EventSequence    int64          `json:"event_sequence"`
	ResultID         string         `json:"result_id,omitempty"`
	PayloadHash      string         `json:"payload_hash"`
	SafeDetail       string         `json:"safe_detail"`
	AcceptedState    string         `json:"accepted_state"`
	ObservedAt       time.Time      `json:"observed_at"`
	CreatedAt        time.Time      `json:"created_at"`
}

type OpenRequest struct {
	OrganizationID string
	RunID          string
	Generation     int64
	ReasonCode     ReasonCode
	FailureCode    FailureCode
	SafeDetail     string
	Manifest       Manifest
	Now            time.Time
}

type FactRequest struct {
	Manifest        Manifest
	ProcessState    ProcessState
	ExitCode        *int
	ProcessExitedAt *time.Time
	ObservedAt      time.Time
	ExpectedVersion int64
}

type ResolveRequest struct {
	OrganizationID  string
	CaseID          string
	RunID           string
	ExpectedVersion int64
	Resolution      Resolution
	EvidenceID      string
	ActorID         string
	SafeDetail      string
	Now             time.Time
}

var (
	ErrInvalidRequest        = errors.New("reconciliation: invalid request")
	ErrNotFound              = errors.New("reconciliation: resource not found")
	ErrVersionConflict       = errors.New("reconciliation: version conflict")
	ErrCaseAlreadyOpen       = errors.New("reconciliation: case is already open")
	ErrEvidenceConflict      = errors.New("reconciliation: evidence conflict")
	ErrStaleEvidence         = errors.New("reconciliation: evidence belongs to an old supervisor generation")
	ErrEvidenceNotAccepted   = errors.New("reconciliation: evidence is not accepted")
	ErrResolutionUnsupported = errors.New("reconciliation: resolution is not supported")
)

func (r OpenRequest) Validate() error {
	if !membership.ValidateUUID(r.OrganizationID) || !membership.ValidateUUID(r.RunID) || r.Generation <= 0 || len(r.SafeDetail) > 4096 {
		return fmt.Errorf("%w: case identity is invalid", ErrInvalidRequest)
	}
	if !validReason(r.ReasonCode) || !validFailure(r.FailureCode) {
		return fmt.Errorf("%w: failure taxonomy is invalid", ErrInvalidRequest)
	}
	if err := r.Manifest.Validate(); err != nil {
		return err
	}
	if r.Manifest.OrganizationID != r.OrganizationID || r.Manifest.RunID != r.RunID || r.Manifest.Generation != r.Generation {
		return fmt.Errorf("%w: manifest does not match case", ErrInvalidRequest)
	}
	return nil
}

func (r FactRequest) Validate() error {
	if err := r.Manifest.Validate(); err != nil {
		return err
	}
	if !validProcessState(r.ProcessState) || r.ExpectedVersion < 0 || len(r.ManifestHash()) == 0 {
		return fmt.Errorf("%w: supervisor fact is invalid", ErrInvalidRequest)
	}
	if r.ProcessState == ProcessExited && r.ProcessExitedAt == nil {
		return fmt.Errorf("%w: exited process requires exit time", ErrInvalidRequest)
	}
	if r.ProcessState != ProcessExited && r.ProcessExitedAt != nil {
		return fmt.Errorf("%w: non-exited process has exit time", ErrInvalidRequest)
	}
	return nil
}

func (r FactRequest) ManifestHash() string {
	hash, _ := r.Manifest.Hash()
	return hash
}

func (r ResolveRequest) Validate() error {
	if !membership.ValidateUUID(r.OrganizationID) || !membership.ValidateUUID(r.CaseID) || !membership.ValidateUUID(r.RunID) || r.ExpectedVersion <= 0 || strings.TrimSpace(r.ActorID) == "" || !membership.ValidateUUID(r.EvidenceID) {
		return fmt.Errorf("%w: resolution identity is invalid", ErrInvalidRequest)
	}
	if len(r.SafeDetail) > 4096 || !validResolution(r.Resolution) {
		return fmt.Errorf("%w: resolution is invalid", ErrInvalidRequest)
	}
	return nil
}

func (e Evidence) Validate() error {
	if !membership.ValidateUUID(e.ID) || !membership.ValidateUUID(e.CaseID) || !membership.ValidateUUID(e.RunID) || e.Generation <= 0 || e.DeviceGeneration <= 0 || e.CenterEpoch <= 0 || len(strings.TrimSpace(e.SupervisorNonce)) < 16 || len(e.SupervisorNonce) > 128 || e.ProcessPID < 0 || e.EventSequence < 0 || len(e.SafeDetail) > 4096 || !validEvidenceKind(e.Kind) || !validSource(e.Source) || !validProcessState(e.ProcessState) {
		return fmt.Errorf("%w: evidence is invalid", ErrInvalidRequest)
	}
	if e.Source == SourceDaemon && !membership.ValidateUUID(e.DeviceID) {
		return fmt.Errorf("%w: daemon evidence requires a device identity", ErrInvalidRequest)
	}
	if e.Source != SourceDaemon && e.DeviceID != "" {
		return fmt.Errorf("%w: non-daemon evidence cannot carry a device identity", ErrInvalidRequest)
	}
	if e.ProcessExited && e.Kind != EvidenceProcessExited && e.Kind != EvidenceProcessNotFound && e.Kind != EvidenceResultConfirmed {
		return fmt.Errorf("%w: only terminal evidence may assert process exit", ErrInvalidRequest)
	}
	if e.ResultID != "" && !membership.ValidateUUID(e.ResultID) {
		return fmt.Errorf("%w: result id is invalid", ErrInvalidRequest)
	}
	if !isSHA256(e.PayloadHash) {
		return fmt.Errorf("%w: evidence hash is invalid", ErrInvalidRequest)
	}
	return nil
}

func VerifyEvidence(manifest Manifest, evidence Evidence) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if err := evidence.Validate(); err != nil {
		return err
	}
	if evidence.OrganizationID != manifest.OrganizationID || evidence.RunID != manifest.RunID || evidence.Generation != manifest.Generation || evidence.DeviceGeneration != manifest.DeviceGeneration || evidence.CenterEpoch != manifest.CenterEpoch || evidence.SupervisorNonce != manifest.SupervisorNonce || (evidence.Source == SourceDaemon && evidence.DeviceID != manifest.DeviceID) {
		return ErrStaleEvidence
	}
	if manifest.ProcessPID != 0 && evidence.ProcessPID != 0 && manifest.ProcessPID != evidence.ProcessPID {
		return fmt.Errorf("%w: PID does not match the supervisor manifest", ErrStaleEvidence)
	}
	if !manifest.ProcessStartedAt.IsZero() && !evidence.ProcessStartedAt.IsZero() && !manifest.ProcessStartedAt.Equal(evidence.ProcessStartedAt) {
		return fmt.Errorf("%w: process start time does not match the supervisor manifest", ErrStaleEvidence)
	}
	if evidence.Kind == EvidenceResultConfirmed && !evidence.ProcessExited {
		return fmt.Errorf("%w: result confirmation requires process exit evidence", ErrInvalidRequest)
	}
	return nil
}

func CanRelease(e Evidence) bool {
	return e.AcceptedState == "ACCEPTED" && e.ProcessExited &&
		(e.Kind == EvidenceProcessExited || e.Kind == EvidenceProcessNotFound || e.Kind == EvidenceResultConfirmed)
}

func ResolveWithEvidence(request ResolveRequest, evidence Evidence) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if evidence.ID != request.EvidenceID || evidence.CaseID != request.CaseID || evidence.RunID != request.RunID {
		return ErrEvidenceConflict
	}
	if evidence.AcceptedState != "ACCEPTED" {
		return ErrEvidenceNotAccepted
	}
	switch request.Resolution {
	case ResolutionProcessExitedFailed, ResolutionProcessNotStartedCanceled:
		if !CanRelease(evidence) {
			return ErrEvidenceNotAccepted
		}
	case ResolutionResultAccepted:
		if evidence.Kind != EvidenceResultConfirmed || evidence.ResultID == "" {
			return ErrEvidenceNotAccepted
		}
	case ResolutionManualReview, ResolutionRejected:
		// Manual review closes this case but intentionally does not release a Run.
	default:
		return ErrResolutionUnsupported
	}
	return nil
}

func validReason(value ReasonCode) bool {
	switch value {
	case ReasonLeaseExpired, ReasonDeviceOffline, ReasonEpochConflict, ReasonAuthorizationRevoked,
		ReasonUnknownProcess, ReasonPIDReuse, ReasonStartupCrash, ReasonCancelUnconfirmed,
		ReasonResultUnconfirmed, ReasonCapacityReleaseFailed:
		return true
	default:
		return false
	}
}

func validFailure(value FailureCode) bool {
	switch value {
	case FailureCredentialRejected, FailureQuotaExhausted, FailureRateLimited, FailureProviderOverloaded,
		FailureModelNotFound, FailureContextTooLarge, FailureImageTooLarge, FailureNetworkUnreachable,
		FailureWorktreePrepFailed, FailureRepoUnavailable, FailureRuntimeUnavailable, FailureQueueTimeout,
		FailureRunTimeout, FailureAuthorizationRevoked, FailureUnknownProcess, FailureInternal:
		return true
	default:
		return false
	}
}

func validResolution(value Resolution) bool {
	switch value {
	case ResolutionProcessExitedFailed, ResolutionProcessNotStartedCanceled, ResolutionResultAccepted,
		ResolutionManualReview, ResolutionRejected:
		return true
	default:
		return false
	}
}

func validEvidenceKind(value EvidenceKind) bool {
	switch value {
	case EvidenceProcessStarted, EvidenceProcessRunning, EvidenceProcessExited, EvidenceProcessNotFound,
		EvidenceResultConfirmed, EvidenceHeartbeat, EvidenceRevocation:
		return true
	default:
		return false
	}
}

func validSource(value EvidenceSource) bool {
	switch value {
	case SourceDaemon, SourceCenter, SourceOperator:
		return true
	default:
		return false
	}
}

func validProcessState(value ProcessState) bool {
	switch value {
	case ProcessIntended, ProcessStarted, ProcessRunning, ProcessExited, ProcessNotFound, ProcessUnknown:
		return true
	default:
		return false
	}
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
