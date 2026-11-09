import type { CollaborationMode, Device } from "@openfox/api";
import type { MessageKey } from "@openfox/i18n";

// Agent 协作模式：跟随 Runtime / 默认 / 计划模式。

export const COLLABORATION_MODES: readonly CollaborationMode[] = [
  "FOLLOW_RUNTIME",
  "DEFAULT",
  "PLAN",
];

export const collaborationNameKey = (mode: CollaborationMode): MessageKey =>
  `agents.sections.runtime.collaborationModes.${mode}`;
export const collaborationDescKey = (mode: CollaborationMode): MessageKey =>
  `agents.sections.runtime.collaborationDescs.${mode}`;

/**
 * 把读到的协作模式规范化：空串与历史值 FOLLOW_AGENT 等于 FOLLOW_RUNTIME；
 * 三个取值之外的值返回 null（无法识别，派发时会失败）。
 */
export function collaborationModeOf(
  value: string | null | undefined,
): CollaborationMode | null {
  if (!value || value === "FOLLOW_AGENT") return "FOLLOW_RUNTIME";
  return (COLLABORATION_MODES as readonly string[]).includes(value)
    ? (value as CollaborationMode)
    : null;
}

/**
 * 电脑上这个 Runtime 支持的协作模式；电脑或 Runtime 不在列表里（拿不到心跳）时返回 undefined，调用方不做禁用。
 * 服务端缺省 collaboration_modes 时按只有 FOLLOW_RUNTIME 处理（与派发时的核对一致）。
 */
export function supportedCollaborationModes(
  devices: readonly Device[] | undefined,
  computerId: string,
  runtime: string,
): readonly CollaborationMode[] | undefined {
  const entry = devices
    ?.find((item) => item.id === computerId)
    ?.runtimes.find((item) => item.provider === runtime);
  if (!entry) return undefined;
  return entry.collaboration_modes ?? ["FOLLOW_RUNTIME"];
}
