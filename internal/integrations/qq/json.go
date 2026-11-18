package qq

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

func ID(v string) bool {
	return len(v) > 0 && len(v) <= MaxIDBytes && utf8.ValidString(v) && strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}
func secret(v string) bool {
	return len(v) > 0 && len(v) <= 8192 && utf8.ValidString(v) && strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}
func text(v string) bool {
	return utf8.ValidString(v) && strings.TrimSpace(v) != "" && utf8.RuneCountInString(v) <= MaxTextRunes && !strings.ContainsRune(v, 0)
}

// Authentication inputs reject duplicate keys, trailing documents and deep
// nesting instead of choosing a different last-key interpretation than QQ.
func object(raw []byte) bool {
	if len(raw) == 0 || len(raw) > MaxBodyBytes || !utf8.Valid(raw) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	x, e := d.Token()
	if e != nil || x != json.Delim('{') {
		return false
	}
	if !container(d, '{', 0) {
		return false
	}
	_, e = d.Token()
	return e == io.EOF
}
func container(d *json.Decoder, kind byte, depth int) bool {
	if depth > 32 {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		if kind == '{' {
			x, e := d.Token()
			key, ok := x.(string)
			if e != nil || !ok || seen[key] {
				return false
			}
			seen[key] = true
		}
		x, e := d.Token()
		if e != nil {
			return false
		}
		if delim, ok := x.(json.Delim); ok {
			if delim != '{' && delim != '[' || !container(d, byte(delim), depth+1) {
				return false
			}
		}
	}
	x, e := d.Token()
	return e == nil && (kind == '{' && x == json.Delim('}') || kind == '[' && x == json.Delim(']'))
}
