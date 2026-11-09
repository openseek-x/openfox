import { useCallback } from "react";
import { useLocation, useSearchParams } from "react-router-dom";

import { rememberSpace } from "./queries";

/** Agent 设置面板的页签。 */
export type AgentPanelTab = "settings" | "tasks" | "runs";

/** 面板展开参数（?agentPanel=full）。 */
const AGENT_PANEL = "agentPanel";
/** 页签参数（?agentTab=tasks|runs；settings 是默认值，不写）。 */
const AGENT_TAB = "agentTab";
/** 运行记录弹窗参数（?agentRun=<runId>，只在运行历史页签里有效）。 */
const AGENT_RUN = "agentRun";
/** 任务页签里的子页签（?agentTaskTab=created；assigned 是默认值，不写）。 */
const AGENT_TASK_TAB = "agentTaskTab";
/** Agent 面板自己的参数，关闭抽屉或换一个抽屉时一并去掉。 */
const PANEL_PARAMS = [
  AGENT_PANEL,
  AGENT_TAB,
  AGENT_RUN,
  AGENT_TASK_TAB,
  "agentSection",
] as const;

const TABS: readonly AgentPanelTab[] = ["settings", "tasks", "runs"];

/** 面板在地址里的状态。 */
export interface AgentPanelState {
  expanded: boolean;
  tab: AgentPanelTab;
  /** 运行记录弹窗打开着的 Run；只在展开且停在运行历史页签时有值 */
  runId: string | null;
}

/**
 * 读出面板状态。兼容旧链接：?agentTab=runs（没有 agentPanel）原来的语义是「全屏打开运行历史」，
 * 所以只要页签不是设置，就按展开处理；页签只在展开时显示。未知页签按设置处理。
 */
export function readAgentPanel(params: URLSearchParams): AgentPanelState {
  const raw = params.get(AGENT_TAB);
  const tab = TABS.includes(raw as AgentPanelTab)
    ? (raw as AgentPanelTab)
    : "settings";
  const expanded = params.get(AGENT_PANEL) === "full" || tab !== "settings";
  const run = params.get(AGENT_RUN);
  return {
    expanded,
    tab: expanded ? tab : "settings",
    runId: expanded && tab === "runs" && run ? run : null,
  };
}

/**
 * 把面板状态写进参数（原地修改）：展开写 agentPanel=full，页签不是设置时写 agentTab；
 * 收起时去掉这几个参数（收起后回到设置页签）。runId 只在运行历史页签里写。
 */
export function writeAgentPanel(
  params: URLSearchParams,
  state: { expanded: boolean; tab?: AgentPanelTab; runId?: string | null },
): URLSearchParams {
  for (const name of PANEL_PARAMS) params.delete(name);
  if (!state.expanded) return params;
  const tab = state.tab ?? "settings";
  params.set(AGENT_PANEL, "full");
  if (tab !== "settings") params.set(AGENT_TAB, tab);
  if (tab === "runs" && state.runId) params.set(AGENT_RUN, state.runId);
  return params;
}

/** 任务工作台的子页签：已指派（处理人是它）/ 已创建（它通过空间工具建的）。 */
export type AgentTaskTab = "assigned" | "created";

/** 读出任务工作台子页签；只在展开且停在任务页签时有意义，未知值按已指派处理。 */
export function readAgentTaskTab(params: URLSearchParams): AgentTaskTab {
  return params.get(AGENT_TASK_TAB) === "created" ? "created" : "assigned";
}

/**
 * 切换任务工作台子页签：replace 写入，保留当前路由 state；已指派是默认值，不写参数。
 * 切到其他面板页签或收起时，writeAgentPanel 会一并去掉这个参数。
 */
export function useAgentTaskTab(): [
  AgentTaskTab,
  (next: AgentTaskTab) => void,
] {
  const [params, setParams] = useSearchParams();
  const routeState = useLocation().state as unknown;
  const set = useCallback(
    (next: AgentTaskTab) =>
      setParams(
        (prev) => {
          const result = new URLSearchParams(prev);
          if (next === "created") result.set(AGENT_TASK_TAB, "created");
          else result.delete(AGENT_TASK_TAB);
          return result;
        },
        { replace: true, state: routeState },
      ),
    [setParams, routeState],
  );
  return [readAgentTaskTab(params), set];
}

/**
 * 读写地址栏里的抽屉参数（?agentSettings=<id> / ?crewSettings=<id>），任何页面都能打开，刷新后保持。
 * 打开时登记空间 ID，抽屉据此直接请求详情。
 * Agent 抽屉另有面板状态（展开、页签、运行记录弹窗），都用 replace 写入，不增加历史条数。
 */
function useDrawerParam(name: "agentSettings" | "crewSettings") {
  const [params, setParams] = useSearchParams();
  const routeState = useLocation().state as unknown;
  const value = params.get(name);
  const panel = readAgentPanel(params);
  /** 兼容旧的返回值：直接以全屏打开运行历史时为 "runs" */
  const tab = panel.expanded && panel.tab === "runs" ? ("runs" as const) : null;
  const open = useCallback(
    (
      id: string,
      spaceId?: string,
      initialTab?: AgentPanelTab | "run-settings",
    ) => {
      if (spaceId) rememberSpace(id, spaceId);
      setParams((prev) => {
        const next = new URLSearchParams(prev);
        // 两个抽屉占同一位置，同时只开一个
        next.delete("agentSettings");
        next.delete("crewSettings");
        for (const param of PANEL_PARAMS) next.delete(param);
        next.set(name, id);
        // 指定页签即以展开状态打开（新地址统一写 agentPanel=full&agentTab=…）
        if (name === "agentSettings" && initialTab) {
          if (initialTab === "run-settings") next.set("agentSection", "run");
          else writeAgentPanel(next, { expanded: true, tab: initialTab });
        }
        return next;
      });
    },
    [name, setParams],
  );
  const close = useCallback(
    () =>
      setParams((prev) => {
        const next = new URLSearchParams(prev);
        next.delete(name);
        for (const param of PANEL_PARAMS) next.delete(param);
        return next;
      }),
    [name, setParams],
  );
  /** 改面板状态（展开 / 收起、切页签、开关运行记录弹窗）：replace，并保留当前路由 state（如任务抽屉的历史标记） */
  const setPanel = useCallback(
    (state: {
      expanded: boolean;
      tab?: AgentPanelTab;
      runId?: string | null;
    }) =>
      setParams((prev) => writeAgentPanel(new URLSearchParams(prev), state), {
        replace: true,
        state: routeState,
      }),
    [setParams, routeState],
  );
  return {
    id: value,
    tab,
    panel,
    initialRunSection: params.get("agentSection") === "run",
    open,
    close,
    setPanel,
  };
}

export const useAgentSettingsParam = () => useDrawerParam("agentSettings");
export const useCrewSettingsParam = () => useDrawerParam("crewSettings");
