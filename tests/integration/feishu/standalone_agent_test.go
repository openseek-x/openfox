package feishuint

import (
	"net/http"
	"testing"
)

func TestStandaloneAgentRegistrationAndIdentity(t *testing.T) {
	for _, provider := range []string{"feishu", "lark"} {
		t.Run(provider, func(t *testing.T) {
			b := setupBotsFor(t, provider, false)
			agent := b.createAgent(b.admin, b.foxSpace, "Standalone", "PRIVATE")
			input := map[string]any{"purpose": "agent", "agent_id": agent}
			b.guest.expect(http.StatusForbidden, "", "POST", b.botsPath("/registrations"), input)
			b.member.expect(http.StatusNotFound, "", "POST", b.botsPath("/registrations"), input)
			bot := b.makeAgentBot(b.admin, agent)
			if b.bots(b.admin).AppConnected || b.view(b.admin).Personal != nil {
				t.Fatal("standalone bot must not create organization or personal bindings")
			}
			push := func(union, chat string) inboundRow {
				id, err := b.fake.PushMessage(bot.AppID, chat, "p2p", union, "standalone message", false)
				if err != nil {
					t.Fatal(err)
				}
				return b.waitInbound(bot.ID, id)
			}
			if row := push("on_owner", "standalone-owner"); row.Status != "DONE" {
				t.Fatalf("scanner=%+v", row)
			}
			conv := b.conversationFor(bot.ID, "standalone-owner")
			if conv.Owner != b.adminID || conv.Visibility != "PRIVATE" || b.messages(conv.ID)[0].AuthorUserID != b.adminID {
				t.Fatalf("wrong scanner identity: %+v", conv)
			}
			if row := push("on_stranger", "standalone-stranger"); row.Status != "REJECTED" || row.Result != "unbound" {
				t.Fatalf("unbound sender=%+v", row)
			}
			// Membership is checked for every inbound message, not frozen at scan time.
			b.exec(`UPDATE openfox.organization_members SET status = 'REVOKED', authorization_version = authorization_version + 1 WHERE organization_id = $1::uuid AND user_id = $2::uuid`, b.org, b.adminID)
			if row := push("on_owner", "standalone-removed"); row.Status != "REJECTED" || row.Result != "unbound" {
				t.Fatalf("removed scanner=%+v", row)
			}
		})
	}
}

func TestRegistrationRejectsOtherTenantBrand(t *testing.T) {
	for _, provider := range []string{"feishu", "lark"} {
		t.Run(provider, func(t *testing.T) {
			b := setupBotsFor(t, provider, false)
			other := "lark"
			if provider == "lark" {
				other = "feishu"
			}
			b.fake.SetRegistrationBrand(other)
			agent := b.createAgent(b.owner, b.foxSpace, "Wrong brand", "PRIVATE")
			var reg regView
			b.owner.json(201, "POST", b.botsPath("/registrations"), map[string]any{"purpose": "agent", "agent_id": agent}, &reg)
			done := b.complete(b.owner, reg)
			if done.Status != "FAILED" || done.ErrorCode != "rejected" || len(b.bots(b.owner).Agents) != 0 {
				t.Fatalf("foreign brand accepted: %+v", done)
			}
		})
	}
}
