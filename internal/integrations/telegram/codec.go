package telegram

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

func (id ID) String() string { return strconv.FormatInt(int64(id), 10) }
func (id ID) MarshalJSON() ([]byte, error) {
	if int64(id) < -MaxID || int64(id) > MaxID {
		return nil, ErrInvalid
	}
	return json.Marshal(id.String())
}
func (id *ID) UnmarshalJSON(raw []byte) error {
	s := string(raw)
	n, e := strconv.ParseInt(s, 10, 64)
	if e != nil || n < -MaxID || n > MaxID || strconv.FormatInt(n, 10) != s {
		return ErrInvalid
	}
	*id = ID(n)
	return nil
}
func validID(id ID) bool    { return id != 0 && int64(id) >= -MaxID && int64(id) <= MaxID }
func positiveID(id ID) bool { return id > 0 && int64(id) <= MaxID }
func bounded(s string, n int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= n && !strings.ContainsRune(s, 0)
}
func username(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
func fixed(kind error, code string) error { return &Error{Kind: kind, Code: code} }
func incomplete(write bool) error {
	if write {
		return fixed(ErrUnknown, "result_unknown")
	}
	return fixed(ErrIncomplete, "incomplete_response")
}

func object(raw []byte) (map[string]json.RawMessage, error) {
	if !validJSON(raw) {
		return nil, ErrInvalid
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil, ErrInvalid
	}
	return m, nil
}
func required(m map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		v := bytes.TrimSpace(m[name])
		if len(v) == 0 || bytes.Equal(v, []byte("null")) {
			return false
		}
	}
	return true
}

// Reject duplicate/case-alias keys, invalid Unicode, and deep or trailing JSON.
// encoding/json's normal last-key-wins and replacement-rune behavior must not
// decide an identity, target, or entity offset.
func validJSON(raw []byte) bool {
	if !utf8.Valid(raw) || !validEscapes(raw) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	first, e := d.Token()
	if e != nil || first != json.Delim('{') || !jsonContainer(d, '{', 0) {
		return false
	}
	_, e = d.Token()
	return e == io.EOF
}
func jsonContainer(d *json.Decoder, kind byte, depth int) bool {
	if depth > 16 {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		if kind == '{' {
			v, e := d.Token()
			s, ok := v.(string)
			s = strings.ToLower(s)
			if e != nil || !ok || seen[s] {
				return false
			}
			seen[s] = true
		}
		v, e := d.Token()
		if e != nil {
			return false
		}
		if x, ok := v.(json.Delim); ok {
			if (x != '{' && x != '[') || !jsonContainer(d, byte(x), depth+1) {
				return false
			}
		}
	}
	end, e := d.Token()
	return e == nil && (kind == '{' && end == json.Delim('}') || kind == '[' && end == json.Delim(']'))
}
func validEscapes(raw []byte) bool {
	in := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			in = !in
			continue
		}
		if !in || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		v, e := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if v >= 0xDC00 && v <= 0xDFFF {
			return false
		}
		if v >= 0xD800 && v <= 0xDBFF {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			lo, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if e != nil || lo < 0xDC00 || lo > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return true
}
