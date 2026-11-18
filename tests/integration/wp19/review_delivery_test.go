package wp19

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/deviceauth"
	"openfox/internal/executiondelivery"
	"openfox/internal/reconciliation"
)

func TestReviewResultReconcilesAndRemainsIdempotent(t *testing.T) {
	e := newReconcileEnv(t)
	_, lease := e.startRun(true)
	e.expire(lease.RunID)
	input := executiondelivery.ResultInput{ID: newID(t), Generation: lease.Generation, Status: "SUCCEEDED", Output: "补传结果"}
	appendResult := func() error {
		return e.deviceTx(orgA, func(ctx context.Context, tx pgx.Tx) error {
			_, err := (executiondelivery.Service{}).AppendResult(ctx, tx, orgA, deviceauth.Principal{DeviceID: e.device}, lease.RunID, input)
			return err
		})
	}
	if err := appendResult(); err != nil {
		t.Fatal(err)
	}
	if err := appendResult(); err != nil {
		t.Fatalf("同一结果重试：%v", err)
	}
	e.expectRow(`SELECT state || ':' || reconciliation_required::text FROM openfox.runs WHERE id=$1::uuid`, []any{lease.RunID}, "SUCCEEDED:false")
	e.expectRow(`SELECT status || ':' || resolution FROM openfox.run_reconciliation_cases WHERE run_id=$1::uuid`, []any{lease.RunID}, "RESOLVED:RESULT_ACCEPTED")
	e.expectReleasedOnce(lease.RunID)
	input.Output = "同 ID 不同内容"
	if err := appendResult(); !errors.Is(err, executiondelivery.ErrResultConflict) {
		t.Fatalf("冲突结果：%v", err)
	}
}

func TestReviewGeneratedEventsDoNotConsumeDeviceSequence(t *testing.T) {
	e := newReconcileEnv(t)
	_, lease := e.startRun(true)
	e.expire(lease.RunID)
	if _, err := e.report(lease.RunID, processReport(t, lease.Generation, reconciliation.EvidenceProcessRunning, true)); err != nil {
		t.Fatal(err)
	}
	appendEvent := func(sequence int64) {
		t.Helper()
		if err := e.deviceTx(orgA, func(ctx context.Context, tx pgx.Tx) error {
			batch, err := (executiondelivery.Service{}).AppendEvents(ctx, tx, orgA, deviceauth.Principal{DeviceID: e.device}, lease.RunID, []executiondelivery.EventInput{{ID: newID(t), Generation: lease.Generation, Sequence: sequence, Type: "output", Payload: json.RawMessage(`{"text":"事件"}`)}})
			if err == nil && batch.HighestContiguous != sequence {
				t.Fatalf("连续确认受服务端序号干扰：%d", batch.HighestContiguous)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(1)
	if err := e.deviceTx(orgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := (executiondelivery.Service{}).AppendResult(ctx, tx, orgA, deviceauth.Principal{DeviceID: e.device}, lease.RunID, executiondelivery.ResultInput{ID: newID(t), Generation: lease.Generation, Status: "SUCCEEDED", Output: "完成"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	appendEvent(2)
}

// 统计服务端向 Go 传输的多行结果，避免连续确认重新传输整个事件历史。
type reviewCountingTx struct {
	pgx.Tx
	rows int
}
type reviewCountingRows struct {
	pgx.Rows
	owner *reviewCountingTx
}

func (r *reviewCountingRows) Next() bool {
	ok := r.Rows.Next()
	if ok {
		r.owner.rows++
	}
	return ok
}
func (tx *reviewCountingTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := tx.Tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &reviewCountingRows{Rows: rows, owner: tx}, nil
}

func TestReviewContiguousAcknowledgementBoundsTransferredRows(t *testing.T) {
	e := newReconcileEnv(t)
	_, lease := e.startRun(true)
	e.exec(`INSERT INTO openfox.run_events (organization_id,run_id,generation,sequence,type,payload_hash)
 SELECT $1::uuid,$2::uuid,$3,n,'output',repeat('a',64) FROM generate_series(1,1000) n WHERE n<>500`, orgA, lease.RunID, lease.Generation)
	if err := e.deviceTx(orgA, func(ctx context.Context, tx pgx.Tx) error {
		measured := &reviewCountingTx{Tx: tx}
		batch, err := (executiondelivery.Service{}).AppendEvents(ctx, measured, orgA, deviceauth.Principal{DeviceID: e.device}, lease.RunID, []executiondelivery.EventInput{{ID: newID(t), Generation: lease.Generation, Sequence: 500, Type: "output", Payload: json.RawMessage(`{"text":"补齐空洞"}`)}})
		if err != nil {
			return err
		}
		if batch.HighestContiguous != 1000 {
			t.Errorf("连续确认=%d", batch.HighestContiguous)
		}
		if measured.rows > 1 {
			t.Errorf("确认单个事件传输了 %d 行历史", measured.rows)
		}
		_, err = (executiondelivery.Service{}).AppendResult(ctx, tx, orgA, deviceauth.Principal{DeviceID: e.device}, lease.RunID, executiondelivery.ResultInput{ID: newID(t), Generation: lease.Generation, Status: "SUCCEEDED", Output: "完成"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
