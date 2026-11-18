package integrations

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const ProviderBugly = "bugly"
const BuglyBodyLimit = 1 << 20

var ErrBuglyPayload = errors.New("bugly: invalid payload")
var ErrBuglyInactive = errors.New("bugly: reception is unavailable")
var ErrBuglySourceDeleted = errors.New("bugly: original task is unavailable")

type BuglyPayload struct {
	ProductID   string
	IssueKey    string
	IssueType   string
	ProjectName string
	Title       string
	Description string
	Priority    string
}

// Decode duplicates before decoding typed fields. encoding/json alone silently
// accepts the last duplicate key (including the malformed official example).
func buglyJSONValue(d *json.Decoder, depth int) (any, error) {
	if depth > 16 {
		return nil, ErrBuglyPayload
	}
	t, e := d.Token()
	if e != nil {
		return nil, ErrBuglyPayload
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	switch delim {
	case '{':
		m := map[string]any{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return nil, ErrBuglyPayload
			}
			s, ok := k.(string)
			if !ok || len(m) >= 100 {
				return nil, ErrBuglyPayload
			}
			if _, ok = m[s]; ok {
				return nil, ErrBuglyPayload
			}
			v, e := buglyJSONValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			m[s] = v
		}
		t, e = d.Token()
		if e != nil || t != json.Delim('}') {
			return nil, ErrBuglyPayload
		}
		return m, nil
	case '[':
		a := []any{}
		for d.More() {
			if len(a) >= 100 {
				return nil, ErrBuglyPayload
			}
			v, e := buglyJSONValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		t, e = d.Token()
		if e != nil || t != json.Delim(']') {
			return nil, ErrBuglyPayload
		}
		return a, nil
	default:
		return nil, ErrBuglyPayload
	}
}
func buglyText(s string, max int, multiline bool) bool {
	if !utf8.ValidString(s) || len([]rune(s)) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t')) {
			return false
		}
	}
	return true
}
func buglyString(m map[string]any, key string, max int, required, multiline bool) (string, error) {
	v, ok := m[key]
	if !ok && !required {
		return "", nil
	}
	s, ok := v.(string)
	if !ok || !buglyText(s, max, multiline) || (required && strings.TrimSpace(s) == "") {
		return "", ErrBuglyPayload
	}
	return s, nil
}

var buglyURL = regexp.MustCompile(`(?i)https?://[^\s<>"'` + "`" + `]+`)

func buglySafeText(s string) string {
	// Decode entities before finding URLs so token&amp;secret cannot bypass query
	// removal. No HTML is rendered: callers persist a fenced literal block.
	s = html.UnescapeString(s)
	return buglyURL.ReplaceAllStringFunc(s, func(raw string) string {
		u, e := url.Parse(raw)
		if e != nil {
			return "[invalid URL]"
		}
		u.User = nil
		u.Fragment = ""
		q, e := url.ParseQuery(u.RawQuery)
		if e != nil {
			u.RawQuery = ""
			return u.String()
		}
		for k := range q {
			n := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(k, "_", ""), "-", ""))
			if strings.Contains(n, "token") || strings.Contains(n, "secret") || strings.Contains(n, "password") || strings.Contains(n, "signature") || strings.Contains(n, "credential") || n == "auth" || n == "authorization" || n == "apikey" || n == "accesskey" || n == "key" || n == "sig" {
				q.Del(k)
			}
		}
		u.RawQuery = q.Encode()
		return u.String()
	})
}
func ParseBuglyPayload(body []byte) (BuglyPayload, error) {
	var p BuglyPayload
	if len(body) == 0 || len(body) > BuglyBodyLimit || !utf8.Valid(body) {
		return p, ErrBuglyPayload
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	v, e := buglyJSONValue(d, 0)
	if e != nil {
		return p, e
	}
	if _, e = d.Token(); e != io.EOF {
		return p, ErrBuglyPayload
	}
	m, ok := v.(map[string]any)
	if !ok {
		return p, ErrBuglyPayload
	}
	for _, f := range []struct {
		k        string
		dest     *string
		max      int
		required bool
	}{{"ProductID", &p.ProductID, 255, true}, {"IssueKey", &p.IssueKey, 255, true}, {"IssueType", &p.IssueType, 64, true}, {"ProjectName", &p.ProjectName, 200, false}} {
		*f.dest, e = buglyString(m, f.k, f.max, f.required, false)
		if e != nil {
			return p, e
		}
	}
	fields, ok := m["FieldValues"].(map[string]any)
	if !ok {
		return p, ErrBuglyPayload
	}
	p.Title, e = buglyString(fields, "title", 500, true, false)
	if e != nil {
		return p, e
	}
	p.Title = buglySafeText(strings.TrimSpace(p.Title))
	raw, e := buglyString(fields, "description", 10000, false, true)
	if e != nil {
		return p, e
	}
	p.Priority, e = buglyString(fields, "priority", 64, false, false)
	if e != nil {
		return p, e
	}
	var notes []string
	for _, k := range []string{"reporter", "current_owner"} {
		s, e := buglyString(fields, k, 255, false, false)
		if e != nil {
			return p, e
		}
		if s != "" {
			notes = append(notes, k+": "+s)
		}
	}
	if len(notes) > 0 {
		raw = strings.Join(notes, "\n") + "\n\n" + raw
	}
	raw = buglySafeText(raw)
	if raw != "" {
		longest, current := 0, 0
		for _, c := range raw {
			if c == '`' {
				current++
				if current > longest {
					longest = current
				}
			} else {
				current = 0
			}
		}
		n := longest + 1
		if n < 3 {
			n = 3
		}
		fence := strings.Repeat("`", n)
		p.Description = fmt.Sprintf("%stext\n%s\n%s", fence, raw, fence)
	}
	if !buglyText(p.Title, 500, false) || !buglyText(p.Description, 10000, true) {
		return p, ErrBuglyPayload
	}
	return p, nil
}
