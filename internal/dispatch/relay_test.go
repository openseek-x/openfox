package dispatch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeBus 模拟 PostgreSQL 通知：Notify 投递给所有当前的监听者（包括发送者自己）。
type fakeBus struct {
	mu        sync.Mutex
	listeners map[int]func(string)
	next      int
	failures  int // 前 failures 次 Listen 立即失败，用于验证重连
	listens   int
	notifyErr error
}

func (b *fakeBus) Notify(_ context.Context, _ string, payload string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.notifyErr != nil {
		return b.notifyErr
	}
	for _, listener := range b.listeners {
		listener(payload)
	}
	return nil
}

func (b *fakeBus) Listen(ctx context.Context, _ string, onNotify func(string)) error {
	b.mu.Lock()
	b.listens++
	if b.failures > 0 {
		b.failures--
		b.mu.Unlock()
		return errors.New("connection refused")
	}
	if b.listeners == nil {
		b.listeners = map[int]func(string){}
	}
	id := b.next
	b.next++
	b.listeners[id] = onNotify
	b.mu.Unlock()
	<-ctx.Done()
	b.mu.Lock()
	delete(b.listeners, id)
	b.mu.Unlock()
	return ctx.Err()
}

func (b *fakeBus) listenerCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.listeners)
}

func TestRelayForwardsAcrossInstancesWithoutEcho(t *testing.T) {
	bus := &fakeBus{}
	hubA, hubB := NewHub(), NewHub()
	relayA, err := NewPostgresRelay(bus, hubA, nil)
	if err != nil {
		t.Fatal(err)
	}
	relayB, err := NewPostgresRelay(bus, hubB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if relayA.Instance() == relayB.Instance() {
		t.Fatal("relay instances share an ID")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relayA.Run(ctx)
	go relayB.Run(ctx)
	waitFor(t, func() bool { return bus.listenerCount() == 2 })

	onA, cancelA := hubA.Subscribe("device-1")
	defer cancelA()
	onB, cancelB := hubB.Subscribe("device-1")
	defer cancelB()

	offer := Offer{OrganizationID: "org", OfferID: "offer", RunID: "run", Generation: 2, DeviceID: "device-1", ExpiresAt: time.Unix(20, 0).UTC()}
	if delivered := hubA.PublishOffer(offer); delivered != 1 {
		t.Fatalf("local delivered = %d, want 1", delivered)
	}
	if wake := receive(t, onA); wake.OfferID != "offer" {
		t.Fatalf("local wake = %+v", wake)
	}
	wake := receive(t, onB)
	if wake.Type != MessageRunDispatch || wake.OfferID != "offer" || wake.Generation != 2 || !wake.ExpiresAt.Equal(offer.ExpiresAt) {
		t.Fatalf("relayed wake = %+v", wake)
	}
	// 发送实例忽略自己的通知：本地连接不会再收到第二次。
	select {
	case extra := <-onA:
		t.Fatalf("sender received its own relayed wake: %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}

	// 未知类型与格式错误的通知被丢弃。
	relayB.receive(`{"instance":"other","device_id":"device-1","wake":{"type":"run.unknown"}}`)
	relayB.receive(`not json`)
	select {
	case extra := <-onB:
		t.Fatalf("invalid notification delivered: %+v", extra)
	default:
	}
}

func TestRelayDropsWhenQueueFullAndReconnects(t *testing.T) {
	bus := &fakeBus{failures: 1}
	hub := NewHub()
	relay, err := NewPostgresRelay(bus, hub, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 未运行发送循环时队列会满：Broadcast 必须非阻塞并计数。
	for i := 0; i < relayQueue+5; i++ {
		relay.Broadcast("device-1", Wake{Type: MessageRunCancel})
	}
	if dropped := relay.takeDropped(); dropped != 5 {
		t.Fatalf("dropped = %d, want 5", dropped)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.Run(ctx)
	}()
	// 第一次 Listen 失败，约 1s（±20%）后重连成功。
	waitForWithin(t, 3*time.Second, func() bool { return bus.listenerCount() == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not stop after cancel")
	}
}

func TestJitterStaysWithinBounds(t *testing.T) {
	for i := 0; i < 100; i++ {
		if got := jitter(10 * time.Second); got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("jitter = %s", got)
		}
	}
}

func receive(t *testing.T, channel <-chan Wake) Wake {
	t.Helper()
	select {
	case wake := <-channel:
		return wake
	case <-time.After(time.Second):
		t.Fatal("no wake received")
		return Wake{}
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	waitForWithin(t, time.Second, condition)
}

func waitForWithin(t *testing.T, limit time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
