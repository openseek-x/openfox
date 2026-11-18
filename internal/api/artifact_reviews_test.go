package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"openfox/internal/artifactreview"
	"openfox/internal/artifacts"
	"openfox/internal/executiondelivery"
	"openfox/internal/identity"
	"openfox/internal/office"
	"os"
	"strings"
	"testing"
)

func TestArtifactReviewRoutesRequireAuthentication(t *testing.T) {
	handler := New(identity.Service{}, nil, false)
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/organizations/o/runs/r/artifacts/a/office"},
		{"GET", "/v1/organizations/o/runs/r/artifacts/a/office-preview"},
		{"GET", "/v1/organizations/o/runs/r/artifacts/a/office-preview?format=xlsx"},
		{"POST", "/v1/organizations/o/conversations/c/messages"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(c.method, c.path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: %d", c.method, c.path, response.Code)
		}
	}
}
func TestReviewSourceBytesAreVerified(t *testing.T) {
	storage := artifacts.Local{Root: t.TempDir()}
	text := "\ufeff中文🙂\r\n"
	key := "org/run/artifact"
	stored, err := storage.Put(context.Background(), key, strings.NewReader(text), 1024)
	if err != nil {
		t.Fatal(err)
	}
	h := Handler{delivery: executiondelivery.Service{Storage: storage}}
	file := artifactreview.File{StorageKey: key, SHA256: stored.SHA256, SizeBytes: stored.Size, MIMEType: "text/plain", State: "AVAILABLE"}
	if got, err := h.readReviewFile(context.Background(), file); err != nil || got != text {
		t.Fatalf("%q %v", got, err)
	}
	file.SHA256 = strings.Repeat("0", 64)
	if _, err := h.readReviewFile(context.Background(), file); err == nil {
		t.Fatal("corrupt source accepted")
	}
}

func TestOfficeChartSourcePassesStorageValidation(t *testing.T) {
	raw, err := os.ReadFile("../../tests/frontend/e2e/fixtures/office-preview/styled.pptx")
	if err != nil {
		t.Fatal(err)
	}
	storage := artifacts.Local{Root: t.TempDir()}
	stored, err := storage.Put(context.Background(), "org/run/chart", strings.NewReader(string(raw)), office.MaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	h := Handler{delivery: executiondelivery.Service{Storage: storage}}
	f := artifactreview.File{StorageKey: "org/run/chart", SHA256: stored.SHA256, SizeBytes: stored.Size, MIMEType: office.PPTX, State: "AVAILABLE"}
	if got, err := h.readReviewFile(context.Background(), f); err != nil || got != string(raw) {
		t.Fatalf("chart preview source rejected: %v", err)
	}
}
