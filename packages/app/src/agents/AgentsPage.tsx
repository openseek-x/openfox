import { useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import type { AgentDirectoryEntry } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  ConfirmDialog,
  Dialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Skeleton,
  TextInput,
  cn,
} from "@openfox/ui";

import { SpaceIcon } from "../spaces/SpaceIcon";
import { AgentAvatar, CrewAvatar } from "./AgentAvatar";
import {
  useArchiveEntry,
  useCopyAgent,
  useFavoriteEntry,
  usePinEntry,
} from "./actions";
import { CreateAgentDialog } from "./CreateAgentDialog";
import { CreateCrewEntry } from "./CreateCrewDialog";
import { DirectoryCard, type CardActions } from "./DirectoryCard";
import { filterDirectory, groupDirectory, type DirectoryFilter } from "./model";
import { useAgentSettingsParam, useCrewSettingsParam } from "./params";
import { useArchivedDirectory, useDirectory, useSpaces } from "./queries";

const FILTERS: DirectoryFilter[] = ["all", "agent", "crew", "mine"];

/** 对话页入口。 */
export function chatPath(
  orgId: string,
  entry: Pick<AgentDirectoryEntry, "type" | "id">,
) {
  return `/o/${orgId}/chat?${entry.type === "crew" ? "crew" : "agent"}=${encodeURIComponent(entry.id)}`;
}

/** 空间 Agents 页传入的当前空间；全局页不传。 */
export interface AgentsPageSpace {
  id: string;
  name: string;
  icon: string | null;
  /** 已停用空间只读：不能新建 Agent / 小队 */
  retired: boolean;
}

/**
 * 全部 Agent 和小队：筛选、搜索、按置顶 / 空间分组的 3 列卡片。
 * 传入 space 时只列这个空间的 Agent 与小队（含已归档弹窗），新建默认这个空间，页头改成空间面包屑。
 */
export function AgentsPage({ space }: { space?: AgentsPageSpace } = {}) {
  const t = useT();
  const { orgId = "" } = useParams();
  const navigate = useNavigate();
  const directory = useDirectory(orgId);
  // 空间页的「显示已归档（N）」只数本空间；全局页沿用目录接口给的总数，不多发请求
  const archivedInSpace = useArchivedDirectory(orgId, Boolean(space));
  const spaces = useSpaces(orgId);
  const agentDrawer = useAgentSettingsParam();
  const crewDrawer = useCrewSettingsParam();
  const pin = usePinEntry(orgId);
  const favorite = useFavoriteEntry(orgId);
  const archive = useArchiveEntry(orgId);
  const copy = useCopyAgent(orgId);

  const [filter, setFilter] = useState<DirectoryFilter>("all");
  const [query, setQuery] = useState("");
  // 折叠状态只在本次浏览内有效
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());
  const [dialog, setDialog] = useState<null | "agent" | "crew" | "archived">(
    null,
  );
  const [confirm, setConfirm] = useState<AgentDirectoryEntry | null>(null);

  const allEntries = directory.data?.entries;
  const entries = useMemo(
    () =>
      (allEntries ?? []).filter(
        (entry) => !space || entry.space_id === space.id,
      ),
    [allEntries, space],
  );
  const archivedCount = space
    ? (archivedInSpace.data?.entries ?? []).filter(
        (entry) => entry.space_id === space.id,
      ).length
    : (directory.data?.archived_count ?? 0);
  const canCreate = !space?.retired;
  const visible = useMemo(
    () => filterDirectory(entries, filter, query),
    [entries, filter, query],
  );
  const groups = useMemo(
    () => groupDirectory(visible, spaces.data ?? []),
    [visible, spaces.data],
  );

  const open = (entry: AgentDirectoryEntry) =>
    entry.type === "agent"
      ? agentDrawer.open(entry.id, entry.space_id)
      : crewDrawer.open(entry.id, entry.space_id);

  const actions: CardActions = {
    onOpen: open,
    onChat: (entry) => navigate(chatPath(orgId, entry)),
    onFavorite: (entry) =>
      favorite.mutate({ target: entry, favorite: !entry.favorite }),
    onPin: (entry) => pin.mutate({ target: entry, pinned: !entry.pinned }),
    onCopy: (entry) =>
      void copy(entry).then((agent) => {
        if (agent) agentDrawer.open(agent.id, agent.space_id);
      }),
    onArchive: setConfirm,
  };

  const toggleGroup = (id: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  // 空间页页头矮一些（与任务、技能页一致），按钮用 28 尺寸
  const buttonSize = space ? "28" : undefined;
  const headerActions = (
    <>
      <Menu
        label={t("agents.directory.more")}
        placement="bottom-end"
        trigger={({ ref, toggle, open: menuOpen }) => (
          <IconButton
            ref={ref}
            label={t("agents.directory.more")}
            icon={<Icon name="ellipsis" />}
            size={buttonSize}
            active={menuOpen}
            aria-expanded={menuOpen}
            onClick={toggle}
          />
        )}
      >
        <MenuItem
          icon={<Icon name="archive" />}
          onSelect={() => setDialog("archived")}
        >
          {t("agents.directory.viewArchived")}
        </MenuItem>
      </Menu>
      <Button
        variant="outline"
        size={buttonSize}
        disabled={!canCreate}
        leading={<Icon name="plus" />}
        onClick={() => setDialog("crew")}
      >
        {t("agents.directory.createCrew")}
      </Button>
      <Button
        size={buttonSize}
        disabled={!canCreate}
        leading={<Icon name="plus" />}
        onClick={() => setDialog("agent")}
      >
        {t("agents.directory.createAgent")}
      </Button>
    </>
  );

  const body = (
    <>
      <div className="flex items-center gap-2">
        <div
          role="radiogroup"
          aria-label={t("agents.directory.filters.label")}
          className="flex flex-1 gap-2"
        >
          {FILTERS.map((value) => (
            <button
              key={value}
              type="button"
              role="radio"
              aria-checked={filter === value}
              onClick={() => setFilter(value)}
              className={cn(
                "h-8 shrink-0 rounded-full border px-3.5 text-body whitespace-nowrap",
                filter === value
                  ? "border-transparent bg-surface-active font-medium"
                  : "border-line-default text-fg-secondary hover:bg-surface-hover",
              )}
            >
              {t(`agents.directory.filters.${value}`)}
            </button>
          ))}
        </div>
        <TextInput
          className="w-[240px]"
          type="search"
          aria-label={t("agents.directory.search")}
          placeholder={t("agents.directory.search")}
          leading={<Icon name="search" />}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
      </div>

      {directory.data?.truncated && (
        <Callout tone="info">{t("agents.directory.truncated")}</Callout>
      )}

      {directory.isLoading ? (
        <div
          aria-busy="true"
          aria-label={t("agents.directory.loading")}
          className="grid grid-cols-3 gap-3"
        >
          {Array.from({ length: 6 }, (_, i) => (
            <Skeleton key={i} className="h-[72px] rounded-xl" />
          ))}
        </div>
      ) : directory.isError ? (
        <div className="flex flex-col items-center gap-2 py-16 text-center">
          <Icon name="wifi-off" size={28} className="text-fg-muted" />
          <p className="text-body font-medium">
            {t("agents.directory.errorTitle")}
          </p>
          <p className="text-body-sm text-fg-muted">
            {t("agents.directory.errorDesc")}
          </p>
          <Button
            variant="outline"
            leading={<Icon name="refresh" />}
            onClick={() => void directory.refetch()}
          >
            {t("common.retry")}
          </Button>
        </div>
      ) : entries.length === 0 ? (
        <div className="flex flex-col items-center gap-2 py-16 text-center">
          <Icon name="openfox-empty-robot" size={64} />
          <p className="text-body font-medium">
            {t("agents.directory.emptyTitle")}
          </p>
          <p className="text-body-sm text-fg-muted">
            {t("agents.directory.emptyDesc")}
          </p>
          <Button
            disabled={!canCreate}
            leading={<Icon name="plus" />}
            onClick={() => setDialog("agent")}
          >
            {t("agents.directory.emptyAction")}
          </Button>
        </div>
      ) : groups.length === 0 ? (
        <p className="py-16 text-center text-body text-fg-muted">
          {t("agents.directory.noMatch")}
        </p>
      ) : (
        groups.map((group) => {
          const label =
            group.kind === "pinned"
              ? t("agents.directory.pinned")
              : group.kind === "unassigned"
                ? t("agents.directory.unassigned")
                : group.name;
          const folded = collapsed.has(group.id);
          return (
            <section
              key={group.id}
              aria-label={label}
              className="flex flex-col gap-2"
            >
              <button
                type="button"
                aria-expanded={!folded}
                aria-label={t("agents.directory.toggleGroup", { name: label })}
                onClick={() => toggleGroup(group.id)}
                className="flex h-7 items-center gap-1.5 self-start text-body font-medium"
              >
                {group.kind === "pinned" && (
                  <Icon name="pin" size={14} className="text-fg-muted" />
                )}
                {label}
                <span className="font-normal text-fg-muted">
                  {group.entries.length}
                </span>
                <Icon
                  name="chevron-down"
                  size={14}
                  className={cn(
                    "text-fg-muted transition-transform",
                    folded && "-rotate-90",
                  )}
                />
              </button>
              {!folded && (
                <div className="grid grid-cols-3 gap-3">
                  {group.entries.map((entry) => (
                    <DirectoryCard
                      key={`${entry.type}:${entry.id}`}
                      entry={entry}
                      actions={actions}
                    />
                  ))}
                </div>
              )}
            </section>
          );
        })
      )}

      {directory.data && (
        <button
          type="button"
          onClick={() => setDialog("archived")}
          className="self-start text-body-sm text-fg-muted hover:text-fg-default"
        >
          {t("agents.directory.showArchived", { count: archivedCount })}
        </button>
      )}

      {dialog === "agent" && (
        <CreateAgentDialog
          orgId={orgId}
          defaultSpaceId={space?.id}
          onClose={() => setDialog(null)}
          onCreated={(agent) => {
            setDialog(null);
            agentDrawer.open(agent.id, agent.space_id);
          }}
        />
      )}
      {dialog === "crew" && (
        <CreateCrewEntry
          orgId={orgId}
          defaultSpaceId={space?.id}
          onClose={() => setDialog(null)}
          onCreateAgent={() => setDialog("agent")}
          onCreated={(crew) => {
            setDialog(null);
            crewDrawer.open(crew.id, crew.space_id);
          }}
        />
      )}
      {dialog === "archived" && (
        <ArchivedDialog
          orgId={orgId}
          spaceId={space?.id}
          onClose={() => setDialog(null)}
        />
      )}
      <ConfirmDialog
        open={confirm !== null}
        danger
        title={t(
          confirm?.type === "crew"
            ? "agents.archive.crewTitle"
            : "agents.archive.title",
          { name: confirm?.name ?? "" },
        )}
        description={t(
          confirm?.type === "crew"
            ? "agents.archive.crewDesc"
            : "agents.archive.desc",
          { name: confirm?.name ?? "" },
        )}
        confirmLabel={t("agents.archive.confirm")}
        onCancel={() => setConfirm(null)}
        onConfirm={() => {
          if (confirm) archive.mutate({ target: confirm, archived: true });
          setConfirm(null);
        }}
      />
    </>
  );

  if (!space)
    return (
      <div className="mx-auto flex max-w-[1100px] flex-col gap-4 px-8 pt-3 pb-10">
        <header className="flex h-16 items-center gap-2 border-b border-line-default">
          <h1 className="flex-1 text-heading font-medium">
            {t("agents.directory.title")}
          </h1>
          {headerActions}
        </header>
        {body}
      </div>
    );

  // 空间页：面包屑页头沿用任务、技能页的写法（空间图标 · 空间名 › Agents）
  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-default px-4">
        <nav
          aria-label={t("agents.directory.breadcrumb")}
          className="flex flex-1 items-center gap-1.5 text-body"
        >
          <SpaceIcon icon={space.icon} />
          <span className="text-fg-muted">{space.name}</span>
          <Icon name="chevron-right" size={12} className="text-fg-subtle" />
          <h1 className="font-medium">{t("agents.directory.spaceTitle")}</h1>
        </nav>
        {headerActions}
      </header>
      <div className="min-h-0 flex-1 overflow-auto">
        <div className="mx-auto flex max-w-[1100px] flex-col gap-4 px-8 pt-3 pb-10">
          {body}
        </div>
      </div>
    </div>
  );
}

/** 已归档 Agent 与小队：可以恢复。 */
export function ArchivedDialog({
  orgId,
  spaceId,
  onClose,
}: {
  orgId: string;
  /** 只列这个空间的已归档项；不传时列全部 */
  spaceId?: string;
  onClose: () => void;
}) {
  const t = useT();
  const archived = useArchivedDirectory(orgId);
  const restore = useArchiveEntry(orgId);
  const list = (archived.data?.entries ?? []).filter(
    (entry) => !spaceId || entry.space_id === spaceId,
  );
  return (
    <Dialog
      open
      onClose={onClose}
      width={520}
      title={t("agents.archivedList.title")}
      description={t("agents.archivedList.desc")}
    >
      {archived.isLoading ? (
        <Skeleton className="h-12 w-full" />
      ) : list.length === 0 ? (
        <p className="py-6 text-center text-body-sm text-fg-muted">
          {t("agents.archivedList.empty")}
        </p>
      ) : (
        <ul className="flex flex-col">
          {list.map((entry) => (
            <li
              key={`${entry.type}:${entry.id}`}
              className="flex items-center gap-3 border-b border-line-soft py-2.5 last:border-b-0"
            >
              {entry.type === "crew" ? (
                <CrewAvatar name={entry.name} members={[]} size={32} />
              ) : (
                <AgentAvatar
                  name={entry.name}
                  avatar={entry.avatar}
                  size={32}
                />
              )}
              <div className="flex min-w-0 flex-1 flex-col">
                <span className="truncate text-body font-medium">
                  {entry.name}
                </span>
                <span className="truncate text-caption text-fg-muted">
                  {entry.type === "crew"
                    ? t("agents.archivedList.crewMeta", {
                        space: entry.space_name,
                      })
                    : [entry.title, entry.space_name]
                        .filter(Boolean)
                        .join(" · ")}
                </span>
              </div>
              <Button
                variant="outline"
                size="28"
                loading={
                  restore.isPending && restore.variables?.target.id === entry.id
                }
                onClick={() =>
                  restore.mutate(
                    { target: entry, archived: false },
                    {
                      onSettled: () => void archived.refetch(),
                    },
                  )
                }
              >
                {t("agents.archivedList.restore")}
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  );
}
