package reconciliation

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

const (
	testOrg      = "00000000-0000-0000-0000-0000000000a1"
	testRun      = "00000000-0000-0000-0000-0000000000d1"
	testDev      = "00000000-0000-0000-0000-000000000011"
	testCase     = "00000000-0000-0000-0000-0000000000c1"
	testEvidence = "00000000-0000-0000-0000-0000000000e1"
)

func TestReconcilerRequiresMatchingExitEvidence(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	manifest := testManifest(now)
	reconciler := NewReconciler(func() time.Time { return now })
	item, err := reconciler.Open(context.Background(), OpenRequest{
		OrganizationID: testOrg, RunID: testRun, Generation: 1,
		ReasonCode: ReasonUnknownProcess, FailureCode: FailureUnknownProcess,
		Manifest: manifest, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	nonExit := Evidence{OrganizationID: testOrg, ID: testEvidence, CaseID: item.ID, RunID: testRun,
		Generation: 1, Kind: EvidenceProcessRunning, Source: SourceDaemon,
		DeviceID: testDev, DeviceGeneration: manifest.DeviceGeneration, CenterEpoch: manifest.CenterEpoch,
		SupervisorNonce: manifest.SupervisorNonce, ProcessPID: manifest.ProcessPID,
		ProcessStartedAt: manifest.ProcessStartedAt, ProcessState: ProcessRunning,
		PayloadHash: testHash("running"), ObservedAt: now}
	if _, err := reconciler.SubmitEvidence(context.Background(), nonExit); err != nil {
		t.Fatal(err)
	}
	_, err = reconciler.Resolve(context.Background(), ResolveRequest{
		OrganizationID: testOrg, CaseID: item.ID, RunID: testRun, ExpectedVersion: 1,
		Resolution: ResolutionProcessExitedFailed, EvidenceID: testEvidence, ActorID: testDev, Now: now,
	})
	if !errors.Is(err, ErrEvidenceNotAccepted) {
		t.Fatalf("resolve with live process error = %v, want evidence rejection", err)
	}

	exitID := "00000000-0000-0000-0000-0000000000e2"
	exitCode := 1
	exit := nonExit
	exit.ID = exitID
	exit.Kind = EvidenceProcessExited
	exit.ProcessState = ProcessExited
	exit.ProcessExited = true
	exit.ExitCode = &exitCode
	exit.PayloadHash = testHash("exit")
	exit.ObservedAt = now.Add(time.Second)
	if _, err := reconciler.SubmitEvidence(context.Background(), exit); err != nil {
		t.Fatal(err)
	}
	resolved, err := reconciler.Resolve(context.Background(), ResolveRequest{
		OrganizationID: testOrg, CaseID: item.ID, RunID: testRun, ExpectedVersion: 1,
		Resolution: ResolutionProcessExitedFailed, EvidenceID: exitID, ActorID: testDev, Now: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != CaseResolved || resolved.Resolution != ResolutionProcessExitedFailed || resolved.Version != 2 {
		t.Fatalf("resolved case = %+v", resolved)
	}
}

func TestReconcilerRejectsStaleSupervisorIdentity(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	manifest := testManifest(now)
	reconciler := NewReconciler(func() time.Time { return now })
	item, err := reconciler.Open(context.Background(), OpenRequest{
		OrganizationID: testOrg, RunID: testRun, Generation: 1,
		ReasonCode: ReasonEpochConflict, FailureCode: FailureAuthorizationRevoked,
		Manifest: manifest, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence := Evidence{OrganizationID: testOrg, ID: testEvidence, CaseID: item.ID, RunID: testRun,
		Generation: 1, Kind: EvidenceProcessExited, Source: SourceDaemon,
		DeviceID: testDev, DeviceGeneration: manifest.DeviceGeneration, CenterEpoch: manifest.CenterEpoch,
		SupervisorNonce: "stale-supervisor-nonce-0001", ProcessPID: manifest.ProcessPID,
		ProcessStartedAt: manifest.ProcessStartedAt, ProcessState: ProcessExited, ProcessExited: true,
		PayloadHash: testHash("stale"), ObservedAt: now}
	if _, err := reconciler.SubmitEvidence(context.Background(), evidence); err != nil {
		t.Fatal(err)
	}
	_, err = reconciler.Resolve(context.Background(), ResolveRequest{
		OrganizationID: testOrg, CaseID: item.ID, RunID: testRun, ExpectedVersion: 1,
		Resolution: ResolutionProcessExitedFailed, EvidenceID: testEvidence, ActorID: testDev, Now: now,
	})
	if !errors.Is(err, ErrEvidenceNotAccepted) {
		t.Fatalf("stale evidence error = %v", err)
	}
}

func TestLocalJournalWritesAtomicManifestAndAppendOnlyFacts(t *testing.T) {
	root := t.TempDir()
	journal, err := NewLocalJournal(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	manifest := testManifest(now)
	if _, err := journal.WriteManifest(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	loaded, err := journal.ReadManifest(context.Background(), testOrg, testRun, 1)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SupervisorNonce != manifest.SupervisorNonce || loaded.ProcessPID != manifest.ProcessPID {
		t.Fatalf("loaded manifest = %+v", loaded)
	}
	if err := journal.Append(context.Background(), JournalEntry{
		ID: testEvidence, OrganizationID: testOrg, RunID: testRun, Generation: 1,
		Kind: EvidenceProcessExited, Source: SourceDaemon, ProcessState: ProcessExited,
		ProcessExited: true, ObservedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func testManifest(now time.Time) Manifest {
	return Manifest{OrganizationID: testOrg, RunID: testRun, DeviceID: testDev,
		Generation: 1, DeviceGeneration: 1, CenterEpoch: 1,
		SupervisorNonce: "supervisor-nonce-0001", ProcessPID: 1234,
		ProcessStartedAt: now, LastEventSequence: 4}
}

func testHash(value string) string {
	hash, _ := JournalPayloadHash(value)
	return hash
}
