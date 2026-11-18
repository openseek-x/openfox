package supervisor

import (
	"encoding/json"
	"openfox/internal/runtime"
)

func artifactReviewTools() []runtime.Tool {
	return []runtime.Tool{
		{Name: toolPrefix + "file_comment_read", Description: "Read a bounded UTF-8 range of the immutable artifact (Office files return a JSON content projection). Start with start=0, limit=8192. Continue from returned end only while end < returned size_bytes. Original binary file size is not a read offset.", InputSchema: json.RawMessage(`{"type":"object","properties":{"context_id":{"type":"string","format":"uuid"},"start":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":8192}},"required":["context_id","start"],"additionalProperties":false}`)},
		{Name: toolPrefix + "file_comment_apply", Description: "Submit one replacement for each selected exact text/cell annotation being changed. Do not include region, pdf_text or office_range feedback in edits. For requested Office formula/chart/layout changes use file_comment_export and file_comment_replace instead. expected must exactly equal the raw anchor.quote. For CSV, replacement is the decoded cell value. For Office, replacement is the decoded selected text fragment or constant cell, not XML or a file. The server validates ranges, SHA and scope and stores a candidate for the user to review and accept; the original remains unchanged. Use only when the user asks for edits, not for questions. Normal conversation write permissions apply.", InputSchema: json.RawMessage(`{"type":"object","properties":{"context_id":{"type":"string","format":"uuid"},"sha256":{"type":"string"},"summary":{"type":"string","maxLength":2000},"edits":{"type":"array","minItems":1,"maxItems":20,"items":{"type":"object","properties":{"thread_id":{"type":"string"},"expected":{"type":"string"},"replacement":{"type":"string"}},"required":["thread_id","expected","replacement"],"additionalProperties":false}}},"required":["context_id","sha256","summary","edits"],"additionalProperties":false}`)},
	}
}
