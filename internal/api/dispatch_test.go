package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/dispatch"
)

func TestWriteDispatchErrorMapsDomainErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{dispatch.ErrInvalidRequest, http.StatusBadRequest, "INVALID_REQUEST"},
		{dispatch.ErrNotFound, http.StatusNotFound, "NOT_FOUND"},
		{dispatch.ErrForbidden, http.StatusForbidden, "FORBIDDEN"},
		{dispatch.ErrAuthorizationRevoked, http.StatusForbidden, "AUTHORIZATION_REVOKED"},
		{dispatch.ErrOfferUnavailable, http.StatusConflict, "OFFER_UNAVAILABLE"},
		{dispatch.ErrGenerationConflict, http.StatusConflict, "GENERATION_CONFLICT"},
		{dispatch.ErrCancelRequested, http.StatusConflict, "CANCEL_REQUESTED"},
		{dispatch.ErrReconciliationRequired, http.StatusConflict, "RECONCILIATION_REQUIRED"},
		{dispatch.ErrIdempotencyConflict, http.StatusConflict, "IDEMPOTENCY_CONFLICT"},
		{fmt.Errorf("wrapped: %w", dispatch.ErrStateConflict), http.StatusConflict, "RUN_STATE_CONFLICT"},
		{&pgconn.PgError{Code: "40001"}, http.StatusServiceUnavailable, "RETRY"},
		{&pgconn.PgError{Code: "40P01"}, http.StatusServiceUnavailable, "RETRY"},
		{&pgconn.PgError{Code: "55P03"}, http.StatusServiceUnavailable, "RETRY"},
	}
	for _, item := range cases {
		recorder := httptest.NewRecorder()
		writeDispatchError(recorder, httptest.NewRequest(http.MethodPost, "/", nil), item.err)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: decode body: %v", item.err, err)
		}
		if recorder.Code != item.status || body.Error.Code != item.code {
			t.Errorf("%v: got %d %s, want %d %s", item.err, recorder.Code, body.Error.Code, item.status, item.code)
		}
	}
}
