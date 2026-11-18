package cliagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Gemini 0.67.0 admits its own project temp in read_file and permits parent
// patterns in glob. Validate complete model frames before native sees a call;
// its builtin tool names alone are not a workspace authorization boundary.
func geminiToolCall(root string, raw json.RawMessage) error {
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil {
		return fmt.Errorf("invalid Gemini function call")
	}
	for key := range keys {
		if key != "name" && key != "args" && key != "id" {
			return fmt.Errorf("noncanonical Gemini function field")
		}
	}
	var call struct {
		Name string                     `json:"name"`
		Args map[string]json.RawMessage `json:"args"`
		ID   string                     `json:"id,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&call) != nil || call.Args == nil || len(call.Args) > 10 || len(call.ID) > 256 {
		return fmt.Errorf("invalid Gemini function call")
	}
	required := ""
	fields := map[string]string{}
	switch call.Name {
	case "read_file":
		required = "file_path"
		fields = map[string]string{"file_path": "path", "start_line": "line", "end_line": "line"}
	case "list_directory":
		required = "dir_path"
		fields = map[string]string{"dir_path": "path", "ignore": "globs", "file_filtering_options": "filters"}
	case "glob":
		required = "pattern"
		fields = map[string]string{"pattern": "glob", "dir_path": "path", "case_sensitive": "bool", "respect_git_ignore": "bool", "respect_gemini_ignore": "bool"}
	case "grep_search":
		required = "pattern"
		fields = map[string]string{"pattern": "regex", "dir_path": "path", "include_pattern": "glob", "exclude_pattern": "regex", "names_only": "bool", "max_matches_per_file": "matches", "total_max_matches": "matches"}
	default:
		return fmt.Errorf("Gemini tool is not supported")
	}
	if _, ok := call.Args[required]; !ok {
		return fmt.Errorf("Gemini tool argument is missing")
	}
	for key, value := range call.Args {
		kind, ok := fields[key]
		if !ok {
			return fmt.Errorf("Gemini tool argument is not supported")
		}
		switch kind {
		case "path", "glob", "regex":
			var s string
			if json.Unmarshal(value, &s) != nil || s == "" || len(s) > 4096 || !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0 {
				return fmt.Errorf("invalid Gemini tool string")
			}
			if kind == "path" && !geminiSnapshotPath(root, s) {
				return fmt.Errorf("Gemini tool path is outside its snapshot")
			}
			if kind == "glob" && !geminiGlob(s) {
				return fmt.Errorf("Gemini glob is outside its supported relative grammar")
			}
		case "line", "matches":
			var n int
			if json.Unmarshal(value, &n) != nil || n < 1 || (kind == "matches" && n > 200) || n > 1000000 {
				return fmt.Errorf("Gemini tool count is outside its limit")
			}
		case "bool":
			var b bool
			if bytes.Equal(value, []byte("null")) || json.Unmarshal(value, &b) != nil {
				return fmt.Errorf("invalid Gemini tool flag")
			}
		case "globs":
			var a []string
			if json.Unmarshal(value, &a) != nil || a == nil || len(a) > 20 {
				return fmt.Errorf("invalid Gemini ignore list")
			}
			for _, s := range a {
				if !geminiGlob(s) {
					return fmt.Errorf("invalid Gemini ignore pattern")
				}
			}
		case "filters":
			var filters map[string]json.RawMessage
			if json.Unmarshal(value, &filters) != nil || filters == nil || len(filters) > 2 {
				return fmt.Errorf("invalid Gemini filters")
			}
			for k, v := range filters {
				var b bool
				if (k != "respect_git_ignore" && k != "respect_gemini_ignore") || bytes.Equal(v, []byte("null")) || json.Unmarshal(v, &b) != nil {
					return fmt.Errorf("invalid Gemini filter")
				}
			}
		}
	}
	return nil
}

func geminiSnapshotPath(root, s string) bool {
	if s == "" || strings.HasPrefix(s, "@") || strings.ContainsAny(s, "\\\x00") || len(s) > 4096 {
		return false
	}
	for _, p := range strings.Split(s, "/") {
		if p == ".." {
			return false
		}
	}
	if !filepath.IsAbs(s) {
		s = filepath.Join(root, s)
	}
	rel, e := filepath.Rel(root, s)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

// No brace/extglob/class expansion: those can synthesize absolute/parent paths
// after a lexical check. Ordinary relative *, ** and ? patterns remain available.
func geminiGlob(s string) bool {
	if s == "" || len(s) > 4096 || strings.HasPrefix(s, "/") || strings.Contains(s, "..") || strings.ContainsAny(s, "\\{}[]!()\x00") || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return false
	}
	return true
}

func geminiJSONObject(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	x, e := d.Token()
	if e != nil || x != json.Delim('{') {
		return false
	}
	if !geminiJSONContainer(d, '{', 0) {
		return false
	}
	_, e = d.Token()
	return e == io.EOF
}
func geminiJSONContainer(d *json.Decoder, kind byte, depth int) bool {
	if depth > 32 {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		if kind == '{' {
			x, e := d.Token()
			s, ok := x.(string)
			s = strings.ToLower(s) // Go struct decoding folds case; JavaScript does not.
			if e != nil || !ok || seen[s] {
				return false
			}
			seen[s] = true
		}
		x, e := d.Token()
		if e != nil {
			return false
		}
		if v, ok := x.(json.Delim); ok {
			if (v != '{' && v != '[') || !geminiJSONContainer(d, byte(v), depth+1) {
				return false
			}
		}
	}
	x, e := d.Token()
	return e == nil && ((kind == '{' && x == json.Delim('}')) || (kind == '[' && x == json.Delim(']')))
}

type geminiStreamState struct {
	Finished, Content bool
	Calls             int
}

func (s *geminiStreamState) accept(root string, raw []byte) error {
	if !geminiJSONObject(raw) {
		return fmt.Errorf("invalid Gemini response JSON")
	}
	var frame map[string]json.RawMessage
	if json.Unmarshal(raw, &frame) != nil {
		return fmt.Errorf("invalid Gemini response")
	}
	if _, ok := frame["error"]; ok {
		return fmt.Errorf("Gemini returned a model error")
	}
	var candidates []map[string]json.RawMessage
	if value, ok := frame["candidates"]; ok {
		if json.Unmarshal(value, &candidates) != nil || len(candidates) > 1 {
			return fmt.Errorf("unsupported Gemini candidate shape")
		}
	}
	for _, c := range candidates {
		var index int
		if value, ok := c["index"]; ok {
			if json.Unmarshal(value, &index) != nil || bytes.Equal(value, []byte("null")) {
				return fmt.Errorf("invalid Gemini candidate index")
			}
		}
		var finishReason string
		if value, ok := c["finishReason"]; ok {
			if json.Unmarshal(value, &finishReason) != nil {
				return fmt.Errorf("invalid Gemini finish reason")
			}
		}
		contentObject := map[string]json.RawMessage{}
		if value, ok := c["content"]; ok {
			if json.Unmarshal(value, &contentObject) != nil || contentObject == nil {
				return fmt.Errorf("invalid Gemini content")
			}
		}
		var role string
		if value, ok := contentObject["role"]; ok {
			if json.Unmarshal(value, &role) != nil {
				return fmt.Errorf("invalid Gemini role")
			}
		}
		var parts []map[string]json.RawMessage
		if value, ok := contentObject["parts"]; ok {
			if json.Unmarshal(value, &parts) != nil || parts == nil {
				return fmt.Errorf("invalid Gemini parts")
			}
		}
		if index != 0 || (role != "" && role != "model") || len(parts) > 64 {
			return fmt.Errorf("unsupported Gemini candidate")
		}

		if s.Finished && len(parts) > 0 {
			return fmt.Errorf("Gemini content after terminal frame")
		}
		for _, p := range parts {
			if len(p) == 0 {
				return fmt.Errorf("empty Gemini content part")
			}
			content := 0
			for k, v := range p {
				switch k {
				case "text":
					var value string
					if json.Unmarshal(v, &value) != nil || bytes.Equal(v, []byte("null")) {
						return fmt.Errorf("invalid Gemini text")
					}
					content++
					s.Content = s.Content || value != ""
				case "thought":
					var value bool
					if json.Unmarshal(v, &value) != nil || bytes.Equal(v, []byte("null")) {
						return fmt.Errorf("invalid Gemini thought flag")
					}
				case "thoughtSignature":
					var value string
					if json.Unmarshal(v, &value) != nil || len(value) > 64<<10 {
						return fmt.Errorf("invalid Gemini thought signature")
					}
				case "functionCall":
					if err := geminiToolCall(root, v); err != nil {
						return err
					}
					content++
					s.Content = true
					s.Calls++
					if s.Calls > 32 {
						return fmt.Errorf("Gemini tool batch exceeds limit")
					}
				default:
					return fmt.Errorf("unsupported Gemini response part")
				}
			}
			if content != 1 {
				return fmt.Errorf("ambiguous Gemini response part")
			}
		}
		if finishReason != "" {
			if finishReason != "STOP" {
				return fmt.Errorf("Gemini model did not finish normally")
			}
			s.Finished = true
		}
	}
	return nil
}
