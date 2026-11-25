package stage12e2

// 阶段 12e：组织配额（NF-05）的集成测试。覆盖：角色限制、范围校验、并发修改、
// 调小上限不影响已在排队和运行的 Run、只限制新进入的；用量接口。

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/dispatch"
	"openfox/internal/tenancy"
)

type policyView struct {
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

type runtimeConfigView struct {
	ConfigVersion int64      `json:"config_version"`
	Policy        policyView `json:"policy"`
}

type usageView struct {
	Queued    int  `json:"queued"`
	QueueMax  int  `json:"queue_max"`
	QueueFull bool `json:"queue_full"`
}

func (c *qcClient) runtimeConfig() runtimeConfigView {
	c.env.t.Helper()
	var config runtimeConfigView
	c.must(http.StatusOK, http.MethodGet, "runtime-config", nil, &config)
	return config
}

func (c *qcClient) usage() usageView {
	c.env.t.Helper()
	var usage usageView
	c.must(http.StatusOK, http.MethodGet, "runtime-config/usage", nil, &usage)
	return usage
}

func patchBody(config runtimeConfigView) map[string]any {
	return map[string]any{"expected_version": config.ConfigVersion, "policy": config.Policy}
}

// workerTx 以组织 worker 上下文执行（与派发 worker 相同的租户上下文）。
func (e *qcEnv) workerTx(fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: dispatch.WorkerActorID, AuthorizationVersion: 1}
	return e.database.RunForOrganization(e.ctx, e.org, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), fn)
}

func (e *qcEnv) materialize() (created, rejected int) {
	e.t.Helper()
	store, err := dispatch.NewStore(dispatch.Config{})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.workerTx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		created, rejected, err = store.MaterializeInputs(ctx, tx, e.org, 50)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return created, rejected
}

func TestQuotaRoleRestriction(t *testing.T) {
	env := newQcEnv(t)
	config := env.owner().runtimeConfig()
	config.Policy.QueueMax = 200
	for _, client := range []*qcClient{env.member(), env.guest(), env.client(qcAdminID)} {
		client.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPatch, "runtime-config", patchBody(config))
	}
	if response := env.outsider().do(http.MethodPatch, "runtime-config", patchBody(config)); response.Status == http.StatusOK {
		t.Fatalf("outsider patched another organization: %s", response.Body)
	}
	// 缺少 CSRF 令牌。
	env.owner().expectError(http.StatusForbidden, "CSRF_REQUIRED", http.MethodPatch, "runtime-config", patchBody(config), "X-CSRF-Token", "")
	if got := env.owner().runtimeConfig(); got.ConfigVersion != config.ConfigVersion || got.Policy.QueueMax == 200 {
		t.Fatalf("rejected writes changed the config: %+v", got)
	}
	// 组织 ADMIN 可以修改。
	var updated runtimeConfigView
	env.admin().must(http.StatusOK, http.MethodPatch, "runtime-config", patchBody(config), &updated)
	if updated.ConfigVersion != config.ConfigVersion+1 || updated.Policy.QueueMax != 200 {
		t.Fatalf("updated = %+v", updated)
	}
	// 所有成员可读队列状态，排队上限仅给组织所有者和管理员。
	for name, client := range map[string]*qcClient{"member": env.member(), "guest": env.guest(), "space admin": env.client(qcAdminID)} {
		response := client.must(http.StatusOK, http.MethodGet, "runtime-config/usage", nil, nil)
		var fields map[string]any
		response.decode(t, &fields)
		if len(fields) != 3 || fields["organization_id"] != env.org || fields["queued"] != float64(0) || fields["queue_full"] != false {
			t.Fatalf("%s usage = %s, want only organization_id, queued, queue_full", name, response.Body)
		}
	}
	for name, client := range map[string]*qcClient{"owner": env.owner(), "admin": env.admin()} {
		var fields map[string]any
		client.must(http.StatusOK, http.MethodGet, "runtime-config/usage", nil, &fields)
		if len(fields) != 4 || fields["organization_id"] != env.org || fields["queued"] != float64(0) || fields["queue_full"] != false || fields["queue_max"] != float64(200) {
			t.Fatalf("%s queue status = %v", name, fields)
		}
	}
	if response := env.outsider().do(http.MethodGet, "runtime-config/usage", nil); response.Status == http.StatusOK {
		t.Fatalf("outsider read usage: %s", response.Body)
	}
}

func TestQuotaRangeValidation(t *testing.T) {
	env := newQcEnv(t)
	config := env.owner().runtimeConfig()
	cases := map[string]func(*runtimeConfigView){
		"queue_max 0":        func(c *runtimeConfigView) { c.Policy.QueueMax = 0 },
		"queue_max negative": func(c *runtimeConfigView) { c.Policy.QueueMax = -5 },
		"queue_max 10001":    func(c *runtimeConfigView) { c.Policy.QueueMax = 10001 },
		"active_max 0":       func(c *runtimeConfigView) { c.Policy.ActiveMax = 0 },
		"active_max 101":     func(c *runtimeConfigView) { c.Policy.ActiveMax = 101 },
		"version 0":          func(c *runtimeConfigView) { c.ConfigVersion = 0 },
	}
	for name, mutate := range cases {
		invalid := config
		mutate(&invalid)
		response := env.owner().do(http.MethodPatch, "runtime-config", patchBody(invalid))
		if response.Status != http.StatusBadRequest || response.code() != "INVALID_REQUEST" {
			t.Fatalf("%s = %d %s, want 400 INVALID_REQUEST", name, response.Status, response.Body)
		}
	}
	if got := env.owner().runtimeConfig(); got != config {
		t.Fatalf("invalid writes changed the config: %+v", got)
	}
	// 边界值本身允许。
	for _, bounds := range [][2]int{{1, 1}, {10000, 100}} {
		current := env.owner().runtimeConfig()
		current.Policy.QueueMax, current.Policy.ActiveMax = bounds[0], bounds[1]
		env.owner().must(http.StatusOK, http.MethodPatch, "runtime-config", patchBody(current), nil)
	}
	// 过期版本号。
	env.owner().expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, "runtime-config", patchBody(config))
}

func TestQuotaConcurrentUpdateOnlyOneWins(t *testing.T) {
	env := newQcEnv(t)
	config := env.owner().runtimeConfig()
	clients := []*qcClient{env.owner(), env.admin()}
	const attempts = 8
	statuses := make([]qcResponse, attempts)
	var wait sync.WaitGroup
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			next := config
			next.Policy.QueueMax = 100 + index
			statuses[index] = clients[index%2].do(http.MethodPatch, "runtime-config", patchBody(next))
		}(index)
	}
	wait.Wait()
	won, winner := 0, 0
	for index, response := range statuses {
		switch {
		case response.Status == http.StatusOK:
			won++
			winner = 100 + index
		case response.Status == http.StatusConflict && response.code() == "VERSION_CONFLICT":
		default:
			t.Fatalf("attempt %d = %d %s", index, response.Status, response.Body)
		}
	}
	final := env.owner().runtimeConfig()
	if won != 1 || final.ConfigVersion != config.ConfigVersion+1 || final.Policy.QueueMax != winner {
		t.Fatalf("won = %d final = %+v winner = %d", won, final, winner)
	}
	// 每次成功修改恰好一条审计。
	if got := env.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'runtime_config.updated'`, env.org); got != 1 {
		t.Fatalf("audit rows = %d, want 1", got)
	}
}

// 调小队列上限：已在排队的 Run 不受影响；新输入不会进入队列也不会丢失，放宽后恢复。
func TestQuotaShrinkQueueKeepsQueuedRuns(t *testing.T) {
	env := newQcEnv(t)
	fixture := env.newRunFixture()
	queued := []string{fixture.seedRun("QUEUED", false), fixture.seedRun("QUEUED", false), fixture.seedRun("QUEUED", false)}
	if usage := env.owner().usage(); usage.Queued != 3 || usage.QueueFull {
		t.Fatalf("usage before = %+v", usage)
	}
	config := env.owner().runtimeConfig()
	config.Policy.QueueMax = 1
	env.owner().must(http.StatusOK, http.MethodPatch, "runtime-config", patchBody(config), nil)
	for _, run := range queued {
		var state string
		env.scalar(&state, `SELECT state FROM openfox.runs WHERE id = $1::uuid`, run)
		if state != "QUEUED" {
			t.Fatalf("run %s state = %s after shrinking the queue", run, state)
		}
	}
	usage := env.owner().usage()
	if usage.Queued != 3 || usage.QueueMax != 1 || !usage.QueueFull {
		t.Fatalf("usage after shrink = %+v", usage)
	}
	// 成员也能得知队列已满（不含上限）。
	if summary := env.member().usage(); summary.Queued != 3 || !summary.QueueFull || summary.QueueMax != 0 {
		t.Fatalf("member usage after shrink = %+v", summary)
	}
	// 新输入：队列已满，不物化、不拒绝、不丢失。
	conversation := fixture.sendMessage()
	if created, rejected := env.materialize(); created != 0 || rejected != 0 {
		t.Fatalf("materialize while full: created = %d rejected = %d", created, rejected)
	}
	pending := func() int {
		return env.count(`SELECT count(*) FROM openfox.conversation_inputs WHERE organization_id = $1::uuid
			AND conversation_id = $2::uuid AND status = 'PENDING'`, env.org, conversation)
	}
	if pending() != 1 {
		t.Fatalf("pending inputs = %d, want 1", pending())
	}
	// 放宽后新输入进入队列。
	config = env.owner().runtimeConfig()
	config.Policy.QueueMax = 10
	env.owner().must(http.StatusOK, http.MethodPatch, "runtime-config", patchBody(config), nil)
	if created, rejected := env.materialize(); created != 1 || rejected != 0 {
		t.Fatalf("materialize after raise: created = %d rejected = %d", created, rejected)
	}
	if usage := env.owner().usage(); usage.Queued != 4 || usage.QueueFull || pending() != 0 {
		t.Fatalf("usage after raise = %+v pending = %d", usage, pending())
	}
}

// 调小活动上限：已占用的槽位不受影响；新预留被拒绝。调大超过平台槽位时生效值仍是平台槽位。
func TestQuotaShrinkActiveKeepsRunningReservations(t *testing.T) {
	env := newQcEnv(t)
	fixture := env.newRunFixture()
	// 预留行由超级用户连接在组织 worker 租户上下文里写入：容量触发器（SECURITY DEFINER、受 FORCE RLS 约束）
	// 需要租户上下文才能看到组织容量行；这与派发器经容量函数写入时的上下文一致。
	reserve := func() error {
		tx, err := env.superuser.Begin(env.ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(env.ctx)
		if _, err := tx.Exec(env.ctx, `SELECT openfox_private.set_tenant_context($1::uuid, $2::uuid, 1, 'organization_worker')`,
			env.org, dispatch.WorkerActorID); err != nil {
			return err
		}
		if _, err := tx.Exec(env.ctx, `
			INSERT INTO openfox.capacity_reservations (organization_id, id, run_id, generation, device_id, agent_id, status)
			SELECT $1::uuid, gen_random_uuid(), gen_random_uuid(), 1, a.device_id, a.id, 'CLAIMED'
			  FROM openfox.agents a WHERE a.organization_id = $1::uuid AND a.id = $2::uuid`, env.org, fixture.agent); err != nil {
			return err
		}
		return tx.Commit(env.ctx)
	}
	for index := 0; index < 2; index++ {
		if err := reserve(); err != nil {
			t.Fatal(err)
		}
	}
	config := env.owner().runtimeConfig()
	config.Policy.ActiveMax = 1
	env.owner().must(http.StatusOK, http.MethodPatch, "runtime-config", patchBody(config), nil)
	if got := env.count(`SELECT count(*) FROM openfox.capacity_reservations WHERE organization_id = $1::uuid AND status = 'CLAIMED'`, env.org); got != 2 {
		t.Fatalf("claimed reservations = %d after shrinking, want 2", got)
	}
	var pgError *pgconn.PgError
	if err := reserve(); !errors.As(err, &pgError) || pgError.Code != "55000" {
		t.Fatalf("new reservation above the lowered limit: err = %v, want SQLSTATE 55000", err)
	}
	// 调大到超过平台槽位（夹具为 4）：设置被接受，但生效值仍受平台槽位限制。
	config = env.owner().runtimeConfig()
	config.Policy.ActiveMax = 50
	env.owner().must(http.StatusOK, http.MethodPatch, "runtime-config", patchBody(config), nil)
	for index := 0; index < 2; index++ {
		if err := reserve(); err != nil {
			t.Fatalf("reservation %d within platform capacity: %v", index, err)
		}
	}
	if err := reserve(); !errors.As(err, &pgError) || pgError.Code != "55000" {
		t.Fatalf("reservation above platform capacity: err = %v, want SQLSTATE 55000", err)
	}
}
