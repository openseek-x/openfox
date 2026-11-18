package api

import (
	"net/http"

	"github.com/coder/websocket"
	"openfox/internal/membership"
)

func (h *DeviceManagementHandler) gitlabProxy(w http.ResponseWriter, r *http.Request) {
	sel, ok := h.selectDevice(w, r)
	if !ok {
		return
	}
	id := r.PathValue("streamID")
	if !membership.ValidateUUID(id) || h.proxy.Pool == nil {
		http.NotFound(w, r)
		return
	}
	addr, err := h.proxy.Claim(r.Context(), sel, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	if err := ws.Write(r.Context(), websocket.MessageText, []byte(addr)); err != nil {
		return
	}
	ws.SetReadLimit(32768)
	h.proxy.Serve(websocket.NetConn(r.Context(), ws, websocket.MessageBinary), sel, id)
}
