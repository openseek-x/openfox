package api

import (
	"net/http"
	"openfox/internal/agents"
	"strconv"
)

func (h *Handler) registerAgentMCPRoutes(mux *http.ServeMux) {
	const base = "/v1/organizations/{organizationID}/spaces/{spaceID}/agents/{agentID}/mcp-servers"
	mux.HandleFunc("GET "+base, h.agentMCP)
	mux.HandleFunc("PUT "+base+"/{serverID}", h.agentMCP)
	mux.HandleFunc("DELETE "+base+"/{serverID}", h.agentMCP)
	mux.HandleFunc("POST "+base+"/discover", h.agentMCP)
	mux.HandleFunc("GET "+base+"/inventory", h.agentMCPInventory)
	mux.HandleFunc("POST "+base+"/inventory/refresh", h.agentMCPInventory)
	mux.HandleFunc("PUT "+base+"/inherited/{name}", h.agentMCPInventory)
}
func (h *Handler) agentMCP(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, r.Method != "GET")
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	store := h.agentStore()
	actor := h.agentActor(auth, r)
	s, a, id := r.PathValue("spaceID"), r.PathValue("agentID"), r.PathValue("serverID")
	var out any
	var err error
	switch r.Method {
	case "GET":
		var list agents.MCPList
		list, err = store.ListMCP(r.Context(), auth.Selection, actor, s, a)
		list.OAuthRedirectURI = h.centerURL() + mcpCallback
		out = list
	case "DELETE":
		var version int64
		version, err = strconv.ParseInt(r.URL.Query().Get("version"), 10, 64)
		if err != nil || version < 1 {
			err = agents.ErrInvalidRequest
		} else {
			err = store.DeleteMCP(r.Context(), auth.Selection, actor, s, a, id, version)
		}
	default:
		var in agents.MCPInput
		if decodeJSON(w, r, &in) != nil {
			err = agents.ErrInvalidRequest
		} else if r.Method == "PUT" {
			out, err = store.PutMCP(r.Context(), auth.Selection, actor, s, a, id, in, h.importSafe)
		} else {
			out, err = store.DiscoverMCP(r.Context(), auth.Selection, actor, s, a, in, h.importSafe)
		}
	}
	if err != nil {
		writeAgentError(w, r, err)
		return
	}
	if r.Method == "DELETE" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) agentMCPInventory(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.beginAgentRequest(w, r, r.Method != "GET")
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var change *agents.MCPInheritInput
	if r.Method == "PUT" {
		change = new(agents.MCPInheritInput)
		if decodeJSON(w, r, change) != nil {
			writeAgentError(w, r, agents.ErrInvalidRequest)
			return
		}
	}
	out, e := h.agentStore().MCPInventory(r.Context(), auth.Selection, h.agentActor(auth, r), r.PathValue("spaceID"), r.PathValue("agentID"), r.PathValue("name"), change)
	if e != nil {
		writeAgentError(w, r, e)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
