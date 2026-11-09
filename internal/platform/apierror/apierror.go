package apierror

import (
	"encoding/json"
	"net/http"
)

type Response struct {
	Error Error `json:"error"`
}

type Error struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details,omitempty"`
}

func Write(w http.ResponseWriter, status int, code, message, requestID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Response{Error: Error{Code: code, Message: message, RequestID: requestID}})
}

// WriteDetails 与 Write 相同，另外输出 error.details（为空时省略）。
func WriteDetails(w http.ResponseWriter, status int, code, message, requestID string, details map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Response{Error: Error{Code: code, Message: message, RequestID: requestID, Details: details}})
}
