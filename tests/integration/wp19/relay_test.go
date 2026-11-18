package wp19

import (
	"context"
	"os"
	"testing"
	"time"

	"openfox/internal/dispatch"
	"openfox/internal/platform/postgres"
)

// TestRelayWakesDeviceOnAnotherInstance 用两个独立连接池模拟两个中心实例：
// 实例 A 发布的唤醒经 PostgreSQL NOTIFY 到达实例 B 上的设备连接，A 自己不会重复收到。
func TestRelayWakesDeviceOnAnotherInstance(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	open := func() *postgres.Pool {
		pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 2, ContextRole: "openfox_context", AppRole: "openfox_app",
			StatementTimeout: 3 * time.Second, LockTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return pool
	}
	poolA, poolB := open(), open()
	defer poolA.Close()
	defer poolB.Close()

	hubA, hubB := dispatch.NewHub(), dispatch.NewHub()
	relayA, err := dispatch.NewPostgresRelay(poolA, hubA, nil)
	if err != nil {
		t.Fatal(err)
	}
	relayB, err := dispatch.NewPostgresRelay(poolB, hubB, nil)
	if err != nil {
		t.Fatal(err)
	}
	runContext, stop := context.WithCancel(ctx)
	defer stop()
	go relayA.Run(runContext)
	go relayB.Run(runContext)

	onA, cancelA := hubA.Subscribe(device1)
	defer cancelA()
	onB, cancelB := hubB.Subscribe(device1)
	defer cancelB()

	// LISTEN 在后台建立；重复发布直到 B 收到，避免依赖固定等待。
	deadline := time.Now().Add(10 * time.Second)
	var relayed dispatch.Wake
	for relayed.Type == "" {
		if time.Now().After(deadline) {
			t.Fatal("instance B never received the relayed wake")
		}
		hubA.PublishCancel(device1, orgA, "run-relay", 7)
		select {
		case relayed = <-onB:
		case <-time.After(200 * time.Millisecond):
		}
	}
	if relayed.Type != dispatch.MessageRunCancel || relayed.RunID != "run-relay" || relayed.Generation != 7 || relayed.OrganizationID != orgA {
		t.Fatalf("relayed wake = %+v", relayed)
	}

	// A 上的连接只收到本地投递（每次发布一次），不会收到自己 NOTIFY 的回声。
	// 先排空上面重试循环留下的唤醒，再单独发布一次计数。
	time.Sleep(300 * time.Millisecond)
	drain(onA)
	drain(onB)
	hubA.PublishCancel(device1, orgA, "run-relay-2", 8)
	select {
	case wake := <-onB:
		if wake.RunID != "run-relay-2" {
			t.Fatalf("second relayed wake = %+v", wake)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second relayed wake missing")
	}
	time.Sleep(300 * time.Millisecond)
	if got := drain(onA); got != 1 {
		t.Fatalf("instance A received %d wakes for one publish, want 1 (no echo)", got)
	}
}

func drain(channel <-chan dispatch.Wake) int {
	count := 0
	for {
		select {
		case <-channel:
			count++
		default:
			return count
		}
	}
}
