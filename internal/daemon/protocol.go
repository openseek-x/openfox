package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"openfox/internal/devices"
)

const ProtocolMajor = 1

// 中心推送的派发唤醒类型，与 internal/dispatch 保持一致。
const (
	MessageRunDispatch = "run.dispatch"
	MessageRunCancel   = "run.cancel"
	// MessageDeviceCommand 是远程命令唤醒，payload {command_id, kind}。
	MessageDeviceCommand = "device.command"
)

const (
	ProtocolSubprotocol = "openfox.device.v1"
	MaxMessageBytes     = 256 * 1024
)

var (
	ErrProtocolMismatch  = errors.New("daemon: protocol mismatch")
	ErrEnvelopeMismatch  = errors.New("daemon: envelope identity mismatch")
	ErrReconcileRequired = errors.New("daemon: reconciliation required")
	ErrMessageTooLarge   = errors.New("daemon: message too large")
)

type Envelope struct {
	ProtocolMajor      int    `json:"protocol_major"`
	ProtocolMinor      int    `json:"protocol_minor"`
	MessageID          string `json:"message_id"`
	CenterEpoch        int64  `json:"center_epoch"`
	Type               string `json:"type"`
	OrganizationID     string `json:"organization_id,omitempty"`
	ComputerID         string `json:"computer_id"`
	RunID              string `json:"run_id,omitempty"`
	DispatchGeneration int64  `json:"dispatch_generation,omitempty"`
}

type Message struct {
	Envelope
	Payload json.RawMessage `json:"payload,omitempty"`
}

type HeartbeatPayload struct {
	CenterEpoch     int64           `json:"center_epoch"`
	ProtocolMajor   int             `json:"protocol_major"`
	ProtocolMinor   int             `json:"protocol_minor"`
	DaemonVersion   string          `json:"daemon_version"`
	OperatingSystem string          `json:"operating_system"`
	Architecture    string          `json:"architecture"`
	Capabilities    json.RawMessage `json:"capabilities"`
}

type HeartbeatAckPayload struct {
	DeviceID    string `json:"device_id"`
	CenterEpoch int64  `json:"center_epoch"`
	Version     int64  `json:"version"`
	ReceivedAt  string `json:"received_at"`
}

type ProtocolErrorPayload struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	CenterEpoch int64  `json:"center_epoch,omitempty"`
}

func NewMessage(messageType, computerID string, centerEpoch int64, payload any) (Message, error) {
	if strings.TrimSpace(messageType) == "" || strings.TrimSpace(computerID) == "" || centerEpoch <= 0 {
		return Message{}, ErrProtocolMismatch
	}
	messageID, err := newMessageID()
	if err != nil {
		return Message{}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("encode daemon message payload: %w", err)
	}
	if len(encoded) > MaxMessageBytes {
		return Message{}, ErrMessageTooLarge
	}
	return Message{Envelope: Envelope{
		ProtocolMajor: ProtocolMajor,
		ProtocolMinor: 0,
		MessageID:     messageID,
		CenterEpoch:   centerEpoch,
		Type:          messageType,
		ComputerID:    computerID,
	}, Payload: encoded}, nil
}

func (m Message) Heartbeat() (devices.Heartbeat, error) {
	if m.Type != "hello" && m.Type != "heartbeat" {
		return devices.Heartbeat{}, ErrProtocolMismatch
	}
	var payload HeartbeatPayload
	if len(m.Payload) == 0 || json.Unmarshal(m.Payload, &payload) != nil {
		return devices.Heartbeat{}, ErrProtocolMismatch
	}
	if payload.CenterEpoch != m.CenterEpoch {
		return devices.Heartbeat{}, ErrProtocolMismatch
	}
	return devices.Heartbeat{
		CenterEpoch: payload.CenterEpoch, ProtocolMajor: payload.ProtocolMajor,
		ProtocolMinor: payload.ProtocolMinor, DaemonVersion: payload.DaemonVersion,
		OperatingSystem: payload.OperatingSystem, Architecture: payload.Architecture,
		Capabilities: append([]byte(nil), payload.Capabilities...),
	}, nil
}

func NewHeartbeatMessage(messageType, computerID string, heartbeat devices.Heartbeat) (Message, error) {
	return NewMessage(messageType, computerID, heartbeat.CenterEpoch, HeartbeatPayload{
		CenterEpoch: heartbeat.CenterEpoch, ProtocolMajor: heartbeat.ProtocolMajor,
		ProtocolMinor: heartbeat.ProtocolMinor, DaemonVersion: heartbeat.DaemonVersion,
		OperatingSystem: heartbeat.OperatingSystem, Architecture: heartbeat.Architecture,
		Capabilities: append([]byte(nil), heartbeat.Capabilities...),
	})
}

func newMessageID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return hex.EncodeToString(value[0:4]) + "-" + hex.EncodeToString(value[4:6]) + "-" +
		hex.EncodeToString(value[6:8]) + "-" + hex.EncodeToString(value[8:10]) + "-" +
		hex.EncodeToString(value[10:16]), nil
}

func (e Envelope) Validate(expectedComputerID string, expectedEpoch int64) error {
	if e.ProtocolMajor != ProtocolMajor || e.ProtocolMinor < 0 || e.MessageID == "" || e.CenterEpoch <= 0 || e.Type == "" {
		return ErrProtocolMismatch
	}
	if strings.TrimSpace(expectedComputerID) == "" || e.ComputerID != expectedComputerID {
		return ErrEnvelopeMismatch
	}
	if expectedEpoch > 0 && e.CenterEpoch != expectedEpoch {
		return ErrProtocolMismatch
	}
	return nil
}
