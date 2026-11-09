package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"openfox/internal/daemon"
	"openfox/internal/deviceauth"
	"openfox/internal/devices"
	"openfox/internal/dispatch"
)

type DeviceStreamHandler struct {
	deviceAuth deviceauth.Service
	store      devices.Store
	now        func() time.Time
	hub        *dispatch.Hub
}

// RegisterDeviceStreamRoute 注册设备 WSS。hub 可选：首个心跳确认后，
// 该连接开始接收 run.dispatch / run.cancel 唤醒（只是提示，事实以 HTTPS 为准）。
func RegisterDeviceStreamRoute(mux *http.ServeMux, deviceAuth deviceauth.Service, store devices.Store, hub ...*dispatch.Hub) {
	if mux == nil {
		return
	}
	handler := &DeviceStreamHandler{
		deviceAuth: deviceAuth,
		store:      store,
		now:        time.Now,
	}
	if len(hub) > 0 {
		handler.hub = hub[0]
	}
	mux.Handle("GET /v1/device/connect", handler)
}

func (h *DeviceStreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.store == nil || h.now == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "device stream is not ready")
		return
	}
	token := bearerToken(r)
	if token == "" {
		writeError(w, r, http.StatusUnauthorized, "DEVICE_UNAUTHENTICATED", "device authentication is required")
		return
	}
	principal, err := h.deviceAuth.Authenticate(r.Context(), token)
	if err != nil {
		writeDeviceError(w, r, err)
		return
	}
	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{daemon.ProtocolSubprotocol},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	if connection.Subprotocol() != daemon.ProtocolSubprotocol {
		_ = connection.Close(websocket.StatusProtocolError, "unsupported subprotocol")
		return
	}
	connection.SetReadLimit(daemon.MaxMessageBytes)
	defer connection.Close(websocket.StatusNormalClosure, "")

	streamContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscribed := false
	for {
		messageType, payload, err := connection.Read(streamContext)
		if err != nil {
			return
		}
		if messageType != websocket.MessageText || len(payload) == 0 || len(payload) > daemon.MaxMessageBytes {
			h.writeError(streamContext, connection, daemon.Message{}, "PROTOCOL_ERROR", "text message is required", 0)
			return
		}
		var message daemon.Message
		if err := json.Unmarshal(payload, &message); err != nil {
			h.writeError(streamContext, connection, daemon.Message{}, "PROTOCOL_ERROR", "message is invalid", 0)
			return
		}
		if err := message.Envelope.Validate(principal.DeviceID, 0); err != nil {
			h.writeError(streamContext, connection, message, "IDENTITY_MISMATCH", "message identity is invalid", message.CenterEpoch)
			return
		}
		heartbeat, err := message.Heartbeat()
		if err != nil || heartbeat.CenterEpoch != message.CenterEpoch {
			h.writeError(streamContext, connection, message, "PROTOCOL_ERROR", "heartbeat payload is invalid", message.CenterEpoch)
			return
		}
		result, err := h.store.RecordHeartbeat(streamContext, principal, heartbeat, h.now().UTC())
		if err != nil {
			if errors.Is(err, devices.ErrEpochConflict) {
				currentEpoch, epochErr := h.store.CenterEpoch(streamContext)
				if epochErr == nil {
					h.writeError(streamContext, connection, message, "EPOCH_CONFLICT", "center epoch requires reconciliation", currentEpoch)
				}
				return
			}
			h.writeError(streamContext, connection, message, "HEARTBEAT_REJECTED", "heartbeat was rejected", message.CenterEpoch)
			return
		}
		if err := h.writeAck(streamContext, connection, message, result); err != nil {
			return
		}
		if !subscribed && h.hub != nil {
			subscribed = true
			wakes, unsubscribe := h.hub.Subscribe(principal.DeviceID)
			defer unsubscribe()
			go h.forwardWakes(streamContext, connection, principal.DeviceID, result.CenterEpoch, wakes)
		}
	}
}

// forwardWakes 把 Hub 唤醒写成协议消息。coder/websocket 的 Write 可与 Read 并发；
// 写失败时取消连接上下文，让读循环退出并关闭连接。
func (h *DeviceStreamHandler) forwardWakes(ctx context.Context, connection *websocket.Conn, deviceID string, epoch int64, wakes <-chan dispatch.Wake) {
	for {
		select {
		case <-ctx.Done():
			return
		case wake := <-wakes:
			messageID, err := newUUID()
			if err != nil {
				continue
			}
			var payload []byte
			switch wake.Type {
			case dispatch.MessageRunDispatch:
				payload, _ = json.Marshal(map[string]any{"offer_id": wake.OfferID, "expires_at": wake.ExpiresAt.UTC().Format(time.RFC3339Nano)})
			case dispatch.MessageDeviceCommand, "gitlab.proxy":
				payload, _ = json.Marshal(map[string]string{"command_id": wake.CommandID, "kind": wake.CommandKind})
			default:
				payload = []byte(`{}`)
			}
			message := daemon.Message{Envelope: daemon.Envelope{
				ProtocolMajor: daemon.ProtocolMajor, ProtocolMinor: 0, MessageID: messageID,
				CenterEpoch: epoch, Type: wake.Type, OrganizationID: wake.OrganizationID,
				ComputerID: deviceID, RunID: wake.RunID, DispatchGeneration: wake.Generation,
			}, Payload: payload}
			if err := h.writeMessage(ctx, connection, message); err != nil {
				_ = connection.Close(websocket.StatusGoingAway, "write failed")
				return
			}
		}
	}
}

func (h *DeviceStreamHandler) writeAck(ctx context.Context, connection *websocket.Conn, message daemon.Message, result devices.HeartbeatResult) error {
	payload, err := json.Marshal(daemon.HeartbeatAckPayload{
		DeviceID: result.DeviceID, CenterEpoch: result.CenterEpoch,
		Version: result.Version, ReceivedAt: result.ReceivedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	ack := daemon.Message{Envelope: daemon.Envelope{
		ProtocolMajor: message.ProtocolMajor, ProtocolMinor: message.ProtocolMinor,
		MessageID: message.MessageID, CenterEpoch: result.CenterEpoch,
		Type: message.Type + ".ack", ComputerID: result.DeviceID,
	}, Payload: payload}
	return h.writeMessage(ctx, connection, ack)
}

func (h *DeviceStreamHandler) writeError(ctx context.Context, connection *websocket.Conn, message daemon.Message, code, reason string, epoch int64) error {
	if connection == nil {
		return nil
	}
	if epoch <= 0 {
		epoch = message.CenterEpoch
	}
	if epoch <= 0 {
		epoch = 1
	}
	payload, err := json.Marshal(daemon.ProtocolErrorPayload{Code: code, Message: reason, CenterEpoch: epoch})
	if err != nil {
		return err
	}
	errorMessage := daemon.Message{Envelope: daemon.Envelope{
		ProtocolMajor: daemon.ProtocolMajor, ProtocolMinor: 0,
		MessageID: message.MessageID, CenterEpoch: epoch,
		Type: "reconcile_required", ComputerID: message.ComputerID,
	}, Payload: payload}
	if code != "EPOCH_CONFLICT" {
		errorMessage.Type = "error"
	}
	return h.writeMessage(ctx, connection, errorMessage)
}

func (h *DeviceStreamHandler) writeMessage(ctx context.Context, connection *websocket.Conn, message daemon.Message) error {
	payload, err := json.Marshal(message)
	if err != nil || len(payload) > daemon.MaxMessageBytes {
		return daemon.ErrMessageTooLarge
	}
	writeContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return connection.Write(writeContext, websocket.MessageText, payload)
}
