import { useState } from "react";
import { Link } from "react-router-dom";

import type { Device, SpaceAgent } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Icon } from "@openfox/ui";

import { canEditAgent } from "../agents/model";
import { useAgentSettingsParam } from "../agents/params";
import { useAgent, useDevices, useMe, useOrgRole } from "../agents/queries";
import { ComputerDrawer } from "../computers/ComputersRail";

// Agent 的电脑离线或没有绑定电脑时，在对话输入框上方给一条非阻塞提示。
// 不影响发送：消息照常发出并排队。

export type ComputerNoticeState =
  | { kind: "none" }
  | { kind: "noComputer" }
  | { kind: "offline"; computerId: string; name: string | null };

/**
 * 下一条消息会用到的电脑是否离线。对话覆盖（8d）只能换同一台电脑上的 Runtime，
 * 所以按 Agent 当前配置的电脑判断。在线状态与电脑列表一致：优先取电脑列表里的 online，
 * 列表里看不到这台电脑时（无权查看、列表未返回）退回 Agent 详情的 online（同一套心跳判定）。
 */
export function computerNoticeState(
  agent: Pick<SpaceAgent, "online" | "computer_name" | "config">,
  devices: ReadonlyArray<Pick<Device, "id" | "name" | "online">> | undefined,
): ComputerNoticeState {
  const computerId = agent.config?.computer_id;
  if (!computerId) return { kind: "noComputer" };
  const device = devices?.find((item) => item.id === computerId);
  const online = device ? device.online : agent.online;
  if (online) return { kind: "none" };
  return {
    kind: "offline",
    computerId,
    name: device?.name ?? agent.computer_name ?? null,
  };
}

/**
 * 输入框上方的电脑提示。数据还在加载、拿不到 Agent 时不显示；
 * 电脑列表沿用现有查询键（侧栏电脑入口每 30 秒刷新同一份缓存），电脑上线后提示自然消失。
 * agent 已知时（小助手）直接传入，不再请求 Agent 详情。
 */
export function ComputerNotice({
  orgId,
  spaceId,
  agentId,
  agent: provided,
}: {
  orgId: string;
  spaceId: string | undefined;
  agentId: string;
  agent?: SpaceAgent;
}) {
  const t = useT();
  const query = useAgent(orgId, provided ? undefined : spaceId, agentId);
  const agent = provided ?? query.data;
  const devices = useDevices(orgId, Boolean(agent?.config?.computer_id));
  const me = useMe();
  const role = useOrgRole(orgId);
  const settings = useAgentSettingsParam();
  const [viewingComputer, setViewingComputer] = useState<string | null>(null);
  if (!agent || agent.id !== agentId) return null;
  if (agent.config?.computer_id && devices.isLoading) return null;
  const state = computerNoticeState(agent, devices.data);
  if (agent.builtin_kind === "ASSISTANT" && state.kind !== "noComputer") {
    return (
      <>
        {state.kind === "offline" && (
          <div
            role="status"
            className="mx-auto mb-3 flex w-fit max-w-full items-center gap-1.5 rounded-xl border border-line-default/70 bg-surface-card px-3 py-2 text-body text-fg-default/80"
          >
            <Icon
              name="openfox-alert-circle"
              size={16}
              className="text-warning"
            />
            <span>{t("chat.computerNotice.assistantOffline")}</span>
            <button
              type="button"
              aria-label={
                state.name
                  ? t("chat.computerNotice.viewDetailsOf", { name: state.name })
                  : undefined
              }
              className="shrink-0 underline decoration-current/40 underline-offset-4 hover:decoration-current"
              onClick={() => setViewingComputer(state.computerId)}
            >
              {t("chat.computerNotice.viewDetails")}
            </button>
          </div>
        )}
        {viewingComputer && (
          <ComputerDrawer
            orgId={orgId}
            deviceId={viewingComputer}
            onClose={() => setViewingComputer(null)}
          />
        )}
      </>
    );
  }
  if (state.kind === "none") return null;
  // 内置小助手只能由服务端维护配置，不提供「更换电脑」
  const canChange =
    !agent.builtin_kind &&
    me.isSuccess &&
    canEditAgent(agent, me.data?.id, role);
  const changeComputer = () => {
    settings.open(agent.id, agent.space_id);
    // 抽屉渲染后滚到「电脑 & Runtime」分区（只滚动定位，不改抽屉自身的展开状态）
    requestAnimationFrame(() =>
      requestAnimationFrame(() =>
        document
          .getElementById("agent-section-runtime")
          ?.scrollIntoView?.({ block: "start" }),
      ),
    );
  };
  return (
    <Callout
      tone="warning"
      className="mb-2 py-2 text-body-sm"
      action={
        <span className="flex shrink-0 flex-wrap items-center gap-2">
          {state.kind === "offline" && (
            <Link
              to={`/o/${orgId}/settings/computers?computer=${encodeURIComponent(state.computerId)}`}
            >
              <Button size="28" variant="outline">
                {t("chat.computerNotice.viewComputer")}
              </Button>
            </Link>
          )}
          {canChange && (
            <Button size="28" variant="outline" onClick={changeComputer}>
              {t("chat.computerNotice.changeComputer")}
            </Button>
          )}
        </span>
      }
    >
      {state.kind === "offline"
        ? t("chat.computerNotice.offline", {
            name: state.name ?? t("chat.computerNotice.unnamed"),
          })
        : t("chat.computerNotice.noComputer")}
    </Callout>
  );
}
