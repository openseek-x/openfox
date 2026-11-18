// Package office provides bounded, non-executing OOXML content review. It does
// not render Office layout, run macros, resolve external resources or calculate formulas.
package office

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"os"
	"path"
	"strings"
)

const (
	DOCX        = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	XLSX        = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	PPTX        = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	MaxBytes    = 10 << 20
	maxExpanded = 32 << 20
	maxPart     = 8 << 20
	maxUnits    = 10000
	wordNS      = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	sheetNS     = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	slideNS     = "http://schemas.openxmlformats.org/presentationml/2006/main"
	drawNS      = "http://schemas.openxmlformats.org/drawingml/2006/main"
	relNS       = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	pkgNS       = "http://schemas.openxmlformats.org/package/2006/relationships"
)

var ErrUnsupported = errors.New("unsupported, malformed or oversized Office package")
var ErrEdit = errors.New("Office target is read-only, stale or replacement is invalid")

func MIME(value string) bool { return value == DOCX || value == XLSX || value == PPTX }
func MIMEForName(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".docx":
		return DOCX
	case ".xlsx":
		return XLSX
	case ".pptx":
		return PPTX
	}
	return ""
}

type node struct {
	name                                   xml.Name
	attrs                                  []xml.Attr
	start, openEnd, closeStart, end, index int
	text                                   string
	textBuffer                             strings.Builder
	parent                                 *node
	children                               []*node
}

func (n *node) attr(name string) string {
	for _, a := range n.attrs {
		if a.Name.Local == name && a.Name.Space == "" {
			return a.Value
		}
	}
	return ""
}
func (n *node) child(ns, name string) *node {
	for _, c := range n.children {
		if c.name.Space == ns && c.name.Local == name {
			return c
		}
	}
	return nil
}
func (n *node) all(ns, name string) (out []*node) {
	if n.name.Space == ns && n.name.Local == name {
		out = append(out, n)
	}
	for _, c := range n.children {
		out = append(out, c.all(ns, name)...)
	}
	return
}
func (n *node) ancestor(ns, name string) *node {
	for p := n.parent; p != nil; p = p.parent {
		if p.name.Space == ns && p.name.Local == name {
			return p
		}
	}
	return nil
}
func parseXML(data []byte) (*node, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	var root, current *node
	count, depth := 0, 0
	for {
		start := int(d.InputOffset())
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, ErrUnsupported
		}
		end := int(d.InputOffset())
		switch t := token.(type) {
		case xml.StartElement:
			count++
			depth++
			if count > 100000 || depth > 100 {
				return nil, ErrUnsupported
			}
			n := &node{name: t.Name, attrs: t.Attr, start: start, openEnd: end, index: count, parent: current}
			if current == nil {
				if root != nil {
					return nil, ErrUnsupported
				}
				root = n
			} else {
				current.children = append(current.children, n)
			}
			current = n
		case xml.EndElement:
			if current == nil {
				return nil, ErrUnsupported
			}
			current.text = current.textBuffer.String()
			current.closeStart, current.end = start, end
			current = current.parent
			depth--
		case xml.CharData:
			if current != nil {
				current.textBuffer.Write(t)
			} else if strings.TrimSpace(string(t)) != "" {
				return nil, ErrUnsupported
			}
		case xml.Directive:
			return nil, ErrUnsupported // no DTD/entity expansion
		case xml.ProcInst:
			if t.Target != "xml" || root != nil {
				return nil, ErrUnsupported
			}
		}
	}
	if root == nil || current != nil {
		return nil, ErrUnsupported
	}
	return root, nil
}

type Package struct {
	archive *zip.Reader
	parts   map[string][]byte
	main    string
	mime    string
	view    View
	targets map[string]target
	sheets  []string
}
type Unit struct {
	ID          string           `json:"id"`
	Section     string           `json:"section"`
	Label       string           `json:"label"`
	Text        string           `json:"text"`
	Type        string           `json:"type"`
	Editable    bool             `json:"editable"`
	Source      *TextSource      `json:"source,omitempty"`
	SlideSource *SlideTextSource `json:"slide_source,omitempty"`
	CellSource  *CellSource      `json:"cell_source,omitempty"`
}

// TextSource locates a plain Word text run in its immutable OOXML paragraph.
// Offset is UTF-16 code units, matching the renderer's paragraph coordinates.
// It is a read projection; edits are still authorized against ID and exact text.
type TextSource struct {
	Part      string `json:"part"`
	Paragraph int    `json:"paragraph"`
	Offset    int    `json:"offset"`
}

// SlideTextSource identifies text within one slide shape's text story.
type SlideTextSource struct {
	Part   string `json:"part"`
	Shape  uint32 `json:"shape"`
	Offset int    `json:"offset"`
}
type View struct {
	Kind        string       `json:"kind"`
	Units       []Unit       `json:"units"`
	Spreadsheet *Spreadsheet `json:"spreadsheet,omitempty"`
}
type target struct {
	Unit
	part string
	node *node
}

func (p *Package) View() View                  { return p.view }
func (p *Package) Unit(id string) (Unit, bool) { t, ok := p.targets[id]; return t.Unit, ok }

type readBudget struct {
	expanded uint64
	entries  int
}

func Read(data []byte, mime string) (*Package, error) {
	return readPackage(data, mime, &readBudget{}, true)
}

func readPackage(data []byte, mime string, budget *readBudget, allowChartBooks bool) (*Package, error) {
	if !MIME(mime) || len(data) > MaxBytes {
		return nil, ErrUnsupported
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(z.File) > 2048 {
		return nil, ErrUnsupported
	}
	p := &Package{archive: z, parts: map[string][]byte{}, mime: mime, targets: map[string]target{}, view: View{Units: []Unit{}}}
	budget.entries += len(z.File)
	if budget.entries > 2048 {
		return nil, ErrUnsupported
	}
	embedded := map[string]bool{}
	for _, f := range z.File {
		name := f.Name
		clean := strings.TrimSuffix(name, "/")
		if clean == "" || path.Clean(clean) != clean || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") || strings.ContainsAny(name, "\\\x00") || f.Mode()&os.ModeSymlink != 0 || f.Flags&1 != 0 {
			return nil, ErrUnsupported
		}
		if _, exists := p.parts[name]; exists {
			return nil, ErrUnsupported
		}
		if f.UncompressedSize64 > maxPart {
			return nil, ErrUnsupported
		}
		budget.expanded += f.UncompressedSize64
		if budget.expanded > maxExpanded {
			return nil, ErrUnsupported
		}
		lower := strings.ToLower(name)
		if strings.Contains(lower, "vbaproject") || strings.Contains(lower, "_xmlsignatures/") {
			return nil, ErrUnsupported
		}
		if strings.Contains(lower, "/embeddings/") {
			// 原生图表常携带 XLSX 数据源；只接收一层、受限且可验证的工作簿。
			if !allowChartBooks || (mime != DOCX && mime != PPTX) || !strings.HasSuffix(lower, ".xlsx") {
				return nil, ErrUnsupported
			}
			embedded[name] = false
		}
		r, e := f.Open()
		if e != nil {
			return nil, ErrUnsupported
		}
		body, e := io.ReadAll(io.LimitReader(r, maxPart+1))
		closeErr := r.Close()
		if e != nil || closeErr != nil || len(body) > maxPart || uint64(len(body)) != f.UncompressedSize64 {
			return nil, ErrUnsupported
		}
		p.parts[name] = body
	}
	if len(embedded) > 0 {
		prefix := "ppt/charts/"
		if mime == DOCX {
			prefix = "word/charts/"
		}
		for name := range p.parts {
			if !strings.HasPrefix(name, prefix+"_rels/") || !strings.HasSuffix(name, ".xml.rels") {
				continue
			}
			chart := prefix + strings.TrimSuffix(path.Base(name), ".rels")
			if _, ok := p.parts[chart]; !ok {
				return nil, ErrUnsupported
			}
			rels, err := p.relationships(chart)
			if err != nil {
				return nil, err
			}
			for _, rel := range rels {
				if _, exists := embedded[rel.part]; exists && rel.kind == relNS+"/package" {
					embedded[rel.part] = true
				}
			}
		}
		for name, referenced := range embedded {
			if !referenced {
				return nil, ErrUnsupported
			}
			if _, err := readPackage(p.parts[name], XLSX, budget, false); err != nil {
				return nil, err
			}
		}
	}
	ct, err := p.xml("[Content_Types].xml")
	if err != nil || ct.name.Local != "Types" || ct.name.Space != "http://schemas.openxmlformats.org/package/2006/content-types" {
		return nil, ErrUnsupported
	}
	for _, n := range ct.children {
		v := strings.ToLower(n.attr("ContentType"))
		if strings.Contains(v, "macroenabled") || strings.Contains(v, "vba") || strings.Contains(v, "digital-signature") {
			return nil, ErrUnsupported
		}
	}
	rels, err := p.relationships("")
	if err != nil {
		return nil, err
	}
	for _, r := range rels {
		if r.kind == relNS+"/officeDocument" {
			if p.main != "" {
				return nil, ErrUnsupported
			}
			p.main = r.part
		}
	}
	if p.main == "" {
		return nil, ErrUnsupported
	}
	want := map[string]string{DOCX: "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml", XLSX: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml", PPTX: "application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"}[mime]
	matched := false
	for _, n := range ct.children {
		if n.attr("PartName") == "/"+p.main && n.attr("ContentType") == want {
			matched = true
		}
	}
	if !matched {
		return nil, ErrUnsupported
	}
	if err = p.extract(); err != nil {
		return nil, err
	}
	totalText := 0
	for _, u := range p.view.Units {
		totalText += len(u.Text) + len(u.Label) + len(u.Section)
		if totalText > 1<<20 {
			return nil, ErrUnsupported
		}
	}
	return p, nil
}
func (p *Package) xml(part string) (*node, error) {
	body, ok := p.parts[part]
	if !ok {
		return nil, ErrUnsupported
	}
	return parseXML(body)
}

type relationship struct{ part, kind string }

func (p *Package) relationships(part string) (map[string]relationship, error) {
	name := "_rels/.rels"
	if part != "" {
		name = path.Join(path.Dir(part), "_rels", path.Base(part)+".rels")
	}
	root, err := p.xml(name)
	if err != nil || root.name.Space != pkgNS || root.name.Local != "Relationships" {
		return nil, ErrUnsupported
	}
	out := map[string]relationship{}
	seen := map[string]bool{}
	for _, n := range root.children {
		if n.name.Space != pkgNS || n.name.Local != "Relationship" {
			return nil, ErrUnsupported
		}
		id := n.attr("Id")
		if id == "" || seen[id] {
			return nil, ErrUnsupported
		}
		seen[id] = true
		if n.attr("TargetMode") == "External" {
			continue
		} // never fetched
		u, e := url.Parse(n.attr("Target"))
		if e != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, ErrUnsupported
		}
		target := u.Path
		if !strings.HasPrefix(target, "/") {
			target = path.Join(path.Dir(part), target)
		}
		target = strings.TrimPrefix(target, "/")
		if target == "" || strings.HasPrefix(target, "../") || path.Clean(target) != target || strings.ContainsAny(target, "\\\x00") {
			return nil, ErrUnsupported
		}
		if _, ok := p.parts[target]; !ok {
			return nil, ErrUnsupported
		}
		out[id] = relationship{target, n.attr("Type")}
	}
	return out, nil
}
func relationshipID(n *node) string {
	for _, a := range n.attrs {
		if a.Name.Space == relNS && a.Name.Local == "id" {
			return a.Value
		}
	}
	return ""
}
