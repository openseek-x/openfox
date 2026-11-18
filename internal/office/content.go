package office

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

func (p *Package) add(part string, n *node, section int, title, label, text, typ string, editable bool) error {
	if len(p.view.Units) >= maxUnits {
		return ErrUnsupported
	}
	id := fmt.Sprintf("s%d.n%d", section, n.index)
	if _, exists := p.targets[id]; exists {
		return ErrUnsupported
	}
	u := Unit{ID: id, Section: title, Label: label, Text: text, Type: typ, Editable: editable && len(text) <= 8192 && validXMLText(text)}
	p.targets[id] = target{u, part, n}
	p.view.Units = append(p.view.Units, u)
	return nil
}
func (p *Package) extract() error {
	root, err := p.xml(p.main)
	if err != nil {
		return err
	}
	switch p.mime {
	case DOCX:
		p.view.Kind = "docx"
		if root.name.Space != wordNS || root.name.Local != "document" {
			return ErrUnsupported
		}
		body := root.child(wordNS, "body")
		if body == nil {
			return ErrUnsupported
		}
		protected := false
		// The document may declare protection in its settings relationship.
		if _, ok := p.parts[relationshipPart(p.main)]; ok {
			rels, e := p.relationships(p.main)
			if e != nil {
				return e
			}
			for _, r := range rels {
				if r.kind == relNS+"/settings" {
					s, e := p.xml(r.part)
					if e != nil {
						return e
					}
					for _, n := range s.all(wordNS, "documentProtection") {
						for _, a := range n.attrs {
							if a.Name.Local == "enforcement" && a.Value != "0" && a.Value != "false" && a.Value != "off" {
								protected = true
							}
						}
					}
				}
			}
		}
		fieldText := map[*node]bool{}
		fieldDepth := 0
		var fields func(*node)
		fields = func(n *node) {
			if n.name.Space == wordNS && n.name.Local == "fldChar" {
				for _, a := range n.attrs {
					if a.Name.Local == "fldCharType" {
						if a.Value == "begin" {
							fieldDepth++
						}
						if a.Value == "end" && fieldDepth > 0 {
							fieldDepth--
						}
					}
				}
			}
			if n.name.Space == wordNS && n.name.Local == "t" && fieldDepth > 0 {
				fieldText[n] = true
			}
			for _, child := range n.children {
				fields(child)
			}
		}
		fields(body)
		for i, para := range body.all(wordNS, "p") {
			plain := true
			for _, name := range []string{"drawing", "pict", "fldChar", "instrText", "fldSimple", "del", "ins", "moveFrom", "moveTo", "tab", "br", "cr", "sym", "footnoteReference", "endnoteReference", "sdt"} {
				if len(para.all(wordNS, name)) > 0 {
					plain = false
				}
			}
			offset := 0
			editable := !protected && para.ancestor(wordNS, "sdt") == nil && para.ancestor(wordNS, "del") == nil && para.ancestor(wordNS, "ins") == nil && para.ancestor(wordNS, "drawing") == nil
			for _, name := range []string{"fldChar", "instrText", "fldSimple", "del", "ins", "moveFrom", "moveTo"} {
				if len(para.all(wordNS, name)) > 0 {
					editable = false
				}
			}
			for j, t := range para.all(wordNS, "t") {
				if t.ancestor(wordNS, "p") != para {
					continue
				}
				canEdit := editable && !fieldText[t] && t.ancestor(wordNS, "fldSimple") == nil && t.ancestor(wordNS, "moveFrom") == nil && t.ancestor(wordNS, "moveTo") == nil && t.ancestor(wordNS, "pict") == nil && len(t.children) == 0 && t.ancestor(wordNS, "sdt") == nil && t.ancestor(wordNS, "drawing") == nil
				if err = p.add(p.main, t, 1, "Document", fmt.Sprintf("P%d · %d", i+1, j+1), t.text, "text", canEdit); err != nil {
					return err
				}
				if plain {
					u := &p.view.Units[len(p.view.Units)-1]
					u.Source = &TextSource{Part: p.main, Paragraph: i, Offset: offset}
					target := p.targets[u.ID]
					target.Unit = *u
					p.targets[u.ID] = target
				}
				offset += len(utf16.Encode([]rune(t.text)))
			}
		}
	case PPTX:
		p.view.Kind = "pptx"
		if root.name.Space != slideNS || root.name.Local != "presentation" {
			return ErrUnsupported
		}
		rels, e := p.relationships(p.main)
		if e != nil {
			return e
		}
		ids := root.child(slideNS, "sldIdLst")
		if ids == nil {
			return ErrUnsupported
		}
		seenSlides := map[string]bool{}
		for i, id := range ids.children {
			r, ok := rels[relationshipID(id)]
			if !ok || r.kind != relNS+"/slide" || seenSlides[r.part] {
				return ErrUnsupported
			}
			seenSlides[r.part] = true
			slide, e := p.xml(r.part)
			if e != nil || slide.name.Space != slideNS || slide.name.Local != "sld" {
				return ErrUnsupported
			}
			locations := map[*node]*SlideTextSource{}
			shapeIDs := map[string]int{}
			for _, n := range slide.all(slideNS, "cNvPr") {
				shapeIDs[n.attr("id")]++
			}
			for _, shape := range slide.all(slideNS, "sp") {
				nv := shape.child(slideNS, "nvSpPr")
				body := shape.child(slideNS, "txBody")
				if nv == nil || body == nil || nv.child(slideNS, "cNvPr") == nil {
					continue
				}
				id, err := strconv.ParseUint(nv.child(slideNS, "cNvPr").attr("id"), 10, 32)
				if err != nil || shapeIDs[nv.child(slideNS, "cNvPr").attr("id")] != 1 || len(body.all(drawNS, "fld")) > 0 || len(body.all(drawNS, "br")) > 0 {
					continue
				}
				offset := 0
				for _, para := range body.all(drawNS, "p") {
					for _, text := range para.all(drawNS, "t") {
						locations[text] = &SlideTextSource{Part: r.part, Shape: uint32(id), Offset: offset}
						offset += len(utf16.Encode([]rune(text.text)))
					}
					offset++ // OOXML paragraphs are separated by one story newline.
				}
			}
			for j, t := range slide.all(drawNS, "t") {
				editable := len(t.children) == 0 && t.parent != nil && t.parent.name.Space == drawNS && t.parent.name.Local == "r" && slide.attr("show") != "0" && slide.attr("show") != "false"
				if e = p.add(r.part, t, i+1, fmt.Sprintf("Slide %d", i+1), fmt.Sprintf("Text %d", j+1), t.text, "text", editable); e != nil {
					return e
				}
				u := &p.view.Units[len(p.view.Units)-1]
				u.SlideSource = locations[t]
				target := p.targets[u.ID]
				target.Unit = *u
				p.targets[u.ID] = target
			}
		}
	case XLSX:
		p.view.Kind = "xlsx"
		if root.name.Space != sheetNS || root.name.Local != "workbook" {
			return ErrUnsupported
		}
		rels, e := p.relationships(p.main)
		if e != nil {
			return e
		}
		var shared []*node
		for _, r := range rels {
			if r.kind == relNS+"/sharedStrings" {
				s, e := p.xml(r.part)
				if e != nil || s.name.Space != sheetNS || s.name.Local != "sst" {
					return ErrUnsupported
				}
				shared = s.children
			}
		}
		sheets := root.child(sheetNS, "sheets")
		if sheets == nil {
			return ErrUnsupported
		}
		seen := map[string]bool{}
		protected := false
		if protection := root.child(sheetNS, "workbookProtection"); protection != nil {
			for _, key := range []string{"lockStructure", "lockWindows", "lockRevision"} {
				if v := protection.attr(key); v == "1" || v == "true" {
					protected = true
				}
			}
		}
		for i, s := range sheets.children {
			r, ok := rels[relationshipID(s)]
			if !ok {
				return ErrUnsupported
			}
			if r.kind != relNS+"/worksheet" {
				continue
			}
			if seen[r.part] {
				return ErrUnsupported
			}
			seen[r.part] = true
			p.sheets = append(p.sheets, r.part)
			sheet, e := p.xml(r.part)
			if e != nil || sheet.name.Space != sheetNS || sheet.name.Local != "worksheet" {
				return ErrUnsupported
			}
			readonly := protected || sheet.child(sheetNS, "sheetProtection") != nil || (s.attr("state") != "" && s.attr("state") != "visible")
			data := sheet.child(sheetNS, "sheetData")
			if data == nil {
				continue
			}
			refs := map[string]bool{}
			for _, c := range data.all(sheetNS, "c") {
				ref := c.attr("r")
				if !cellReference(ref) || refs[ref] {
					return ErrUnsupported
				}
				refs[ref] = true
				text, typ, editable := cellValue(c, shared)
				if e = p.add(r.part, c, i+1, s.attr("name"), ref, text, typ, editable && !readonly); e != nil {
					return e
				}
				row, col, _ := cellCoordinates(ref)
				u := &p.view.Units[len(p.view.Units)-1]
				u.CellSource = &CellSource{Part: r.part, Sheet: i, Row: row, Column: col}
				target := p.targets[u.ID]
				target.Unit = *u
				p.targets[u.ID] = target
			}
		}
	}
	return nil
}
func cellValue(c *node, shared []*node) (text, typ string, editable bool) {
	v := c.child(sheetNS, "v")
	if v != nil {
		text = v.text
	}
	if f := c.child(sheetNS, "f"); f != nil {
		return "=" + f.text, "formula", false
	}
	// Metadata and rich strings require editors beyond the bounded constant-cell subset.
	simple := c.attr("cm") == "" && c.attr("vm") == ""
	for _, n := range c.children {
		if n.name.Space != sheetNS || (n.name.Local != "v" && n.name.Local != "is") {
			simple = false
		}
	}
	switch c.attr("t") {
	case "s":
		i, e := strconv.Atoi(text)
		if e != nil || i < 0 || i >= len(shared) {
			return "[invalid shared string]", "unsupported", false
		}
		item := shared[i]
		var b strings.Builder
		for _, t := range item.all(sheetNS, "t") {
			b.WriteString(t.text)
		}
		return decodeXString(b.String()), "text", simple && len(item.children) == 1 && item.child(sheetNS, "t") != nil
	case "inlineStr":
		item := c.child(sheetNS, "is")
		if item == nil {
			return "", "text", simple
		}
		var b strings.Builder
		for _, t := range item.all(sheetNS, "t") {
			b.WriteString(t.text)
		}
		return decodeXString(b.String()), "text", simple && len(item.children) == 1 && item.child(sheetNS, "t") != nil
	case "", "n":
		if v == nil {
			return "", "text", simple
		}
		return text, "number", simple && validNumber(text)
	case "b":
		return text, "boolean", simple && (text == "0" || text == "1")
	default:
		return text, "unsupported", false
	}
}
func cellReference(s string) bool {
	i, col := 0, 0
	for i < len(s) && s[i] >= 'A' && s[i] <= 'Z' {
		col = col*26 + int(s[i]-'A') + 1
		i++
		if col > 16384 {
			return false
		}
	}
	if col == 0 || i == len(s) || s[i] == '0' {
		return false
	}
	row, e := strconv.Atoi(s[i:])
	return e == nil && row >= 1 && row <= 1048576
}
