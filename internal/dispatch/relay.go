package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	mathrand "math/rand/v2"
	"sync"
	"time"
)

// WakeChannel 是跨实例设备唤醒使用的 PostgreSQL 通知通道。
const WakeChannel = "openfox_device_wake"

const (
	// relayQueue 是待广播唤醒的上限；满了直接丢弃（唤醒只是提示，设备轮询兜底）。
	relayQueue = 256
	// relayNotifyTimeout 限制单条 NOTIFY 的耗时，避免数据库卡顿时堆积。
	relayNotifyTimeout = 3 * time.Second
	// 监听连接断开后的重连等待：从 relayMinBackoff 起翻倍，上限 relayMaxBackoff，±20% 抖动。
	relayMinBackoff = time.Second
	relayMaxBackoff = 30 * time.Second
	// 连接保持超过 relayStableAfter 才视为恢复正常，重置重连等待。
	relayStableAfter = 30 * time.Second
)

// Notifier 是跨实例转发所需的最小数据库边界（由 postgres.Pool 实现）。
type Notifier interface {
	Notify(ctx context.Context, channel, payload string) error
	Listen(ctx context.Context, channel string, onNotify func(payload string)) error
}

// relayMessage 是通知载荷：只含路由与唤醒提示，不含正文、凭据或用户信息。
type relayMessage struct {
	Instance string `json:"instance"`
	DeviceID string `json:"device_id"`
	Wake     Wake   `json:"wake"`
}

// PostgresRelay 通过 PostgreSQL LISTEN/NOTIFY 在中心实例之间转发设备唤醒：
// 本实例 Publish 的唤醒被 NOTIFY 出去，其他实例收到后投递给自己持有的设备连接；
// 带本实例 ID 的通知会被忽略，避免重复投递。通知丢失（队列满、断线期间）不影响正确性。
type PostgresRelay struct {
	notifier Notifier
	hub      *Hub
	logger   *slog.Logger
	instance string
	queue    chan relayMessage

	droppedMu sync.Mutex
	dropped   int
}

// NewPostgresRelay 创建转发器并挂到 hub 上；调用方需要运行 Run。
func NewPostgresRelay(notifier Notifier, hub *Hub, logger *slog.Logger) (*PostgresRelay, error) {
	if notifier == nil || hub == nil {
		return nil, errors.New("dispatch: relay requires a notifier and a hub")
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	relay := &PostgresRelay{
		notifier: notifier, hub: hub, logger: logger,
		instance: hex.EncodeToString(raw[:]), queue: make(chan relayMessage, relayQueue),
	}
	hub.SetRelay(relay)
	return relay, nil
}

// Instance 返回本实例在通知中使用的 ID。
func (r *PostgresRelay) Instance() string { return r.instance }

// Broadcast 实现 Relay：非阻塞入队，队列满时丢弃并计数。
func (r *PostgresRelay) Broadcast(deviceID string, wake Wake) {
	select {
	case r.queue <- relayMessage{Instance: r.instance, DeviceID: deviceID, Wake: wake}:
	default:
		r.droppedMu.Lock()
		r.dropped++
		r.droppedMu.Unlock()
	}
}

// Run 同时运行发送与监听，直到 ctx 取消。
func (r *PostgresRelay) Run(ctx context.Context) {
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		r.send(ctx)
	}()
	go func() {
		defer wait.Done()
		r.listen(ctx)
	}()
	wait.Wait()
}

func (r *PostgresRelay) send(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case message := <-r.queue:
			payload, err := json.Marshal(message)
			if err != nil {
				continue
			}
			notifyContext, cancel := context.WithTimeout(ctx, relayNotifyTimeout)
			err = r.notifier.Notify(notifyContext, WakeChannel, string(payload))
			cancel()
			if err != nil && ctx.Err() == nil {
				r.logger.Warn("dispatch_relay_notify_failed", "device_id", message.DeviceID, "type", message.Wake.Type, "error", err.Error())
			}
			if dropped := r.takeDropped(); dropped > 0 {
				r.logger.Warn("dispatch_relay_queue_full", "dropped", dropped)
			}
		}
	}
}

func (r *PostgresRelay) takeDropped() int {
	r.droppedMu.Lock()
	defer r.droppedMu.Unlock()
	dropped := r.dropped
	r.dropped = 0
	return dropped
}

func (r *PostgresRelay) listen(ctx context.Context) {
	backoff := relayMinBackoff
	for {
		started := time.Now()
		err := r.notifier.Listen(ctx, WakeChannel, r.receive)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= relayStableAfter {
			backoff = relayMinBackoff
		}
		wait := jitter(backoff)
		message := "listener stopped"
		if err != nil {
			message = err.Error()
		}
		r.logger.Warn("dispatch_relay_listener_disconnected", "error", message, "retry_in", wait.Round(time.Millisecond).String())
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > relayMaxBackoff {
			backoff = relayMaxBackoff
		}
	}
}

// receive 处理一条通知：忽略本实例发出的、格式不对的或类型未知的消息，其余只做本地投递。
func (r *PostgresRelay) receive(payload string) {
	var message relayMessage
	if err := json.Unmarshal([]byte(payload), &message); err != nil {
		return
	}
	if message.Instance == r.instance || message.DeviceID == "" {
		return
	}
	if message.Wake.Type != MessageRunDispatch && message.Wake.Type != MessageRunCancel && message.Wake.Type != "gitlab.proxy" {
		return
	}
	r.hub.Deliver(message.DeviceID, message.Wake)
}

func jitter(base time.Duration) time.Duration {
	// ±20% 抖动，避免多个实例在数据库恢复时同时重连。
	factor := 0.8 + 0.4*mathrand.Float64()
	return time.Duration(float64(base) * factor)
}
