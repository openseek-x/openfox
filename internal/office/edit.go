package office

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Change struct{ ID, Expected, Replacement string }
type edit struct {
	start, end int
	value      string
}

func relationshipPart(part string) string {
	return path.Join(path.Dir(part), "_rels", path.Base(part)+".rels")
}

var number = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[Ee][+-]?[0-9]+)?$`)

func validNumber(s string) bool {
	if !number.MatchString(s) {
		return false
	}
	f, e := strconv.ParseFloat(s, 64)
	return e == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
}
func validXMLText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r != 9 && r != 10 && r != 13 && (r < 32 || r == 0xfffe || r == 0xffff) {
			return false
		}
	}
	return true
}
func escaped(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Only the edited element is rebuilt. Namespace prefixes and all unmodified
// parts stay intact; raw ZIP copying avoids serializer-wide formatting changes.
func elementName(raw []byte, n *node) string {
	s := string(raw[n.start+1 : n.openEnd])
	i := strings.IndexAny(s, " \t\n\r/>")
	if i < 0 {
		return s
	}
	return s[:i]
}
func startTag(raw []byte, n *node, values map[string]string) string {
	name := elementName(raw, n)
	var b strings.Builder
	b.WriteByte('<')
	b.WriteString(name)
	for _, a := range n.attrs {
		key := a.Name.Local
		switch a.Name.Space {
		case "xmlns":
			key = "xmlns:" + key
		case "http://www.w3.org/XML/1998/namespace":
			key = "xml:" + key
		case "":
		default:
			// Existing arbitrary namespaced attributes retain their original spelling.
			// Find their declaration in scope rather than inventing namespace bindings.
			prefix := ""
			for cur := n; cur != nil && prefix == ""; cur = cur.parent {
				for _, d := range cur.attrs {
					if d.Name.Space == "xmlns" && d.Value == a.Name.Space {
						prefix = d.Name.Local
						break
					}
				}
			}
			if prefix != "" {
				key = prefix + ":" + key
			}
		}
		if _, replace := values[key]; replace {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(key)
		b.WriteString(`="`)
		b.WriteString(escaped(a.Value))
		b.WriteByte('"')
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if values[k] == "\x00" {
			continue
		}
		b.WriteByte(' ')
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escaped(values[k]))
		b.WriteByte('"')
	}
	b.WriteByte('>')
	return b.String()
}
func (p *Package) Replace(changes []Change) ([]byte, error) {
	if len(changes) == 0 || len(changes) > 20 {
		return nil, ErrEdit
	}
	edits := map[string][]edit{}
	seen := map[string]bool{}
	changed := false
	for _, c := range changes {
		t, ok := p.targets[c.ID]
		if !ok || !t.Editable || seen[c.ID] || c.Expected != t.Text || len(c.Replacement) > 8192 || !validXMLText(c.Replacement) {
			return nil, ErrEdit
		}
		seen[c.ID] = true
		changed = changed || c.Replacement != t.Text
		if c.Replacement == t.Text {
			continue
		}
		n, raw := t.node, p.parts[t.part]
		name := elementName(raw, n)
		replacement := ""
		if p.mime == XLSX {
			attrs := map[string]string{"t": "inlineStr"}
			prefix := ""
			if i := strings.IndexByte(name, ':'); i >= 0 {
				prefix = name[:i+1]
			}
			switch t.Type {
			case "text":
				replacement = "<" + prefix + `is><` + prefix + `t xml:space="preserve">` + escaped(encodeXString(c.Replacement)) + "</" + prefix + "t></" + prefix + "is>"
			case "number":
				if !validNumber(c.Replacement) {
					return nil, ErrEdit
				}
				attrs["t"] = "n"
				replacement = "<" + prefix + "v>" + c.Replacement + "</" + prefix + "v>"
			case "boolean":
				if c.Replacement != "0" && c.Replacement != "1" {
					return nil, ErrEdit
				}
				attrs["t"] = "b"
				replacement = "<" + prefix + "v>" + c.Replacement + "</" + prefix + "v>"
			default:
				return nil, ErrEdit
			}
			replacement = startTag(raw, n, attrs) + replacement + "</" + name + ">"
		} else {
			if strings.ContainsAny(c.Replacement, "\r\n\t") {
				return nil, ErrEdit
			}
			replacement = startTag(raw, n, map[string]string{"xml:space": "preserve"}) + escaped(c.Replacement) + "</" + name + ">"
		}
		edits[t.part] = append(edits[t.part], edit{n.start, n.end, replacement})
	}
	if !changed {
		return nil, ErrEdit
	}
	if p.mime == XLSX {
		// Formula values are cached, not evaluated here. Invalidate them on every
		// worksheet and require recalculation in a spreadsheet application.
		for _, part := range p.sheets {
			root, e := p.xml(part)
			if e != nil {
				return nil, e
			}
			for _, c := range root.all(sheetNS, "c") {
				if c.child(sheetNS, "f") != nil {
					if v := c.child(sheetNS, "v"); v != nil {
						edits[part] = append(edits[part], edit{v.start, v.end, ""})
					}
				}
			}
		}
		root, e := p.xml(p.main)
		if e != nil {
			return nil, e
		}
		if calc := root.child(sheetNS, "calcPr"); calc != nil {
			tag := startTag(p.parts[p.main], calc, map[string]string{"fullCalcOnLoad": "1", "forceFullCalc": "1"})
			edits[p.main] = append(edits[p.main], edit{calc.start, calc.openEnd, tag})
			if bytes.HasSuffix(p.parts[p.main][calc.start:calc.openEnd], []byte("/>")) {
				edits[p.main][len(edits[p.main])-1].value += "</" + elementName(p.parts[p.main], calc) + ">"
			}
		} else {
			prefix := ""
			name := elementName(p.parts[p.main], root)
			if i := strings.IndexByte(name, ':'); i >= 0 {
				prefix = name[:i+1]
			}
			pos := root.closeStart
			// calcPr must precede the later optional workbook children in schema order.
			after := map[string]bool{"oleSize": true, "customWorkbookViews": true, "pivotCaches": true, "smartTagPr": true, "smartTagTypes": true, "webPublishing": true, "fileRecoveryPr": true, "webPublishObjects": true, "extLst": true}
			for _, n := range root.children {
				if after[n.name.Local] {
					pos = n.start
					break
				}
			}
			edits[p.main] = append(edits[p.main], edit{pos, pos, "<" + prefix + `calcPr fullCalcOnLoad="1" forceFullCalc="1"/>`})
		}
	}
	modified := map[string][]byte{}
	for part, list := range edits {
		sort.Slice(list, func(i, j int) bool { return list[i].start < list[j].start })
		var b bytes.Buffer
		pos := 0
		for _, e := range list {
			if e.start < pos {
				return nil, ErrEdit
			}
			b.Write(p.parts[part][pos:e.start])
			b.WriteString(e.value)
			pos = e.end
		}
		b.Write(p.parts[part][pos:])
		if b.Len() > maxPart {
			return nil, ErrEdit
		}
		modified[part] = b.Bytes()
	}
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	_ = w.SetComment(p.archive.Comment)
	for _, f := range p.archive.File {
		if data, ok := modified[f.Name]; ok {
			h := f.FileHeader
			h.CRC32 = 0
			h.CompressedSize = 0
			h.UncompressedSize = 0
			h.CompressedSize64 = 0
			h.UncompressedSize64 = 0
			wr, e := w.CreateHeader(&h)
			if e != nil {
				return nil, e
			}
			if _, e = wr.Write(data); e != nil {
				return nil, e
			}
		} else if e := w.Copy(f); e != nil {
			return nil, e
		}
	}
	if e := w.Close(); e != nil {
		return nil, e
	}
	if _, e := Read(out.Bytes(), p.mime); e != nil {
		return nil, e
	}
	return out.Bytes(), nil
}
