import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useRef } from "react";

import type { ConversationSummary, Task } from "@openfox/api";

import { keys, useApi } from "../../context";
import { useOpenTaskPage } from "../navigation";

/** 指派后查找 TASK_ASSIGNMENT 会话：每秒一次，最多 10 次（约 10 秒）。 */
export const OPEN_SESSION_POLL_MS = 1000;
export const OPEN_SESSION_POLL_TIMES = 10;

/** 该任务 + Agent 打开状态的指派会话（未关闭、未归档）；与服务端复用指派会话的条件一致。 */
export function findAssignmentConversation(
  list: readonly ConversationSummary[],
  agentId: string,
): ConversationSummary | undefined {
  return list.find(
    (c) =>
      c.origin === "TASK_ASSIGNMENT" &&
      c.agent_id === agentId &&
      c.lifecycle === "OPEN" &&
      !c.archived_at,
  );
}

export interface OpenSessionOptions {
  /** 召唤菜单指派：用户明确要求运行，总是打开，不看偏好 open_session_on_assign */
  always?: boolean;
  /** 已知本次运行所在的会话（选择了在原有对话中运行）：直接打开，不轮询 */
  conversationId?: string | null;
}

/**
 * 指派 Agent 保存成功后打开本次运行的会话：轮询该任务 + Agent 打开状态的 TASK_ASSIGNMENT 会话，找到就打开；超时静默放弃。
 * 指派后的落点是任务全页的并排会话（/o/<org>/tasks/<id>?view=chat&conversationId=…），不跳到独立的 /chat/<id>。
 * 新开对话运行时旧会话已在服务端降级为普通会话，轮询只会找到新建的那个。
 * 组件卸载或再次指派时取消上一次轮询。
 */
export function useOpenSessionOnAssign(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  const openPage = useOpenTaskPage();
  const token = useRef(0);
  useEffect(
    () => () => {
      token.current += 1;
    },
    [],
  );
  return useCallback(
    async (
      task: Pick<Task, "id" | "display_id">,
      agentId: string,
      options: OpenSessionOptions = {},
    ) => {
      const mine = ++token.current;
      try {
        if (!options.always) {
          const prefs = await queryClient.fetchQuery({
            queryKey: keys.preferences,
            queryFn: api.me.preferences.get,
            staleTime: 30_000,
          });
          if (!prefs.open_session_on_assign) return;
        }
        if (options.conversationId) {
          if (token.current === mine)
            openPage(task.display_id, options.conversationId);
          return;
        }
        for (let i = 0; i < OPEN_SESSION_POLL_TIMES; i += 1) {
          if (token.current !== mine) return;
          const page = await api.conversations.page(orgId, {
            task_id: task.id,
            agent_id: agentId,
          });
          const found = findAssignmentConversation(page.conversations, agentId);
          if (found) {
            if (token.current === mine) openPage(task.display_id, found.id);
            return;
          }
          await new Promise((resolve) =>
            setTimeout(resolve, OPEN_SESSION_POLL_MS),
          );
        }
      } catch {
        // 偏好或会话查询失败：不打扰用户，保持在任务详情
      }
    },
    [api, orgId, queryClient, openPage],
  );
}
