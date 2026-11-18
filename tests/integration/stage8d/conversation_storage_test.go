package stage8d

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"openfox/internal/devices"
	"openfox/internal/tenancy"
)

// 阶段 11h：按会话清理必须满足设备能力 + 设备所有者 + 会话参与者；能力不足的旧设备返回 ErrCapabilityUnsupported（API 为 409）。
func (h *harness) testConversationStorageCleanup(t *testing.T) {
	h.exec(`UPDATE openfox.physical_devices SET owner_user_id=$1::uuid WHERE id=$2::uuid`, alice, device1)
	h.exec(`DELETE FROM openfox.device_commands WHERE kind='storage_cleanup'`)
	user := func(actor string) tenancy.OrganizationSelection {
		return tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: actor, AuthorizationVersion: h.authVersion(orgA, actor)}
	}
	store := devices.PostgresStore{Pool: h.pool}
	conversationID := h.newConversation(alice)
	params := func(conversation string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"action": "preview", "days": 0, "categories": []string{"conversation"}, "conversation_id": conversation})
		return raw
	}
	openCommands := func() (count int) {
		h.queryRow(`SELECT count(*) FROM openfox.device_commands WHERE kind='storage_cleanup' AND status IN ('PENDING','DELIVERED')`, nil, &count)
		return
	}

	// 旧 daemon：心跳没有声明能力，中心不下发，也不留下命令行。
	h.setCapabilities(fullCapabilities)
	if _, _, err := store.CreateCommand(h.ctx, user(alice), device1, devices.CommandStorageCleanup, params(conversationID)); !errors.Is(err, devices.ErrCapabilityUnsupported) {
		t.Fatalf("old daemon accepted conversation filter: %v", err)
	}
	if openCommands() != 0 {
		t.Fatal("command stored for unsupported device")
	}
	// 不带会话的既有请求不受能力影响。
	global, _ := json.Marshal(map[string]any{"action": "preview", "days": 0, "categories": []string{"task"}})
	if _, created, err := store.CreateCommand(h.ctx, user(alice), device1, devices.CommandStorageCleanup, global); err != nil || !created {
		t.Fatalf("existing org-wide request regressed: %v", err)
	}
	h.exec(`DELETE FROM openfox.device_commands WHERE kind='storage_cleanup'`)

	// 新 daemon：声明能力后，所有者 + 参与者可以下发，参数原样保存会话 ID。
	h.setCapabilities(strings.Replace(fullCapabilities, "{", `{"storage.conversation_filter":true,`, 1))
	command, created, err := store.CreateCommand(h.ctx, user(alice), device1, devices.CommandStorageCleanup, params(conversationID))
	if err != nil || !created || !strings.Contains(string(command.Params), conversationID) {
		t.Fatalf("supported device: %+v %v %v", command, created, err)
	}
	h.exec(`DELETE FROM openfox.device_commands WHERE kind='storage_cleanup'`)

	// 设备所有者不是会话参与者（他人的私有会话）：拒绝。
	private := h.newConversation(carol)
	if _, _, err = store.CreateCommand(h.ctx, user(alice), device1, devices.CommandStorageCleanup, params(private)); err == nil {
		t.Fatal("device owner cleaned a conversation they do not participate in")
	}
	// 会话参与者不是设备所有者：拒绝。
	if _, _, err = store.CreateCommand(h.ctx, user(carol), device1, devices.CommandStorageCleanup, params(private)); err == nil {
		t.Fatal("non-owner cleaned device storage")
	}
	// 不存在的会话 ID：拒绝。
	if _, _, err = store.CreateCommand(h.ctx, user(alice), device1, devices.CommandStorageCleanup, params(newID(t))); err == nil {
		t.Fatal("unknown conversation accepted")
	}
	// 组织不符：orgB 下看不到 orgA 的会话。
	if _, _, err = store.CreateCommand(h.ctx, tenancy.OrganizationSelection{OrganizationID: orgB, ActorID: alice, AuthorizationVersion: h.authVersion(orgB, alice)}, device1, devices.CommandStorageCleanup, params(conversationID)); err == nil {
		t.Fatal("cross-organization conversation accepted")
	}
	if openCommands() != 0 {
		t.Fatal("rejected requests left commands behind")
	}
}
