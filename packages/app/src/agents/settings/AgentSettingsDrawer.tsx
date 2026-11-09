import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router-dom";

import type { AgentRunSummary, SpaceAgent } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  ConfirmDialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  Skeleton,
  Spinner,
  Tabs,
  Tooltip,
  cn,
} from "@openfox/ui";

import { formatDuration } from "../../conversations/steps/format";
import { isKnownWaitReason } from "../../conversations/steps/rows";
import { InlineEdit } from "../../settings/InlineEdit";
import { useOrgPeople } from "../../tasks/queries";
import { formatRelativeTime } from "../../tasks/primitives/dates";
import { AgentAvatar, AvatarPicker } from "../AgentAvatar";
import { useArchiveEntry, useCopyAgent, useFavoriteEntry } from "../actions";
import { chatPath } from "../AgentsPage";
import { AssignTaskPalette } from "../AssignTaskPalette";
import { AVATAR_COLORS, readAvatar, writeAvatar } from "../model";
import { useAgentSettingsParam, type AgentPanelTab } from "../params";
import {
  useAgent,
  useAgentRuns,
  useAgentStats,
  useDevices,
  useEntitySpace,
  useMe,
  useSpaces,
} from "../queries";
import { AccessDialog } from "./AccessDialog";
import {
  InstructionsSection,
  RunSection,
  TriggersSection,
} from "./BasicSections";
import { AgentTools } from "./AgentTools";
import { GitSection } from "./GitSection";
import { DangerCard, SettingsCard, Stats, formatTokens } from "./parts";
import { RuntimeSection } from "./RuntimeSection";
import { SkillsSection } from "./SkillsSection";
import { useAgentSkills } from "../../skills/queries";
import { useAgentWrites } from "./writes";
import { RunTranscriptDialog } from "./RunTranscriptDialog";
import { AgentTaskWorkbench } from "./AgentTaskWorkbench";

const SECTIONS = [
  {
    id: "instructions",
    icon: "notebook-pen",
    title: "agents.sections.instructions.title",
    desc: "agents.sections.instructions.desc",
  },
  {
    id: "triggers",
    icon: "waypoints",
    title: "agents.sections.triggers.title",
    desc: "agents.sections.triggers.desc",
  },
  {
    id: "tools",
    icon: "plug",
    title: "agents.sections.tools.title",
    desc: "agents.sections.tools.desc",
  },
  {
    id: "run",
    icon: "list-checks",
    title: "agents.sections.run.title",
    desc: "agents.sections.run.desc",
  },
  {
    id: "runtime",
    icon: "monitor",
    title: "agents.sections.runtime.title",
    desc: "agents.sections.runtime.desc",
  },
  {
    id: "git",
    icon: "folder-git-2",
    title: "agents.sections.git.title",
    desc: "agents.sections.git.desc",
  },
  {
    id: "skills",
    icon: "sparkles",
    title: "agents.sections.skills.title",
    desc: "agents.sections.skills.desc",
  },
] as const;

type SectionId = (typeof SECTIONS)[number]["id"];
type Tab = AgentPanelTab;

/** 全局挂载：地址栏带 ?agentSettings=<id> 时打开设置抽屉。 */
export function AgentSettingsHost({ orgId }: { orgId: string }) {
  const param = useAgentSettingsParam();
  if (!param.id) return null;
  return (
    <AgentSettingsDrawer
      key={param.id}
      orgId={orgId}
      agentId={param.id}
      onClose={param.close}
    />
  );
}

/** 抽屉右、下边距（与收起时的 right-[9px] / bottom-[9px] 一致）。 */
const DRAWER_GAP = 9;

/**
 * 展开时面板从主内容区（OrgLayout 的 main[data-org-main]）左缘开始铺，
 * 主导航图标栏和模块侧栏不被遮住、仍可点击。
 * 返回主内容区的左缘；找不到或还没布局（测试环境）时返回 null，由调用方按满宽兜底。
 */
function useMainLeft(active: boolean): number | null {
  const [left, setLeft] = useState<number | null>(null);
  useLayoutEffect(() => {
    if (!active) return;
    const main = document.querySelector<HTMLElement>("main[data-org-main]");
    if (!main) return;
    const update = () => {
      const value = main.getBoundingClientRect().left;
      setLeft(value > 0 ? value : null);
    };
    update();
    // 侧栏收起 / 展开会改变主内容区的宽度，跟着更新
    const observer =
      typeof ResizeObserver === "undefined" ? null : new ResizeObserver(update);
    observer?.observe(main);
    window.addEventListener("resize", update);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", update);
    };
  }, [active]);
  return left;
}

/**
 * Agent 设置抽屉：宽 720 右侧滑入，非模态，可展开为全屏；
 * 全屏时有「设置 / 任务 / 运行历史」页签，设置页签顶部有锚点胶囊。
 * 展开状态与页签写进地址（?agentPanel=full&agentTab=tasks|runs，replace），刷新后保留；
 * 展开时从主内容区左缘开始铺，不遮住主导航与侧栏。
 */
export function AgentSettingsDrawer({
  orgId,
  agentId,
  onClose,
}: {
  orgId: string;
  agentId: string;
  onClose: () => void;
}) {
  const t = useT();
  const location = useEntitySpace(orgId, "agent", agentId);
  const agentQuery = useAgent(orgId, location.spaceId, agentId);
  const agent = agentQuery.data;
  const param = useAgentSettingsParam();
  const { expanded, tab, runId } = param.panel;
  const setTab = (next: Tab) => param.setPanel({ expanded: true, tab: next });
  const mainLeft = useMainLeft(expanded);
  const [entered, setEntered] = useState(false);
  const dirty = useRef(new Set<string>());
  const [pending, setPending] = useState<null | (() => void)>(null);
  const setDirty = useCallback((section: string, value: boolean) => {
    if (value) dirty.current.add(section);
    else dirty.current.delete(section);
  }, []);
  /** 有未保存修改时先确认 */
  const guard = (action: () => void) => {
    if (dirty.current.size > 0) setPending(() => action);
    else action();
  };

  useEffect(() => {
    const frame = requestAnimationFrame(() => setEntered(true));
    return () => cancelAnimationFrame(frame);
  }, []);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return;
      // 弹窗、菜单打开时由它们自己处理 Esc；上层还开着任务抽屉（非模态 dialog）时先关它，这里不动
      if (
        document.querySelector(
          '[aria-modal="true"],[role="menu"],[role="listbox"],[role="dialog"]',
        )
      )
        return;
      guard(onClose);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  });

  const notFound =
    location.notFound ||
    (agentQuery.isError &&
      (agentQuery.error as { status?: number }).status === 404);

  return createPortal(
    <aside
      role="complementary"
      aria-label={t("agents.drawer.label", { name: agent?.name ?? "" })}
      style={{
        width: expanded
          ? `calc(100vw - ${(mainLeft ?? DRAWER_GAP) + DRAWER_GAP}px)`
          : 720,
      }}
      className={cn(
        "fixed top-11 right-[9px] bottom-[9px] z-30 flex max-w-[calc(100vw-18px)] flex-col overflow-hidden rounded-xl border border-line-default bg-surface-card shadow-dialog",
        "transition-[transform,width] duration-300 ease-drawer",
        entered ? "translate-x-0" : "translate-x-[calc(100%+9px)]",
      )}
    >
      <div className="flex h-11 shrink-0 items-center gap-1 px-3">
        <IconButton
          label={t("agents.drawer.close")}
          icon={<Icon name="x" />}
          size="28"
          onClick={() => guard(onClose)}
        />
        <IconButton
          label={
            expanded ? t("agents.drawer.collapse") : t("agents.drawer.expand")
          }
          icon={
            <Icon name={expanded ? "minimize-2" : "maximize-2"} size={14} />
          }
          size="28"
          onClick={() =>
            // 收起时回到设置页签（去掉 agentPanel / agentTab）
            param.setPanel({ expanded: !expanded })
          }
        />
        {expanded && (
          <Tabs
            className="mx-auto border-b-0"
            label={t("agents.drawer.tabs.label")}
            value={tab}
            onChange={(next) => guard(() => setTab(next as Tab))}
            tabs={[
              { id: "settings", label: t("agents.drawer.tabs.settings") },
              { id: "tasks", label: t("agents.drawer.tabs.tasks") },
              { id: "runs", label: t("agents.drawer.tabs.runs") },
            ]}
          />
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {notFound ? (
          <p className="p-10 text-center text-body text-fg-muted">
            {t("agents.drawer.notFound")}
          </p>
        ) : agentQuery.isError ? (
          <div className="flex flex-col items-center gap-2 p-10">
            <p className="text-body text-fg-muted">
              {t("agents.drawer.loadFailed")}
            </p>
            <Button variant="outline" onClick={() => void agentQuery.refetch()}>
              {t("common.retry")}
            </Button>
          </div>
        ) : !agent ? (
          <div aria-busy="true" className="flex flex-col gap-3 p-6">
            <Skeleton className="size-16 rounded-full" />
            <Skeleton className="h-6 w-1/2" />
            <Skeleton className="h-10 w-full" />
          </div>
        ) : tab === "tasks" ? (
          // 任务工作台铺满抽屉宽度（看板 / 表格需要横向空间）
          <div className="flex h-full min-h-0 flex-col">
            <AgentTaskWorkbench orgId={orgId} agent={agent} />
          </div>
        ) : (
          <div
            className={cn(
              "mx-auto flex flex-col gap-4 px-6 pb-8",
              expanded && "max-w-[760px]",
            )}
          >
            {tab === "settings" && (
              <SettingsTab
                orgId={orgId}
                agent={agent}
                expanded={expanded}
                initialRunSection={param.initialRunSection}
                setDirty={setDirty}
                onShowRuns={() => setTab("runs")}
              />
            )}
            {tab === "runs" && (
              <AgentRuns
                orgId={orgId}
                agent={agent}
                runId={runId}
                onOpenRun={(id) =>
                  param.setPanel({ expanded: true, tab: "runs", runId: id })
                }
                onCloseRun={() => setTab("runs")}
              />
            )}
          </div>
        )}
      </div>
      <ConfirmDialog
        open={pending !== null}
        title={t("agents.drawer.unsaved")}
        description={t("agents.drawer.unsavedDesc")}
        confirmLabel={t("agents.drawer.discard")}
        cancelLabel={t("agents.drawer.keepEditing")}
        danger
        onCancel={() => setPending(null)}
        onConfirm={() => {
          const action = pending;
          dirty.current.clear();
          setPending(null);
          action?.();
        }}
      />
    </aside>,
    document.body,
  );
}

function SettingsTab({
  orgId,
  agent,
  expanded,
  initialRunSection,
  setDirty,
  onShowRuns,
}: {
  orgId: string;
  agent: SpaceAgent;
  expanded: boolean;
  initialRunSection: boolean;
  setDirty: (section: string, dirty: boolean) => void;
  onShowRuns: () => void;
}) {
  const t = useT();
  const writes = useAgentWrites(orgId, agent);
  const devices = useDevices(orgId);
  const attachedSkills = useAgentSkills(orgId, agent.space_id, agent.id);
  const [open, setOpen] = useState<Set<SectionId>>(
    () => new Set(initialRunSection ? ["run"] : []),
  );
  useEffect(() => {
    if (!initialRunSection) return;
    setOpen((prev) => new Set(prev).add("run"));
    const frame = requestAnimationFrame(() =>
      document
        .getElementById("agent-section-run")
        ?.scrollIntoView?.({ block: "start" }),
    );
    return () => cancelAnimationFrame(frame);
  }, [initialRunSection]);
  const toggle = (id: SectionId) =>
    setOpen((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const jump = (id: SectionId) => {
    setOpen((prev) => new Set(prev).add(id));
    requestAnimationFrame(() =>
      document
        .getElementById(`agent-section-${id}`)
        ?.scrollIntoView({ block: "start" }),
    );
  };
  const device = devices.data?.find((d) => d.id === agent.config?.computer_id);
  const props = { orgId, agent, writes, setDirty };
  const body: Record<SectionId, ReactNode> = {
    instructions: <InstructionsSection {...props} />,
    triggers: <TriggersSection {...props} />,
    tools: <AgentTools orgId={orgId} agent={agent} />,
    run: <RunSection {...props} deviceSlots={device?.max_slots ?? null} />,
    runtime: <RuntimeSection {...props} />,
    git: <GitSection {...props} />,
    skills: <SkillsSection {...props} />,
  };
  const summary: Partial<Record<SectionId, string>> = {
    instructions: agent.config?.instructions ? undefined : t("agents.none"),
    skills: attachedSkills.data?.length
      ? t("skills.agent.count", { count: attachedSkills.data.length })
      : t("agents.none"),
  };

  return (
    <>
      {expanded && (
        <nav
          aria-label={t("agents.drawer.anchors")}
          className="sticky top-0 z-10 -mx-6 flex flex-wrap items-center gap-2 border-b border-line-default bg-surface-card px-6 py-2"
        >
          <AgentAvatar name={agent.name} avatar={agent.avatar} size={28} />
          {SECTIONS.map((section) => (
            <button
              key={section.id}
              type="button"
              onClick={() => jump(section.id)}
              className="h-7 rounded-full border border-line-default px-3 text-caption hover:bg-surface-hover"
            >
              {t(section.title)}
            </button>
          ))}
        </nav>
      )}
      <Profile
        orgId={orgId}
        agent={agent}
        writes={writes}
        onShowRuns={onShowRuns}
      />
      {SECTIONS.map((section) => (
        <SettingsCard
          key={section.id}
          id={`agent-section-${section.id}`}
          icon={section.icon}
          title={t(section.title)}
          desc={t(section.desc)}
          summary={summary[section.id]}
          open={open.has(section.id)}
          onToggle={() => toggle(section.id)}
        >
          {body[section.id]}
        </SettingsCard>
      ))}
      <ArchiveCard orgId={orgId} agent={agent} />
    </>
  );
}

function Profile({
  orgId,
  agent,
  writes,
  onShowRuns,
}: {
  orgId: string;
  agent: SpaceAgent;
  writes: ReturnType<typeof useAgentWrites>;
  onShowRuns: () => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const navigate = useNavigate();
  const me = useMe();
  const spaces = useSpaces(orgId);
  const stats = useAgentStats(agent, orgId);
  const favorite = useFavoriteEntry(orgId);
  const copy = useCopyAgent(orgId);
  const drawer = useAgentSettingsParam();
  const [editing, setEditing] = useState<
    null | "name" | "title" | "description"
  >(null);
  const [access, setAccess] = useState(false);
  const [assign, setAssign] = useState(false);
  const space = spaces.data?.find((s) => s.id === agent.space_id);
  // SpaceAgent 不带所有者邮箱，从组织成员里查
  const people = useOrgPeople(orgId);
  const ownerEmail =
    agent.owner_user_id === me.data?.id
      ? (me.data?.email ?? "")
      : (people.data?.find((p) => p.id === agent.owner_user_id)?.email ?? "");
  const owner =
    agent.owner_user_id === me.data?.id
      ? t("agents.you")
      : ownerEmail || agent.owner_user_id.slice(0, 8);
  const avatar = readAvatar(agent.avatar) ?? {
    kind: "icon" as const,
    value: "bot",
    color: AVATAR_COLORS[0],
  };
  const total =
    (stats.data?.input_tokens ?? 0) + (stats.data?.output_tokens ?? 0);

  return (
    <section className="flex flex-col gap-3 pt-2">
      <div className="flex items-center gap-4">
        <AvatarPicker
          key={`${orgId}:${agent.id}`}
          orgId={orgId}
          name={agent.name}
          value={avatar}
          disabled={writes.profilePending}
          onChange={(next) =>
            void writes.patchProfile({ avatar: writeAvatar(next) }, true)
          }
        />
        <Stats
          items={[
            {
              label: t("agents.drawer.stats.tasks"),
              value: stats.data?.task_count ?? "—",
            },
            {
              label: t("agents.drawer.stats.runs"),
              value: stats.data?.run_count ?? "—",
            },
            {
              label: t("agents.drawer.stats.tokens"),
              value: stats.data ? formatTokens(total) : "—",
            },
            {
              label: t("agents.drawer.stats.lastRun"),
              value: stats.data?.last_run_at
                ? formatRelativeTime(stats.data.last_run_at, locale)
                : t("agents.drawer.stats.never"),
            },
          ]}
        />
      </div>
      <div className="flex min-w-0 items-center gap-1.5 text-title">
        {editing === "name" ? (
          <InlineEdit
            value={agent.name}
            label={t("agents.drawer.editName")}
            maxLength={255}
            onCommit={(name) => void writes.patchProfile({ name })}
            onDone={() => setEditing(null)}
            className="max-w-[240px]"
          />
        ) : (
          <button
            type="button"
            onClick={() => setEditing("name")}
            className="truncate font-bold hover:underline"
          >
            {agent.name}
          </button>
        )}
        <span className="text-fg-muted">-</span>
        {editing === "title" ? (
          <InlineEdit
            value={agent.title}
            label={t("agents.drawer.editTitle")}
            maxLength={64}
            placeholder={t("agents.drawer.titlePlaceholder")}
            onCommit={(title) => void writes.patchProfile({ title })}
            onDone={() => setEditing(null)}
            className="max-w-[240px]"
          />
        ) : (
          <button
            type="button"
            onClick={() => setEditing("title")}
            className="truncate text-fg-muted hover:underline"
          >
            {agent.title || t("agents.drawer.titlePlaceholder")}
          </button>
        )}
        {agent.archived_at && (
          <Badge tone="muted">{t("agents.drawer.archived")}</Badge>
        )}
      </div>
      {editing === "description" ? (
        <InlineEdit
          value={agent.description}
          label={t("agents.drawer.editDescription")}
          maxLength={10000}
          placeholder={t("agents.drawer.descriptionPlaceholder")}
          onCommit={(description) => void writes.patchProfile({ description })}
          onDone={() => setEditing(null)}
        />
      ) : (
        <button
          type="button"
          onClick={() => setEditing("description")}
          className="self-start text-left text-body text-fg-muted hover:underline"
        >
          {agent.description || t("agents.drawer.noDescription")}
        </button>
      )}
      <button
        type="button"
        aria-label={t("agents.drawer.access", {
          space: space?.name ?? "",
          visibility: t(`agents.visibility.${agent.visibility}`),
          owner,
        })}
        onClick={() => setAccess(true)}
        className="flex items-center gap-1.5 self-start rounded-md px-1 py-0.5 text-body hover:bg-surface-hover"
      >
        <Icon name="orbit" className="text-fg-muted" />
        <span>{space?.name ?? t("agents.unknownSpace")}</span>
        <span className="text-fg-muted">·</span>
        <span className="text-fg-muted">
          {t(`agents.visibility.${agent.visibility}`)}
        </span>
        <span className="text-fg-muted">·</span>
        <span>{owner}</span>
      </button>
      <div className="flex items-center gap-2 pt-1">
        <Button
          className="flex-1"
          leading={<Icon name="plus" />}
          disabled={!agent.allow_assign || Boolean(agent.archived_at)}
          onClick={() => setAssign(true)}
        >
          {t("agents.drawer.assign")}
        </Button>
        <Button
          variant="outline"
          className="flex-1"
          leading={<Icon name="message" />}
          disabled={!agent.allow_direct_chat || Boolean(agent.archived_at)}
          onClick={() =>
            navigate(chatPath(orgId, { type: "agent", id: agent.id }))
          }
        >
          {t("agents.drawer.chat")}
        </Button>
        <IconButton
          variant="outline"
          label={
            agent.favorite
              ? t("agents.card.unfavorite")
              : t("agents.card.favorite")
          }
          active={agent.favorite}
          icon={
            <Icon
              name="star"
              className={
                agent.favorite ? "fill-current text-warning" : undefined
              }
            />
          }
          onClick={() =>
            favorite.mutate({
              target: { type: "agent", id: agent.id },
              favorite: !agent.favorite,
            })
          }
        />
        <Menu
          label={t("agents.drawer.more")}
          placement="bottom-end"
          className="w-[200px]"
          trigger={({ ref, toggle, open }) => (
            <IconButton
              ref={ref}
              variant="outline"
              label={t("agents.drawer.more")}
              icon={<Icon name="ellipsis" />}
              active={open}
              onClick={toggle}
            />
          )}
        >
          <MenuItem icon={<Icon name="history" />} onSelect={onShowRuns}>
            {t("agents.drawer.viewRuns")}
          </MenuItem>
          <MenuItem
            icon={<Icon name="copy" />}
            onSelect={() =>
              void copy(agent).then((next) => {
                if (next) drawer.open(next.id, next.space_id);
              })
            }
          >
            {t("agents.drawer.copy")}
          </MenuItem>
          <Tooltip content={t("agents.comingSoon")}>
            <span className="block">
              <MenuItem icon={<Icon name="git-fork" />} disabled>
                {t("agents.drawer.clone")}
              </MenuItem>
            </span>
          </Tooltip>
          <MenuSeparator />
          <ArchiveMenuItem orgId={orgId} agent={agent} />
        </Menu>
      </div>
      {access && (
        <AccessDialog
          orgId={orgId}
          ownerEmail={ownerEmail || agent.owner_user_id}
          initial={{
            spaceId: agent.space_id,
            visibility: agent.visibility,
            ownerId: agent.owner_user_id,
          }}
          saving={writes.profilePending}
          onClose={() => setAccess(false)}
          onSave={(changes) =>
            void writes
              .patchProfile({
                ...(changes.spaceId ? { space_id: changes.spaceId } : {}),
                ...(changes.visibility
                  ? { visibility: changes.visibility }
                  : {}),
                ...(changes.ownerId ? { owner_user_id: changes.ownerId } : {}),
              })
              .then((saved) => {
                if (saved) setAccess(false);
              })
          }
        />
      )}
      {assign && (
        <AssignTaskPalette
          orgId={orgId}
          target={{
            kind: "agent",
            id: agent.id,
            name: agent.name,
            spaceId: agent.space_id,
          }}
          onClose={() => setAssign(false)}
        />
      )}
    </section>
  );
}

/** 归档确认（抽屉菜单与底部 DangerCard 共用） */
function useArchiveConfirm(orgId: string, agent: SpaceAgent) {
  const t = useT();
  const archive = useArchiveEntry(orgId);
  const [open, setOpen] = useState(false);
  const target = {
    type: "agent" as const,
    id: agent.id,
    space_id: agent.space_id,
    name: agent.name,
    version: agent.version,
  };
  const dialog = (
    <ConfirmDialog
      open={open}
      danger
      title={t("agents.archive.drawerTitle")}
      description={t("agents.archive.drawerDesc", { name: agent.name })}
      confirmLabel={t("agents.archive.confirm")}
      loading={archive.isPending}
      onCancel={() => setOpen(false)}
      onConfirm={() =>
        archive.mutate(
          { target, archived: true },
          { onSettled: () => setOpen(false) },
        )
      }
    />
  );
  return {
    ask: () => setOpen(true),
    restore: () => archive.mutate({ target, archived: false }),
    dialog,
  };
}

function ArchiveMenuItem({
  orgId,
  agent,
}: {
  orgId: string;
  agent: SpaceAgent;
}) {
  const t = useT();
  const confirm = useArchiveConfirm(orgId, agent);
  return (
    <>
      <MenuItem
        danger
        icon={<Icon name="archive" />}
        disabled={Boolean(agent.archived_at)}
        onSelect={confirm.ask}
      >
        {t("agents.drawer.archive")}
      </MenuItem>
      {confirm.dialog}
    </>
  );
}

function ArchiveCard({ orgId, agent }: { orgId: string; agent: SpaceAgent }) {
  const t = useT();
  const confirm = useArchiveConfirm(orgId, agent);
  return (
    <>
      <DangerCard
        desc={t("agents.sections.archive.desc")}
        archived={Boolean(agent.archived_at)}
        onArchive={confirm.ask}
        onRestore={confirm.restore}
      />
      {confirm.dialog}
    </>
  );
}

const RUN_TONE = {
  QUEUED: "muted",
  PREPARING: "info",
  RUNNING: "info",
  CANCEL_REQUESTED: "warning",
  SUCCEEDED: "success",
  FAILED: "danger",
  CANCELED: "muted",
} as const;

/**
 * 全屏「运行历史」页签：GET …/runs，游标分页。
 * 行 = 状态、会话标题 / 任务编号、开始时间、耗时、token。
 * 点行（主操作「查看记录」）在当前页弹出这次运行的执行记录（?agentRun=<runId>，刷新后保留）；
 * 行尾「打开会话」保留原来的跳转：打开对应会话并用 ?run= 定位到这次运行。
 */
function AgentRuns({
  orgId,
  agent,
  runId,
  onOpenRun,
  onCloseRun,
}: {
  orgId: string;
  agent: SpaceAgent;
  runId: string | null;
  onOpenRun: (runId: string) => void;
  onCloseRun: () => void;
}) {
  const t = useT();
  const runs = useAgentRuns(agent, orgId, true);
  const list = runs.data?.pages.flatMap((page) => page.runs) ?? [];
  const dialog = runId ? (
    <RunTranscriptDialog
      key={runId}
      orgId={orgId}
      runId={runId}
      summary={list.find((run) => run.id === runId)}
      onClose={onCloseRun}
    />
  ) : null;
  // 弹窗固定挂在同一位置：列表从加载中变成有数据时不重新挂载，避免重复请求第一页事件
  return (
    <>
      {runs.isLoading ? (
        <Spinner className="mx-auto my-6" />
      ) : list.length === 0 ? (
        <p className="py-10 text-center text-body text-fg-muted">
          {t("agents.runs.empty")}
        </p>
      ) : (
        <AgentRunList
          orgId={orgId}
          runs={runs}
          list={list}
          onOpenRun={onOpenRun}
        />
      )}
      {dialog}
    </>
  );
}

/** 运行历史列表（有数据时）。 */
function AgentRunList({
  orgId,
  runs,
  list,
  onOpenRun,
}: {
  orgId: string;
  runs: ReturnType<typeof useAgentRuns>;
  list: AgentRunSummary[];
  onOpenRun: (runId: string) => void;
}) {
  const t = useT();
  const { locale, formatDate } = useI18n();
  const navigate = useNavigate();
  const none = t("steps.history.none");
  const grid =
    "grid grid-cols-[88px_minmax(0,1fr)_120px_88px_64px] items-center gap-3 px-2";
  return (
    <div className="flex flex-col pt-2">
      <div aria-hidden className="flex items-center gap-1">
        <div className={cn(grid, "h-8 flex-1 text-caption text-fg-muted")}>
          <span>{t("steps.history.columns.state")}</span>
          <span>{t("steps.history.columns.conversation")}</span>
          <span>{t("steps.history.columns.startedAt")}</span>
          <span className="text-right">
            {t("steps.history.columns.duration")}
          </span>
          <span className="text-right">
            {t("steps.history.columns.tokens")}
          </span>
        </div>
        <span className="w-7 shrink-0" />
      </div>
      <ul className="flex flex-col">
        {list.map((run) => {
          const title = run.conversation_title || t("agents.runs.untitled");
          const startedAt = run.started_at ?? run.created_at;
          return (
            <li
              key={run.id}
              className="flex items-center gap-1 border-b border-line-soft last:border-b-0"
            >
              <button
                type="button"
                aria-label={t("agents.runTranscript.view", { title })}
                aria-haspopup="dialog"
                onClick={() => onOpenRun(run.id)}
                className={cn(
                  grid,
                  "h-11 min-w-0 flex-1 rounded-md text-left text-body hover:bg-surface-hover",
                )}
              >
                <span
                  title={
                    run.state === "QUEUED" && isKnownWaitReason(run.wait_reason)
                      ? t(`connect.waitReason.${run.wait_reason}`)
                      : undefined
                  }
                >
                  <Badge tone={RUN_TONE[run.state]}>
                    {t(`agents.runs.states.${run.state}`)}
                  </Badge>
                </span>
                <span className="flex min-w-0 items-center gap-2">
                  {run.task_display_id && (
                    <span className="shrink-0 font-mono text-caption text-fg-muted">
                      {run.task_display_id}
                    </span>
                  )}
                  <span className="truncate">{title}</span>
                </span>
                <span
                  className="truncate text-caption text-fg-muted"
                  title={formatDate(startedAt, {
                    dateStyle: "medium",
                    timeStyle: "short",
                  })}
                >
                  {formatRelativeTime(startedAt, locale)}
                </span>
                <span className="text-right text-caption text-fg-muted">
                  {run.duration_ms === null
                    ? none
                    : formatDuration(run.duration_ms, t)}
                </span>
                <span className="text-right text-caption text-fg-muted">
                  {run.usage
                    ? formatTokens(run.usage.input + run.usage.output)
                    : none}
                </span>
              </button>
              <Tooltip content={t("agents.runTranscript.openConversation")}>
                <IconButton
                  label={t("steps.history.open", { title })}
                  icon={<Icon name="message" size={14} />}
                  size="28"
                  onClick={() =>
                    navigate(
                      `/o/${orgId}/chat/${encodeURIComponent(run.conversation_id)}?run=${encodeURIComponent(run.id)}`,
                    )
                  }
                />
              </Tooltip>
            </li>
          );
        })}
      </ul>
      {runs.hasNextPage && (
        <Button
          variant="ghost"
          className="self-center"
          loading={runs.isFetchingNextPage}
          onClick={() => void runs.fetchNextPage()}
        >
          {t("agents.runs.loadMore")}
        </Button>
      )}
    </div>
  );
}
