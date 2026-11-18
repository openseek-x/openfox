package stage8d

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/dispatch"
	"openfox/internal/mcpconnect"
	"openfox/internal/secrets"
)

func (h *harness) testMCPContext(t *testing.T) {
	h.setCapabilities(fullCapabilities)
	h.setAgentConfig("codex", "gpt-5.1", "FOLLOW_RUNTIME")
	h.exec(`UPDATE openfox.physical_devices SET owner_user_id=$2,capabilities=capabilities||'{"agent_mcp":true}'::jsonb WHERE id=$1`, device1, alice)
	box, e := secrets.New(make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	local := mcpconnect.Config{Name: "owner-tools", Transport: "stdio", Command: "server", Args: []string{"local-arg"}, Scope: mcpconnect.DeviceOwner, AuthMode: "none"}
	pub := mcpconnect.Config{Name: "shared", Transport: "http", URL: "https://mcp.example.test", Scope: mcpconnect.Public, AuthMode: "api_key", AuthHeader: "X-API-Key", Credential: "lease-bound"}
	save := func(c mcpconnect.Config) string {
		id := newID(t)
		sealed, e := mcpconnect.Seal(box, orgA, agentA, id, c)
		if e != nil {
			t.Fatal(e)
		}
		var d, u, rt any
		status := "connected"
		if c.Scope == mcpconnect.DeviceOwner {
			d, u, rt = device1, alice, "codex"
			status = "device"
		}
		h.exec(`INSERT INTO openfox.agent_mcp_servers(organization_id,agent_id,id,name,scope,config,status,secret_ciphertext,secret_nonce,secret_key_id,device_id,owner_user_id,runtime)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, orgA, agentA, id, c.Name, c.Scope, c.Redacted(), status, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, d, u, rt)
		return id
	}
	localID, pubID := save(local), save(pub)
	conv := h.newConversation(alice)
	run := h.sendAndDispatch(alice, conv, "owner input")
	lease := h.claim(run)
	contextOf := func(tenant, device string, support bool) (dispatch.RunContext, error) {
		var c dispatch.RunContext
		var out error
		h.inDevice(tenant, func(ctx context.Context, tx pgx.Tx) error {
			c, out = h.store.ContextForDevice(ctx, tx, tenant, device, run, lease.Generation, box, support)
			return nil
		})
		return c, out
	}
	if _, e := contextOf(orgA, device1, false); !errors.Is(e, dispatch.ErrMCPUnsupported) {
		t.Fatalf("legacy requester: %v", e)
	}
	for _, test := range []struct{ org, device string }{{orgB, device1}, {orgA, "00000000-0000-0000-0000-000000000022"}} {
		if c, e := contextOf(test.org, test.device, true); e == nil || len(c.MCPServers) != 0 {
			t.Fatalf("wrong tenant/device: %v", e)
		}
	}
	c, e := contextOf(orgA, device1, true)
	if e != nil || len(c.MCPServers) != 2 || c.MCPActorUserID != alice || c.MCPDeviceOwnerID != alice || c.MCPServers[0].Args[0] != "local-arg" || c.MCPServers[1].Headers["X-API-Key"] != "lease-bound" {
		t.Fatalf("wrong bound context: servers=%d error=%v", len(c.MCPServers), e)
	}
	h.exec(`UPDATE openfox.physical_devices SET owner_user_id=$1 WHERE id=$2`, carol, device1)
	c, e = contextOf(orgA, device1, true)
	if e != nil || len(c.MCPServers) != 1 || c.MCPDeviceOwnerID != "" {
		t.Fatalf("ownership change: %v", e)
	}
	h.exec(`UPDATE openfox.physical_devices SET owner_user_id=$1 WHERE id=$2`, alice, device1)
	h.exec(`UPDATE openfox.messages SET authenticated_user_input=false WHERE id IN (SELECT message_id FROM openfox.conversation_inputs WHERE organization_id=$1 AND run_id=$2)`, orgA, run)
	c, e = contextOf(orgA, device1, true)
	if e != nil || len(c.MCPServers) != 1 || c.MCPServers[0].Scope != mcpconnect.Public {
		t.Fatalf("worker posting on owner's behalf activated personal tools: %v", e)
	}
	h.exec(`UPDATE openfox.messages SET authenticated_user_input=true WHERE id IN (SELECT message_id FROM openfox.conversation_inputs WHERE organization_id=$1 AND run_id=$2)`, orgA, run)
	h.exec(`INSERT INTO openfox.conversation_inputs(organization_id,conversation_id,body,status,run_id,assigned_at) VALUES($1,$2,'automated','ASSIGNED',$3,clock_timestamp())`, orgA, conv, run)
	c, e = contextOf(orgA, device1, true)
	if e != nil || len(c.MCPServers) != 1 || c.MCPServers[0].Name != pub.Name {
		t.Fatalf("mixed run used local: %v", e)
	}
	h.exec(`UPDATE openfox.agent_mcp_servers SET expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, pubID)
	c, e = contextOf(orgA, device1, true)
	if e != nil || len(c.MCPServers) != 0 {
		t.Fatalf("expired credential: %v", e)
	}
	h.exec(`DELETE FROM openfox.agent_mcp_servers WHERE id IN($1,$2)`, pubID, localID)
	h.finish(run, lease)
	if _, e = contextOf(orgA, device1, true); e == nil {
		t.Fatal("finished lease retrieved context")
	}
}
