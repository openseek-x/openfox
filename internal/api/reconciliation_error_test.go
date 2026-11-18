package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"openfox/internal/reconciliation"
)

// 12a：人工标记失败与自动裁决竞争、版本过期时的错误映射必须稳定（前端据此提示“已处理/请刷新”）。
func TestWriteReconciliationErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
	}{
		{reconciliation.ErrInvalidRequest, http.StatusBadRequest},
		{reconciliation.ErrNotFound, http.StatusNotFound},
		{reconciliation.ErrVersionConflict, http.StatusConflict},
		{reconciliation.ErrStaleEvidence, http.StatusConflict},
		{errors.New("boom"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		recorder := httptest.NewRecorder()
		writeReconciliationError(recorder, httptest.NewRequest(http.MethodPost, "/", nil), c.err)
		if recorder.Code != c.status {
			t.Errorf("%v -> %d, want %d", c.err, recorder.Code, c.status)
		}
	}
}
