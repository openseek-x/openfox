import type { AgentDirectoryEntry } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  Tooltip,
  cn,
} from "@openfox/ui";

import { AgentAvatar, CrewAvatar } from "./AgentAvatar";
import { runtimeLabel } from "./model";

export interface CardActions {
  onOpen: (entry: AgentDirectoryEntry) => void;
  onChat: (entry: AgentDirectoryEntry) => void;
  onFavorite: (entry: AgentDirectoryEntry) => void;
  onPin: (entry: AgentDirectoryEntry) => void;
  onCopy: (entry: AgentDirectoryEntry) => void;
  onArchive: (entry: AgentDirectoryEntry) => void;
}

/** 所有者显示：是自己时显示「你」。 */
export function ownerLabel(
  entry: Pick<AgentDirectoryEntry, "owner_email" | "mine">,
  you: string,
) {
  return entry.mine ? you : entry.owner_email;
}

/**
 * 目录卡片：头像、「名称 - 职位」、meta 行；
 * 悬停显示「对话 | ☆」组合按钮与「···」菜单。点击卡片打开设置抽屉。
 */
export function DirectoryCard({
  entry,
  actions,
}: {
  entry: AgentDirectoryEntry;
  actions: CardActions;
}) {
  const t = useT();
  const crew = entry.type === "crew";
  const noChat = entry.allow_direct_chat === false;
  const owner = ownerLabel(entry, t("agents.you"));
  const meta = crew
    ? [owner, t("agents.members", { count: entry.member_count })]
    : [
        entry.model && entry.model !== "default"
          ? entry.model
          : t("agents.modelDefault"),
        runtimeLabel(entry.runtime),
        owner,
      ].filter(Boolean);
  return (
    <div
      className="group relative flex h-[72px] items-center gap-3 rounded-xl border border-line-default bg-surface-card px-4 hover:bg-surface-hover focus-within:bg-surface-hover"
      data-testid={`directory-card-${entry.id}`}
    >
      <button
        type="button"
        aria-label={entry.name}
        onClick={() => actions.onOpen(entry)}
        className="absolute inset-0 rounded-xl"
      />
      {crew ? (
        <CrewAvatar
          name={entry.name}
          members={(entry.members ?? []).filter((m) => m.kind !== "HUMAN")}
        />
      ) : (
        <AgentAvatar
          name={entry.name}
          avatar={entry.avatar}
          locked={entry.visibility === "PRIVATE"}
        />
      )}
      <div className="pointer-events-none flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex min-w-0 items-center gap-1.5">
          <span className="truncate text-body font-medium text-fg-default">
            {entry.name}
          </span>
          {entry.title && (
            <span className="truncate text-body text-fg-muted">
              - {entry.title}
            </span>
          )}
          {crew && (
            <Badge tone="neutral" className="gap-0.5">
              <Icon name="users" size={12} />
              {t("agents.crewBadge")}
            </Badge>
          )}
        </div>
        <div className="flex min-w-0 items-center gap-1 text-caption text-fg-muted">
          {!crew && <Icon name="bot" size={12} />}
          <span className="truncate">{meta.join(" · ")}</span>
        </div>
      </div>
      <div
        className={cn(
          // 悬停操作叠在卡片右侧，不占布局宽度：未悬停时名称与 meta 可用整行宽度
          "absolute top-1/2 right-3 flex -translate-y-1/2 items-center gap-1 rounded-md bg-surface-hover pl-2 opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100",
        )}
      >
        <div className="flex h-7 items-center rounded-md border border-line-default bg-surface-card">
          <button
            type="button"
            aria-disabled={noChat || undefined}
            title={noChat ? t("agents.contacts.cannotChat") : undefined}
            onClick={() => {
              if (!noChat) actions.onChat(entry);
            }}
            className={cn(
              "flex h-full items-center gap-1 rounded-l-md px-2 text-body-sm hover:bg-surface-muted",
              noChat && "cursor-not-allowed opacity-50",
            )}
          >
            <Icon name="message" size={14} />
            {t("agents.card.chat")}
          </button>
          <span aria-hidden className="h-4 w-px bg-line-default" />
          <button
            type="button"
            aria-label={
              entry.favorite
                ? t("agents.card.unfavorite")
                : t("agents.card.favorite")
            }
            aria-pressed={entry.favorite}
            title={
              entry.favorite
                ? t("agents.card.unfavorite")
                : t("agents.card.favorite")
            }
            onClick={() => actions.onFavorite(entry)}
            className="flex h-full items-center rounded-r-md px-1.5 hover:bg-surface-muted"
          >
            <Icon
              name="star"
              size={14}
              className={
                entry.favorite ? "fill-current text-warning" : undefined
              }
            />
          </button>
        </div>
        <Menu
          label={t("agents.card.menu", { name: entry.name })}
          placement="bottom-end"
          trigger={({ ref, toggle, open }) => (
            <IconButton
              ref={ref}
              label={t("agents.card.menu", { name: entry.name })}
              icon={<Icon name="ellipsis" />}
              size="28"
              variant="outline"
              active={open}
              aria-expanded={open}
              onClick={toggle}
            />
          )}
        >
          <MenuItem
            icon={<Icon name={entry.pinned ? "pin-off" : "pin"} />}
            onSelect={() => actions.onPin(entry)}
          >
            {entry.pinned ? t("agents.card.unpin") : t("agents.card.pin")}
          </MenuItem>
          <MenuItem
            icon={<Icon name="sliders-horizontal" />}
            onSelect={() => actions.onOpen(entry)}
          >
            {t("agents.card.settings")}
          </MenuItem>
          {!crew && (
            <MenuItem
              icon={<Icon name="copy" />}
              onSelect={() => actions.onCopy(entry)}
            >
              {t("agents.card.copy")}
            </MenuItem>
          )}
          {!crew && (
            <Tooltip content={t("agents.comingSoon")}>
              <span className="block">
                <MenuItem icon={<Icon name="git-fork" />} disabled>
                  {t("agents.card.clone")}
                </MenuItem>
              </span>
            </Tooltip>
          )}
          <MenuSeparator />
          <MenuItem
            danger
            icon={<Icon name="archive" />}
            onSelect={() => actions.onArchive(entry)}
          >
            {t("agents.card.archive")}
          </MenuItem>
        </Menu>
      </div>
    </div>
  );
}
