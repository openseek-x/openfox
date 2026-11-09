package feishuint

import (
	"net/http"
	"testing"
	"time"
)

func TestEventsAreDeduplicatedAndFiltered(t *testing.T) {
	w := setup(t)
	w.bind()
	w.startManager("mgr-a")
	w.waitStatus("CONNECTED")
	for i := 0; i < 2; i++ {
		if _, err := w.fake.PushEventWithID(w.appID, "evt-dup", "im.message.receive_v1", `{"message":{}}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.fake.PushEventWithID(w.appID, "evt-other", "im.message.receive_v1", `{}`); err != nil {
		t.Fatal(err)
	}
	// app_id 与连接不符的事件被丢弃（仍然 ACK）。
	if _, err := w.fake.PushEventFor(w.appID, "cli_someoneelse1", "evt-foreign", "im.message.receive_v1", `{}`); err != nil {
		t.Fatal(err)
	}
	count := func(id string) int {
		return w.count(`SELECT count(*) FROM openfox.im_inbound_events WHERE organization_id = $1::uuid AND event_id = $2`, w.org, id)
	}
	if count("evt-dup") != 1 || count("evt-other") != 1 || count("evt-foreign") != 0 {
		t.Fatalf("recorded dup=%d other=%d foreign=%d", count("evt-dup"), count("evt-other"), count("evt-foreign"))
	}
}

func TestReconnectAfterServerDrop(t *testing.T) {
	w := setup(t)
	w.bind()
	w.startManager("mgr-a")
	w.waitStatus("CONNECTED")
	w.fake.DropConnections(w.appID)
	waitFor(t, "second handshake", func() bool { return w.fake.TotalConnects(w.appID) == 2 && w.fake.ConnCount(w.appID) == 1 })
	app := w.waitStatus("CONNECTED")
	if app.LastError != "" {
		t.Fatalf("app = %+v", app)
	}
}

func TestOnlyLeaseHolderConnects(t *testing.T) {
	w := setup(t)
	w.bind()
	a, b := w.startManager("mgr-a"), w.startManager("mgr-b")
	w.waitStatus("CONNECTED")
	time.Sleep(1500 * time.Millisecond)
	if w.fake.ConnCount(w.appID) != 1 || w.fake.TotalConnects(w.appID) != 1 {
		t.Fatalf("connections = %d, connects = %d, want a single connection", w.fake.ConnCount(w.appID), w.fake.TotalConnects(w.appID))
	}
	var owner string
	w.scan(&owner, `SELECT lease_owner FROM openfox.im_connection_states WHERE organization_id = $1::uuid`, w.org)
	if owner != "mgr-a" && owner != "mgr-b" {
		t.Fatalf("lease owner = %q", owner)
	}
	// 持有者正常停止会释放租约，另一个实例随即接手。
	holder, other := a, b
	if owner == "mgr-b" {
		holder, other = b, a
	}
	holder.stop()
	waitFor(t, "takeover", func() bool {
		var now string
		w.scan(&now, `SELECT lease_owner FROM openfox.im_connection_states WHERE organization_id = $1::uuid`, w.org)
		return now == other.Instance && w.fake.ConnCount(w.appID) == 1
	})
	w.waitStatus("CONNECTED")
}

func TestExpiredLeaseIsTakenOver(t *testing.T) {
	w := setup(t)
	app := w.bind()
	// 模拟持有者崩溃：租约属于一个不存在的实例，约 2 秒后过期。
	w.exec(`UPDATE openfox.im_connection_states SET lease_owner = 'crashed-instance', lease_expires_at = clock_timestamp() + interval '2 seconds'
	         WHERE connection_id = $1::uuid`, app.ConnectionID)
	w.startManager("mgr-b")
	time.Sleep(900 * time.Millisecond)
	if w.fake.TotalConnects(w.appID) != 0 {
		t.Fatal("a live lease held by another instance must not be taken over")
	}
	waitFor(t, "takeover after the lease expires", func() bool { return w.fake.TotalConnects(w.appID) == 1 })
	w.waitStatus("CONNECTED")
	var owner string
	w.scan(&owner, `SELECT lease_owner FROM openfox.im_connection_states WHERE connection_id = $1::uuid`, app.ConnectionID)
	if owner != "mgr-b" {
		t.Fatalf("lease owner = %q", owner)
	}
}

func TestHandshakeRejectionNeedsAttention(t *testing.T) {
	w := setup(t)
	w.bind()
	w.fake.SetHandshakeStatus(514, 1000040341)
	w.startManager("mgr-a")
	app := w.waitStatus("NEEDS_ATTENTION")
	if app.StatusReason != "credentials_invalid" || app.LastError == "" {
		t.Fatalf("app = %+v", app)
	}
	time.Sleep(1200 * time.Millisecond)
	if w.fake.TotalConnects(w.appID) != 0 {
		t.Fatal("a rejected app must not be retried")
	}
	var state string
	w.scan(&state, `SELECT state FROM openfox.im_connection_states WHERE organization_id = $1::uuid`, w.org)
	if state != "FAILED" {
		t.Fatalf("state = %s", state)
	}
	// 连接数超限：重新验证恢复后，下一次握手被 514 + 1000040350 拒绝。
	w.fake.SetHandshakeStatus(0, 0)
	w.owner.json(http.StatusOK, http.MethodPost, w.appPath()+"/verify", nil, nil)
	w.waitStatus("CONNECTED")
	w.fake.SetHandshakeStatus(514, 1000040350)
	w.fake.DropConnections(w.appID)
	waitFor(t, "connection limit", func() bool {
		v := w.view(w.owner)
		return v.App != nil && v.App.Status == "NEEDS_ATTENTION" && v.App.StatusReason == "connection_limit"
	})
}

func TestEndpointFailureRetries(t *testing.T) {
	w := setup(t)
	w.bind()
	w.fake.SetEndpointCode(1000040343)
	w.startManager("mgr-a")
	waitFor(t, "reconnecting state", func() bool {
		return w.count(`SELECT count(*) FROM openfox.im_connection_states WHERE organization_id = $1::uuid AND state = 'RECONNECTING' AND failure_count >= 1 AND last_error <> ''`, w.org) == 1
	})
	if v := w.view(w.owner); v.App.Status != "PENDING" || v.App.LastError == "" {
		t.Fatalf("app = %+v", v.App)
	}
	w.fake.SetEndpointCode(0)
	app := w.waitStatus("CONNECTED")
	if app.LastError != "" {
		t.Fatalf("app = %+v", app)
	}
	// 凭证被飞书拒绝（端点返回其他业务码）不再重试。
	w.fake.SetEndpointCode(1000040345)
	w.fake.DropConnections(w.appID)
	if app = w.waitStatus("NEEDS_ATTENTION"); app.StatusReason != "credentials_invalid" {
		t.Fatalf("app = %+v", app)
	}
}
