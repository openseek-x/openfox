import type { ReactNode } from "react";

import { useT } from "@openfox/i18n";

import { AgentAvatar } from "../agents/AgentAvatar";
import { useAgent, useEntitySpace } from "../agents/queries";
import type { ComposerAgent } from "./Composer";

/**
 * 一轮 Agent 回复的外框：顶部头像 + 名字 + 「Agent」标签，下方依次是执行时间线、正文与操作栏。
 * 容器宽度 ≥ @lg 时正文与名字左对齐（缩进到头像右侧）；更窄时正文占满整行，避免浪费宽度。
 * group/turn 让操作栏在悬停整轮回复（含时间线）时就显示。
 */
export function AgentTurn({
  orgId,
  spaceId,
  agentId,
  author,
  children,
}: {
  orgId: string;
  spaceId: string;
  agentId?: string;
  author?: ComposerAgent;
  children: ReactNode;
}) {
  const t = useT();
  // 小队成员摘要只有名字；历史作者也可能已退队。按同一 ID 读取头像和卡片使用的资料。
  const lookup = agentId && author?.avatar === undefined ? agentId : null;
  const location = useEntitySpace(orgId, "agent", lookup);
  const authorSpace =
    location.spaceId ?? (location.notFound ? spaceId : undefined);
  const query = useAgent(orgId, authorSpace, lookup);
  const detail = lookup && query.isSuccess ? query.data : undefined;
  const profile = detail ?? author;
  const name = profile?.name ?? t("chat.messages.unknownAuthor");
  const resolved = !!profile || !lookup || query.isError;
  return (
    <div
      data-slot="agent-turn"
      className="group/turn grid min-w-0 grid-cols-[24px_minmax(0,1fr)] gap-x-2.5 gap-y-2"
    >
      <span className="flex">
        <AgentAvatar
          agentId={resolved ? agentId : undefined}
          spaceId={detail?.space_id ?? authorSpace}
          name={name}
          avatar={profile?.avatar ?? undefined}
          size={24}
        />
      </span>
      <span className="flex min-w-0 items-center gap-1.5 text-body-sm font-medium text-fg-default">
        <span className="truncate">{name}</span>
        <span className="shrink-0 rounded-full border border-line-default px-1.5 text-[11px] leading-4 font-normal text-fg-muted">
          {t("chat.messages.agentBadge")}
        </span>
      </span>
      <div className="col-span-2 flex min-w-0 flex-col gap-2 @lg:col-span-1 @lg:col-start-2">
        {children}
      </div>
    </div>
  );
}
