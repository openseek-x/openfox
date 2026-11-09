package dispatch

import (
	"sync"
	"time"
)

// 设备唤醒消息类型。WSS 只做提示：设备收到后仍须通过 HTTPS 端点读取事实。
const (
	MessageRunDispatch = "run.dispatch"
	MessageRunCancel   = "run.cancel"
	// MessageDeviceCommand 提示电脑拉取远程命令；载荷 {command_id, kind}。
	MessageDeviceCommand = "device.command"
)

// hubBuffer 是单个连接的待发唤醒上限；满了直接丢弃，设备的 offer 轮询会兜底。
const hubBuffer = 32

// Wake 是推送给某台设备的唤醒提示。
type Wake struct {
	Type           string    `json:"type"`
	OrganizationID string    `json:"organization_id"`
	RunID          string    `json:"run_id"`
	Generation     int64     `json:"dispatch_generation"`
	OfferID        string    `json:"offer_id,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	CommandID      string    `json:"command_id,omitempty"`
	CommandKind    string    `json:"command_kind,omitempty"`
}

// Relay 把唤醒转发给其他中心实例（见 PostgresRelay）。Broadcast 必须非阻塞。
type Relay interface {
	Broadcast(deviceID string, wake Wake)
}

// Hub 是进程内的设备连接注册表。设置 Relay 后，Publish 同时把唤醒广播给其他实例，
// 由各实例投递给本地连接；未设置或广播丢失时依赖设备轮询（offer-poll-interval）兜底。
type Hub struct {
	mu          sync.Mutex
	subscribers map[string]map[chan Wake]struct{}
	relay       Relay
}

// SetRelay 设置跨实例转发；应在开始 Publish 之前调用。
func (h *Hub) SetRelay(relay Relay) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.relay = relay
	h.mu.Unlock()
}

func NewHub() *Hub {
	return &Hub{subscribers: map[string]map[chan Wake]struct{}{}}
}

// Subscribe 为设备连接注册一个有界通道；返回的 cancel 必须在连接关闭时调用。
func (h *Hub) Subscribe(deviceID string) (<-chan Wake, func()) {
	channel := make(chan Wake, hubBuffer)
	if h == nil {
		return channel, func() {}
	}
	h.mu.Lock()
	set := h.subscribers[deviceID]
	if set == nil {
		set = map[chan Wake]struct{}{}
		h.subscribers[deviceID] = set
	}
	set[channel] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return channel, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subscribers[deviceID], channel)
			if len(h.subscribers[deviceID]) == 0 {
				delete(h.subscribers, deviceID)
			}
			h.mu.Unlock()
		})
	}
}

// Publish 非阻塞地投递给本实例上该设备的所有连接，并（若设置了 Relay）广播给其他实例。
// 返回本实例的投递数。设备在其他实例上的连接也会收到；重复唤醒对设备无害（以 HTTPS 事实为准）。
func (h *Hub) Publish(deviceID string, wake Wake) int {
	if h == nil {
		return 0
	}
	delivered := h.Deliver(deviceID, wake)
	h.mu.Lock()
	relay := h.relay
	h.mu.Unlock()
	if relay != nil {
		relay.Broadcast(deviceID, wake)
	}
	return delivered
}

// Deliver 只投递给本实例上的连接，不再广播；供 Relay 处理来自其他实例的唤醒。
func (h *Hub) Deliver(deviceID string, wake Wake) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delivered := 0
	for channel := range h.subscribers[deviceID] {
		select {
		case channel <- wake:
			delivered++
		default:
		}
	}
	return delivered
}

// PublishOffer 把新建 offer 转成 run.dispatch 唤醒。
func (h *Hub) PublishOffer(offer Offer) int {
	return h.Publish(offer.DeviceID, Wake{
		Type: MessageRunDispatch, OrganizationID: offer.OrganizationID, RunID: offer.RunID,
		Generation: offer.Generation, OfferID: offer.OfferID, ExpiresAt: offer.ExpiresAt,
	})
}

// PublishCancel 通知执行设备停止进程；事实仍以续租响应的 cancel_requested 为准。
func (h *Hub) PublishCancel(deviceID, organizationID, runID string, generation int64) int {
	return h.Publish(deviceID, Wake{
		Type: MessageRunCancel, OrganizationID: organizationID, RunID: runID, Generation: generation,
	})
}

// PublishCommand 提示电脑有待领取的远程命令；事实以设备侧 HTTPS 拉取为准。
func (h *Hub) PublishCommand(deviceID, organizationID, commandID, kind string) int {
	return h.Publish(deviceID, Wake{
		Type: MessageDeviceCommand, OrganizationID: organizationID, CommandID: commandID, CommandKind: kind,
	})
}
