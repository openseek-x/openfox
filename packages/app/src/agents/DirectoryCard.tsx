import type { AgentDirectoryEntry } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
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
  onVariant: (entry: AgentDirectoryEntry) => void;
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
 * 目录卡片（Figma Agent/DirectoryCard）：头像、「名称 - 职位」、meta 行；
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
      className="group relative flex h-[72px] items-center gap-3 rounded-xl border border-line-default bg-surface-card px-4 transition-colors duration-100 hover:bg-surface-hover focus-within:bg-surface-hover has-[[aria-expanded=true]]:bg-surface-hover motion-reduce:transition-none"
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
      <div className="of-row-actions relative flex shrink-0 items-center gap-1 pointer-fine:absolute pointer-fine:group-hover:relative pointer-fine:group-focus-within:relative pointer-fine:has-[[aria-expanded=true]]:relative">
        <div className="flex h-7 items-center rounded-md border border-line-default bg-surface-card">
          <button
            type="button"
            aria-disabled={noChat || undefined}
            title={noChat ? t("agents.contacts.cannotChat") : undefined}
            onClick={() => {
              if (!noChat) actions.onChat(entry);
            }}
            className={cn(
              "flex h-full items-center gap-1 rounded-l-md px-2 text-body-sm",
              noChat
                ? "cursor-not-allowed opacity-50"
                : "hover:bg-control-hover",
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
            className="flex h-full items-center rounded-r-md px-1.5 hover:bg-control-hover"
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
            <MenuItem
              icon={<Icon name="git-fork" />}
              onSelect={() => actions.onVariant(entry)}
            >
              {t("agents.card.clone")}
            </MenuItem>
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
