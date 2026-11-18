package api

import (
	"net/http"
	"strings"

	"openfox/internal/agents"
	"openfox/internal/idempotency"
)

func (h *Handler) createAgentVariant(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	var input agents.VariantInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	if !idempotency.ValidKey(key) {
		writeError(w, r, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key is invalid")
		return
	}
	item, replay, err := h.agentStore().CreateVariant(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"), r.PathValue("agentID"), input, key)
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeCreated(w, replay, item)
}

func (h *Handler) resetAgentInheritance(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Groups []string `json:"groups"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	item, err := h.agentStore().ResetInheritance(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"), r.PathValue("agentID"), input.Groups)
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
