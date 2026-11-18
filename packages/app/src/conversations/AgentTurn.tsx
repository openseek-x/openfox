import type { ReactNode } from "react";

import { useT } from "@openfox/i18n";

import { AgentAvatar } from "../agents/AgentAvatar";
import type { ComposerAgent } from "./Composer";

/**
 * 一轮 Agent 回复的外框：顶部头像 + 名字 + 「Agent」标签，下方依次是执行时间线、正文与操作栏。
 * 容器宽度 ≥ @lg 时正文与名字左对齐（缩进到头像右侧）；更窄时正文占满整行，避免浪费宽度。
 * group/turn 让操作栏在悬停整轮回复（含时间线）时就显示。
 */
export function AgentTurn({
  name,
  avatar,
  children,
}: {
  name: string;
  avatar?: ComposerAgent["avatar"];
  children: ReactNode;
}) {
  const t = useT();
  return (
    <div
      data-slot="agent-turn"
      className="group/turn grid min-w-0 grid-cols-[24px_minmax(0,1fr)] gap-x-2.5 gap-y-2"
    >
      {/* 名字紧挨着显示，头像只作装饰，读屏不重复念名字 */}
      <span aria-hidden="true" className="flex">
        <AgentAvatar name={name} avatar={avatar ?? undefined} size={24} />
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
