import { useMutation, useQueryClient } from "@tanstack/react-query";

import type { ConversationSummary, QuickCommand, Task } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../../../context";
import {
  appendToDraft,
  type DraftMention,
} from "../../../conversations/drafts";
import {
  chatErrorMessage,
  invalidateConversation,
} from "../../../conversations/queries";
import { useOpenTaskPage, useTaskPageId, useTaskPanel } from "../../navigation";

/**
 * 快捷指令「插入到输入框」：只填进输入框，不发送。
 * - 任务全页已打开并排会话 X（?view=chat&conversationId=X）：追加到 X 的草稿（已有内容时空一行），聚焦输入框，不新建会话；
 * - 没有打开的会话：按快捷指令的目标建挂在该任务上的 SHARED 会话（参数同 run.ts），不发消息，
 *   把提示词写进新会话的草稿，打开 /o/<org>/tasks/<id>?view=chat&conversationId=<新会话> 并聚焦输入框。
 * Agent 目标时草稿带上 @该 Agent（等同于 run.ts 发送时的 mentions）；小队不带。
 * 追加到已打开的会话时，只有该会话就是这个 Agent 的单聊会话才带提及（别的会话里提及不到这个 Agent）。
 */
export function useInsertQuickCommand(orgId: string, task: Task) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const openPage = useOpenTaskPage();
  const pageId = useTaskPageId();
  const panel = useTaskPanel();
  // 只认本任务全页上的并排会话；抽屉里打开别的任务时不往全页任务的会话里写
  const openConversationId =
    pageId === task.display_id.toUpperCase() ? panel.conversationId : null;
  return useMutation({
    mutationFn: async (command: QuickCommand) => {
      const target = command.target;
      const text =
        command.prompt.trim() || t("tasks.quickCommands.defaultPrompt");
      const mention: DraftMention | undefined =
        target.kind === "AGENT"
          ? { kind: "agent", id: target.id, name: target.name }
          : undefined;
      if (openConversationId) {
        const current = queryClient.getQueryData<ConversationSummary>(
          keys.conversation(orgId, openConversationId),
        );
        const sameAgent =
          current !== undefined &&
          !current.crew_id &&
          current.agent_id === target.id;
        appendToDraft(
          openConversationId,
          text,
          sameAgent ? mention : undefined,
        );
        return;
      }
      const conversation = await api.conversations.create(orgId, {
        space_id: task.space_id,
        task_id: task.id,
        ...(target.kind === "CREW"
          ? { crew_id: target.id }
          : { agent_id: target.id }),
        visibility: "SHARED",
      });
      invalidateConversation(queryClient, orgId, conversation.id);
      appendToDraft(conversation.id, text, mention);
      openPage(task.display_id, conversation.id);
    },
    onError: (error) => toast(chatErrorMessage(t, error), { tone: "error" }),
  });
}
