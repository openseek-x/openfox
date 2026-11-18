package artifacts

import "testing"

func TestPDFEnvelope(t *testing.T) {
	for _, input := range []string{"", "%PDF-1.7\nnot a document", "<html>%%EOF", "%PDF-9.0\nstartxref\n0\n%%EOF"} {
		if PDFEnvelope([]byte(input)) {
			t.Fatal("accepted invalid envelope")
		}
	}
	if !PDFEnvelope([]byte("%PDF-1.7\nstartxref\n123\n%%EOF\n")) {
		t.Fatal("rejected PDF envelope")
	}
}
