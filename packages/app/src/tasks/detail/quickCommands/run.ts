import { useMutation, useQueryClient } from "@tanstack/react-query";

import type { QuickCommand, Task } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../../../context";
import {
  chatErrorMessage,
  invalidateConversation,
} from "../../../conversations/queries";
import { useOpenTaskPage } from "../../navigation";

/**
 * 触发快捷指令（不新增接口，组合现有接口）：
 * 1. 建挂在该任务上的 SHARED 会话（Agent 或小队）；
 * 2. 发送提示词（空提示词用固定文案）。Agent 会话 @ 该 Agent；小队会话不 @，由队长处理；
 * 3. 偏好 open_session_on_quick_command 为真（默认）时打开会话，否则提示「已发送给 X」；
 * 4. 第 2 步失败：会话已建好，打开它并提示错误，用户可以在会话里重发。
 */
export function useRunQuickCommand(orgId: string, task: Task) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const openPage = useOpenTaskPage();
  return useMutation({
    mutationFn: async (command: QuickCommand) => {
      const target = command.target;
      const conversation = await api.conversations.create(orgId, {
        space_id: task.space_id,
        task_id: task.id,
        ...(target.kind === "CREW"
          ? { crew_id: target.id }
          : { agent_id: target.id }),
        visibility: "SHARED",
      });
      invalidateConversation(queryClient, orgId, conversation.id);
      try {
        await api.conversations.postMessage(orgId, conversation.id, {
          body: command.prompt.trim() || t("tasks.quickCommands.defaultPrompt"),
          // 每次点击一个新的幂等键：同一次点击的重试不会重复发送
          client_message_id: crypto.randomUUID(),
          mentions: target.kind === "AGENT" ? [{ agent_id: target.id }] : [],
        });
      } catch (error) {
        openPage(task.display_id, conversation.id);
        toast(
          t("tasks.quickCommands.sendFailed", {
            reason: chatErrorMessage(t, error),
          }),
          { tone: "error" },
        );
        return;
      }
      const prefs = await queryClient
        .fetchQuery({
          queryKey: keys.preferences,
          queryFn: api.me.preferences.get,
          staleTime: 30_000,
        })
        .catch(() => null);
      // 偏好读取失败按默认值（打开会话）处理
      if (prefs?.open_session_on_quick_command ?? true)
        openPage(task.display_id, conversation.id);
      else toast(t("tasks.quickCommands.sentTo", { name: target.name }));
    },
    onError: (error) => toast(chatErrorMessage(t, error), { tone: "error" }),
  });
}
