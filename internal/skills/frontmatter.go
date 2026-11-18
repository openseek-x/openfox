package skills

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

// SKILL.md 格式：YAML frontmatter（--- 包住，至少 name、description 两个字符串键）+ 正文（指令）。
// 这里只解析技能需要的子集：顶层 `key: value`，值可以是纯文本、单双引号字符串，或 > / | 块标量，
// 缩进的后续行并入上一个键；其他键被忽略。

// Parsed 是 SKILL.md 的解析结果。
type Parsed struct {
	Name        string
	Description string
	Body        string
}

var frontmatterKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ParseSkillMD 解析 SKILL.md。frontmatter 缺失或缺少 name / description 返回 ErrInvalid；name 不合规返回 ErrNameInvalid。
func ParseSkillMD(content []byte) (Parsed, error) {
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(content) {
		return Parsed{}, ErrInvalid
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], " \t") != "---" {
		return Parsed{}, ErrInvalid
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return Parsed{}, ErrInvalid
	}
	values, err := parseFrontmatterLines(lines[1:end])
	if err != nil {
		return Parsed{}, err
	}
	name, hasName := values["name"]
	description, hasDescription := values["description"]
	if !hasName || !hasDescription {
		return Parsed{}, ErrInvalid
	}
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" {
		return Parsed{}, ErrInvalid
	}
	if !ValidName(name) {
		return Parsed{}, ErrNameInvalid
	}
	if utf8.RuneCountInString(description) > MaxDescriptionRunes {
		return Parsed{}, ErrInvalid
	}
	body := strings.Join(lines[end+1:], "\n")
	body = strings.TrimPrefix(body, "\n")
	return Parsed{Name: name, Description: description, Body: body}, nil
}

// parseFrontmatterLines 把 frontmatter 的行解析成 键 → 字符串值。
func parseFrontmatterLines(lines []string) (map[string]string, error) {
	values := map[string]string{}
	for i := 0; i < len(lines); {
		line := lines[i]
		i++
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") && line[0] != ' ' && line[0] != '\t' {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			// 没有归属键的缩进行（例如列表项的续行）：忽略。
			continue
		}
		key, raw, found := strings.Cut(line, ":")
		if !found || !frontmatterKey.MatchString(key) {
			return nil, ErrInvalid
		}
		var continuation []string
		for i < len(lines) && (lines[i] == "" || lines[i][0] == ' ' || lines[i][0] == '\t') {
			continuation = append(continuation, lines[i])
			i++
		}
		value, err := scalarValue(strings.TrimSpace(raw), continuation)
		if err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, nil
}

// scalarValue 把键后的内容与缩进续行还原成字符串。
func scalarValue(raw string, continuation []string) (string, error) {
	if raw != "" && (raw[0] == '>' || raw[0] == '|') {
		return blockScalar(raw[0] == '>', continuation), nil
	}
	joined := raw
	for _, line := range continuation {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			joined += " " + trimmed
		}
	}
	joined = strings.TrimSpace(joined)
	switch {
	case strings.HasPrefix(joined, `"`):
		var value string
		if err := json.Unmarshal([]byte(joined), &value); err != nil {
			return "", ErrInvalid
		}
		return value, nil
	case strings.HasPrefix(joined, "'"):
		if len(joined) < 2 || !strings.HasSuffix(joined, "'") {
			return "", ErrInvalid
		}
		return strings.ReplaceAll(joined[1:len(joined)-1], "''", "'"), nil
	}
	// 纯文本：空白加 # 之后是注释。
	if index := strings.Index(joined, " #"); index >= 0 {
		joined = joined[:index]
	}
	return strings.TrimSpace(joined), nil
}

// blockScalar 处理 > 与 | 块标量：去掉公共缩进；> 把相邻非空行折成一行，| 保留换行。
func blockScalar(fold bool, continuation []string) string {
	indent := -1
	for _, line := range continuation {
		if strings.TrimSpace(line) == "" {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	if indent < 0 {
		return ""
	}
	lines := make([]string, len(continuation))
	for i, line := range continuation {
		if len(line) >= indent {
			lines[i] = strings.TrimRight(line[indent:], " \t")
		}
	}
	if !fold {
		return strings.TrimRight(strings.Join(lines, "\n"), "\n")
	}
	var builder strings.Builder
	for i, line := range lines {
		switch {
		case line == "":
			if builder.Len() > 0 && i > 0 && lines[i-1] != "" {
				builder.WriteString("\n")
			}
		case builder.Len() > 0 && !strings.HasSuffix(builder.String(), "\n"):
			builder.WriteString(" " + line)
		default:
			builder.WriteString(line)
		}
	}
	return strings.TrimRight(builder.String(), "\n")
}

// BuildSkillMD 拼出 SKILL.md：frontmatter 的 name、description 用双引号字符串（避免被其他 YAML 解析器当成数字或布尔），其后是指令。
func BuildSkillMD(name, description, instructions string) []byte {
	var out bytes.Buffer
	out.WriteString("---\nname: " + quoteYAML(name) + "\ndescription: " + quoteYAML(description) + "\n---\n")
	if instructions = strings.TrimSpace(instructions); instructions != "" {
		out.WriteString("\n" + instructions + "\n")
	}
	return out.Bytes()
}

func quoteYAML(value string) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimSpace(buffer.String())
}
