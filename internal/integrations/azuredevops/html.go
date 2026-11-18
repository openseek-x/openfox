package azuredevops

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// PlainDescription produces text, never trusted markup. It reads no attributes
// or URLs. Whitespace outside pre is collapsed, while pre text is preserved.
// The consumer must use the plain-notes path; this is not a Markdown sanitizer.
func PlainDescription(source string) (string, error) {
	if len(source) > MaxCellBytes || !utf8.ValidString(source) {
		return "", &Error{Code: "cell_limit"}
	}
	if source == "" {
		return "", nil
	}
	root, e := html.Parse(strings.NewReader(source))
	if e != nil {
		return "", protocol()
	}
	var out strings.Builder
	appendText := func(s string) error {
		if out.Len()+len(s) > MaxCellBytes {
			return &Error{Code: "cell_limit"}
		}
		out.WriteString(s)
		return nil
	}
	pendingBreak, pendingSpace := false, false
	emit := func(value string, pre bool) error {
		if value == "" {
			return nil
		}
		if !pre {
			fields := strings.Fields(value)
			if len(fields) == 0 {
				pendingSpace = true
				return nil
			}
			first, _ := utf8.DecodeRuneInString(value)
			last, _ := utf8.DecodeLastRuneInString(value)
			leading, trailing := unicode.IsSpace(first), unicode.IsSpace(last)
			value = strings.Join(fields, " ")
			if pendingBreak && out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
				if e := appendText("\n"); e != nil {
					return e
				}
			}
			if !pendingBreak && (pendingSpace || leading) && out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") && !strings.HasSuffix(out.String(), " ") {
				if e := appendText(" "); e != nil {
					return e
				}
			}
			pendingBreak, pendingSpace = false, trailing
			return appendText(value)
		}
		if pendingBreak && out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
			if e := appendText("\n"); e != nil {
				return e
			}
		}
		pendingBreak, pendingSpace = false, false
		return appendText(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n"))
	}
	var walk func(*html.Node, bool, int) error
	walk = func(n *html.Node, pre bool, depth int) error {
		if depth > 512 {
			return &Error{Code: "cell_limit"}
		}
		block := false
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template", "iframe", "object", "embed", "svg", "math", "head":
				return nil
			case "pre":
				pre = true
				block = true
			case "p", "div", "section", "article", "header", "footer", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote", "li", "ul", "ol", "dl", "dt", "dd", "table", "tr", "hr":
				block = true
			case "br":
				pendingBreak, pendingSpace = false, false
				return appendText("\n")
			case "td", "th":
				pendingSpace = true
			}
		}
		if block {
			pendingBreak, pendingSpace = true, false
		}
		if n.Type == html.TextNode {
			if e := emit(n.Data, pre); e != nil {
				return e
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if e := walk(child, pre, depth+1); e != nil {
				return e
			}
		}
		if block {
			pendingBreak, pendingSpace = true, false
		}
		return nil
	}
	if e = walk(root, false, 0); e != nil {
		return "", e
	}
	return out.String(), nil
}
