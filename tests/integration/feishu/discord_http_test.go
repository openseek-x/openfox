package feishuint

import (
	"context"
	"net/http"
	"openfox/internal/integrations"
	"strings"
	"testing"
	"time"
)

func TestDiscordHTTPControlAndGateway(t *testing.T) {
	b, p, mgr, worker := newDiscordHTTPWorld(t)
	agent := b.createAgent(b.owner, b.foxSpace, "Discord HTTP Agent", "SHARED")
	base := b.path("/integrations/discord")
	input := map[string]any{"operation_key": b.newUUID(), "kind": "AGENT", "agent_id": agent, "version": 0, "token": "synthetic-http-token", "extra_prompt": ""}
	var prep integrations.DiscordPreparation
	b.owner.json(200, "POST", base+"/preparations", input, &prep)
	if string(prep.RemoteBotID) != p.bot || prep.Status != "PENDING_CONFIRM" {
		t.Fatalf("preparation %+v", prep)
	}
	// Another owner in a different organization cannot read or consume this proof.
	status, _ := b.ownerB.do("GET", "/v1/organizations/"+b.orgB+"/integrations/discord/preparations/"+prep.ID, nil)
	if status != 404 {
		t.Fatal("cross org preparation", status)
	}
	status, _ = b.member.do("POST", base+"/preparations/"+prep.ID+"/confirm", map[string]any{"version": prep.Version, "remote_bot_id": prep.RemoteBotID, "application_id": prep.ApplicationID})
	if status != 404 && status != 403 {
		t.Fatal("other owner confirm", status)
	}
	var bot integrations.DiscordBot
	b.owner.json(200, "POST", base+"/preparations/"+prep.ID+"/confirm", map[string]any{"version": prep.Version, "remote_bot_id": prep.RemoteBotID, "application_id": prep.ApplicationID}, &bot)
	// Same proof is idempotent for the same caller, never a second connection.
	var again integrations.DiscordBot
	b.owner.json(200, "POST", base+"/preparations/"+prep.ID+"/confirm", map[string]any{"version": prep.Version, "remote_bot_id": prep.RemoteBotID, "application_id": prep.ApplicationID}, &again)
	if again.ID != bot.ID {
		t.Fatal("duplicate connection")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Run(ctx) }()
	defer func() { cancel(); <-done }()
	waitDiscordHTTP(t, func() bool {
		var state string
		b.scan(&state, `SELECT state FROM openfox.im_connection_states WHERE organization_id=$1::uuid AND connection_id=$2::uuid`, b.org, bot.ConnectionID)
		return state == "CONNECTED"
	})
	var session integrations.DiscordBindingSession
	b.owner.json(200, "POST", base+"/bots/"+bot.ID+"/binding-sessions", map[string]any{}, &session)
	if session.Code == "" {
		t.Fatal("missing one-time code")
	}
	p.emit(session.Code, true)
	waitDiscordHTTP(t, func() bool {
		if e := worker.ProcessDiscordInbound(ctx, b.org); e != nil {
			t.Fatal(e)
		}
		var s integrations.DiscordBindingSession
		b.owner.json(200, "GET", base+"/binding-sessions/"+session.ID, nil, &s)
		if s.Code != "" {
			t.Fatal("poll leaked code")
		}
		session.Version = s.Version
		return s.Status == "CANDIDATE"
	})
	var bound integrations.DiscordIdentity
	b.owner.json(200, "POST", base+"/binding-sessions/"+session.ID+"/confirm", map[string]any{"version": session.Version}, &bound)
	b.owner.json(200, "GET", base+"/bots/"+bot.ID+"/guilds", nil, nil)
	b.owner.json(200, "GET", base+"/bots/"+bot.ID+"/guilds/5000001/channels", nil, nil)
	status, _ = b.owner.do("POST", base+"/bots/"+bot.ID+"/channels", map[string]any{"guild_id": "5000001", "channel_id": "7000002", "public_reply_consent": true, "history_consent": false})
	if status != 400 {
		t.Fatal("missing consent", status)
	}
	var channel integrations.DiscordChannel
	b.owner.json(200, "POST", base+"/bots/"+bot.ID+"/channels", map[string]any{"guild_id": "5000001", "channel_id": "7000002", "public_reply_consent": true, "history_consent": true}, &channel)
	message := p.emit("<@"+p.bot+"> browser HTTP original", false)
	waitDiscordHTTP(t, func() bool {
		var n int
		b.scan(&n, `SELECT count(*) FROM openfox.discord_message_receipts WHERE organization_id=$1::uuid AND remote_message_id=$2`, b.org, message)
		return n == 1
	})
	if e := worker.ProcessDiscordInbound(ctx, b.org); e != nil {
		t.Fatal(e)
	}
	var materialized int
	b.scan(&materialized, `SELECT count(*) FROM openfox.discord_inputs WHERE organization_id=$1::uuid AND source_kind='REMOTE'`, b.org)
	if materialized != 1 {
		t.Fatal("materialized", materialized)
	}
	b.owner.json(200, "GET", base+"/bots/"+bot.ID+"/channels", nil, nil)
	b.owner.json(200, "GET", base+"/bots/"+bot.ID+"/deliveries", nil, nil)
	b.owner.json(204, "DELETE", base+"/bots/"+bot.ID+"/channels/"+channel.ID, map[string]any{"version": channel.Version}, nil)
	b.owner.json(204, "DELETE", base+"/personal/"+bound.ID, map[string]any{"version": bound.Version}, nil)
	// Cookie authentication without CSRF cannot mutate connection state.
	req, _ := http.NewRequest("POST", b.server.URL+"/api"+base+"/bots/"+bot.ID+"/disconnect", strings.NewReader(`{"version":1}`))
	req.Header.Set("Content-Type", "application/json")
	resp, e := b.owner.http.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("csrf", resp.StatusCode)
	}
	b.owner.json(204, "POST", base+"/bots/"+bot.ID+"/disconnect", map[string]any{"version": bot.Version}, nil)
	b.owner.json(200, "GET", base+"/bots/"+bot.ID, nil, &again)
	if again.State != "DISCONNECTED" {
		t.Fatal(again.State)
	}
	// Browsers retain the real organization SSE stream. Fixture shutdown must
	// close it before httptest waits, rather than hanging at test cleanup.
	stream, err := b.owner.http.Get(b.server.URL + "/api" + b.path("/events"))
	if err != nil {
		t.Fatal(err)
	}
	if stream.StatusCode != 200 {
		t.Fatal("SSE status", stream.StatusCode)
	}
	closed := make(chan struct{})
	go func() { b.server.CloseClientConnections(); b.server.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE fixture cleanup blocked")
	}
	stream.Body.Close()
	b.log.mu.Lock()
	defer b.log.mu.Unlock()
	for _, body := range b.log.bodies {
		if strings.Contains(body, "synthetic-http-token") {
			t.Fatal("Token in HTTP response")
		}
	}
}
func waitDiscordHTTP(t *testing.T, check func() bool) {
	t.Helper()
	until := time.Now().Add(8 * time.Second)
	for time.Now().Before(until) {
		if check() {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatal("controlled Gateway state deadline")
}
