package runtime

import (
	"strings"
	"unicode/utf8"
)

// OutputRecorder receives decoded, redacted text before display truncation.
// It is local to the daemon, must be bounded and must not perform network I/O.
// Raw protocol frames, image data and tool arguments are never recorded here.
type OutputRecorder func(kind, id, text string)

const (
	DefaultMaxProtocolLineBytes = 8 * 1024 * 1024
	// Claude can repeat a 4 MiB MCP image in both message.content and
	// tool_use_result. This remains a stdout-only transport budget, not a
	// retained-text budget; adapters opt in only when the local tool bridge runs.
	MaxToolProtocolLineBytes  = 12 * 1024 * 1024
	DefaultOutputPreviewBytes = 256 * 1024
	OutputTruncationNotice    = "\n\n[OpenFox: output truncated at the retained text limit.]"
)

// TextBuffer bounds retained UTF-8 text independently of bytes passing through
// a protocol stream. Reaching the limit does not stop the running process.
type TextBuffer struct {
	Limit     int
	text      strings.Builder
	truncated bool
}

func (b *TextBuffer) limit() int {
	if b.Limit > 0 {
		return b.Limit
	}
	return DefaultMaxOutput
}

func (b *TextBuffer) WriteString(text string) {
	if b.truncated {
		return
	}
	remaining := b.limit() - b.text.Len()
	if len(text) > remaining {
		b.truncated = true
		text = utf8Prefix(text, remaining)
	}
	b.text.WriteString(text)
}

func (b *TextBuffer) String() string {
	if b.truncated {
		return truncatedText(b.text.String(), b.limit())
	}
	return b.text.String()
}

func (b *TextBuffer) Reset() { b.text.Reset(); b.truncated = false }

// OutputPreview makes clipping visible and includes the notice in the budget.
func OutputPreview(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return truncatedText(text, limit)
}

func truncatedText(text string, limit int) string {
	notice := utf8Prefix(OutputTruncationNotice, limit)
	return utf8Prefix(text, limit-len(notice)) + notice
}

func utf8Prefix(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}
