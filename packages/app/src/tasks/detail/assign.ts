import { useMutation, useQueryClient } from "@tanstack/react-query";

import {
  isApiError,
  type AssignmentConversation,
  type Task,
  type Invitation,
  type TaskAssignee,
} from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../../context";
import { isPersonAssignee } from "../invitationAssignees";
import { replaceAssignee, toRefs } from "../pickers";
import { taskErrorMessage, useUpdateTask, type Person } from "../queries";
import { useOpenSessionOnAssign } from "./openSession";

type AssigneeKind = TaskAssignee["kind"];

export interface AssignOptions {
  /** 召唤菜单指派：用户明确要求运行，成功后总是打开运行对话（属性栏仍按偏好） */
  openAlways?: boolean;
  /** 选择运行对话的结果；只在本次新增 Agent 处理人且选了新开对话时随 PATCH 发送 */
  assignmentConversation?: AssignmentConversation;
  /** 选择在原有对话中运行时的原会话：成功后直接打开它 */
  reuseConversationId?: string | null;
}

/**
 * 设置某一类处理人（同一类只保留一个，与属性栏一致）：value 为 user:/agent:/crew:<id> 或 none。
 * 召唤菜单「把任务指派给 Agent」与属性栏复用同一写法：
 * 同一个 PATCH 接口、同样的乐观更新与失效；新指派 Agent 时打开服务端自动创建（或复用）的会话。
 */
export function useSetTaskAssignee(orgId: string) {
  const update = useUpdateTask(orgId);
  const queryClient = useQueryClient();
  const openSession = useOpenSessionOnAssign(orgId);
  return (
    task: Task,
    value: string,
    kind: AssigneeKind,
    names: {
      people: Person[];
      agents: { id: string; name: string }[];
      invitations?: Invitation[];
    },
    options: AssignOptions = {},
  ) => {
    const previous = task.assignees.find((a) => a.kind === kind);
    const base =
      value === "none"
        ? task.assignees.filter((a) =>
            isPersonAssignee(kind)
              ? !isPersonAssignee(a.kind)
              : a.kind !== kind,
          )
        : replaceAssignee(task.assignees, value, names);
    const next = base.find((a) => a.kind === kind);
    const addsAgent = kind === "AGENT" && next && next.id !== previous?.id;
    // 只在选了新开对话时带字段：reuse 等于缺省行为，不带也能兼容还不认识这个字段的旧服务端
    const fresh = addsAgent && options.assignmentConversation === "new";
    update.mutate(
      {
        task,
        patch: {
          assignees: toRefs(base),
          ...(fresh ? { assignment_conversation: "new" as const } : {}),
        },
        optimistic: { assignees: base },
      },
      {
        onSuccess: () => {
          if (!addsAgent || !next) return;
          // 新开对话时旧会话已降级，刷新会话列表
          if (fresh)
            void queryClient.invalidateQueries({
              queryKey: keys.conversationLists(orgId),
            });
          void openSession(task, next.id, {
            always: options.openAlways,
            conversationId: fresh ? null : options.reuseConversationId,
          });
        },
      },
    );
  };
}

/** 再次运行的错误提示：前置条件不满足时给出具体原因，其它按任务写操作的通用提示 */
export function agentRunErrorMessage(t: Translate, error: unknown): string {
  if (isApiError(error)) {
    if (error.code === "TASK_AGENT_NOT_ASSIGNED")
      return t("tasks.runChoice.errors.notAssigned");
    if (error.code === "TASK_AGENT_RUN_NOT_ALLOWED")
      return t("tasks.runChoice.errors.notRunnable");
  }
  return taskErrorMessage(t, error);
}

export interface AgentRunVars {
  task: Task;
  agentId: string;
  mode: AssignmentConversation;
  /** 在原有对话中运行时的原会话：成功后直接打开它 */
  reuseConversationId?: string | null;
  /** 每次用户确认「开始运行」生成一个；同一次确认内的重试复用（服务端按它去重，避免重复入队） */
  idempotencyKey: string;
}

/**
 * 召唤菜单里再次选择已指派的 Agent：不改处理人，让它再运行一次（POST …/agent-runs），
 * 成功后总是在任务全页打开本次运行的会话。
 */
export function useRerunTaskAgent(orgId: string) {
  const api = useApi();
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const openSession = useOpenSessionOnAssign(orgId);
  return useMutation({
    mutationFn: ({ task, agentId, mode, idempotencyKey }: AgentRunVars) =>
      api.tasks.startAgentRun(
        orgId,
        task.space_id,
        task.id,
        { agent_id: agentId, assignment_conversation: mode },
        idempotencyKey,
      ),
    onSuccess: (_result, { task, agentId, mode, reuseConversationId }) => {
      void queryClient.invalidateQueries({
        queryKey: keys.conversationLists(orgId),
      });
      void openSession(task, agentId, {
        always: true,
        conversationId: mode === "reuse" ? reuseConversationId : null,
      });
    },
    onError: (error) =>
      toast(agentRunErrorMessage(t, error), { tone: "error" }),
  });
}
