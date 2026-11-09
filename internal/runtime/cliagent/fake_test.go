package cliagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"openfox/internal/runtime"
)

// 测试用假命令行：测试二进制自己充当 claude / opencode / pi，按 OPENFOX_FAKE_SCENARIO 指向的 JSON 场景输出。
// 不会运行任何真实的 Runtime。
type scenario struct {
	Version   string   `json:"version"`
	AuthJSON  string   `json:"auth_json"`
	AuthExit  int      `json:"auth_exit"`
	ModelsOut string   `json:"models_out"`
	Stdout    []string `json:"stdout"`
	Stderr    string   `json:"stderr"`
	Exit      int      `json:"exit"`
	SleepMS   int      `json:"sleep_ms"`
	Record    string   `json:"record"`
	// MCPFrom 为 claude 时从 --mcp-config 文件读 URL，为 opencode 时从独立 OPENCODE_CONFIG 文件读；
	// 之后按 MCP 流程调用 MCPTool 并把响应写到 Record + ".mcp"。
	MCPFrom string `json:"mcp_from"`
	MCPTool string `json:"mcp_tool"`
}

func TestMain(m *testing.M) {
	if path := os.Getenv("OPENFOX_FAKE_SCENARIO"); path != "" {
		runFake(path)
		return
	}
	os.Exit(m.Run())
}

func runFake(path string) {
	raw, _ := os.ReadFile(path)
	var sc scenario
	_ = json.Unmarshal(raw, &sc)
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--version" {
		fmt.Println(sc.Version)
		return
	}
	if len(args) > 0 && args[0] == "auth" {
		fmt.Println(sc.AuthJSON)
		os.Exit(sc.AuthExit)
	}
	if len(args) > 0 && (args[0] == "models" || args[0] == "--list-models") {
		fmt.Print(sc.ModelsOut)
		return
	}
	stdin, _ := io.ReadAll(os.Stdin)
	if sc.Record != "" {
		record, _ := json.Marshal(map[string]any{"args": args, "stdin": string(stdin),
			"permission": os.Getenv("OPENCODE_PERMISSION"), "home": os.Getenv("HOME")})
		_ = os.WriteFile(sc.Record, record, 0o600)
	}
	if sc.MCPFrom != "" {
		callMCP(sc, args)
	}
	for _, line := range sc.Stdout {
		fmt.Println(line)
	}
	if sc.Stderr != "" {
		fmt.Fprintln(os.Stderr, sc.Stderr)
	}
	if sc.SleepMS > 0 {
		time.Sleep(time.Duration(sc.SleepMS) * time.Millisecond)
	}
	os.Exit(sc.Exit)
}

func callMCP(sc scenario, args []string) {
	var url string
	switch sc.MCPFrom {
	case "claude":
		for index, arg := range args {
			if arg == "--mcp-config" && index+1 < len(args) {
				raw, _ := os.ReadFile(args[index+1])
				var config struct {
					Servers map[string]struct {
						URL string `json:"url"`
					} `json:"mcpServers"`
				}
				_ = json.Unmarshal(raw, &config)
				url = config.Servers["openfox"].URL
			}
		}
	case "opencode":
		var config struct {
			MCP map[string]struct {
				URL string `json:"url"`
			} `json:"mcp"`
		}
		b, _ := os.ReadFile(os.Getenv("OPENCODE_CONFIG"))
		_ = json.Unmarshal(b, &config)
		url = config.MCP["openfox"].URL
	}
	post := func(body string) string {
		response, err := http.Post(url, "application/json", strings.NewReader(body))
		if err != nil {
			return "ERR " + err.Error()
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return fmt.Sprintf("%d %s", response.StatusCode, data)
	}
	var out bytes.Buffer
	out.WriteString(post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`) + "\n")
	out.WriteString(post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`) + "\n")
	out.WriteString(post(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`) + "\n")
	out.WriteString(post(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":%q,"arguments":{"a":1}}}`, sc.MCPTool)) + "\n")
	out.WriteString(post(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`) + "\n")
	if wrong := strings.Replace(url, "/mcp/", "/mcp/x", 1); wrong != url {
		response, err := http.Post(wrong, "application/json", strings.NewReader(`{}`))
		if err == nil {
			out.WriteString(fmt.Sprintf("wrong-token %d\n", response.StatusCode))
			response.Body.Close()
		}
	}
	_ = os.WriteFile(sc.Record+".mcp", out.Bytes(), 0o600)
}

// fixture 返回指向假命令行的适配器与场景写入函数。
type fixture struct {
	t       *testing.T
	adapter *Adapter
	dir     string
}

func newFixture(t *testing.T, adapter *Adapter) *fixture {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	adapter.Executable = exe
	adapter.StateRoot = dir + "/state"
	adapter.Environment = map[string]string{"OPENFOX_FAKE_SCENARIO": dir + "/scenario.json", "HOME": dir, "XDG_CONFIG_HOME": dir + "/config", "XDG_DATA_HOME": dir + "/data", "OPENCODE_CONFIG": "", "OPENCODE_CONFIG_CONTENT": "", "OPENCODE_AUTH_CONTENT": ""}
	return &fixture{t: t, adapter: adapter, dir: dir}
}

func (f *fixture) set(sc scenario) {
	f.t.Helper()
	if sc.Version == "" {
		sc.Version = "1.2.3"
	}
	if sc.Record == "" {
		sc.Record = f.dir + "/record.json"
	}
	raw, _ := json.Marshal(sc)
	if err := os.WriteFile(f.dir+"/scenario.json", raw, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) record() (rec struct {
	Args       []string `json:"args"`
	Stdin      string   `json:"stdin"`
	Permission string   `json:"permission"`
}) {
	f.t.Helper()
	raw, err := os.ReadFile(f.dir + "/record.json")
	if err != nil {
		f.t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &rec)
	return rec
}

type runOutcome struct {
	events []runtime.Event
	result runtime.Result
}

func (o runOutcome) steps() []runtime.Step {
	var steps []runtime.Step
	for _, event := range o.events {
		if event.Type != runtime.EventStep {
			continue
		}
		var payload struct {
			Step runtime.Step `json:"step"`
		}
		_ = json.Unmarshal(event.Payload, &payload)
		steps = append(steps, payload.Step)
	}
	return steps
}

func (o runOutcome) has(kind runtime.EventType) bool {
	for _, event := range o.events {
		if event.Type == kind {
			return true
		}
	}
	return false
}

// finalStep 返回 id 对应的最后一个步骤。
func (o runOutcome) finalStep(id string) (runtime.Step, bool) {
	var found runtime.Step
	ok := false
	for _, step := range o.steps() {
		if step.ID == id {
			found, ok = step, true
		}
	}
	return found, ok
}

func (f *fixture) run(req runtime.RunRequest) runOutcome {
	f.t.Helper()
	if req.Prompt == "" {
		req.Prompt = "do the thing"
	}
	if req.WorkingDirectory == "" {
		req.WorkingDirectory = f.dir
	}
	execution, err := f.adapter.Start(context.Background(), req)
	if err != nil {
		f.t.Fatalf("Start: %v", err)
	}
	var outcome runOutcome
	done := make(chan struct{})
	go func() {
		defer close(done)
		for event := range execution.Events() {
			outcome.events = append(outcome.events, event)
		}
	}()
	result, err := execution.Wait(context.Background())
	if err != nil {
		f.t.Fatalf("Wait: %v", err)
	}
	<-done
	outcome.result = result
	return outcome
}

func j(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
