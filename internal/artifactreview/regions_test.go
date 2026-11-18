package artifactreview

import (
	"math"
	"testing"
)

func TestVisualAnchorValidation(t *testing.T) {
	a := Anchor{Kind: "region", Page: 2, X: .1, Y: .2, Width: .3, Height: .4}
	if err := ValidateAnchor("%PDF-1.7", "application/pdf", a); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Anchor){func(a *Anchor) { a.X = math.NaN() }, func(a *Anchor) { a.Width = math.Inf(1) }, func(a *Anchor) { a.Page = 0 }, func(a *Anchor) { a.Page = 1001 }, func(a *Anchor) { a.Width = 0 }, func(a *Anchor) { a.X = .9 }, func(a *Anchor) { a.Start = 1 }, func(a *Anchor) { a.Quote = "forged" }} {
		invalid := a
		change(&invalid)
		if err := ValidateAnchor("bytes", "application/pdf", invalid); err == nil {
			t.Fatalf("accepted %+v", invalid)
		}
	}
	if ValidateAnchor("bytes", "image/png", a) == nil {
		t.Fatal("accepted image page 2")
	}
	if ValidateAnchor("bytes", "text/plain", a) == nil {
		t.Fatal("accepted text region")
	}
	if ValidateAnchor("bytes", "application/pdf", Anchor{Kind: "text", Start: 0, End: 5, Quote: "bytes"}) == nil {
		t.Fatal("accepted text patch for PDF")
	}
}
