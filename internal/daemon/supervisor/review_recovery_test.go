package supervisor

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewClaimTransientAndLostResponseRetry(t *testing.T) {
	for _, lost := range []bool{false, true} {
		name := "瞬时失败"
		if lost {
			name = "领取已生效但响应丢失"
		}
		t.Run(name, func(t *testing.T) {
			center := newCenter()
			center.claimFailures, center.claimResponseLost = 1, lost
			adapter := &fakeAdapter{}
			h := newHarness(t, center, adapter, "")
			waitFor(t, "领取重试并完成", func() bool { return len(center.snapshot().results) == 1 })
			h.supervisor.HandleMessage(dispatchWake())
			h.stop()
			if adapter.startCount() != 1 {
				t.Fatalf("同一代次启动了 %d 次", adapter.startCount())
			}
		})
	}
}

func TestReviewResultSurvivesRestart(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			center := newCenter()
			center.offerAvailable, center.resultFailureStatus = false, status
			path := filepath.Join(t.TempDir(), "journal.db")
			h := newHarness(t, center, &fakeAdapter{}, path)
			h.stop()
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			h.supervisor.execute(ctx, cancel, Offer{OrganizationID: testOrg, RunID: testRun, Generation: 1, OfferID: testOffer})
			center.mu.Lock()
			attempts := append([]ResultInput(nil), center.resultAttempts...)
			center.resultFailureStatus = 0
			center.mu.Unlock()
			if len(attempts) == 0 {
				t.Fatal("没有尝试上传结果")
			}
			if err := h.journal.Close(); err != nil {
				t.Fatal(err)
			}
			h2 := newHarness(t, center, &fakeAdapter{}, path)
			waitFor(t, "重启后补传结果", func() bool { return len(center.snapshot().results) > 0 })
			h2.stop()
			got := center.snapshot()
			if got.results[0].ID != attempts[0].ID || got.results[0].Output != "OK" || len(got.reports) != 0 {
				t.Fatalf("结果未稳定补传或被进程证据提前收尾：results=%+v reports=%+v", got.results, got.reports)
			}
		})
	}
}

func TestReviewPrepareShutdownSurvivesRestart(t *testing.T) {
	center := newCenter()
	center.offerAvailable = false
	path := filepath.Join(t.TempDir(), "journal.db")
	h := newHarness(t, center, &fakeAdapter{}, path)
	h.stop()
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errShutdown)
	if _, err := h.journal.Begin(context.Background(), testOrg, testRun, 1, testOffer); err != nil {
		t.Fatal(err)
	}
	run := &runState{supervisor: h.supervisor, offer: Offer{OrganizationID: testOrg, RunID: testRun, Generation: 1}, logger: h.supervisor.config.Logger}
	run.events = newEventUploader(run)
	state, detail := run.prepareFailed(ctx, "", "")
	run.events.close()
	if err := h.journal.Mark(context.Background(), testRun, 1, state, detail); err != nil {
		t.Fatal(err)
	}
	if err := h.journal.Close(); err != nil {
		t.Fatal(err)
	}
	h2 := newHarness(t, center, &fakeAdapter{}, path)
	defer h2.stop()
	waitFor(t, "重启后上报准备阶段退出证据", func() bool { return len(center.snapshot().reports) == 1 })
	report := center.snapshot().reports[0]
	if report.Kind != "PROCESS_NOT_FOUND" || !report.Certain {
		t.Fatalf("错误的进程证据：%+v", report)
	}
}

func TestRuntimeRequiresDurableLaunchMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	j, err := OpenJournal(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = j.db.Exec(`CREATE TRIGGER refuse_launch BEFORE UPDATE OF proc_known ON run_journal
		WHEN NEW.proc_known = 0 BEGIN SELECT RAISE(ABORT, 'test journal write failure'); END`)
	if closeErr := j.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	center, adapter := newCenter(), &fakeAdapter{}
	h := newHarness(t, center, adapter, path)
	waitFor(t, "failed launch result", func() bool { return len(center.snapshot().results) > 0 })
	h.stop()
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.starts != 0 || center.snapshot().results[0].Status != "FAILED" {
		t.Fatal("runtime started or failure result was lost")
	}
}

func TestFinalResultRejectionLeavesDiagnosticAndStopsRetries(t *testing.T) {
	ctx := t.Context()
	journal, err := OpenJournal(ctx, filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err = journal.Begin(ctx, testOrg, testRun, 1, testOffer); err != nil {
		t.Fatal(err)
	}
	in := ResultInput{ID: newUUID(), Generation: 1, Status: "SUCCEEDED"}
	if err = journal.SaveResult(ctx, testOrg, testRun, in); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"RESULT_FINALIZED"}}`))
	}))
	defer server.Close()
	s := Supervisor{config: Config{Client: &Client{BaseURL: server.URL, Token: func(context.Context) (string, error) { return "fixture", nil }}, Journal: journal, Logger: slog.Default()}}
	s.flushResults(ctx)
	s.flushResults(ctx)
	var detail string
	if err := journal.db.QueryRowContext(ctx, "SELECT detail FROM run_journal WHERE run_id=?", testRun).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(detail, in.ID) || !strings.Contains(detail, "RESULT_FINALIZED") {
		t.Fatalf("requests %d, diagnostic %q", calls, detail)
	}
}
