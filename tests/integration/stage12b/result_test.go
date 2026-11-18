package stage12b

// 阶段 12b：运行结果的 git_summary / failure_code 持久化、大小限制与旧格式上报兼容。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"openfox/internal/deviceauth"
	"openfox/internal/executiondelivery"
	"openfox/internal/tenancy"
)

// runningRun 建一个 RUNNING 的 Run，返回 Run ID 与设备 ID。
func (e *qcEnv) runningRun() (runID, deviceID string) {
	e.t.Helper()
	space := e.createSpace(nil)
	agent := e.seedAgent(space.ID, qcOwnerID, "PRIVATE")
	var conv struct {
		ID string `json:"id"`
	}
	e.owner().must(201, "POST", "conversations", map[string]any{"space_id": space.ID, "agent_id": agent}, &conv)
	runID = qcNewID(e.t)
	e.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id, config_version, grant_version,
			actor_id, state, generation, started_at, lease_expires_at)
		SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, a.device_id, g.id, 1, 1, $6::uuid, 'RUNNING', 1, clock_timestamp(), clock_timestamp() + interval '1 hour'
		  FROM openfox.agents a JOIN openfox.device_grants g ON g.organization_id = a.organization_id AND g.device_id = a.device_id
		 WHERE a.organization_id = $1::uuid AND a.id = $5::uuid`, e.org, runID, space.ID, conv.ID, agent, qcOwnerID)
	e.scalar(&deviceID, `SELECT device_id::text FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, agent)
	return runID, deviceID
}

func (e *qcEnv) appendResult(runID, deviceID string, input executiondelivery.ResultInput) (executiondelivery.Result, error) {
	e.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: deviceID, AuthorizationVersion: 1}
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil })
	var result executiondelivery.Result
	err := e.database.RunForOrganization(e.ctx, e.org, tenancy.SourceDeviceRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = (executiondelivery.Service{Pool: e.database}).AppendResult(ctx, tx, e.org, deviceauth.Principal{DeviceID: deviceID}, runID, input)
		return err
	})
	return result, err
}

func TestResultPersistsGitSummaryAndFailureCode(t *testing.T) {
	env := newQcEnv(t)
	run, device := env.runningRun()
	summary := json.RawMessage(`{"v":1,"repo":true,"available":true,"branch":"main","commits":[{"hash":"abc1234","title":"fix"}],"commit_total":1,"uncommitted":2,"unverified_reasons":["UNCOMMITTED_CHANGES"]}`)
	result, err := env.appendResult(run, device, executiondelivery.ResultInput{
		ID: qcNewID(t), Generation: 1, Status: "FAILED", Checks: json.RawMessage(`[]`), GitSummary: summary, FailureCode: "RATE_LIMITED", Unverified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(result.GitSummary, &got); err != nil || got["branch"] != "main" {
		t.Fatalf("git_summary = %s (%v)", result.GitSummary, err)
	}
	var code string
	env.scalar(&code, `SELECT failure_code FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, run)
	if code != "RATE_LIMITED" {
		t.Fatalf("failure_code = %q", code)
	}
}

func TestResultFailedWithoutCodeDefaultsToInternal(t *testing.T) {
	env := newQcEnv(t)
	run, device := env.runningRun()
	if _, err := env.appendResult(run, device, executiondelivery.ResultInput{ID: qcNewID(t), Generation: 1, Status: "FAILED", Checks: json.RawMessage(`[]`)}); err != nil {
		t.Fatal(err)
	}
	var code string
	env.scalar(&code, `SELECT failure_code FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, run)
	if code != "INTERNAL" {
		t.Fatalf("failure_code = %q, want INTERNAL", code)
	}
}

func TestResultOldFormatReportStillAccepted(t *testing.T) {
	env := newQcEnv(t)
	run, device := env.runningRun()
	result, err := env.appendResult(run, device, executiondelivery.ResultInput{ID: qcNewID(t), Generation: 1, Status: "SUCCEEDED", Summary: "ok", Checks: json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.GitSummary) != 0 {
		t.Fatalf("old report must have no git_summary, got %s", result.GitSummary)
	}
}

func TestResultGitSummarySizeLimitAndShape(t *testing.T) {
	env := newQcEnv(t)
	run, device := env.runningRun()
	big := json.RawMessage(`{"v":1,"repo":true,"available":true,"branch":"` + strings.Repeat("x", executiondelivery.MaxGitSummarySize) + `"}`)
	_, err := env.appendResult(run, device, executiondelivery.ResultInput{ID: qcNewID(t), Generation: 1, Status: "SUCCEEDED", Checks: json.RawMessage(`[]`), GitSummary: big})
	if !errors.Is(err, executiondelivery.ErrInvalidRequest) {
		t.Fatalf("oversized git_summary error = %v", err)
	}
	_, err = env.appendResult(run, device, executiondelivery.ResultInput{ID: qcNewID(t), Generation: 1, Status: "SUCCEEDED", Checks: json.RawMessage(`[]`), GitSummary: json.RawMessage(`[1]`)})
	if !errors.Is(err, executiondelivery.ErrInvalidRequest) {
		t.Fatalf("non-object git_summary error = %v", err)
	}
}
