package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/workdir"
	"openfox/internal/runtime"
)

const (
	testOrg   = "11111111-1111-4111-8111-111111111111"
	testRun   = "22222222-2222-4222-8222-222222222222"
	testOffer = "33333333-3333-4333-8333-333333333333"
)

// fakeCenter 模拟中心设备端点，记录调用以便断言。
type fakeCenter struct {
	mu            sync.Mutex
	claims        int
	starts        int
	renews        int
	events        int
	results       []ResultInput
	prepareFailed []string
	contexts      int

	startCode       string // 非空时 start 返回该错误码（409）
	renewCode       string // 非空时 renew 返回该错误码（409）
	cancelOnRenew   bool
	offerAvailable  bool
	directoryMode   string
	directoryRoots  []string
	runtimeName     string
	runTimeoutSecs  int64
	lastContextPath string
	// accepted 按序号记录已接受的事件；failEvents > 0 时接下来的事件请求返回 503 并递减。
	accepted    map[int64]EventInput
	failEvents  int
	eventCalls  int
	rejectUsage bool
	// extend 非空时修改下发的 RunContext 字段。
	extend func(*RunContext)
	// 技能下载。skillStatus[id] 非 0 时返回该状态码；否则返回 skillBundles[id]。
	skillBundles map[string]SkillBundle
	skillStatus  map[string]int
	skillCalls   []string
	// 附件下载。attachmentStatus[id] 非 0 时返回该状态码；否则返回 attachmentFiles[id]。
	attachmentFiles  map[string]string
	attachmentStatus map[string]int
	attachmentCalls  []string
	// 任务工具：记录收到的请求；toolStatus 非 0 时返回该状态码。
	toolRequests []toolRequest
	toolStatus   int
	// 产物上传。记录收到的上传；artifactStatus 非 0 时返回该状态码；rejectArtifactIDs 模拟拒绝结果里的 artifact_ids。
	artifactUploads   []artifactUpload
	artifactStatus    int
	rejectArtifactIDs bool
}

type artifactUpload struct {
	ID, Name, MIME, Generation string
	Body                       []byte
}

type toolRequest struct {
	Tool string
	Body map[string]any
}

func (c *fakeCenter) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	prefix := "/v1/device/organizations/" + testOrg
	writeJSON := func(w http.ResponseWriter, status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	conflict := func(w http.ResponseWriter, code string) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]string{"code": code}})
	}
	lease := func(state string, cancel bool, challenge string) map[string]any {
		return map[string]any{
			"run_id": testRun, "generation": 1, "state": state, "center_epoch": 1,
			"lease_expires_at": time.Now().Add(time.Second), "lease_ttl_ms": 1000, "renew_interval_ms": 50,
			"cancel_requested": cancel, "challenge": challenge,
		}
	}
	mux.HandleFunc("GET "+prefix+"/offers", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer device-token" {
			t.Errorf("missing device token")
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		offers := []map[string]any{}
		if c.offerAvailable {
			offers = append(offers, map[string]any{"offer_id": testOffer, "run_id": testRun, "generation": 1, "expires_at": time.Now().Add(15 * time.Second)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"offers": offers})
	})
	mux.HandleFunc("POST "+prefix+"/offers/"+testOffer+"/claim", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.claims++
		c.mu.Unlock()
		writeJSON(w, http.StatusOK, lease("PREPARING", false, ""))
	})
	mux.HandleFunc("GET "+prefix+"/runs/"+testRun+"/context", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.contexts++
		c.lastContextPath = r.URL.RequestURI()
		mode, name, timeout, roots, extend := c.directoryMode, c.runtimeName, c.runTimeoutSecs, c.directoryRoots, c.extend
		c.mu.Unlock()
		runContext := RunContext{
			RunID: testRun, Generation: 1, OrganizationID: testOrg, SpaceID: "space", ConversationID: "conversation",
			AgentID: "agent", ConfigVersion: 1, Runtime: name, Model: "default", RunTimeoutSeconds: timeout,
			DirectoryMode: mode, DirectoryRoots: roots, Prompt: "say OK",
			History: []HistoryMessage{{Seq: 1, AuthorKind: "USER", Body: "earlier"}},
		}
		if extend != nil {
			extend(&runContext)
		}
		writeJSON(w, http.StatusOK, runContext)
	})
	mux.HandleFunc("GET "+prefix+"/runs/"+testRun+"/skills/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		c.mu.Lock()
		c.skillCalls = append(c.skillCalls, id)
		status, bundle, ok := c.skillStatus[id], c.skillBundles[id], false
		_, ok = c.skillBundles[id]
		c.mu.Unlock()
		if status != 0 {
			writeJSON(w, status, map[string]any{"error": map[string]string{"code": "SKILL_GONE"}})
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "NOT_FOUND"}})
			return
		}
		writeJSON(w, http.StatusOK, bundle)
	})
	mux.HandleFunc("GET "+prefix+"/runs/"+testRun+"/attachments/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		c.mu.Lock()
		c.attachmentCalls = append(c.attachmentCalls, id+"?"+r.URL.RawQuery)
		status, content, ok := c.attachmentStatus[id], c.attachmentFiles[id], false
		_, ok = c.attachmentFiles[id]
		c.mu.Unlock()
		if status != 0 {
			writeJSON(w, status, map[string]any{"error": map[string]string{"code": "ATTACHMENT_GONE"}})
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "NOT_FOUND"}})
			return
		}
		_, _ = w.Write([]byte(content))
	})
	mux.HandleFunc("POST "+prefix+"/runs/"+testRun+"/tools/{tool}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.toolRequests = append(c.toolRequests, toolRequest{Tool: r.PathValue("tool"), Body: body})
		status := c.toolStatus
		c.mu.Unlock()
		if status != 0 {
			writeJSON(w, status, map[string]any{"error": map[string]string{"code": "STATE_CONFLICT"}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "text": "done"})
	})
	mux.HandleFunc("POST "+prefix+"/runs/"+testRun+"/artifacts", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upload := artifactUpload{ID: r.Header.Get("X-Artifact-ID"), Name: r.Header.Get("X-Artifact-Name"), MIME: r.Header.Get("X-Artifact-MIME"),
			Generation: r.Header.Get("X-Run-Generation"), Body: body}
		c.mu.Lock()
		c.artifactUploads = append(c.artifactUploads, upload)
		status := c.artifactStatus
		c.mu.Unlock()
		if status != 0 {
			writeJSON(w, status, map[string]any{"error": map[string]string{"code": "STATE_CONFLICT"}})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": upload.ID, "name": upload.Name, "size_bytes": len(body), "mime_type": upload.MIME, "state": "AVAILABLE"})
	})
	mux.HandleFunc("POST "+prefix+"/runs/"+testRun+"/start", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.starts++
		code := c.startCode
		c.mu.Unlock()
		if code != "" {
			conflict(w, code)
			return
		}
		writeJSON(w, http.StatusOK, lease("RUNNING", false, ""))
	})
	mux.HandleFunc("POST "+prefix+"/runs/"+testRun+"/lease", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Generation int64  `json:"generation"`
			Challenge  string `json:"challenge"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.renews++
		code, cancel := c.renewCode, c.cancelOnRenew
		c.mu.Unlock()
		if code != "" {
			conflict(w, code)
			return
		}
		writeJSON(w, http.StatusOK, lease("RUNNING", cancel, body.Challenge))
	})
	mux.HandleFunc("POST "+prefix+"/runs/"+testRun+"/prepare-failed", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			FailureCode string `json:"failure_code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.prepareFailed = append(c.prepareFailed, body.FailureCode)
		c.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{})
	})
	mux.HandleFunc("POST "+prefix+"/runs/"+testRun+"/events", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Events []EventInput `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.eventCalls++
		if c.failEvents > 0 {
			c.failEvents--
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]string{"code": "NOT_READY"}})
			return
		}
		// 与中心一致：序号必须为正、代次匹配、单条 ≤ 64 KiB；同一序号重复上报按幂等处理（不要求连续）。
		for _, event := range body.Events {
			if event.Sequence <= 0 || event.Generation != 1 || len(event.Payload) > 64*1024 {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "INVALID_REQUEST"}})
				return
			}
		}
		if c.accepted == nil {
			c.accepted = map[int64]EventInput{}
		}
		for _, event := range body.Events {
			if _, seen := c.accepted[event.Sequence]; !seen {
				c.accepted[event.Sequence] = event
				c.events++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	})
	mux.HandleFunc("POST "+prefix+"/runs/"+testRun+"/result", func(w http.ResponseWriter, r *http.Request) {
		var body ResultInput
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		if c.rejectArtifactIDs && len(body.ArtifactIDs) > 0 {
			c.mu.Unlock()
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "INVALID_REQUEST"}})
			return
		}
		if c.rejectUsage && body.Usage != nil {
			// 模拟旧中心：严格解码拒绝未知字段 usage。
			c.mu.Unlock()
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "INVALID_REQUEST"}})
			return
		}
		c.results = append(c.results, body)
		c.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{})
	})
	return mux
}

func (c *fakeCenter) snapshot() fakeCenter {
	c.mu.Lock()
	defer c.mu.Unlock()
	accepted := make(map[int64]EventInput, len(c.accepted))
	for sequence, event := range c.accepted {
		accepted[sequence] = event
	}
	return fakeCenter{
		claims: c.claims, starts: c.starts, renews: c.renews, events: c.events, contexts: c.contexts,
		results: append([]ResultInput(nil), c.results...), prepareFailed: append([]string(nil), c.prepareFailed...),
		lastContextPath: c.lastContextPath, accepted: accepted, eventCalls: c.eventCalls,
		skillCalls:      append([]string(nil), c.skillCalls...),
		attachmentCalls: append([]string(nil), c.attachmentCalls...),
		artifactUploads: append([]artifactUpload(nil), c.artifactUploads...),
	}
}

// eventTypes 按序号返回已接受事件的类型，并检查序号从 1 起连续。
func eventTypes(t *testing.T, accepted map[int64]EventInput) []string {
	t.Helper()
	types := make([]string, 0, len(accepted))
	for sequence := int64(1); sequence <= int64(len(accepted)); sequence++ {
		event, ok := accepted[sequence]
		if !ok {
			t.Fatalf("event sequence %d missing (accepted %d)", sequence, len(accepted))
		}
		types = append(types, event.Type)
	}
	return types
}

// prepSteps 按序号返回 prep_env 步骤。
func prepSteps(t *testing.T, accepted map[int64]EventInput) []runtime.Step {
	t.Helper()
	var steps []runtime.Step
	for sequence := int64(1); sequence <= int64(len(accepted)); sequence++ {
		event := accepted[sequence]
		if event.Type != "step" {
			continue
		}
		var body struct {
			Data struct {
				Schema int          `json:"schema"`
				Step   runtime.Step `json:"step"`
			} `json:"data"`
		}
		if err := json.Unmarshal(event.Payload, &body); err != nil || body.Data.Schema != 1 {
			t.Fatalf("step payload %s: %v", event.Payload, err)
		}
		if body.Data.Step.Kind == runtime.StepKindPrepEnv {
			steps = append(steps, body.Data.Step)
		}
	}
	return steps
}

// fakeAdapter 是测试 Runtime：block=true 时进程一直运行到被取消。
type fakeAdapter struct {
	mu       sync.Mutex
	starts   int
	block    bool
	usage    *runtime.Usage
	requests []runtime.RunRequest
	// output 非空时替代默认回复 "OK"（脱敏测试用）。
	output string
	// onStart 在 Start 里（Run 仍在进行时）被调用，用来检查此刻磁盘上的状态（附件目录等）。
	onStart func(runtime.RunRequest)
	// approvalModes 是该适配器声明支持的权限档位；为空表示只支持 FOLLOW_RUNTIME。
	approvalModes []string
	// collaborationModes 是该适配器声明支持的协作模式；为空表示只支持 FOLLOW_RUNTIME。
	collaborationModes []string
	// tools 为 true 时实现 runtime.ToolCapableAdapter 并返回支持，声明发布工具；默认 false 与之前一致。
	tools bool
}

func (a *fakeAdapter) SupportsTools() bool { return a.tools }

func (a *fakeAdapter) ApprovalModes() []string { return a.approvalModes }

func (a *fakeAdapter) CollaborationModes() []string { return a.collaborationModes }

func (a *fakeAdapter) Name() string { return "codex" }
func (a *fakeAdapter) Detect(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{Runtime: "codex", Runnable: true}, nil
}

func (a *fakeAdapter) Start(_ context.Context, request runtime.RunRequest) (runtime.Execution, error) {
	normalized, err := request.Normalize()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.starts++
	a.requests = append(a.requests, normalized)
	block, usage, output, onStart := a.block, a.usage, a.output, a.onStart
	a.mu.Unlock()
	if onStart != nil {
		onStart(normalized)
	}
	if output == "" {
		output = "OK"
	}
	execution := &fakeExecution{events: make(chan runtime.Event, 4), done: make(chan struct{}), cancel: make(chan struct{}), usage: usage, output: output}
	go execution.run(block)
	return execution, nil
}

func (a *fakeAdapter) startCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.starts
}

type fakeExecution struct {
	events chan runtime.Event
	done   chan struct{}
	cancel chan struct{}
	once   sync.Once
	result runtime.Result
	usage  *runtime.Usage
	output string
}

func (e *fakeExecution) run(block bool) {
	e.events <- runtime.Event{Sequence: 1, Type: runtime.EventStarted, At: time.Now()}
	if block {
		<-e.cancel
		// 与真实适配器一致：补发的 cancelled 事件不带序号。
		e.events <- runtime.Event{Type: runtime.EventCancelled, Text: "cancelled"}
		e.result = runtime.Result{Status: runtime.StatusCancelled, ProcessExited: true}
	} else {
		e.events <- runtime.Event{Sequence: 2, Type: runtime.EventOutput, Text: e.output, At: time.Now()}
		e.result = runtime.Result{Status: runtime.StatusSucceeded, Output: e.output, ProcessExited: true, Usage: e.usage}
	}
	close(e.events)
	close(e.done)
}

func (e *fakeExecution) Events() <-chan runtime.Event { return e.events }
func (e *fakeExecution) Wait(ctx context.Context) (runtime.Result, error) {
	select {
	case <-e.done:
		return e.result, nil
	case <-ctx.Done():
		return runtime.Result{}, ctx.Err()
	}
}
func (e *fakeExecution) Cancel(ctx context.Context) error {
	e.once.Do(func() { close(e.cancel) })
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (e *fakeExecution) Recovery() runtime.Recovery { return runtime.Recovery{} }

type harness struct {
	center     *fakeCenter
	adapter    *fakeAdapter
	supervisor *Supervisor
	journal    *Journal
	journalAt  string
	cancel     context.CancelFunc
	done       chan error
}

func newHarness(t *testing.T, center *fakeCenter, adapter *fakeAdapter, journalPath string) *harness {
	t.Helper()
	return newHarnessWithRoots(t, center, adapter, journalPath, nil)
}

func newHarnessWithRoots(t *testing.T, center *fakeCenter, adapter *fakeAdapter, journalPath string, roots map[string]string, deviceIDs ...string) *harness {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, os.ErrPermission) || strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
			t.Skipf("sandbox does not permit TCP listeners: %v", err)
		}
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(center.handler(t))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := workdir.NewManager(workdir.GuardPolicy{ManagedRoot: filepath.Join(root, "workspaces"), AllowNonGit: true})
	if err != nil {
		t.Fatal(err)
	}
	if journalPath == "" {
		journalPath = filepath.Join(root, "state", "journal.db")
	}
	journal, err := OpenJournal(context.Background(), journalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	deviceID := ""
	if len(deviceIDs) > 0 {
		deviceID = deviceIDs[0]
	}
	supervisor, err := New(Config{
		DeviceID: deviceID,
		Client:   &Client{BaseURL: server.URL, Token: func(context.Context) (string, error) { return "device-token", nil }},
		Adapter:  adapter, Workdir: manager, DirectoryRoots: roots, Journal: journal, Organizations: []string{testOrg},
		PollInterval: time.Second, AttachmentRoot: filepath.Join(root, "attachments"), ArtifactRoot: filepath.Join(root, "run-artifacts"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{center: center, adapter: adapter, supervisor: supervisor, journal: journal, journalAt: journalPath, cancel: cancel, done: make(chan error, 1)}
	go func() { h.done <- supervisor.Run(ctx) }()
	t.Cleanup(h.stop)
	return h
}

func (h *harness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	<-h.done
	h.cancel = nil
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newCenter() *fakeCenter {
	return &fakeCenter{offerAvailable: true, directoryMode: "NONE", runtimeName: "codex", runTimeoutSecs: 600}
}

func dispatchWake() daemon.Message {
	return daemon.Message{Envelope: daemon.Envelope{Type: daemon.MessageRunDispatch, OrganizationID: testOrg, RunID: testRun, DispatchGeneration: 1}}
}

func TestSupervisorRunsOfferOnceAndUploadsResult(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{}
	h := newHarness(t, center, adapter, "")
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	// 重复唤醒与轮询不得再次领取或启动进程。
	for i := 0; i < 5; i++ {
		h.supervisor.HandleMessage(dispatchWake())
	}
	time.Sleep(200 * time.Millisecond)
	snapshot := center.snapshot()
	if snapshot.claims != 1 || snapshot.starts != 1 || adapter.startCount() != 1 {
		t.Fatalf("claims=%d starts=%d processes=%d, want 1/1/1", snapshot.claims, snapshot.starts, adapter.startCount())
	}
	result := snapshot.results[0]
	if result.Status != "SUCCEEDED" || result.Output != "OK" || result.Generation != 1 || result.ID == "" {
		t.Fatalf("result = %+v", result)
	}
	if snapshot.events < 2 {
		t.Fatalf("events uploaded = %d", snapshot.events)
	}
	// prep_env：workdir 运行中 → runtime 运行中（workdir 完成）→ done（含 session）。
	waitFor(t, "prep steps", func() bool { return len(prepSteps(t, center.snapshot().accepted)) == 3 })
	prep := prepSteps(t, center.snapshot().accepted)
	if prep[0].State != runtime.StepRunning || len(prep[0].Substeps) != 1 || prep[0].Substeps[0].Kind != "workdir" || prep[0].ID != "prep" {
		t.Fatalf("prep begin = %+v", prep[0])
	}
	if prep[1].Substeps[0].State != runtime.StepDone || prep[1].Substeps[1].Kind != "runtime" || prep[1].Substeps[1].State != runtime.StepRunning {
		t.Fatalf("prep runtime = %+v", prep[1])
	}
	last := prep[2]
	if last.State != runtime.StepDone || last.DurationMS == nil || last.EndedAt == nil || *last.Progress != 1 ||
		len(last.Substeps) != 3 || last.Substeps[2].Kind != "session" || last.Substeps[1].State != runtime.StepDone {
		t.Fatalf("prep done = %+v", last)
	}
	if !strings.Contains(snapshot.lastContextPath, "generation=1") {
		t.Fatalf("context path = %s", snapshot.lastContextPath)
	}
	adapter.mu.Lock()
	request := adapter.requests[0]
	adapter.mu.Unlock()
	if !strings.Contains(request.Prompt, "[USER] earlier") || !strings.HasSuffix(request.Prompt, "Current request:\nsay OK") ||
		request.Timeout != 600*time.Second || !filepath.IsAbs(request.WorkingDirectory) {
		t.Fatalf("runtime request = %+v", request)
	}
	if _, err := os.Stat(filepath.Join(request.WorkingDirectory, ".git")); !os.IsNotExist(err) {
		t.Fatalf("NONE mode workspace must not be a Git repository: %v", err)
	}
	state, err := h.journal.State(context.Background(), testRun, 1)
	if err != nil || state != JournalFinished {
		t.Fatalf("journal state = %q, %v", state, err)
	}
}

func TestSupervisorStopsProcessOnUserCancelWake(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{block: true}
	h := newHarness(t, center, adapter, "")
	waitFor(t, "process start", func() bool { return adapter.startCount() == 1 })
	// 代次不匹配的取消必须忽略。
	h.supervisor.HandleMessage(daemon.Message{Envelope: daemon.Envelope{Type: daemon.MessageRunCancel, OrganizationID: testOrg, RunID: testRun, DispatchGeneration: 2}})
	time.Sleep(100 * time.Millisecond)
	if len(center.snapshot().results) != 0 {
		t.Fatal("stale-generation cancel stopped the run")
	}
	h.supervisor.HandleMessage(daemon.Message{Envelope: daemon.Envelope{Type: daemon.MessageRunCancel, OrganizationID: testOrg, RunID: testRun, DispatchGeneration: 1}})
	waitFor(t, "canceled result", func() bool { return len(center.snapshot().results) == 1 })
	if status := center.snapshot().results[0].Status; status != "CANCELED" {
		t.Fatalf("status = %s", status)
	}
	waitFor(t, "cancelled event", func() bool { return center.snapshot().events == 5 })
	if types := strings.Join(eventTypes(t, center.snapshot().accepted), ","); types != "step,step,step,started,cancelled" {
		t.Fatalf("event types = %s, want 3 prep_env steps + started + cancelled", types)
	}
}

func TestSupervisorStopsProcessWhenRenewReportsCancel(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{block: true}
	center.cancelOnRenew = true
	newHarness(t, center, adapter, "")
	waitFor(t, "canceled result", func() bool { return len(center.snapshot().results) == 1 })
	if status := center.snapshot().results[0].Status; status != "CANCELED" {
		t.Fatalf("status = %s", status)
	}
}

func TestSupervisorStopsWithoutResultWhenLeaseRejected(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{block: true}
	h := newHarness(t, center, adapter, "")
	waitFor(t, "process start", func() bool { return adapter.startCount() == 1 })
	center.mu.Lock()
	center.renewCode = "RECONCILIATION_REQUIRED"
	center.mu.Unlock()
	waitFor(t, "journal finished", func() bool {
		state, _ := h.journal.State(context.Background(), testRun, 1)
		return state == JournalFinished
	})
	if results := center.snapshot().results; len(results) != 0 {
		t.Fatalf("result uploaded after lease loss: %+v", results)
	}
}

func TestSupervisorReportsCancelBeforeStartWithoutProcess(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{}
	center.startCode = "CANCEL_REQUESTED"
	newHarness(t, center, adapter, "")
	waitFor(t, "prepare-failed", func() bool { return len(center.snapshot().prepareFailed) == 1 })
	if code := center.snapshot().prepareFailed[0]; code != "CANCELED" || adapter.startCount() != 0 {
		t.Fatalf("prepare-failed=%s processes=%d", code, adapter.startCount())
	}
}

func TestSupervisorRejectsUnsupportedDirectoryAndRuntime(t *testing.T) {
	for name, mutate := range map[string]func(*fakeCenter){
		"REPO_UNAVAILABLE":    func(c *fakeCenter) { c.directoryMode, c.directoryRoots = "EXPLICIT", []string{"unmapped"} },
		"RUNTIME_UNAVAILABLE": func(c *fakeCenter) { c.runtimeName = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			center, adapter := newCenter(), &fakeAdapter{}
			mutate(center)
			newHarness(t, center, adapter, "")
			waitFor(t, "prepare-failed", func() bool { return len(center.snapshot().prepareFailed) == 1 })
			snapshot := center.snapshot()
			if snapshot.prepareFailed[0] != name || snapshot.starts != 0 || adapter.startCount() != 0 {
				t.Fatalf("prepare-failed=%v starts=%d processes=%d", snapshot.prepareFailed, snapshot.starts, adapter.startCount())
			}
			// prep_env 以 failed 结束，失败的子步骤对应失败原因。
			waitFor(t, "prep failed", func() bool {
				prep := prepSteps(t, center.snapshot().accepted)
				return len(prep) > 0 && prep[len(prep)-1].State == runtime.StepFailed
			})
			prep := prepSteps(t, center.snapshot().accepted)
			final := prep[len(prep)-1]
			failing := final.Substeps[len(final.Substeps)-1]
			want := map[string]string{"REPO_UNAVAILABLE": "repo", "RUNTIME_UNAVAILABLE": "runtime"}[name]
			if failing.State != runtime.StepFailed || failing.Kind != want {
				t.Fatalf("prep failed = %+v", final)
			}
		})
	}
}

func TestSupervisorRestartDoesNotRelaunchUnfinishedRun(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{block: true}
	first := newHarness(t, center, adapter, "")
	waitFor(t, "process start", func() bool { return adapter.startCount() == 1 })
	journalPath := first.journalAt
	first.stop()
	// 正常退出会把记录标为 FINISHED 并以 DAEMON_SHUTDOWN 失败上报。
	if results := center.snapshot().results; len(results) != 1 || results[0].Status != "FAILED" ||
		!strings.HasPrefix(results[0].Summary, "DAEMON_SHUTDOWN") {
		t.Fatalf("shutdown result = %+v", results)
	}
	// 再模拟崩溃现场：记录停在 RUNNING（进程事实未知）。
	reopened, err := OpenJournal(context.Background(), journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.db.Exec(`UPDATE run_journal SET state = 'RUNNING'`); err != nil {
		t.Fatal(err)
	}
	_ = reopened.Close()

	secondAdapter := &fakeAdapter{}
	second := newHarness(t, center, secondAdapter, journalPath)
	time.Sleep(300 * time.Millisecond)
	if secondAdapter.startCount() != 0 {
		t.Fatal("restarted daemon relaunched an unfinished run")
	}
	state, err := second.journal.State(context.Background(), testRun, 1)
	if err != nil || state != JournalAbandoned {
		t.Fatalf("journal state after restart = %q, %v", state, err)
	}
}

func TestJournalFileIsPrivateAndBeginIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "journal.db")
	journal, err := OpenJournal(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode = %v, %v", info.Mode(), err)
	}
	first, err := journal.Begin(context.Background(), testOrg, testRun, 1, testOffer)
	if err != nil || !first {
		t.Fatalf("first Begin = %v, %v", first, err)
	}
	second, err := journal.Begin(context.Background(), testOrg, testRun, 1, testOffer)
	if err != nil || second {
		t.Fatalf("second Begin = %v, %v", second, err)
	}
	next, err := journal.Begin(context.Background(), testOrg, testRun, 2, testOffer)
	if err != nil || !next {
		t.Fatalf("new generation Begin = %v, %v", next, err)
	}
	if _, err := OpenJournal(context.Background(), "relative.db"); err == nil {
		t.Fatal("relative journal path accepted")
	}
}

func TestBuildPromptDropsOldestHistoryWithinBudget(t *testing.T) {
	big := strings.Repeat("x", runtime.MaxPromptBytes/2)
	prompt := buildPrompt(RunContext{Prompt: "now", History: []HistoryMessage{
		{AuthorKind: "USER", Body: big}, {AuthorKind: "AGENT", Body: big}, {AuthorKind: "USER", Body: "recent"},
	}})
	if len(prompt) > runtime.MaxPromptBytes || !strings.Contains(prompt, "[USER] recent") || !strings.HasSuffix(prompt, "now") {
		t.Fatalf("prompt length=%d suffix=%q", len(prompt), prompt[len(prompt)-20:])
	}
	if strings.Count(prompt, big) > 1 {
		t.Fatal("prompt kept both oversized history entries")
	}
	if got := buildPrompt(RunContext{Prompt: "only"}); got != "only" {
		t.Fatalf("empty-history prompt = %q", got)
	}
}

// gitRepository 创建一个带初始提交的本地仓库并返回其规范路径。
func gitRepository(t *testing.T) string {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=OpenFox Test", "-c", "user.email=test@example.invalid", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	return repo
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestSupervisorExplicitRootRunsInConversationWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := gitRepository(t)
	head := gitOutput(t, repo, "rev-parse", "HEAD")
	center, adapter := newCenter(), &fakeAdapter{}
	center.directoryMode, center.directoryRoots = "EXPLICIT", []string{"team/openfox"}
	newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/openfox": repo})
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	if result := center.snapshot().results[0]; result.Status != "SUCCEEDED" {
		t.Fatalf("result = %+v", result)
	}
	adapter.mu.Lock()
	workdirPath := adapter.requests[0].WorkingDirectory
	adapter.mu.Unlock()
	// 在管理根下的独立 worktree 中执行，而不是原仓库目录。
	if workdirPath == repo || strings.HasPrefix(workdirPath, repo+string(filepath.Separator)) {
		t.Fatalf("run used the original repository directory %s", workdirPath)
	}
	if top := gitOutput(t, workdirPath, "rev-parse", "--show-toplevel"); top != workdirPath {
		t.Fatalf("worktree top-level = %s, want %s", top, workdirPath)
	}
	if branch := gitOutput(t, workdirPath, "rev-parse", "--abbrev-ref", "HEAD"); !strings.HasPrefix(branch, "openfox/work/") {
		t.Fatalf("worktree branch = %s", branch)
	}
	// 原仓库的分支、HEAD 与工作区保持不变。
	if got := gitOutput(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatalf("original HEAD moved: %s -> %s", head, got)
	}
	if status := gitOutput(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("original worktree changed: %q", status)
	}
}

func TestSupervisorExplicitRootFailures(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	plain, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := gitRepository(t)
	for name, setup := range map[string]struct {
		roots []string
		local map[string]string
	}{
		"multiple roots": {[]string{"a", "b"}, map[string]string{"a": repo, "b": repo}},
		"not a git repo": {[]string{"plain"}, map[string]string{"plain": plain}},
		"unmapped root":  {[]string{"missing"}, map[string]string{"a": repo}},
	} {
		t.Run(name, func(t *testing.T) {
			center, adapter := newCenter(), &fakeAdapter{}
			center.directoryMode, center.directoryRoots = "EXPLICIT", setup.roots
			newHarnessWithRoots(t, center, adapter, "", setup.local)
			waitFor(t, "prepare-failed", func() bool { return len(center.snapshot().prepareFailed) == 1 })
			snapshot := center.snapshot()
			if snapshot.prepareFailed[0] != "REPO_UNAVAILABLE" || snapshot.starts != 0 || adapter.startCount() != 0 {
				t.Fatalf("prepare-failed=%v starts=%d processes=%d", snapshot.prepareFailed, snapshot.starts, adapter.startCount())
			}
		})
	}
}

func TestSupervisorUploadsUsageAndFallsBackForOldCenter(t *testing.T) {
	for _, reject := range []bool{false, true} {
		center, adapter := newCenter(), &fakeAdapter{usage: &runtime.Usage{Input: 120, CachedInput: 20, Output: 30, ReasoningOutput: 5}}
		center.rejectUsage = reject
		newHarness(t, center, adapter, "")
		waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
		result := center.snapshot().results[0]
		if reject {
			if result.Usage != nil || result.Status != "SUCCEEDED" {
				t.Fatalf("fallback result = %+v", result)
			}
			continue
		}
		if result.Usage == nil || *result.Usage != (runtime.Usage{Input: 120, CachedInput: 20, Output: 30, ReasoningOutput: 5}) {
			t.Fatalf("result usage = %+v", result.Usage)
		}
	}
}

func TestSupervisorSpillsFailedEventsAndRetriesLater(t *testing.T) {
	previous := eventRetryDelay
	eventRetryDelay = time.Millisecond
	t.Cleanup(func() { eventRetryDelay = previous })
	center, adapter := newCenter(), &fakeAdapter{}
	center.failEvents = 1 << 30
	h := newHarness(t, center, adapter, "")
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	// 上传全部失败：事件进入 outbox，结果照常上报。
	waitFor(t, "events spilled", func() bool {
		events, _ := h.journal.PendingEvents(context.Background(), testRun, 100)
		return len(events) >= 5
	})
	if center.snapshot().events != 0 {
		t.Fatalf("events accepted while center failing: %d", center.snapshot().events)
	}
	// 中心恢复后，下一个轮询周期补传 outbox，序号连续且不重复。
	center.mu.Lock()
	center.failEvents = 0
	center.mu.Unlock()
	waitFor(t, "outbox drained", func() bool {
		events, _ := h.journal.PendingEvents(context.Background(), testRun, 100)
		return len(events) == 0
	})
	if types := strings.Join(eventTypes(t, center.snapshot().accepted), ","); types != "step,step,step,started,output" {
		t.Fatalf("event types after drain = %s", types)
	}
}

func TestSupervisorDrainsOutboxOnShutdown(t *testing.T) {
	previous := eventRetryDelay
	eventRetryDelay = time.Millisecond
	t.Cleanup(func() { eventRetryDelay = previous })
	center, adapter := newCenter(), &fakeAdapter{block: true}
	center.failEvents = 1 << 30
	h := newHarness(t, center, adapter, "")
	waitFor(t, "events spilled", func() bool {
		events, _ := h.journal.PendingEvents(context.Background(), testRun, 100)
		return len(events) >= 3
	})
	center.mu.Lock()
	center.failEvents = 0
	center.mu.Unlock()
	// 退出：在途 Run 以 DAEMON_SHUTDOWN 结束，退出前把 outbox 全部补传。
	h.stop()
	if events, _ := h.journal.PendingEvents(context.Background(), testRun, 100); len(events) != 0 {
		t.Fatalf("outbox not drained on shutdown: %d", len(events))
	}
	if types := strings.Join(eventTypes(t, center.snapshot().accepted), ","); !strings.HasPrefix(types, "step,step,step,started") || !strings.HasSuffix(types, "cancelled") {
		t.Fatalf("event types = %s", types)
	}
}

func TestJournalOutboxKeepsNewestEventsPerRun(t *testing.T) {
	journal, err := OpenJournal(context.Background(), filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	batch := make([]EventInput, 0, outboxPerRun+50)
	for sequence := int64(1); sequence <= outboxPerRun+50; sequence++ {
		batch = append(batch, EventInput{ID: newUUID(), Generation: 1, Sequence: sequence, Type: "step", Payload: json.RawMessage(`{}`)})
	}
	if err := journal.SaveEvents(context.Background(), testOrg, testRun, batch); err != nil {
		t.Fatal(err)
	}
	// 重复写入同一事件不产生重复。
	if err := journal.SaveEvents(context.Background(), testOrg, testRun, batch[len(batch)-1:]); err != nil {
		t.Fatal(err)
	}
	other := []EventInput{{ID: newUUID(), Generation: 1, Sequence: 1, Type: "step", Payload: json.RawMessage(`{}`)}}
	if err := journal.SaveEvents(context.Background(), testOrg, "other-run", other); err != nil {
		t.Fatal(err)
	}
	pending, err := journal.PendingEvents(context.Background(), testRun, outboxPerRun+100)
	if err != nil || len(pending) != outboxPerRun || pending[0].Sequence != 51 {
		t.Fatalf("pending = %d, %v", len(pending), err)
	}
	runs, err := journal.OutboxRuns(context.Background())
	if err != nil || len(runs) != 2 {
		t.Fatalf("outbox runs = %v, %v", runs, err)
	}
	if err := journal.DeleteEvents(context.Background(), []string{pending[0].ID}); err != nil {
		t.Fatal(err)
	}
	if pending, _ = journal.PendingEvents(context.Background(), testRun, 1); pending[0].Sequence != 52 {
		t.Fatalf("after delete first = %d", pending[0].Sequence)
	}
}

func TestEventInputBoundsPayload(t *testing.T) {
	data := json.RawMessage(`{"schema":1,"step":{"id":"x","kind":"exec","state":"done","output":"` + strings.Repeat("a", 50*1024) + `"}}`)
	// 控制字符在 JSON 中按 \u00XX 转义，文本加数据会超过 64 KiB，此时只保留数据。
	input := eventInput(1, 7, runtime.Event{Type: runtime.EventStep, Text: strings.Repeat("\x01", 8*1024), Payload: data})
	if len(input.Payload) > maxEventPayload || !strings.Contains(string(input.Payload), `"data"`) || input.Sequence != 7 {
		t.Fatalf("payload len = %d", len(input.Payload))
	}
	tooBig := json.RawMessage(`{"x":"` + strings.Repeat("a", maxEventData) + `"}`)
	if input := eventInput(1, 1, runtime.Event{Type: runtime.EventStep, Payload: tooBig}); strings.Contains(string(input.Payload), `"data"`) {
		t.Fatal("oversized data was kept")
	}
}
