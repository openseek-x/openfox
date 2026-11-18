package toolbridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"openfox/internal/runtime"
)

func post(t *testing.T, endpoint, method string, params any) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "call-one", "method": method, "params": params})
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return response.StatusCode, result
}

func TestNativeImageContentAndMetadata(t *testing.T) {
	var encoded bytes.Buffer
	_ = png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	calls := 0
	bridge, err := Start(context.Background(), []runtime.Tool{{Name: "observe", ReadOnly: true, InputSchema: json.RawMessage(`{"type":"object"}`)}}, func(context.Context, string, string, json.RawMessage) runtime.ToolResult {
		calls++
		return runtime.ToolResult{Text: "page screenshot", OK: true, Image: &runtime.ToolImage{MIMEType: "image/png", Data: encoded.Bytes()}}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	_, listed := post(t, bridge.URL, "tools/list", nil)
	tool := listed["result"].(map[string]any)["tools"].([]any)[0].(map[string]any)
	if tool["annotations"].(map[string]any)["readOnlyHint"] != true {
		t.Fatal("read-only metadata missing")
	}
	_, reply := post(t, bridge.URL, "tools/call", map[string]any{"name": "observe"})
	result := reply["result"].(map[string]any)
	content := result["content"].([]any)
	picture := content[1].(map[string]any)
	if result["isError"] != false || len(content) != 2 || picture["type"] != "image" || picture["mimeType"] != "image/png" || picture["data"] != base64.StdEncoding.EncodeToString(encoded.Bytes()) {
		t.Fatal("native image was not delivered")
	}
	post(t, bridge.URL, "tools/call", map[string]any{"name": "unknown"})
	status, _ := post(t, strings.Replace(bridge.URL, bridge.Token, "wrong", 1), "tools/list", nil)
	if status != 404 || calls != 1 {
		t.Fatal("unknown token or tool invoked callback")
	}
}

func TestCancellationStopsNewCallsAndDiscardsLateImages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	calls := 0
	bridge, err := Start(ctx, []runtime.Tool{{Name: "act", InputSchema: json.RawMessage(`{"type":"object"}`)}}, func(ctx context.Context, _, _ string, _ json.RawMessage) runtime.ToolResult {
		calls++
		close(started)
		<-ctx.Done()
		return runtime.TextResult("late successful action", true)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	go func() { <-started; cancel() }()
	_, reply := post(t, bridge.URL, "tools/call", map[string]any{"name": "act"})
	result := reply["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatal("cancelled call reported success")
	}
	post(t, bridge.URL, "tools/call", map[string]any{"name": "act"})
	if calls != 1 {
		t.Fatal("cancelled run accepted another call")
	}
}

func TestBridgeBoundsConcurrentCallsAndCloseCancelsThem(t *testing.T) {
	started := make(chan struct{}, 4)
	finished := make(chan bool, 4)
	bridge, err := Start(context.Background(), []runtime.Tool{{Name: "observe", InputSchema: json.RawMessage(`{"type":"object"}`)}}, func(ctx context.Context, _, _ string, _ json.RawMessage) runtime.ToolResult {
		started <- struct{}{}
		<-ctx.Done()
		return runtime.TextResult("late", true)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	for range 4 {
		go func() {
			_, reply := post(t, bridge.URL, "tools/call", map[string]any{"name": "observe"})
			result, _ := reply["result"].(map[string]any)
			finished <- result["isError"] == true
		}()
	}
	for range 4 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("tool did not start")
		}
	}
	_, rejected := post(t, bridge.URL, "tools/call", map[string]any{"name": "observe"})
	if rejected["error"] == nil {
		t.Fatal("bridge accepted unbounded concurrent work")
	}
	bridge.Close()
	for range 4 {
		select {
		case cancelled := <-finished:
			if !cancelled {
				t.Error("closing bridge reported late success")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("closing bridge did not cancel callback")
		}
	}
}
