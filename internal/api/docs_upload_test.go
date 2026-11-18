package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openfox/internal/artifacts"
	"openfox/internal/docs"
)

// 存储层超限必须映射为 413，其余存储错误保持 500。
func TestWriteStoreErrorMapsTooLarge(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"artifact too large", artifacts.ErrTooLarge, http.StatusRequestEntityTooLarge, "DOC_ATTACHMENT_TOO_LARGE"},
		{"wrapped too large", fmt.Errorf("put: %w", artifacts.ErrTooLarge), http.StatusRequestEntityTooLarge, "DOC_ATTACHMENT_TOO_LARGE"},
		{"other storage failure", errors.New("disk full"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			writeStoreError(recorder, request, tc.err, docs.ErrAttachmentLarge)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.status)
			}
			if body := recorder.Body.String(); !strings.Contains(body, tc.code) {
				t.Fatalf("body %s does not carry code %s", body, tc.code)
			}
		})
	}
}
