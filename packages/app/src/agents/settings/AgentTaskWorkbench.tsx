import { useMemo } from "react";

import type { SpaceAgent, TaskListQuery } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, Tabs } from "@openfox/ui";

import { useOptionalCreateTaskDialog } from "../../tasks/create/CreateTaskProvider";
import { defaultDisplay, type DisplaySettings } from "../../tasks/model";
import { useSpaceSource, useTaskCounts } from "../../tasks/queries";
import { SpaceTaskWorkspace } from "../../tasks/SpaceTaskWorkspace";
import { TaskState } from "../../tasks/TaskWorkspace";
import { useAgentTaskTab, type AgentTaskTab } from "../params";

/** 显示设置的本机存储键：按组织 + Agent 区分，与空间任务页互不影响。 */
export const agentTasksPageKey = (agentId: string) => `agent:${agentId}`;

/** 抽屉里空间有限，默认用列表布局；其余与空间任务页的默认值相同。 */
export const AGENT_TASKS_DISPLAY: DisplaySettings = {
  ...defaultDisplay("space"),
  layout: "list",
};

/** 两个子页签的固定查询范围。 */
export function agentTaskParams(
  agentId: string,
  tab: AgentTaskTab,
): TaskListQuery {
  return tab === "created"
    ? { created_via_agent: [agentId] }
    : { assignee: [`agent:${agentId}`] };
}

/**
 * Agent 抽屉「任务」页签的任务工作台（D23）：
 * 顶部「已指派 / 已创建」两个页签带数量（?agentTaskTab=created，replace）；
 * 下面是固定在 Agent 所在空间的任务工作区：查找、筛选、显示设置（按 agent:<id> 单独存本机，默认列表）。
 * 「已创建」按 created_via_agent 查：Agent 通过空间工具建的任务（task.created 的操作人是 Run 发起人）。
 */
export function AgentTaskWorkbench({
  orgId,
  agent,
}: {
  orgId: string;
  agent: SpaceAgent;
}) {
  const t = useT();
  const [tab, setTab] = useAgentTaskTab();
  const create = useOptionalCreateTaskDialog();
  const source = useSpaceSource(orgId, agent.space_id);
  const assignedParams = useMemo(
    () => agentTaskParams(agent.id, "assigned"),
    [agent.id],
  );
  const createdParams = useMemo(
    () => agentTaskParams(agent.id, "created"),
    [agent.id],
  );
  // 页签上的数量：不带查找、筛选，只看范围
  const assigned = useTaskCounts(orgId, source, assignedParams);
  const created = useTaskCounts(orgId, source, createdParams);
  const archived = Boolean(agent.archived_at);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <Tabs
        className="mx-4 shrink-0"
        label={t("agents.tasks.tabsLabel")}
        value={tab}
        onChange={(next) => setTab(next as AgentTaskTab)}
        tabs={[
          {
            id: "assigned",
            label: t("agents.tasks.assigned"),
            count: assigned.data?.total,
          },
          {
            id: "created",
            label: t("agents.tasks.created"),
            count: created.data?.total,
          },
        ]}
      />
      <SpaceTaskWorkspace
        orgId={orgId}
        spaceId={agent.space_id}
        // 两个页签共用一份显示设置；切页签只换查询范围（key 变化时重置查找与筛选）
        pageKey={agentTasksPageKey(agent.id)}
        key={tab}
        baseParams={tab === "created" ? createdParams : assignedParams}
        fallbackDisplay={AGENT_TASKS_DISPLAY}
        // 新建任务时处理人预填这个 Agent；已归档的 Agent 不提供新建入口
        onCreate={
          archived || !create
            ? undefined
            : (statusId) =>
                create.openCreate({
                  spaceId: agent.space_id,
                  statusId,
                  assignee: { value: `agent:${agent.id}`, name: agent.name },
                  openAfterCreate: true,
                })
        }
        empty={
          <TaskState
            icon={
              <Icon name="list-checks" size={24} className="text-fg-muted" />
            }
            title={
              tab === "created"
                ? t("agents.tasks.createdEmpty")
                : t("agents.tasks.empty")
            }
            desc={
              tab === "created"
                ? t("agents.tasks.createdEmptyDesc")
                : t("agents.tasks.emptyDesc")
            }
          />
        }
      />
    </div>
  );
}
