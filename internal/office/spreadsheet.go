package office

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"sort"
	"strconv"
	"strings"
)

const chartNS = "http://schemas.openxmlformats.org/drawingml/2006/chart"
const sheetDrawingNS = "http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing"

type CellSource struct {
	Part   string `json:"part"`
	Sheet  int    `json:"sheet"`
	Row    int    `json:"row"`
	Column int    `json:"column"`
}
type Spreadsheet struct {
	SHA256 string             `json:"sha256"`
	Sheets []SpreadsheetSheet `json:"sheets"`
}
type SpreadsheetSheet struct {
	Part          string         `json:"part"`
	Name          string         `json:"name"`
	Index         int            `json:"index"`
	Hidden        bool           `json:"hidden"`
	FrozenRows    int            `json:"frozen_rows"`
	FrozenColumns int            `json:"frozen_columns"`
	Drawings      []SheetDrawing `json:"drawings"`
}
type DrawingPoint struct {
	Row    int     `json:"row"`
	Column int     `json:"column"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
}
type SheetDrawing struct {
	Kind   string        `json:"kind"`
	Name   string        `json:"name"`
	From   DrawingPoint  `json:"from"`
	To     *DrawingPoint `json:"to,omitempty"`
	Width  float64       `json:"width"`
	Height float64       `json:"height"`
	Data   []byte        `json:"data,omitempty"`
}
type xmlReplacement struct {
	start, end int
	data       []byte
}

func replaceXML(raw []byte, changes []xmlReplacement) []byte {
	sort.Slice(changes, func(i, j int) bool { return changes[i].start > changes[j].start })
	out := append([]byte(nil), raw...)
	for _, c := range changes {
		out = append(append(append([]byte(nil), out[:c.start]...), c.data...), out[c.end:]...)
	}
	return out
}
func cellCoordinates(ref string) (int, int, bool) {
	if !cellReference(ref) {
		return 0, 0, false
	}
	i, col := 0, 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		col = col*26 + int(ref[i]-'A') + 1
		i++
	}
	row, _ := strconv.Atoi(ref[i:])
	return row - 1, col - 1, true
}

// RangeBounds returns inclusive, zero-based bounds of canonical A1 references.
func RangeBounds(ref string) (r1, c1, r2, c2 int, ok bool) {
	parts := strings.Split(ref, ":")
	if len(parts) < 1 || len(parts) > 2 {
		return
	}
	r1, c1, ok = cellCoordinates(parts[0])
	if !ok {
		return
	}
	r2, c2 = r1, c1
	if len(parts) == 2 {
		r2, c2, ok = cellCoordinates(parts[1])
	}
	ok = ok && r1 <= r2 && c1 <= c2
	return
}
func (p *Package) RangeQuote(part, ref string) (string, error) {
	r1, c1, r2, c2, ok := RangeBounds(ref)
	found := false
	for _, s := range p.sheets {
		if s == part {
			found = true
		}
	}
	if p.mime != XLSX || !found || !ok || (r2-r1+1)*(c2-c1+1) > 256 {
		return "", ErrUnsupported
	}
	cells := map[[2]int]string{}
	for _, u := range p.view.Units {
		if u.CellSource != nil && u.CellSource.Part == part {
			cells[[2]int{u.CellSource.Row, u.CellSource.Column}] = u.Text
		}
	}
	rows := make([]string, 0, r2-r1+1)
	for r := r1; r <= r2; r++ {
		cols := make([]string, 0, c2-c1+1)
		for c := c1; c <= c2; c++ {
			cols = append(cols, cells[[2]int{r, c}])
		}
		rows = append(rows, strings.Join(cols, "\t"))
	}
	quote := strings.Join(rows, "\n")
	if len(quote) > 8192 {
		return "", ErrUnsupported
	}
	return quote, nil
}

// SpreadsheetPreview creates a bounded, disposable render copy. Cell content,
// styles and edit authority always come from the immutable original Package.
// Panes are composed from the full-sheet bitmap by the client; only the copy's
// view metadata is removed, never the workbook's actual freeze settings.
func (p *Package) SpreadsheetPreview() ([]byte, error) {
	if p.mime != XLSX {
		return nil, ErrUnsupported
	}
	p.view.Spreadsheet = nil
	root, err := p.xml(p.main)
	if err != nil {
		return nil, err
	}
	rels, err := p.relationships(p.main)
	if err != nil {
		return nil, err
	}
	meta := &Spreadsheet{Sheets: []SpreadsheetSheet{}}
	modified := map[string][]byte{}
	imageBytes := 0
	drawings := 0
	for index, s := range root.child(sheetNS, "sheets").children {
		rel, ok := rels[relationshipID(s)]
		if !ok || rel.kind != relNS+"/worksheet" {
			return nil, ErrUnsupported
		}
		sheet, err := p.xml(rel.part)
		if err != nil {
			return nil, err
		}
		item := SpreadsheetSheet{Part: rel.part, Name: s.attr("name"), Index: index, Hidden: s.attr("state") != "" && s.attr("state") != "visible", Drawings: []SheetDrawing{}}
		changes := []xmlReplacement{}
		for _, pane := range sheet.all(sheetNS, "pane") {
			if pane.attr("state") != "frozen" && pane.attr("state") != "frozenSplit" {
				return nil, ErrUnsupported
			}
			count := func(v string) (int, error) {
				if v == "" {
					return 0, nil
				}
				return strconv.Atoi(v)
			}
			rows, e1 := count(pane.attr("ySplit"))
			cols, e2 := count(pane.attr("xSplit"))
			if e1 != nil || e2 != nil || rows < 0 || cols < 0 || rows > 1048576 || cols > 16384 {
				return nil, ErrUnsupported
			}
			item.FrozenRows = rows
			item.FrozenColumns = cols
			changes = append(changes, xmlReplacement{pane.start, pane.end, nil})
		}
		if len(changes) > 0 {
			modified[rel.part] = replaceXML(p.parts[rel.part], changes)
		}
		for _, d := range sheet.all(sheetNS, "drawing") {
			sr, err := p.relationships(rel.part)
			if err != nil {
				return nil, err
			}
			dr, ok := sr[relationshipID(d)]
			if !ok || dr.kind != relNS+"/drawing" {
				return nil, ErrUnsupported
			}
			drawing, err := p.xml(dr.part)
			if err != nil {
				return nil, err
			}
			links, err := p.relationships(dr.part)
			if err != nil {
				return nil, err
			}
			for _, anchor := range drawing.children {
				drawings++
				if drawings > 128 {
					return nil, ErrUnsupported
				}
				obj, err := p.sheetDrawing(anchor, links)
				if err != nil {
					return nil, err
				}
				imageBytes += len(obj.Data)
				if imageBytes > 4<<20 {
					return nil, ErrUnsupported
				}
				if obj.Kind == "chart" {
					refs := anchor.all(chartNS, "chart")
					link := links[relationshipID(refs[0])]
					body, err := p.chartPreview(link.part)
					if err != nil {
						return nil, err
					}
					modified[link.part] = body
				}
				item.Drawings = append(item.Drawings, obj)
			}
		}
		// Unsupported visible drawing families must not silently disappear.
		if len(sheet.all(sheetNS, "legacyDrawing")) > 0 || len(sheet.all(sheetNS, "oleObjects")) > 0 {
			return nil, ErrUnsupported
		}
		meta.Sheets = append(meta.Sheets, item)
	}
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, f := range p.archive.File {
		if raw, ok := modified[f.Name]; ok {
			if len(raw) > maxPart {
				return nil, ErrUnsupported
			}
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
			if _, e = wr.Write(raw); e != nil {
				return nil, e
			}
		} else if e := w.Copy(f); e != nil {
			return nil, e
		}
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	if out.Len() > MaxBytes {
		return nil, ErrUnsupported
	}
	// Recheck the derived copy against the same ZIP/OOXML bounds before serving it.
	if _, err = Read(out.Bytes(), XLSX); err != nil {
		return nil, err
	}
	meta.SHA256 = fmt.Sprintf("%x", sha256.Sum256(out.Bytes()))
	p.view.Spreadsheet = meta
	return out.Bytes(), nil
}
func (p *Package) sheetDrawing(n *node, links map[string]relationship) (SheetDrawing, error) {
	out := SheetDrawing{}
	if n.name.Space != sheetDrawingNS || (n.name.Local != "oneCellAnchor" && n.name.Local != "twoCellAnchor") {
		return out, ErrUnsupported
	}
	point := func(v *node) (DrawingPoint, error) {
		result := DrawingPoint{}
		if v == nil {
			return result, ErrUnsupported
		}
		vals := map[string]int64{}
		for _, key := range []string{"row", "col", "rowOff", "colOff"} {
			child := v.child(sheetDrawingNS, key)
			if child == nil {
				return result, ErrUnsupported
			}
			x, e := strconv.ParseInt(child.text, 10, 64)
			if e != nil || x < 0 {
				return result, ErrUnsupported
			}
			vals[key] = x
		}
		if vals["row"] >= 1048576 || vals["col"] >= 16384 || vals["rowOff"] > 9525*100000 || vals["colOff"] > 9525*100000 {
			return result, ErrUnsupported
		}
		result.Row = int(vals["row"])
		result.Column = int(vals["col"])
		result.X = float64(vals["colOff"]) / 9525
		result.Y = float64(vals["rowOff"]) / 9525
		return result, nil
	}
	var err error
	out.From, err = point(n.child(sheetDrawingNS, "from"))
	if err != nil {
		return out, err
	}
	if n.name.Local == "twoCellAnchor" {
		v, e := point(n.child(sheetDrawingNS, "to"))
		if e != nil {
			return out, e
		}
		out.To = &v
	} else {
		ext := n.child(sheetDrawingNS, "ext")
		if ext == nil {
			return out, ErrUnsupported
		}
		x, e1 := strconv.ParseInt(ext.attr("cx"), 10, 64)
		y, e2 := strconv.ParseInt(ext.attr("cy"), 10, 64)
		if e1 != nil || e2 != nil || x <= 0 || y <= 0 || x > 9525*100000 || y > 9525*100000 {
			return out, ErrUnsupported
		}
		out.Width = float64(x) / 9525
		out.Height = float64(y) / 9525
	}
	pic := n.child(sheetDrawingNS, "pic")
	charts := n.all(chartNS, "chart")
	if pic != nil {
		out.Kind = "image"
		blips := pic.all(drawNS, "blip")
		if len(blips) != 1 || len(blips[0].children) > 0 || len(pic.all(drawNS, "srcRect")) > 0 {
			return out, ErrUnsupported
		}
		// Canvas draws the unmodified rectangular image. Do not silently drop
		// crops, tiling, outlines, masks or DrawingML effects.
		fill := pic.child(sheetDrawingNS, "blipFill")
		if fill == nil {
			return out, ErrUnsupported
		}
		stretch := fill.child(drawNS, "stretch")
		if stretch == nil || len(fill.all(drawNS, "tile")) > 0 {
			return out, ErrUnsupported
		}
		for _, rect := range stretch.children {
			if rect.name.Space != drawNS || rect.name.Local != "fillRect" {
				return out, ErrUnsupported
			}
			for _, a := range rect.attrs {
				if a.Value != "0" {
					return out, ErrUnsupported
				}
			}
		}
		if shape := pic.child(sheetDrawingNS, "spPr"); shape != nil {
			for _, prop := range shape.children {
				if prop.name.Space != drawNS || (prop.name.Local != "xfrm" && !(prop.name.Local == "prstGeom" && prop.attr("prst") == "rect")) {
					return out, ErrUnsupported
				}
			}
		}
		for _, x := range pic.all(drawNS, "xfrm") {
			enabled := func(value string) bool { return value != "" && value != "0" && value != "false" }
			if x.attr("rot") != "" && x.attr("rot") != "0" || enabled(x.attr("flipH")) || enabled(x.attr("flipV")) {
				return out, ErrUnsupported
			}
		}
		id := ""
		for _, a := range blips[0].attrs {
			if a.Name.Space == relNS && a.Name.Local == "embed" {
				id = a.Value
			}
		}
		rel, ok := links[id]
		if !ok || rel.kind != relNS+"/image" {
			return out, ErrUnsupported
		}
		raw := p.parts[rel.part]
		conf, _, e := image.DecodeConfig(bytes.NewReader(raw))
		if e != nil || conf.Width <= 0 || conf.Height <= 0 || int64(conf.Width)*int64(conf.Height) > 16_000_000 {
			return out, ErrUnsupported
		}
		out.Data = raw
	} else if len(charts) == 1 {
		out.Kind = "chart"
		rel, ok := links[relationshipID(charts[0])]
		if !ok || rel.kind != relNS+"/chart" {
			return out, ErrUnsupported
		}
	} else {
		return out, ErrUnsupported
	}
	names := n.all(sheetDrawingNS, "cNvPr")
	if len(names) > 0 {
		out.Name = names[0].attr("name")
	}
	return out, nil
}

// Fill missing chart caches from stored cells. This does not evaluate formulas,
// links, defined names or external workbooks. Unresolvable caches use PDF.
func (p *Package) chartPreview(part string) ([]byte, error) {
	root, err := p.xml(part)
	if err != nil {
		return nil, err
	}
	changes := []xmlReplacement{}
	for _, kind := range []string{"numRef", "strRef"} {
		for _, ref := range root.all(chartNS, kind) {
			if ref.child(chartNS, "numCache") != nil || ref.child(chartNS, "strCache") != nil {
				continue
			}
			f := ref.child(chartNS, "f")
			if f == nil {
				return nil, ErrUnsupported
			}
			values, err := p.chartValues(f.text)
			if err != nil {
				return nil, err
			}
			numeric := kind == "numRef"
			for _, v := range values {
				if v != "" && !validNumber(v) {
					numeric = false
				}
			}
			if !numeric && ref.parent.name.Local != "cat" && kind == "numRef" {
				return nil, ErrUnsupported
			}
			typ, cache := "strRef", "strCache"
			if numeric {
				typ, cache = "numRef", "numCache"
			}
			var b bytes.Buffer
			fmt.Fprintf(&b, `<c:%s xmlns:c="%s"><c:f>`, typ, chartNS)
			_ = xml.EscapeText(&b, []byte(f.text))
			fmt.Fprintf(&b, `</c:f><c:%s>`, cache)
			if numeric {
				b.WriteString("<c:formatCode>General</c:formatCode>")
			}
			fmt.Fprintf(&b, `<c:ptCount val="%d"/>`, len(values))
			for i, v := range values {
				if v == "" {
					continue
				}
				fmt.Fprintf(&b, `<c:pt idx="%d"><c:v>`, i)
				_ = xml.EscapeText(&b, []byte(v))
				b.WriteString("</c:v></c:pt>")
			}
			fmt.Fprintf(&b, "</c:%s></c:%s>", cache, typ)
			changes = append(changes, xmlReplacement{ref.start, ref.end, b.Bytes()})
		}
	}
	return replaceXML(p.parts[part], changes), nil
}
func (p *Package) chartValues(formula string) ([]string, error) {
	at := strings.LastIndex(formula, "!")
	if at <= 0 {
		return nil, ErrUnsupported
	}
	name := formula[:at]
	if strings.HasPrefix(name, "'") && strings.HasSuffix(name, "'") {
		name = strings.ReplaceAll(name[1:len(name)-1], "''", "'")
	}
	if strings.ContainsAny(name, "[]") {
		return nil, ErrUnsupported
	}
	ref := strings.ReplaceAll(formula[at+1:], "$", "")
	r1, c1, r2, c2, ok := RangeBounds(ref)
	if !ok || (r1 != r2 && c1 != c2) || (r2-r1+1)*(c2-c1+1) > 4096 {
		return nil, ErrUnsupported
	}
	cells := map[[2]int]string{}
	found := false
	root, _ := p.xml(p.main)
	rels, _ := p.relationships(p.main)
	for _, s := range root.child(sheetNS, "sheets").children {
		if s.attr("name") == name {
			found = true
			rel := rels[relationshipID(s)]
			for _, u := range p.view.Units {
				if u.CellSource == nil || u.CellSource.Part != rel.part || u.CellSource.Row < r1 || u.CellSource.Row > r2 || u.CellSource.Column < c1 || u.CellSource.Column > c2 {
					continue
				}
				value := u.Text
				if u.Type == "formula" {
					target := p.targets[u.ID]
					v := target.node.child(sheetNS, "v")
					if v == nil || v.text == "" {
						return nil, ErrUnsupported
					}
					value = v.text
				}
				cells[[2]int{u.CellSource.Row, u.CellSource.Column}] = value
			}
		}
	}
	if !found {
		return nil, ErrUnsupported
	}
	out := []string{}
	for r := r1; r <= r2; r++ {
		for c := c1; c <= c2; c++ {
			out = append(out, cells[[2]int{r, c}])
		}
	}
	return out, nil
}
