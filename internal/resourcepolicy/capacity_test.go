package resourcepolicy

import "testing"

func TestQueueDecisionExplainsBackpressure(t *testing.T) {
	policy := DefaultPolicy()
	decision := policy.QueueDecision(0, policy.ActiveMax)
	if decision.Allowed || decision.Reason != BackpressureActiveFull || !decision.Retryable {
		t.Fatalf("active decision = %#v", decision)
	}
	decision = policy.QueueDecision(policy.QueueMax, 0)
	if decision.Allowed || decision.Reason != BackpressureQueueFull {
		t.Fatalf("queue decision = %#v", decision)
	}
	if !policy.QueueDecision(1, 1).Allowed {
		t.Fatal("normal queue decision was rejected")
	}
}

func TestDiskDecisionReservesMinimumFreeSpace(t *testing.T) {
	policy := DefaultPolicy()
	free := uint64(policy.DiskMinFreeBytes)
	if policy.DiskDecision(free, 1).Allowed {
		t.Fatal("disk decision allowed the minimum free-space boundary to be crossed")
	}
	if !policy.DiskDecision(free+1024, 1024).Allowed {
		t.Fatal("disk decision rejected an exactly safe upload")
	}
}
