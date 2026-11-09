import type {
  ApprovalMode,
  ConversationOverrideOptions,
  ConversationOverrides,
  Device,
  ReasoningEffort,
} from "@openfox/api";
import type { MessageKey } from "@openfox/i18n";

// 对话输入框里 Runtime / 模型 / 权限模式覆盖的纯函数。
// 有效配置的规则与后端 runtimepolicy.Effective 一致，只用于「还没有会话」时预览所选值；
// 已有会话的有效值一律以服务端返回的 summary.effective 为准。

/** 四档权限，按严格程度从低到高排列（与后端 runtimepolicy.ApprovalModes 一致） */
export const APPROVAL_MODES: readonly ApprovalMode[] = [
  "FOLLOW_RUNTIME",
  "READ_ONLY",
  "WORKSPACE_WRITE",
  "FULL_ACCESS",
];

export const approvalNameKey = (mode: ApprovalMode): MessageKey =>
  `chat.overrides.approvalModes.${mode}`;
export const approvalDescKey = (mode: ApprovalMode): MessageKey =>
  `chat.overrides.approvalDescs.${mode}`;

export const REASONING_EFFORTS: readonly ReasoningEffort[] = [
  "DEFAULT",
  "LOW",
  "MEDIUM",
  "HIGH",
  "EXTRA_HIGH",
  "MAX",
  "ULTRA",
];

export const reasoningNameKey = (effort: ReasoningEffort): MessageKey =>
  `chat.overrides.reasoningLevels.${effort}`;
export const reasoningDescKey = (effort: ReasoningEffort): MessageKey =>
  `chat.overrides.reasoningDescs.${effort}`;

/** 一组完整的取值（Agent 默认或有效值） */
export interface OverrideValues {
  runtime: string;
  model: string;
  approval_mode: ApprovalMode;
  reasoning_effort?: ReasoningEffort;
}

/** 覆盖值：null = 沿用 Agent 默认 */
export type OverrideSelection = Pick<
  ConversationOverrides,
  "runtime" | "model" | "approval_mode" | "reasoning_effort"
>;

export type OverrideRuntime = NonNullable<ConversationOverrides["runtime"]>;

/** 一次修改：字段缺省 = 不改，null = 恢复 Agent 默认（与 PATCH 请求体一致） */
export interface OverridePatch {
  runtime?: OverrideRuntime | null;
  model?: string | null;
  approval_mode?: ApprovalMode | null;
  reasoning_effort?: ReasoningEffort | null;
}

export const NO_OVERRIDES: OverrideSelection = {
  runtime: null,
  model: null,
  approval_mode: null,
  reasoning_effort: null,
};

export const hasOverrides = (value: OverrideSelection) =>
  value.runtime !== null ||
  value.model !== null ||
  value.approval_mode !== null ||
  value.reasoning_effort != null;

/** 清除覆盖后模型会回到什么：只覆盖了不同的 Runtime 时是 default，否则是 Agent 配置的模型 */
export function agentDefaultModel(
  defaults: OverrideValues,
  selection: OverrideSelection,
): string {
  return selection.runtime && selection.runtime !== defaults.runtime
    ? "default"
    : defaults.model;
}

/** 与后端 runtimepolicy.Effective 相同的规则 */
export function effectiveOf(
  defaults: OverrideValues,
  selection: OverrideSelection,
): OverrideValues {
  return {
    runtime: selection.runtime ?? defaults.runtime,
    model: selection.model ?? agentDefaultModel(defaults, selection),
    approval_mode: selection.approval_mode ?? defaults.approval_mode,
    reasoning_effort:
      selection.reasoning_effort ?? defaults.reasoning_effort ?? "DEFAULT",
  };
}

/** 把覆盖选择应用到一次修改上（用于草稿；null 表示恢复默认，即从选择里去掉） */
export function applyPatch(
  selection: OverrideSelection,
  patch: OverridePatch,
): OverrideSelection {
  return {
    runtime: "runtime" in patch ? (patch.runtime ?? null) : selection.runtime,
    model: "model" in patch ? (patch.model ?? null) : selection.model,
    approval_mode:
      "approval_mode" in patch
        ? (patch.approval_mode ?? null)
        : selection.approval_mode,
    reasoning_effort:
      "reasoning_effort" in patch
        ? (patch.reasoning_effort ?? null)
        : (selection.reasoning_effort ?? null),
  };
}

/**
 * 菜单里选了某个值，换算成 PATCH 的字段：
 * - 选的就是 Agent 默认值：发 null（恢复默认），不留一个多余的覆盖；
 * - 换 Runtime 时若已有模型覆盖，一并清除：旧模型多半不属于新 Runtime，后端会整体拒绝；
 *   只覆盖 Runtime 时后端把模型视为 default，不需要前端再单独发一次模型请求。
 */
export function patchFor(
  field: keyof OverrideSelection,
  value: string | null,
  defaults: OverrideValues,
  selection: OverrideSelection,
): OverridePatch {
  if (field === "runtime") {
    const next =
      value === defaults.runtime ? null : (value as OverrideRuntime | null);
    const patch: OverridePatch = { runtime: next };
    if (selection.model !== null) patch.model = null;
    if (selection.reasoning_effort != null) patch.reasoning_effort = null;
    return patch;
  }
  if (field === "model") {
    const base = agentDefaultModel(defaults, selection);
    const patch: OverridePatch = { model: value === base ? null : value };
    if (selection.reasoning_effort != null) patch.reasoning_effort = null;
    return patch;
  }
  if (field === "reasoning_effort") {
    return {
      reasoning_effort:
        value === (defaults.reasoning_effort ?? "DEFAULT")
          ? null
          : (value as ReasoningEffort | null),
    };
  }
  return {
    approval_mode:
      value === defaults.approval_mode ? null : (value as ApprovalMode | null),
  };
}

/** 是否与当前覆盖选择相同（没有变化时不发请求） */
export function isNoop(
  patch: OverridePatch,
  selection: OverrideSelection,
): boolean {
  const next = applyPatch(selection, patch);
  return (
    next.runtime === selection.runtime &&
    next.model === selection.model &&
    next.approval_mode === selection.approval_mode &&
    (next.reasoning_effort ?? null) === (selection.reasoning_effort ?? null)
  );
}

/** PATCH 请求体里只带发生变化的字段 */
export function requestBody(
  patch: OverridePatch,
  expectedVersion: number,
): {
  runtime?: OverrideRuntime | null;
  model?: string | null;
  approval_mode?: ApprovalMode | null;
  reasoning_effort?: ReasoningEffort | null;
  expected_version: number;
} {
  const body: ReturnType<typeof requestBody> = {
    expected_version: expectedVersion,
  };
  if ("runtime" in patch) body.runtime = patch.runtime ?? null;
  if ("model" in patch) body.model = patch.model ?? null;
  if ("approval_mode" in patch)
    body.approval_mode = patch.approval_mode ?? null;
  if ("reasoning_effort" in patch)
    body.reasoning_effort = patch.reasoning_effort ?? null;
  return body;
}

/** 草稿里已选的值，换算成创建会话后的第一次 PATCH 请求体（没有选择时为 null） */
export function draftRequestBody(
  selection: OverrideSelection,
  expectedVersion: number,
) {
  if (!hasOverrides(selection)) return null;
  const patch: OverridePatch = {};
  if (selection.runtime !== null) patch.runtime = selection.runtime;
  if (selection.model !== null) patch.model = selection.model;
  if (selection.approval_mode !== null)
    patch.approval_mode = selection.approval_mode;
  if (selection.reasoning_effort != null)
    patch.reasoning_effort = selection.reasoning_effort;
  return requestBody(patch, expectedVersion);
}

/**
 * 还没有会话时没有可选项接口可调（它需要会话 id），改用「电脑」数据里同一份心跳的 Runtime 列表。
 * 形状与接口一致，菜单不用区分来源。
 */
export function optionsFromDevice(device: Device): ConversationOverrideOptions {
  return {
    computer_online: device.online,
    runtimes: device.runtimes.map((runtime) => ({
      provider: runtime.provider,
      runnable: runtime.runnable,
      models: runtime.models,
      approval_modes: runtime.approval_modes,
      reasoning_efforts: runtime.reasoning_efforts ?? ["DEFAULT"],
    })),
  };
}
