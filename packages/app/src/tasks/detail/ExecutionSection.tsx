import type { Task } from "@openfox/api";
import { useT } from "@openfox/i18n";

import type { Person } from "../queries";

import {
  ConversationRow,
  ConversationListFeedback,
  InvokeAgentButton,
  useTaskConversations,
} from "./TaskConversations";

/**
 * 执行区块（详情右栏顶部）：「召唤 Agent」按钮 + 该任务绑定的会话（Agent + 会话状态），点击打开任务 + AI 面板。
 * 没有会话时只显示按钮；会话查询挂在 conversations 前缀下，conversation.updated / run.state 事件会让它失效。
 */
export function ExecutionSection({
  orgId,
  task,
  showInvoke = true,
  people,
}: {
  orgId: string;
  task: Task;
  /** 窄布局「更多属性」浮层里不重复「召唤 Agent」（按钮已在标题上方） */
  showInvoke?: boolean;
  people?: Person[];
}) {
  const t = useT();
  const query = useTaskConversations(orgId, task);
  const list = query.list;
  if (!showInvoke && list.length === 0) return null;
  return (
    <div className="mb-3 flex flex-col gap-2">
      {showInvoke && (
        <InvokeAgentButton
          orgId={orgId}
          task={task}
          className="w-full justify-center"
        />
      )}
      {list.length > 0 && (
        <section aria-label={t("tasks.detail.execution")}>
          <ul className="flex flex-col gap-2">
            {list.map((item) => (
              <ConversationRow
                key={item.id}
                task={task}
                item={item}
                people={people}
                compact
              />
            ))}
          </ul>
        </section>
      )}
      <ConversationListFeedback query={query} />
    </div>
  );
}
