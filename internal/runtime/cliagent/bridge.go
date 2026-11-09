package cliagent

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"openfox/internal/runtime"
)

// 任务工具桥：在 127.0.0.1 随机端口启动一个最小的 MCP（Streamable HTTP，JSON 响应模式）服务，
// 把 RunRequest.Tools 作为 MCP 工具暴露给 Claude Code（--mcp-config，type=http）与 OpenCode（remote MCP）。
// 每次 Run 一个服务与一个随机路径令牌，Run 结束即关闭；只实现 initialize / ping / tools/list / tools/call。
// 协议出处：https://modelcontextprotocol.io/specification/2025-03-26/basic/transports#streamable-http
const (
	bridgeServerName = "openfox"
	// bridgeBodyLimit 限制单个请求体；bridgeCallTimeout 限制单次工具调用。
	bridgeBodyLimit   = 1 << 20
	bridgeCallTimeout = 2 * time.Minute
	bridgeProtocol    = "2025-03-26"
)

type bridgeInfo struct {
	url    string
	token  string
	server *http.Server
	once   sync.Once
}

func (b *bridgeInfo) close() {
	b.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = b.server.Shutdown(ctx)
		_ = b.server.Close()
	})
}

func startBridge(ctx context.Context, tools []runtime.Tool, call func(context.Context, string, string, json.RawMessage) (string, bool)) (*bridgeInfo, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(raw)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	known := make(map[string]runtime.Tool, len(tools))
	for _, tool := range tools {
		known[tool.Name] = tool
	}
	path := "/mcp/" + token
	handler := &bridgeHandler{path: path, tools: tools, known: known, call: call, base: context.WithoutCancel(ctx)}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second}
	go func() { _ = server.Serve(listener) }()
	return &bridgeInfo{url: "http://" + listener.Addr().String() + path, token: token, server: server}, nil
}

type bridgeHandler struct {
	path  string
	tools []runtime.Tool
	known map[string]runtime.Tool
	call  func(context.Context, string, string, json.RawMessage) (string, bool)
	base  context.Context
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (h *bridgeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.URL.Path), []byte(h.path)) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK)
		return
	default:
		// 不提供服务端主动推送流。
		w.Header().Set("Allow", "POST, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, bridgeBodyLimit+1))
	if err != nil || len(body) > bridgeBodyLimit {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var request rpcRequest
	if json.Unmarshal(body, &request) != nil || request.Method == "" {
		writeRPC(w, nil, nil, &rpcError{Code: -32700, Message: "parse error"})
		return
	}
	if len(request.ID) == 0 || string(request.ID) == "null" {
		// 通知（notifications/initialized 等）：无响应体。
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(request.Params, &params)
		version := params.ProtocolVersion
		if version == "" || len(version) > 32 {
			version = bridgeProtocol
		}
		writeRPC(w, request.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": bridgeServerName, "version": "1"},
		}, nil)
	case "ping":
		writeRPC(w, request.ID, map[string]any{}, nil)
	case "tools/list":
		list := make([]map[string]any, 0, len(h.tools))
		for _, tool := range h.tools {
			list = append(list, map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": json.RawMessage(tool.InputSchema)})
		}
		writeRPC(w, request.ID, map[string]any{"tools": list}, nil)
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(request.Params, &params) != nil {
			writeRPC(w, request.ID, nil, &rpcError{Code: -32602, Message: "invalid params"})
			return
		}
		if _, ok := h.known[params.Name]; !ok {
			writeRPC(w, request.ID, nil, &rpcError{Code: -32602, Message: "unknown tool"})
			return
		}
		if len(params.Arguments) == 0 {
			params.Arguments = json.RawMessage(`{}`)
		}
		callCtx, cancel := context.WithTimeout(h.base, bridgeCallTimeout)
		defer cancel()
		go func() {
			select {
			case <-r.Context().Done():
				cancel()
			case <-callCtx.Done():
			}
		}()
		text, ok := h.call(callCtx, params.Name, string(request.ID), params.Arguments)
		writeRPC(w, request.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}}, "isError": !ok,
		}, nil)
	default:
		writeRPC(w, request.ID, nil, &rpcError{Code: -32601, Message: "method not found"})
	}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rpcErr *rpcError) {
	response := map[string]any{"jsonrpc": "2.0"}
	if len(id) > 0 {
		response["id"] = json.RawMessage(id)
	} else {
		response["id"] = nil
	}
	if rpcErr != nil {
		response["error"] = rpcErr
	} else {
		response["result"] = result
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(encoded)
}
