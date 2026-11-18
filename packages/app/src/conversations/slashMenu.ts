import { useMemo } from "react";

import type { QuickCommand, Skill, SpaceAgent } from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";

import { isSupportedRuntime } from "../agents/model";
import { useDevices } from "../agents/queries";
import { useAgentSkills } from "../skills/queries";
import {
  useQuickCommands,
  menuCommands,
} from "../tasks/detail/quickCommands/queries";
import { computerNoticeState } from "./ComputerNotice";

// 阶段 12f · E-05：对话输入框的 `/` 菜单。列出 Agent 绑定的技能与当前可用的快捷指令，
// 选择后只把文字插入输入框（不自动发送）；电脑离线或 Runtime 不支持时置灰并给出原因。

export interface SlashEntry {
  id: string;
  group: "skill" | "command";
  label: string;
  description?: string;
  /** 选中后插入的文字 */
  insert: string;
  /** 置灰原因；为空表示可用 */
  disabledReason?: string;
}

/** 技能依赖 Agent 的电脑与 Runtime：没绑电脑、电脑离线或 Runtime 不在支持列表里时不可用。 */
export function skillBlockReason(
  t: Translate,
  agent: Pick<SpaceAgent, "online" | "computer_name" | "config">,
  devices: Parameters<typeof computerNoticeState>[1],
): string | undefined {
  const state = computerNoticeState(agent, devices);
  if (state.kind === "noComputer") return t("chat.slash.noComputer");
  if (state.kind === "offline") return t("chat.slash.offline");
  const runtime = agent.config?.runtime;
  if (!runtime || !isSupportedRuntime(runtime))
    return t("chat.slash.runtimeUnsupported");
  return undefined;
}

/** 纯函数：组装菜单条目（技能在前，快捷指令在后；快捷指令保持服务端的收藏置顶顺序）。 */
export function buildSlashEntries(input: {
  t: Translate;
  agentId: string;
  skills: Skill[];
  commands: QuickCommand[];
  blockReason?: string;
}): SlashEntry[] {
  const { t, agentId, skills, commands, blockReason } = input;
  const skillEntries = skills.map<SlashEntry>((skill) => ({
    id: `skill:${skill.id}`,
    group: "skill",
    label: skill.name,
    description: skill.description,
    insert: t("chat.slash.useSkill", { name: skill.name }),
    disabledReason: blockReason,
  }));
  const commandEntries = commands
    .filter(
      (command) =>
        command.target.kind === "AGENT" && command.target.id === agentId,
    )
    .map<SlashEntry>((command) => ({
      id: `command:${command.id}`,
      group: "command",
      label: command.name,
      description: command.prompt.trim() || undefined,
      insert: command.prompt.trim() || t("tasks.quickCommands.defaultPrompt"),
      disabledReason: command.target.available
        ? blockReason
        : t(
            `tasks.quickCommands.unavailable.${command.target.unavailable_reason ?? "NOT_VISIBLE"}`,
          ),
    }));
  return [...skillEntries, ...commandEntries];
}

export function filterSlashEntries(entries: SlashEntry[], query: string) {
  const needle = query.trim().toLowerCase();
  if (!needle) return entries;
  return entries.filter((entry) =>
    `${entry.label} ${entry.description ?? ""}`.toLowerCase().includes(needle),
  );
}

/** 菜单数据：只在输入框打开斜杠菜单时才请求（enabled）。 */
export function useSlashEntries(
  orgId: string | undefined,
  agent: SpaceAgent | undefined,
  enabled: boolean,
): SlashEntry[] {
  const t = useT();
  const active = enabled && Boolean(orgId && agent);
  const skills = useAgentSkills(
    orgId ?? "",
    active ? (agent?.space_id ?? "") : "",
    agent?.id ?? "",
  );
  const commands = useQuickCommands(orgId ?? "", agent?.space_id ?? "", active);
  const devices = useDevices(
    orgId ?? "",
    active && Boolean(agent?.config?.computer_id),
  );
  return useMemo(() => {
    if (!agent) return [];
    return buildSlashEntries({
      t,
      agentId: agent.id,
      skills: skills.data ?? [],
      commands: menuCommands(commands.data),
      blockReason: skillBlockReason(t, agent, devices.data),
    });
  }, [t, agent, skills.data, commands.data, devices.data]);
}
