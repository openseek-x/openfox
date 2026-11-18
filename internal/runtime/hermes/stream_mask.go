package hermes

import "strings"

// Keep only a suffix that could be a split credential. Redacting individual ACP
// chunks is insufficient: providers may echo a key split across notifications.
type streamMask struct {
	secrets []string
	pending string
}

func (m *streamMask) feed(text string) string {
	text = m.pending + text
	m.pending = ""
	var out strings.Builder
	for len(text) > 0 {
		longest := 0
		partial := false
		for _, secret := range m.secrets {
			if secret == "" {
				continue
			}
			if len(text) < len(secret) && strings.HasPrefix(secret, text) {
				partial = true
			}
			if len(secret) > longest && strings.HasPrefix(text, secret) {
				longest = len(secret)
			}
		}
		if partial {
			m.pending = text
			break
		}
		if longest > 0 {
			out.WriteString("[REDACTED]")
			text = text[longest:]
		} else {
			out.WriteByte(text[0])
			text = text[1:]
		}
	}
	return out.String()
}
func (m *streamMask) finish() string {
	if m.pending == "" {
		return ""
	}
	m.pending = ""
	return "[REDACTED]"
}
