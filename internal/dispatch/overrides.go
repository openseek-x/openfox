package dispatch

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"openfox/internal/runtimepolicy"
)

// runSnapshot 是 Run 实际采用的 Runtime / 模型 / 权限模式，以及它所属配置版本里的 Runtime。
type runSnapshot struct {
	runtimepolicy.Config
	// CollaborationMode 是配置版本里的协作模式（没有对话覆盖），已把历史值 FOLLOW_AGENT 规范化为 FOLLOW_RUNTIME。
	CollaborationMode string
	// ConfigRuntime 是 Run 固定的配置版本里的 Runtime；与 Runtime 不同说明对话覆盖了 Runtime。
	ConfigRuntime string
}

// loadRunSnapshot 读取 Run 创建时写入的快照；快照列为空（本迁移之前创建的旧 Run）时回退到配置版本。
// Context 与派发前核对共用这一处，保证设备执行的就是派发时核对过的配置。
func loadRunSnapshot(ctx context.Context, tx pgx.Tx, run dispatchRun) (runSnapshot, error) {
	var snapshot runSnapshot
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(r.runtime, c.runtime), COALESCE(r.model, c.model),
		       COALESCE(r.approval_mode, c.approval_mode), COALESCE(r.reasoning_effort, 'DEFAULT'), c.runtime, c.collaboration_mode
		  FROM openfox.runs r
		  JOIN openfox.agent_config_versions c
		    ON c.organization_id = r.organization_id AND c.agent_id = r.agent_id AND c.config_version = r.config_version
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.id = $1::uuid`, run.ID).
		Scan(&snapshot.Runtime, &snapshot.Model, &snapshot.ApprovalMode, &snapshot.ReasoningEffort, &snapshot.ConfigRuntime, &snapshot.CollaborationMode)
	snapshot.CollaborationMode = runtimepolicy.NormalizeCollaborationMode(snapshot.CollaborationMode)
	return snapshot, err
}

type capabilityVerdict int

const (
	capabilityOK capabilityVerdict = iota
	// capabilityWait：Run 保持 QUEUED，等待 Runtime 变为可运行（等待原因 RUNTIME_UNAVAILABLE）。
	capabilityWait
	// capabilityFail：设备明确不支持要求的权限档位或协作模式，Run 失败，不执行。
	capabilityFail
)

type capabilityCheck struct {
	verdict capabilityVerdict
	// reason 写入 runs.reason（面向运维），message 写入对话的系统消息（面向用户）。
	reason  string
	message string
}

// checkRunCapability 在派发（创建 offer）前，按设备最近一次心跳核对 Run 的 Runtime 与权限模式：
//   - 对话把 Runtime 覆盖成与配置版本不同的值，而该 Runtime 在心跳里不存在或 runnable=false：等待；
//   - 权限模式不是 FOLLOW_RUNTIME 而该 Runtime 在心跳里不存在：等待（可能是后台服务刚启动、尚未上报）；
//   - 权限模式不是 FOLLOW_RUNTIME 而心跳的 approval_modes 不含该档位（旧版后台服务没有该字段时一律不含）：失败。
//   - 协作模式不是 FOLLOW_RUNTIME 时同理——该 Runtime 在心跳里不存在则等待；心跳的 collaboration_modes
//     不含该模式（旧版后台服务没有该字段时一律不含）则失败。协作模式取自配置版本，没有对话覆盖。
//
// 选择「失败」而不是「等待」：设备不支持是长期状态而不是瞬时状态，一直排队会让用户误以为只是繁忙；
// 失败信息直接说明原因，用户可以改回「跟随 Runtime」或升级电脑上的 OpenFox 后重试。
// 无论哪种，都不会退回到更宽的默认行为去执行。
func checkRunCapability(ctx context.Context, tx pgx.Tx, run dispatchRun) (capabilityCheck, error) {
	snapshot, err := loadRunSnapshot(ctx, tx, run)
	if err != nil {
		return capabilityCheck{}, err
	}
	overridden := snapshot.Runtime != snapshot.ConfigRuntime
	approvalRestricted := snapshot.ApprovalMode != "" && snapshot.ApprovalMode != runtimepolicy.FollowRuntime
	reasoningRestricted := snapshot.ReasoningEffort != "" && snapshot.ReasoningEffort != runtimepolicy.ReasoningDefault
	collaborationRestricted := snapshot.CollaborationMode != runtimepolicy.CollaborationFollowRuntime
	restricted := approvalRestricted || reasoningRestricted || collaborationRestricted
	if !overridden && !restricted {
		return capabilityCheck{}, nil
	}
	var raw json.RawMessage
	err = tx.QueryRow(ctx, `SELECT capabilities FROM openfox.physical_devices WHERE id = $1::uuid`, run.ComputerID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return capabilityCheck{verdict: capabilityWait}, nil
	}
	if err != nil {
		return capabilityCheck{}, err
	}
	capability, found := runtimepolicy.Find(runtimepolicy.ParseCapabilities(raw), snapshot.Runtime)
	if (overridden && (!found || !capability.Runnable)) || (!found && restricted) {
		return capabilityCheck{verdict: capabilityWait}, nil
	}
	if approvalRestricted && !capability.SupportsApprovalMode(snapshot.ApprovalMode) {
		return capabilityCheck{
			verdict: capabilityFail,
			reason:  "approval mode " + snapshot.ApprovalMode + " is not supported by runtime " + snapshot.Runtime + " on this computer",
			message: "The run was not started: this computer's " + snapshot.Runtime + " runtime does not support the " +
				snapshot.ApprovalMode + " permission mode (the OpenFox client on the computer may be outdated). " +
				"Switch the conversation back to FOLLOW_RUNTIME or choose another mode, then send again.",
		}, nil
	}
	if reasoningRestricted && !capability.SupportsModelReasoningEffort(snapshot.Model, snapshot.ReasoningEffort) {
		return capabilityCheck{
			verdict: capabilityFail,
			reason:  "reasoning effort " + snapshot.ReasoningEffort + " is not supported by runtime " + snapshot.Runtime + " on this computer",
			message: "The run was not started: this computer's " + snapshot.Runtime + " runtime does not support the " +
				snapshot.ReasoningEffort + " reasoning depth. Update OpenFox on the computer or choose another depth, then send again.",
		}, nil
	}
	if collaborationRestricted && !capability.SupportsCollaborationMode(snapshot.CollaborationMode) {
		return capabilityCheck{
			verdict: capabilityFail,
			reason:  "collaboration mode " + snapshot.CollaborationMode + " is not supported by runtime " + snapshot.Runtime + " on this computer",
			message: CollaborationModeUnsupportedMessage(snapshot.CollaborationMode),
		}, nil
	}
	return capabilityCheck{}, nil
}

// CollaborationModeUnsupportedMessage 是 Agent 协作模式不被设备上的 Runtime 支持时写给用户的失败说明。
func CollaborationModeUnsupportedMessage(mode string) string {
	return "The Agent collaboration mode " + mode + " is not supported by this Runtime; " +
		"update OpenFox on the computer or choose another mode."
}
