// Package providercli exposes selected read operations of the device owner's installed CLIs.
// Login and installation are interactive owner actions, outside the MCP server.
package providercli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type operation struct {
	name, description string
	args              []string
}

type provider struct {
	binary     string
	operations []operation
}

var providers = map[string]provider{
	"feishu": {"lark-cli", []operation{
		{"today_agenda", "Read today's calendar agenda for the signed-in user.", []string{"calendar", "+agenda", "--as", "user"}},
	}},
	"apifox": {"apifox", []operation{
		{"list_projects", "List projects available to the signed-in Apifox user.", []string{"project", "list"}},
		{"list_teams", "List teams available to the signed-in Apifox user.", []string{"team", "list"}},
	}},
	"cnb": {"cnb", []operation{
		{"get_user", "Read the signed-in CNB user's profile.", []string{"users", "get-user-info"}},
		{"list_repositories", "List the first page of repositories owned by the signed-in CNB user.", []string{"repositories", "get-repos"}},
	}},
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type object = map[string]any

func Serve(ctx context.Context, id string, in io.Reader, out io.Writer) error {
	p, ok := providers[id]
	if !ok {
		return errors.New("unsupported provider CLI")
	}
	dir, err := os.MkdirTemp("", "openfox-provider-cli-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var req request
		response := object{"jsonrpc": "2.0", "id": nil}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			response["error"] = rpcError(-32700, "Invalid JSON")
		} else if req.JSONRPC != "2.0" || req.Method == "" {
			response["error"] = rpcError(-32600, "Invalid request")
		} else if len(req.ID) == 0 {
			continue
		} else {
			response["id"] = req.ID
			result, fault := p.handle(ctx, dir, req)
			if fault != nil {
				response["error"] = fault
			} else {
				response["result"] = result
			}
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func rpcError(code int, message string) object { return object{"code": code, "message": message} }

func (p provider) handle(ctx context.Context, dir string, req request) (object, object) {
	switch req.Method {
	case "initialize":
		var client struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &client)
		version := "2025-11-25"
		switch client.ProtocolVersion {
		case "2024-11-05", "2025-03-26", "2025-06-18":
			version = client.ProtocolVersion
		}
		return object{"protocolVersion": version, "capabilities": object{"tools": object{}}, "serverInfo": object{"name": "openfox-" + p.binary, "version": "1"}}, nil
	case "ping":
		return object{}, nil
	case "tools/list":
		tools := make([]object, 0, len(p.operations))
		for _, op := range p.operations {
			tools = append(tools, object{"name": op.name, "description": op.description, "inputSchema": object{"type": "object", "properties": object{}, "additionalProperties": false}, "annotations": object{"readOnlyHint": true}})
		}
		return object{"tools": tools}, nil
	case "tools/call":
		var params struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || len(params.Arguments) > 0 {
			return nil, rpcError(-32602, "Expected a tool name and no arguments")
		}
		for _, op := range p.operations {
			if op.name == params.Name {
				output, err := p.run(ctx, dir, op)
				if err != nil {
					output = err.Error()
				}
				return object{"content": []object{{"type": "text", "text": output}}, "isError": err != nil}, nil
			}
		}
		return nil, rpcError(-32602, "Unknown tool")
	default:
		return nil, rpcError(-32601, "Method not found")
	}
}

// Do not pass runtime credentials, dynamic loader settings or project-level CLI config
// into a delegated read. The official CLI uses its own usual per-user sign-in.
func (p provider) run(ctx context.Context, dir string, op operation) (string, error) {
	exe, err := exec.LookPath(p.binary)
	if err != nil || !filepath.IsAbs(exe) {
		return "", errors.New(p.binary + " was not found in the run computer's PATH. Install the official CLI on that computer.")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, op.args...)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	var paths []string
	for _, part := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(part) {
			paths = append(paths, part)
		}
	}
	cmd.Env = []string{"PATH=" + strings.Join(paths, string(os.PathListSeparator)), "NO_COLOR=1"}
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "SystemRoot", "LANG", "LC_ALL", "TMPDIR"} {
		if val, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+val)
		}
	}
	var stdout limitedOutput
	cmd.Stdout = &stdout
	// CLI diagnostics can contain authorization URLs and tokens. Only its
	// successful read result belongs in the conversation.
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", errors.New("Provider CLI call timed out or was cancelled")
		}
		return "", errors.New(p.binary + " could not complete this read. Check the installed CLI, sign-in, and permissions on the run computer.")
	}
	if stdout.truncated {
		return "", errors.New("Provider CLI output exceeded the 256 KiB limit")
	}
	return strings.TrimSpace(stdout.buf.String()), nil
}

type limitedOutput struct {
	buf       bytes.Buffer
	truncated bool
}

func (w *limitedOutput) Write(data []byte) (int, error) {
	size := len(data)
	remaining := 256*1024 - w.buf.Len()
	if size > remaining {
		data = data[:remaining]
		w.truncated = true
	}
	w.buf.Write(data)
	return size, nil
}
