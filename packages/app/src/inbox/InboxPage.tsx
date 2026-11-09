import { useQuery } from "@tanstack/react-query";
import { memo, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import type { InboxGroup, InboxListQuery, InboxSection } from "@openfox/api";
import { useI18n, useT, type MessageKey } from "@openfox/i18n";
import {
  Avatar,
  Button,
  Checkbox,
  ConfirmDialog,
  Icon,
  IconButton,
  Popover,
  Segmented,
  Spinner,
  TextInput,
  cn,
  type IconName,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import { conversationPath } from "../conversations/ChatPanel";
import { directChatPath } from "../conversations/DirectChat";
import { withChatRail } from "../conversations/chatNavigation";
import { useOpenTaskPage } from "../tasks/navigation";
import { LoadMore, useTaskParam } from "../tasks/parts";
import { formatRelativeTime } from "../tasks/primitives/dates";
import { TaskTypeIcon } from "../tasks/primitives/TaskTypeIcon";
import { StatusIcon } from "../tasks/primitives/StatusIcon";
import { TaskState } from "../tasks/TaskWorkspace";
import {
  useInboxAction,
  useInboxBulk,
  useInboxList,
  useInboxStats,
  type InboxAction,
} from "./queries";
import { useOfferDecision } from "../conversations/OfferCard";
import { SnoozeMenu } from "./SnoozeMenu";

type Importance = "all" | "primary" | "other";

const SECTIONS: Array<{ id: InboxSection; icon: IconName }> = [
  { id: "inbox", icon: "inbox" },
  { id: "later", icon: "clock" },
  { id: "cleared", icon: "check-check" },
];

/** 收件箱 /o/:orgId/inbox；使用三分区布局。 */
export function InboxPage() {
  const t = useT();
  const api = useApi();
  const navigate = useNavigate();
  const { orgId = "" } = useParams();
  const taskParam = useTaskParam();
  const openTaskPage = useOpenTaskPage();
  const [section, setSection] = useState<InboxSection>("inbox");
  const [importance, setImportance] = useState<Importance>("all");
  const [spaceIds, setSpaceIds] = useState<string[]>([]);
  const [spaceMode, setSpaceMode] = useState<"is" | "is_not">("is");
  const [q, setQ] = useState("");
  const [searching, setSearching] = useState(false);
  const [spaceOpen, setSpaceOpen] = useState(false);
  const [confirmClear, setConfirmClear] = useState(false);
  const spaceAnchor = useRef<HTMLButtonElement | null>(null);

  const stats = useInboxStats(orgId);
  const spaces = useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
  });
  const params = useMemo<InboxListQuery>(
    () => ({
      section,
      importance: importance === "all" ? undefined : importance,
      space_ids: spaceIds.length ? spaceIds : undefined,
      space_mode:
        spaceIds.length && spaceMode === "is_not" ? "is_not" : undefined,
      q: q.trim() || undefined,
    }),
    [section, importance, spaceIds, spaceMode, q],
  );
  const pending = useQuery({
    queryKey: keys.conversationOffers(orgId),
    queryFn: () => api.conversations.pendingOffers(orgId),
    enabled: !!orgId,
  });
  const offers =
    section === "inbox" && importance !== "other"
      ? (pending.data ?? []).filter(
          (o) =>
            (!spaceIds.length ||
              spaceIds.includes(o.space_id) === (spaceMode === "is")) &&
            [o.sender, o.title, o.note].some((v) =>
              v.toLocaleLowerCase().includes(q.trim().toLocaleLowerCase()),
            ),
        )
      : [];
  const decide = useOfferDecision(orgId);
  const list = useInboxList(orgId, params);
  const action = useInboxAction(orgId);
  const bulk = useInboxBulk(orgId);
  const groups = useMemo(
    () => list.data?.pages.flatMap((p) => p.groups) ?? [],
    [list.data],
  );
  const counts = stats.data ?? list.data?.pages[0]?.counts;
  const filtered =
    importance !== "all" || spaceIds.length > 0 || q.trim() !== "";
  // 「全部清理」的条数：当前筛选下的组数（全部加载时用实际行数，否则用分区计数）
  const clearCount = list.hasNextPage
    ? (list.data?.pages[0]?.counts.inbox ?? groups.length)
    : groups.length;

  const run = (kind: InboxAction, group: InboxGroup, snoozedUntil?: Date) =>
    action.mutate({
      action: kind,
      section,
      groupIds: [group.id],
      snoozedUntil: snoozedUntil?.toISOString(),
    });

  const openGroup = (group: InboxGroup) => {
    if (group.unread && section !== "cleared") run("read", group);
    // 通知同时带任务和会话：进任务全页并打开该会话的 AI 面板
    if (group.task_display_id && group.conversation_id)
      openTaskPage(group.task_display_id, group.conversation_id);
    else if (group.task_display_id) taskParam.open(group.task_display_id);
    else if (group.direct_chat_id) {
      const link = withChatRail(
        directChatPath(orgId, group.direct_chat_id),
        "home",
      );
      navigate(
        group.direct_message_id
          ? link +
              (link.includes("?") ? "&" : "?") +
              "message=" +
              encodeURIComponent(group.direct_message_id)
          : link,
      );
    } else if (!group.conversation_id && group.automation_id && group.space_id)
      navigate(
        `/o/${orgId}/spaces/${group.space_id}/automations/${group.automation_id}`,
      );
    else if (group.conversation_id)
      navigate(
        withChatRail(conversationPath(orgId, group.conversation_id), "home"),
      );
  };

  const unread = counts?.inbox_unread ?? 0;
  const important = counts?.inbox_important_unread ?? 0;
  const other = counts?.inbox_other_unread ?? 0;
  const segmentLabel = (key: MessageKey, n: number) =>
    section === "inbox" && n > 0 ? `${t(key)} ${n}` : t(key);

  const emptyTitle = filtered
    ? q.trim() || spaceIds.length
      ? t("inbox.empty.filtered")
      : importance === "primary"
        ? t("inbox.empty.primary")
        : t("inbox.empty.other")
    : t(`inbox.empty.${section}`);

  return (
    <div className="flex h-full min-w-0 flex-col bg-surface-canvas">
      <h1 className="sr-only">{t("inbox.title")}</h1>
      <div
        role="tablist"
        aria-label={t("inbox.title")}
        className="flex shrink-0 gap-2 border-b border-line-default p-2"
      >
        {SECTIONS.map((item) => {
          const selected = item.id === section;
          const count = item.id === "inbox" ? unread : (counts?.[item.id] ?? 0);
          const sub =
            item.id === "inbox"
              ? t("inbox.unreadCount", { count: unread })
              : t("inbox.itemCount", { count: counts?.[item.id] ?? 0 });
          return (
            <button
              key={item.id}
              type="button"
              role="tab"
              aria-selected={selected}
              onClick={() => {
                setSection(item.id);
                setImportance("all");
              }}
              className={cn(
                "relative flex h-15 min-w-0 flex-1 items-center gap-4 rounded-lg px-4 text-left transition-colors sm:max-w-64",
                selected
                  ? "bg-surface-selected text-fg-default after:absolute after:-bottom-[9px] after:inset-x-0.5 after:h-0.5 after:rounded-full after:bg-fg-default"
                  : "text-fg-muted hover:bg-surface-hover",
              )}
            >
              <Icon name={item.icon} />
              <span className="flex min-w-0 flex-col">
                <span className="text-body font-medium">
                  {t(`inbox.sections.${item.id}`)}
                </span>
                {count > 0 && (
                  <span className="text-body-sm text-fg-muted tabular-nums">
                    {sub}
                  </span>
                )}
              </span>
            </button>
          );
        })}
      </div>

      <div
        role="toolbar"
        aria-label={t("inbox.toolbar")}
        className="flex min-h-11 shrink-0 flex-wrap items-center gap-2 px-4 py-2"
      >
        <IconButton
          ref={spaceAnchor}
          label={t("inbox.spaceFilter")}
          icon={<Icon name="list-filter" />}
          variant="round"
          size="28"
          className="bg-surface-canvas"
          active={spaceOpen || spaceIds.length > 0}
          aria-expanded={spaceOpen}
          onClick={() => setSpaceOpen((v) => !v)}
        />
        <Popover
          open={spaceOpen}
          onOpenChange={setSpaceOpen}
          anchorRef={spaceAnchor}
          aria-label={t("inbox.spaceFilter")}
          className="w-[260px] p-2"
        >
          <Segmented
            label={t("inbox.spaceFilter")}
            value={spaceMode}
            onChange={setSpaceMode}
            className="mb-2 h-8 w-full"
            options={[
              { value: "is", label: t("inbox.spaceIs") },
              { value: "is_not", label: t("inbox.spaceIsNot") },
            ]}
          />
          <label className="flex h-8 items-center gap-2 rounded-md px-2 text-body hover:bg-surface-hover">
            <Checkbox
              checked={spaceIds.length === 0}
              onChange={() => setSpaceIds([])}
            />
            {t("inbox.allSpaces")}
          </label>
          {(spaces.data ?? []).map((s) => (
            <label
              key={s.id}
              className="flex h-8 items-center gap-2 rounded-md px-2 text-body hover:bg-surface-hover"
            >
              <Checkbox
                checked={spaceIds.includes(s.id)}
                onChange={() =>
                  setSpaceIds((prev) =>
                    prev.includes(s.id)
                      ? prev.filter((x) => x !== s.id)
                      : [...prev, s.id],
                  )
                }
              />
              <Avatar name={s.name} kind="agent" size={16} />
              <span className="truncate">{s.name}</span>
            </label>
          ))}
        </Popover>
        {section === "inbox" && (
          <>
            <span aria-hidden="true" className="h-4 w-px bg-line-default" />
            <div
              role="radiogroup"
              aria-label={t("inbox.importance")}
              className="flex items-center gap-1"
            >
              {[
                {
                  value: "all",
                  label: segmentLabel("inbox.importanceAll", unread),
                },
                {
                  value: "primary",
                  label: segmentLabel("inbox.importancePrimary", important),
                },
                {
                  value: "other",
                  label: segmentLabel("inbox.importanceOther", other),
                },
              ].map((option) => (
                <button
                  key={option.value}
                  type="button"
                  role="radio"
                  aria-checked={importance === option.value}
                  onClick={() => setImportance(option.value as Importance)}
                  className={cn(
                    "h-7 rounded-full border border-line-default px-3 text-body-sm font-medium tabular-nums transition-colors",
                    importance === option.value
                      ? "bg-surface-selected text-fg-default"
                      : "text-fg-muted hover:bg-surface-hover",
                  )}
                >
                  {option.label}
                </button>
              ))}
            </div>
          </>
        )}
        <span className="ml-auto" />
        {searching || q ? (
          <TextInput
            autoFocus
            aria-label={t("inbox.search")}
            placeholder={t("inbox.searchPlaceholder")}
            leading={<Icon name="search" />}
            value={q}
            maxLength={200}
            className="h-7 w-[220px] rounded-full"
            onChange={(event) => setQ(event.target.value)}
            onBlur={() => {
              if (!q) setSearching(false);
            }}
          />
        ) : (
          <IconButton
            label={t("inbox.search")}
            icon={<Icon name="search" />}
            variant="round"
            size="28"
            className="bg-surface-canvas"
            onClick={() => setSearching(true)}
          />
        )}
        <IconButton
          label={t("inbox.markAllRead")}
          icon={<Icon name="mail-check" />}
          variant="round"
          size="28"
          className="bg-surface-canvas"
          disabled={
            section === "cleared" || groups.length === 0 || bulk.isPending
          }
          onClick={() =>
            bulk.mutate({
              operation: "mark_read",
              section,
              importance: params.importance,
              space_ids: params.space_ids,
              space_mode: params.space_mode,
              q: params.q,
            })
          }
        />
        <Button
          variant="outline"
          size="28"
          leading={<Icon name="check-check" />}
          className="rounded-full bg-surface-canvas text-fg-muted"
          style={{ borderRadius: 999 }}
          disabled={
            section !== "inbox" || groups.length === 0 || bulk.isPending
          }
          onClick={() => setConfirmClear(true)}
        >
          {t("inbox.clearAll")}
        </Button>
      </div>

      {list.isLoading ? (
        <TaskState busy icon={<Spinner />} title={t("common.loading")} />
      ) : list.error ? (
        <TaskState
          icon={<Icon name="circle-alert" size={24} className="text-danger" />}
          title={t("inbox.loadFailed")}
          actions={
            <Button variant="outline" onClick={() => void list.refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : groups.length === 0 && offers.length === 0 ? (
        <div
          role="status"
          className="flex flex-1 flex-col items-center gap-4 px-6 pt-16 text-fg-muted"
        >
          <Icon
            name={section === "cleared" ? "check-check" : "inbox"}
            size={32}
            className="text-fg-subtle"
          />
          <p className="text-body">{emptyTitle}</p>
        </div>
      ) : (
        <ul
          aria-label={t("inbox.list")}
          className="min-h-0 flex-1 overflow-y-auto px-4 pb-3"
        >
          {offers.map((o) => (
            <li
              key={o.id}
              className="flex items-center gap-2 border-b border-line-soft px-2 py-2"
            >
              <Avatar name={o.sender} size={24} />
              <div className="min-w-0 flex-1">
                <p className="truncate text-body font-medium" title={o.title}>
                  {o.title}
                </p>
                <p className="break-words text-body-sm text-fg-muted">
                  {t(
                    o.kind === "TRANSFER"
                      ? "tasks.detail.transferReceived"
                      : "tasks.detail.inviteReceived",
                    { name: o.sender },
                  )}
                  {o.transfer_assignee && (
                    <span> · {t("tasks.detail.assigneeIncluded")}</span>
                  )}
                </p>
                {o.note && (
                  <p className="whitespace-pre-wrap break-words text-body-sm">
                    {o.note}
                  </p>
                )}
                {o.assistant && o.kind === "TRANSFER" && (
                  <p className="mt-1 text-body-sm text-fg-muted">
                    {t("chat.assistant.transferNotice")}
                  </p>
                )}
              </div>
              <Button
                size="28"
                variant="outline"
                disabled={decide.isPending}
                onClick={() =>
                  decide.mutate({
                    id: o.id,
                    accept: false,
                    conversation: o.conversation_id,
                  })
                }
              >
                {t("tasks.detail.offerDecline")}
              </Button>
              <Button
                size="28"
                disabled={decide.isPending}
                onClick={() =>
                  decide.mutate({
                    id: o.id,
                    accept: true,
                    conversation: o.conversation_id,
                  })
                }
              >
                {t("tasks.detail.offerAccept")}
              </Button>
            </li>
          ))}
          {groups.map((group) => (
            <Row
              key={group.id}
              group={group}
              section={section}
              onOpen={openGroup}
              onAction={run}
            />
          ))}
          <li>
            <LoadMore
              hasMore={Boolean(list.hasNextPage)}
              loading={list.isFetchingNextPage}
              onLoad={() => void list.fetchNextPage()}
            />
          </li>
        </ul>
      )}

      <ConfirmDialog
        open={confirmClear}
        title={t("inbox.clearAllTitle")}
        description={t("inbox.clearAllDesc", { count: clearCount })}
        confirmLabel={t("inbox.clearAll")}
        loading={bulk.isPending}
        onCancel={() => setConfirmClear(false)}
        onConfirm={() =>
          bulk.mutate(
            {
              operation: "clear_all",
              section: "inbox",
              importance: params.importance,
              space_ids: params.space_ids,
              space_mode: params.space_mode,
              q: params.q,
            },
            { onSettled: () => setConfirmClear(false) },
          )
        }
      />
    </div>
  );
}

const Row = memo(function Row({
  group,
  section,
  onOpen,
  onAction,
}: {
  group: InboxGroup;
  section: InboxSection;
  onOpen: (group: InboxGroup) => void;
  onAction: (action: InboxAction, group: InboxGroup, at?: Date) => void;
}) {
  const t = useT();
  const { locale, formatDate } = useI18n();
  const [snoozeOpen, setSnoozeOpen] = useState(false);
  const snoozeAnchor = useRef<HTMLButtonElement | null>(null);
  const cleared = section === "cleared";
  const title =
    group.task_title ?? group.actor_name ?? t(`inbox.kinds.${group.kind}`);
  const event = t(`inbox.events.${group.kind}`);
  // 服务端的固定 REPLY 摘要与事件文字重复；真实回复和错误摘要原样保留。
  const summary =
    group.kind === "DIRECT_MESSAGE" ||
    (group.kind === "REPLY" &&
      group.safe_summary === "An Agent replied in a conversation.")
      ? ""
      : group.safe_summary;
  const time =
    section === "later" && group.snoozed_until
      ? t("inbox.wakeAt", {
          time: formatDate(group.snoozed_until, {
            month: "short",
            day: "numeric",
            hour: "2-digit",
            minute: "2-digit",
          }),
        })
      : formatRelativeTime(group.latest_at, locale);
  const actions: Array<{ id: InboxAction; icon: IconName; label: MessageKey }> =
    section === "inbox"
      ? [
          { id: "snooze", icon: "clock", label: "inbox.actions.snooze" },
          group.unread
            ? { id: "read", icon: "mail-open", label: "inbox.actions.read" }
            : { id: "unread", icon: "mail", label: "inbox.actions.unread" },
          { id: "clear", icon: "check", label: "inbox.actions.clear" },
        ]
      : section === "later"
        ? [
            { id: "read", icon: "mail-open", label: "inbox.actions.read" },
            { id: "unsnooze", icon: "undo", label: "inbox.actions.unsnooze" },
          ]
        : [{ id: "unclear", icon: "undo", label: "inbox.actions.unclear" }];
  return (
    <li className="group relative flex h-[var(--density-table-row,40px)] items-center gap-2 border-b border-line-soft px-2 first:border-t hover:bg-surface-hover">
      <button
        type="button"
        onClick={() => onOpen(group)}
        aria-label={`${group.task_display_id ?? ""} ${title}`.trim()}
        title={[title, event, summary].filter(Boolean).join(" · ")}
        className="absolute inset-0 rounded-sm outline-none focus-visible:shadow-focus"
      />
      <span
        role="img"
        aria-label={group.unread ? t("inbox.unreadDot") : t("inbox.readDot")}
        className={cn(
          "pointer-events-none absolute -left-2 size-1 rounded-full",
          group.unread && !cleared ? "bg-info" : "bg-info/10",
        )}
      />
      <span className="pointer-events-none relative grid min-w-0 flex-1 grid-cols-[minmax(0,1fr)_minmax(0,1fr)] items-center gap-4">
        <span className="flex min-w-0 items-center gap-2">
          {group.task_status_category && (
            <StatusIcon category={group.task_status_category} labelled />
          )}
          {group.task_id && (
            <TaskTypeIcon
              icon={group.task_type_icon}
              className="text-fg-muted"
            />
          )}
          {group.task_display_id && (
            <span
              title={group.task_display_id.toUpperCase()}
              className="max-w-[35%] shrink-0 truncate text-caption text-fg-muted tabular-nums"
            >
              {group.task_display_id.toUpperCase()}
            </span>
          )}
          <span
            title={title}
            className={cn(
              "min-w-0 truncate text-body",
              group.unread && !cleared ? "font-medium" : "text-fg-secondary",
            )}
          >
            {title}
          </span>
        </span>
        <span
          className={cn(
            "flex min-w-0 items-center gap-2 text-body-sm text-fg-muted",
            cleared && "opacity-60",
          )}
        >
          {group.actor_name && group.task_title && (
            <Avatar
              name={group.actor_name}
              kind={group.actor_kind === "AGENT" ? "agent" : "user"}
              size={16}
              className={cleared ? "grayscale" : undefined}
            />
          )}
          <span
            title={[group.task_title && group.actor_name, event]
              .filter(Boolean)
              .join(" ")}
            className={cn(
              "min-w-0 truncate",
              summary && "max-w-[50%] shrink-0",
            )}
          >
            {group.task_title && group.actor_name && (
              <span>{group.actor_name} </span>
            )}
            {event}
          </span>
          {summary && (
            <span
              title={summary}
              className={cn(
                "min-w-0 truncate rounded-sm border px-1",
                group.kind === "RUN_FAILED" && !cleared
                  ? "border-danger-border bg-danger-soft text-danger"
                  : "border-line-default bg-surface-muted",
              )}
            >
              {summary}
            </span>
          )}
        </span>
      </span>
      <span className="pointer-events-none relative flex w-32 shrink-0 items-center justify-end gap-2 text-caption text-fg-muted tabular-nums">
        {group.count > 1 && (
          <span className="shrink-0 text-caption text-fg-muted">
            +{group.count - 1}
          </span>
        )}
        <time
          dateTime={
            section === "later" && group.snoozed_until
              ? group.snoozed_until
              : group.latest_at
          }
          className="w-24 shrink-0 text-right text-caption text-fg-muted group-focus-within:invisible group-hover:invisible"
        >
          {time}
        </time>
      </span>
      <span
        className={cn(
          "absolute right-2 hidden items-center gap-1 bg-surface-hover group-focus-within:flex group-hover:flex",
          snoozeOpen && "flex",
        )}
      >
        {actions.map((item) =>
          item.id === "snooze" ? (
            <IconButton
              key={item.id}
              ref={snoozeAnchor}
              label={t(item.label)}
              icon={<Icon name={item.icon} />}
              size="28"
              variant="outline"
              aria-expanded={snoozeOpen}
              onClick={() => setSnoozeOpen((v) => !v)}
            />
          ) : (
            <IconButton
              key={item.id}
              label={t(item.label)}
              icon={<Icon name={item.icon} />}
              size="28"
              variant="outline"
              onClick={() => onAction(item.id, group)}
            />
          ),
        )}
      </span>
      <SnoozeMenu
        open={snoozeOpen}
        onOpenChange={setSnoozeOpen}
        anchorRef={snoozeAnchor}
        onSnooze={(at) => onAction("snooze", group, at)}
      />
    </li>
  );
});
