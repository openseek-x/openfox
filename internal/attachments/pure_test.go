package attachments

import (
	"errors"
	"testing"
)

var (
	pngHead  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpegHead = []byte("\xff\xd8\xff\xe0\x00\x10JFIF")
	pdfHead  = []byte("%PDF-1.7\n")
	zipHead  = []byte("PK\x03\x04\x14\x00")
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		head    []byte
		want    string
		allowed bool
	}{
		{"png ok", "a.PNG", pngHead, "image/png", true},
		{"jpeg ok", "a.jpg", jpegHead, "image/jpeg", true},
		{"pdf ok", "a.pdf", pdfHead, "application/pdf", true},
		{"docx is zip", "a.docx", zipHead, "application/zip", true},
		{"txt ok", "a.txt", []byte("hello"), "text/plain; charset=utf-8", true},
		{"empty txt ok", "a.txt", nil, "text/plain; charset=utf-8", true},
		{"markdown with html tag", "a.md", []byte("<!DOCTYPE html><html></html>"), "text/plain; charset=utf-8", true},
		{"svg stored as text", "a.svg", []byte("<?xml version=\"1.0\"?><svg/>"), "text/plain; charset=utf-8", true},
		{"html stored as text", "a.html", []byte("<html><script>1</script></html>"), "text/plain; charset=utf-8", true},
		{"legacy doc binary", "a.doc", []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"), "application/octet-stream", true},
		{"png extension but html content", "a.png", []byte("<html></html>"), "", false},
		{"png extension but jpeg content", "a.png", jpegHead, "", false},
		{"pdf extension but zip content", "a.pdf", zipHead, "", false},
		{"txt extension but binary content", "a.txt", []byte("\x00\x01\x02\x03binary"), "", false},
		{"executable extension", "a.exe", []byte("MZ\x90\x00"), "", false},
		{"script extension", "a.sh", []byte("#!/bin/sh"), "", false},
		{"no extension", "Makefile", []byte("all:"), "", false},
		{"double extension", "a.png.exe", pngHead, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Classify(tc.file, tc.head)
			if tc.allowed {
				if err != nil || got != tc.want {
					t.Fatalf("Classify = %q, %v; want %q", got, err, tc.want)
				}
				return
			}
			if !errors.Is(err, ErrTypeRejected) {
				t.Fatalf("Classify = %q, %v; want ErrTypeRejected", got, err)
			}
		})
	}
}

func TestPreviewableExcludesActiveContent(t *testing.T) {
	for _, contentType := range []string{"image/svg+xml", "text/html", "text/plain; charset=utf-8", "application/pdf"} {
		if Previewable(contentType) {
			t.Errorf("%s must not be previewable", contentType)
		}
	}
	if !Previewable("image/png") {
		t.Error("png should be previewable")
	}
}
