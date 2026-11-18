package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"openfox/internal/imports"
)

func TestWriteImportErrorMapsDomainErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
		msg    string
	}{
		{imports.ErrForbidden, http.StatusForbidden, "FORBIDDEN", ""},
		{imports.ErrNotFound, http.StatusNotFound, "NOT_FOUND", ""},
		{imports.ErrSpaceNotFound, http.StatusNotFound, "SPACE_NOT_FOUND", ""},
		{imports.ErrVersionConflict, http.StatusConflict, "VERSION_CONFLICT", ""},
		{imports.ErrImportState, http.StatusConflict, "IMPORT_STATE", ""},
		{imports.ErrImportActive, http.StatusConflict, "IMPORT_ACTIVE", ""},
		{imports.ErrRunActive, http.StatusConflict, "RUN_ACTIVE", ""},
		{fmt.Errorf("%w: expired", imports.ErrRollbackUnavailable), http.StatusConflict, "IMPORT_ROLLBACK_UNAVAILABLE", ""},
		{fmt.Errorf("%w: 列序号 9 超出文件列数", imports.ErrInvalidConfig), http.StatusBadRequest, "INVALID_IMPORT_CONFIG", ""},
		{fmt.Errorf("%w: x", imports.ErrConfigIncomplete), http.StatusBadRequest, "IMPORT_CONFIG_INCOMPLETE", ""},
		{fmt.Errorf("%w: x", imports.ErrOverwriteConfirmation), http.StatusBadRequest, "OVERWRITE_CONFIRMATION_REQUIRED", ""},
		{fmt.Errorf("%w: 文件是空的", imports.ErrInvalidFile), http.StatusBadRequest, "INVALID_IMPORT_FILE", "文件是空的"},
		{imports.ErrInvalidRequest, http.StatusBadRequest, "INVALID_REQUEST", ""},
	}
	for _, item := range cases {
		recorder := httptest.NewRecorder()
		writeImportError(recorder, httptest.NewRequest(http.MethodPost, "/", nil), item.err)
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: decode body: %v", item.err, err)
		}
		if recorder.Code != item.status || body.Error.Code != item.code {
			t.Errorf("%v: got %d %s, want %d %s", item.err, recorder.Code, body.Error.Code, item.status, item.code)
		}
		if item.msg != "" && body.Error.Message != item.msg {
			t.Errorf("%v: message = %q, want %q", item.err, body.Error.Message, item.msg)
		}
	}
}

func TestImportRollbackMessage(t *testing.T) {
	for _, reason := range []string{"not_finished", "expired", "rolled_back", "nothing_created", "other"} {
		if importRollbackMessage(reason) == "" {
			t.Fatalf("empty message for %q", reason)
		}
	}
}
