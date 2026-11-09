import {
  useNavigate,
  useSearchParams,
  type NavigateOptions,
} from "react-router-dom";
import type { ConversationSummary } from "@openfox/api";

import { taskPagePath } from "../tasks/navigation";

/** 普通会话独立展示；任务会话打开任务详情及其 AI 面板。 */
export function conversationDestination(
  orgId: string,
  conversation: ConversationSummary,
  rail: "home" | "chat" = "chat",
): string {
  if (conversation.task?.display_id) {
    const target = taskPagePath(
      orgId,
      conversation.task.display_id,
      conversation.id,
    );
    return `${target}&rail=${rail}`;
  }
  return withChatRail(
    `/o/${orgId}/chat/${encodeURIComponent(conversation.id)}`,
    rail,
  );
}

/** 首页的会话保留首页导航；没有参数的旧链接仍进入对话页签。 */
export function withChatRail(target: string, rail: string | null): string {
  if (rail !== "home") return target;
  const [pathname, search] = target.split("?");
  const params = new URLSearchParams(search);
  params.set("rail", "home");
  return `${pathname}?${params}`;
}

/** 会话内部切换 / 新建时延续当前入口的导航上下文。 */
export function useChatNavigate() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  return (target: string, options?: NavigateOptions) =>
    navigate(withChatRail(target, params.get("rail")), options);
}
