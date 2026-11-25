package office

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPreviewConversion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/forms/libreoffice/convert" {
			t.Error(r.URL.Path)
		}
		if err := r.ParseMultipartForm(12 << 20); err != nil {
			t.Fatal(err)
		}
		defer r.MultipartForm.RemoveAll()
		if files := r.MultipartForm.File["files"]; len(files) != 1 || files[0].Filename != "document.pptx" {
			t.Error("wrong file")
		}
		if r.FormValue("exportFormFields") != "false" || r.Header.Get("Cookie") != "" {
			t.Error("unsafe request")
		}
		w.Write([]byte("%PDF-1.7\npreview"))
	}))
	defer server.Close()
	pdf, err := NewPreview(server.URL).PDF(context.Background(), []byte("validated file"), "pptx")
	if err != nil || string(pdf) != "%PDF-1.7\npreview" {
		t.Fatalf("%q %v", pdf, err)
	}
}

func TestPreviewRejectsBadResponses(t *testing.T) {
	for _, scenario := range []string{"redirect", "failure", "not-pdf", "oversized", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch scenario {
				case "redirect":
					http.Redirect(w, r, "/other", 302)
				case "failure":
					w.WriteHeader(503)
				case "not-pdf":
					w.Write([]byte("invalid"))
				case "oversized":
					w.Header().Set("Content-Length", "33554433")
					w.Write([]byte("%PDF-"))
				case "timeout":
					select {
					case <-r.Context().Done():
					case <-time.After(200 * time.Millisecond):
					}
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if _, err := NewPreview(server.URL).PDF(ctx, []byte("file"), "docx"); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}

func TestPreviewBounds(t *testing.T) {
	p := NewPreview("http://127.0.0.1:1")
	p.slots <- struct{}{}
	p.slots <- struct{}{}
	if _, err := p.PDF(context.Background(), []byte("file"), "docx"); err == nil {
		t.Fatal("queue unbounded")
	}
	if _, err := NewPreview("").PDF(context.Background(), []byte("file"), "docx"); err == nil {
		t.Fatal("missing configuration accepted")
	}
	if _, err := p.PDF(context.Background(), []byte(strings.Repeat("x", MaxBytes+1)), "docx"); err == nil {
		t.Fatal("oversized input accepted")
	}
}

func TestPreviewRealOffice(t *testing.T) {
	origin := os.Getenv("OPENFOX_TEST_OFFICE_PREVIEW_URL")
	if origin == "" {
		t.Skip("需要隔离的 Office 渲染测试服务")
	}
	converter := NewPreview(origin)
	for kind, mime := range map[string]string{"docx": DOCX, "xlsx": XLSX, "pptx": PPTX} {
		t.Run(kind, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/sample." + kind)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Read(raw, mime); err != nil {
				t.Fatal(err)
			}
			pdf, err := converter.PDF(context.Background(), raw, kind)
			if err != nil || len(pdf) < 1000 {
				t.Fatalf("conversion failed: %d %v", len(pdf), err)
			}
		})
	}
}
