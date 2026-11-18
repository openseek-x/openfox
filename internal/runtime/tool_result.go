package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"unicode/utf8"
)

// BrowserTool recognizes the canonical tool name after a CLI's MCP namespace.
// Browser inputs and results may contain private page or form content. Only
// the operation name and state are suitable for execution events/archives.
func BrowserTool(name string) bool {
	for _, tool := range []string{"openfox_browser_tabs", "openfox_browser_observe", "openfox_browser_act", "openfox_browser_files"} {
		if name == tool || strings.HasSuffix(name, "."+tool) || strings.HasSuffix(name, "_"+tool) {
			return true
		}
	}
	return false
}

func BrowserToolLog(state string) string {
	if state == StepDone {
		return "Browser operation completed."
	}
	return "Browser operation did not complete."
}

const (
	MaxToolTextBytes  = 128 * 1024
	MaxToolImageBytes = 4 * 1024 * 1024
	MaxToolImageEdge  = 2048
)

type ToolCall func(context.Context, string, string, json.RawMessage) ToolResult

// ToolResult carries model input, never an execution-log payload. Images must
// travel as native content blocks, not paths or base64 embedded in Text.
type ToolResult struct {
	Text  string     `json:"text"`
	OK    bool       `json:"ok"`
	Image *ToolImage `json:"image,omitempty"`
}

type ToolImage struct {
	MIMEType string `json:"mime_type"`
	Data     []byte `json:"data"`
}

func TextResult(text string, ok bool) ToolResult { return ToolResult{Text: text, OK: ok} }

// Bounded rejects malformed image output before a provider sees it. Text-only
// tools retain a readable prefix with an explicit truncation marker.
func (r ToolResult) Bounded() ToolResult {
	if len(r.Text) > MaxToolTextBytes {
		end := MaxToolTextBytes - len("\n[truncated]")
		for end > 0 && !utf8.RuneStart(r.Text[end]) {
			end--
		}
		r.Text = r.Text[:end] + "\n[truncated]"
	}
	if r.Image == nil {
		return r
	}
	if !r.OK || len(r.Image.Data) == 0 || len(r.Image.Data) > MaxToolImageBytes {
		return TextResult("invalid or oversized tool image", false)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(r.Image.Data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > MaxToolImageEdge || config.Height > MaxToolImageEdge ||
		(format != "png" && format != "jpeg") || r.Image.MIMEType != "image/"+format {
		return TextResult("invalid or oversized tool image", false)
	}
	return r
}
