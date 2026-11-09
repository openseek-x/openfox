package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/execution"
	"openfox/internal/tenancy"
)

func TestConfigValidateAndNormalize(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	normalized := Config{}.normalized()
	if normalized != DefaultConfig() {
		t.Fatalf("zero config normalized = %+v, want defaults", normalized)
	}
	invalid := map[string]func(*Config){
		"offer too short":         func(c *Config) { c.OfferTTL = 500 * time.Millisecond },
		"lease shorter than 3x":   func(c *Config) { c.LeaseTTL = 2 * c.LeaseRenew },
		"lease too long":          func(c *Config) { c.LeaseTTL = 2 * time.Hour },
		"poll too fast":           func(c *Config) { c.PollInterval = 10 * time.Millisecond },
		"batch zero":              func(c *Config) { c.BatchSize = 0 },
		"batch unbounded":         func(c *Config) { c.BatchSize = MaxBatchSize + 1 },
		"online window too small": func(c *Config) { c.DeviceOnlineAfter = time.Second },
	}
	for name, mutate := range invalid {
		config := DefaultConfig()
		mutate(&config)
		if err := config.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidRequest", name, err)
		}
	}
	if _, err := NewStore(Config{LeaseRenew: time.Minute}); err == nil {
		t.Fatal("NewStore accepted lease TTL shorter than 3x renew")
	}
}

func TestCapacityWaitReason(t *testing.T) {
	cases := []struct {
		err    error
		reason execution.WaitReason
		ok     bool
	}{
		{&pgconn.PgError{Code: "55000", Message: "physical device capacity exhausted"}, execution.WaitComputerCapacity, true},
		{&pgconn.PgError{Code: "55000", Message: "organization capacity exhausted"}, execution.WaitOrganizationCapacity, true},
		{&pgconn.PgError{Code: "55000", Message: "agent capacity exhausted"}, execution.WaitAgentConcurrency, true},
		{&pgconn.PgError{Code: "42501", Message: "grant inactive"}, "", false},
		{errors.New("plain"), "", false},
	}
	for _, item := range cases {
		reason, ok := capacityWaitReason(item.err)
		if reason != item.reason || ok != item.ok {
			t.Errorf("capacityWaitReason(%v) = %q,%v want %q,%v", item.err, reason, ok, item.reason, item.ok)
		}
	}
}

func TestBoundedTextKeepsUTF8Boundary(t *testing.T) {
	if got := boundedText("  short  ", 10); got != "short" {
		t.Fatalf("boundedText trimmed = %q", got)
	}
	// 每个“中”占 3 字节，限 4 字节时只能保留一个完整字符。
	got := boundedText("中中中", 4)
	if got != "中" || !utf8.ValidString(got) {
		t.Fatalf("boundedText utf8 = %q", got)
	}
	if got := boundedText(strings.Repeat("a", 2000), maxDetailBytes); len(got) != maxDetailBytes {
		t.Fatalf("boundedText length = %d", len(got))
	}
}

func TestHubDeliversDropsAndUnsubscribes(t *testing.T) {
	hub := NewHub()
	first, cancelFirst := hub.Subscribe("device-1")
	second, cancelSecond := hub.Subscribe("device-1")
	other, cancelOther := hub.Subscribe("device-2")
	defer cancelOther()

	offer := Offer{OrganizationID: "org", OfferID: "offer", RunID: "run", Generation: 3, DeviceID: "device-1", ExpiresAt: time.Unix(10, 0)}
	if delivered := hub.PublishOffer(offer); delivered != 2 {
		t.Fatalf("PublishOffer delivered = %d, want 2", delivered)
	}
	wake := <-first
	if wake.Type != MessageRunDispatch || wake.OfferID != "offer" || wake.Generation != 3 {
		t.Fatalf("dispatch wake = %+v", wake)
	}
	<-second
	select {
	case wake := <-other:
		t.Fatalf("other device received %+v", wake)
	default:
	}

	if delivered := hub.PublishCancel("device-1", "org", "run", 3); delivered != 2 {
		t.Fatalf("PublishCancel delivered = %d", delivered)
	}
	if wake := <-first; wake.Type != MessageRunCancel || wake.OfferID != "" {
		t.Fatalf("cancel wake = %+v", wake)
	}
	<-second

	// 缓冲区满后非阻塞丢弃。
	for i := 0; i < hubBuffer; i++ {
		hub.PublishCancel("device-1", "org", "run", 3)
	}
	cancelSecond()
	if delivered := hub.PublishCancel("device-1", "org", "run", 3); delivered != 0 {
		t.Fatalf("full buffer delivered = %d, want 0", delivered)
	}
	cancelFirst()
	cancelFirst() // 重复调用安全
	if delivered := hub.PublishCancel("device-1", "org", "run", 3); delivered != 0 {
		t.Fatalf("after unsubscribe delivered = %d", delivered)
	}
	hub.mu.Lock()
	remaining := len(hub.subscribers)
	hub.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("subscriber map size = %d, want 1", remaining)
	}

	var nilHub *Hub
	if nilHub.PublishOffer(offer) != 0 {
		t.Fatal("nil hub delivered")
	}
	_, cancel := nilHub.Subscribe("device-1")
	cancel()
}

type fakeRunner struct {
	controlErr error
	orgErr     error
	calls      int
	sources    []tenancy.Source
	selections []tenancy.OrganizationSelection
}

func (f *fakeRunner) RunControlPlane(context.Context, func(context.Context, pgx.Tx) error) error {
	return f.controlErr
}

func (f *fakeRunner) RunForOrganization(ctx context.Context, organizationID string, source tenancy.Source,
	selector tenancy.OrganizationSelector, _ func(context.Context, pgx.Tx) error) error {
	f.calls++
	f.sources = append(f.sources, source)
	selection, err := selector.SelectOrganization(ctx, organizationID)
	if err != nil {
		return err
	}
	f.selections = append(f.selections, selection)
	return f.orgErr
}

func TestWorkerUsesOrganizationWorkerContextAndReportsPhaseFailures(t *testing.T) {
	failure := errors.New("phase failed")
	runner := &fakeRunner{orgErr: failure}
	worker := &Worker{Pool: runner}
	result, err := worker.RunOrganization(context.Background(), "org-1")
	if !errors.Is(err, failure) {
		t.Fatalf("RunOrganization error = %v", err)
	}
	// 第一个事务是指派事件扫描（task_agent_events），之后每个阶段一个。
	if runner.calls != 7 {
		t.Fatalf("phase transactions = %d, want 7 (assignment scan + one per phase)", runner.calls)
	}
	for i, source := range runner.sources {
		selection := runner.selections[i]
		if source != tenancy.SourceOrganizationWorker || selection.ActorID != WorkerActorID ||
			selection.OrganizationID != "org-1" || selection.AuthorizationVersion != 1 {
			t.Fatalf("phase %d context = %v %+v", i, source, selection)
		}
	}
	if result.OffersCreated != 0 || len(result.Offers) != 0 {
		t.Fatalf("failed phases leaked counts: %+v", result)
	}

	controlFailure := errors.New("list failed")
	if _, err := (&Worker{Pool: &fakeRunner{controlErr: controlFailure}}).RunOnce(context.Background()); !errors.Is(err, controlFailure) {
		t.Fatalf("RunOnce error = %v", err)
	}
	if _, err := (&Worker{}).RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce accepted nil pool")
	}
}

func TestPassBackoffDoublesAndCaps(t *testing.T) {
	cases := map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 5: 16 * time.Second, 6: maxPassBackoff, 100: maxPassBackoff}
	for failures, want := range cases {
		if got := passBackoff(time.Second, failures); got != want {
			t.Errorf("passBackoff(1s, %d) = %s, want %s", failures, got, want)
		}
	}
	if got := passBackoff(time.Minute, 1); got != maxPassBackoff {
		t.Errorf("passBackoff(1m, 1) = %s, want cap", got)
	}
}

func TestResetPhaseAndAdd(t *testing.T) {
	result := PassResult{RunsCreated: 1, InputsRejected: 2, OffersCreated: 1, Offers: []Offer{{}}, OffersReaped: 4, QueueExpired: 5, LeasesExpired: 6, OrphansReclaimed: 7}
	var total PassResult
	total.add(result)
	total.add(result)
	if total.RunsCreated != 2 || len(total.Offers) != 2 || total.LeasesExpired != 12 || total.OrphansReclaimed != 14 {
		t.Fatalf("add = %+v", total)
	}
	for _, phase := range []string{"reclaim_orphans", "reap_offers", "expire_leases", "expire_queued", "materialize_inputs", "offer_queued"} {
		resetPhase(&result, phase)
	}
	if result.RunsCreated != 0 || result.InputsRejected != 0 || result.OffersCreated != 0 || result.Offers != nil ||
		result.OffersReaped != 0 || result.QueueExpired != 0 || result.LeasesExpired != 0 || result.OrphansReclaimed != 0 {
		t.Fatalf("reset = %+v", result)
	}
}
