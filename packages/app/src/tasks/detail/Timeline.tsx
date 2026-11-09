import { useInfiniteQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import type {
  Label,
  Task,
  TaskStatus,
  TimelineActivity,
  TimelineComment,
  AgentAvatarValue,
  Me,
  TimelineItem,
} from "@openfox/api";
import { useI18n, useT, type Translate } from "@openfox/i18n";
import { Avatar, Badge, Drawer, cn, Button, Icon } from "@openfox/ui";
import { Markdown } from "../../conversations/Markdown";
import { keys, useApi } from "../../context";
import { formatRelativeTime } from "../primitives/dates";
import { Link } from "react-router-dom";
import { taskPagePath } from "../navigation";
import { StatusIcon } from "../primitives/StatusIcon";
import { personName, type Person } from "../queries";
import { useMe } from "../../agents/queries";
import { AgentAvatar } from "../../agents/AgentAvatar";
import {
  ConversationRow,
  ConversationListFeedback,
  useTaskConversations,
} from "./TaskConversations";
import { CommentMenu, CommentEditor } from "./CommentActions";
import { useReadOnly } from "../../spaces/readonly";
import { Reactions } from "./Reactions";
import { CommentBox, type ReplyDraft } from "./CommentBox";

export interface NameBook {
  statuses: TaskStatus[];
  labels: Label[];
  people: Person[];
  agents: { id: string; name: string; avatar?: AgentAvatarValue }[];
}

function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}

/** 按 kind 渲染一条动态的文字；未知类型兜底为「更新了这个任务」。 */
export function activityText(
  t: Translate,
  activity: Pick<TimelineActivity, "kind" | "safe_diff" | "actor_name">,
  book: NameBook,
): string {
  const actor = activity.actor_name || t("tasks.activity.someone");
  const d = activity.safe_diff ?? {};
  const statusName = (id: unknown) =>
    book.statuses.find((s) => s.id === id)?.name ?? "—";
  const priority = (value: unknown) =>
    typeof value === "string" &&
    ["NONE", "LOWEST", "LOW", "NORMAL", "HIGH", "URGENT"].includes(value)
      ? t(`tasks.priority.${value as "NONE"}`)
      : "—";
  const person = (kind: unknown, id: unknown) => {
    if (kind === "AGENT")
      return book.agents.find((a) => a.id === id)?.name ?? "Agent";
    const found = book.people.find((p) => p.id === id);
    return found ? personName(found) : "—";
  };
  switch (activity.kind) {
    case "task.created":
      return t("tasks.activity.created", { actor });
    case "task.title_changed":
      return t("tasks.activity.titleChanged", { actor });
    case "task.description_changed":
      return t("tasks.activity.descriptionChanged", { actor });
    case "task.status_changed":
      return t("tasks.activity.statusChanged", {
        actor,
        from: statusName(d.from_status_id),
        to: statusName(d.to_status_id),
      });
    case "task.priority_changed":
      return t("tasks.activity.priorityChanged", {
        actor,
        from: priority(d.from),
        to: priority(d.to),
      });
    case "task.type_changed":
      return t("tasks.activity.typeChanged", { actor });
    case "task.assignee_added":
      return t("tasks.activity.assigneeAdded", {
        actor,
        name: person(d.kind, d.id),
      });
    case "task.assignee_removed":
      return t("tasks.activity.assigneeRemoved", {
        actor,
        name: person(d.kind, d.id),
      });
    case "task.label_added":
    case "task.label_removed": {
      const name = book.labels.find((l) => l.id === d.label_id)?.name ?? "—";
      return t(
        activity.kind === "task.label_added"
          ? "tasks.activity.labelAdded"
          : "tasks.activity.labelRemoved",
        { actor, name },
      );
    }
    case "task.start_date_changed":
    case "task.due_date_changed": {
      const start = activity.kind === "task.start_date_changed";
      if (!str(d.to))
        return t("tasks.activity.dateCleared", {
          actor,
          field: start
            ? t("tasks.fields.startDate")
            : t("tasks.fields.dueDate"),
        });
      return t(
        start ? "tasks.activity.startChanged" : "tasks.activity.dueChanged",
        {
          actor,
          to: str(d.to),
        },
      );
    }
    case "task.parent_changed":
      return t("tasks.activity.parentChanged", { actor });
    case "task.relation_added":
      return t("tasks.activity.relationAdded", { actor });
    case "task.relation_removed":
      return t("tasks.activity.relationRemoved", { actor });
    case "task.link_added":
      return t("tasks.activity.linkAdded", { actor });
    case "task.link_removed":
      return t("tasks.activity.linkRemoved", { actor });
    case "task.field_changed":
      return t("tasks.activity.fieldChanged", {
        actor,
        name: str(d.field_name) || "—",
      });
    case "task.deleted":
      return t("tasks.activity.deleted", { actor });
    // 从「最近删除」恢复；原父任务已删除时变成顶层任务。
    case "task.restored":
      return t(
        str(d.detached_parent_id)
          ? "tasks.trash.activityRestoredDetached"
          : "tasks.trash.activityRestored",
        { actor },
      );
    case "comment.resolved":
      return t("tasks.detail.activityResolved", { actor });
    case "comment.reopened":
      return t("tasks.detail.activityReopened", { actor });
    default:
      return t("tasks.activity.unknown", { actor });
  }
}

export function useTimeline(orgId: string, task: Task, thread = "roots") {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: [...keys.taskSub(orgId, task.id, "timeline"), thread],
    queryFn: ({ pageParam }) =>
      api.tasks.timeline(
        orgId,
        task.space_id,
        task.id,
        pageParam || undefined,
        thread,
      ),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
}

/** 动态页签：活动与评论按时间升序。 */
export function Timeline({
  orgId,
  task,
  book,
}: {
  orgId: string;
  task: Task;
  book: NameBook;
}) {
  const t = useT();
  const { locale } = useI18n();
  const timeline = useTimeline(orgId, task);
  const conversations = useTaskConversations(orgId, task);
  const me = useMe().data;
  const [topic, setTopic] = useState<{ id: string; focus: boolean } | null>(
    null,
  );
  const drafts = useRef(new Map<string, ReplyDraft>()).current;
  const openTopic = (id: string, focus: boolean) => setTopic({ id, focus });
  const items = useMemo(() => {
    const activity = (timeline.data?.pages.flatMap((page) => page.items) ?? [])
      .filter((item) => !item.comment?.reply_to_id)
      .map((item) => ({
        key: itemKey(item),
        at: item.comment?.created_at ?? item.activity?.created_at ?? "",
        item,
      }));
    const chats = conversations.list.map((conversation) => ({
      key: `conversation:${conversation.id}`,
      at: conversation.created_at,
      conversation,
    }));
    return [...activity, ...chats].sort(
      (a, b) => a.at.localeCompare(b.at) || a.key.localeCompare(b.key),
    );
  }, [timeline.data, conversations.list]);
  const root =
    topic &&
    items.find(
      (entry) => "item" in entry && entry.item.comment?.id === topic.id,
    );
  return (
    <>
      <ol
        className="flex flex-col gap-3"
        aria-label={t("tasks.detail.activity")}
      >
        {items.map((entry) =>
          "conversation" in entry ? (
            <ConversationRow
              key={entry.key}
              task={task}
              item={entry.conversation}
              people={book.people}
              activity
            />
          ) : (
            <TimelineRow
              key={entry.key}
              item={entry.item}
              orgId={orgId}
              task={task}
              book={book}
              locale={locale}
              me={me}
              openTopic={openTopic}
              drafts={drafts}
            />
          ),
        )}
        {(conversations.hasNextPage || conversations.isError) && (
          <li>
            <ConversationListFeedback query={conversations} />
          </li>
        )}
        {timeline.isError && !timeline.hasNextPage && (
          <li>
            <Button
              size="28"
              variant="ghost-muted"
              onClick={() => void timeline.refetch()}
            >
              {t("common.retry")}
            </Button>
          </li>
        )}
        {timeline.hasNextPage && (
          <li>
            <Button
              variant="ghost-muted"
              size="28"
              loading={timeline.isFetchingNextPage}
              onClick={() => void timeline.fetchNextPage()}
            >
              {t("tasks.detail.loadMoreActivity")}
            </Button>
          </li>
        )}
      </ol>
      {topic && root && "item" in root && root.item.comment && (
        <Topic
          key={topic.id}
          orgId={orgId}
          task={task}
          book={book}
          root={root.item.comment}
          focus={topic.focus}
          drafts={drafts}
          onClose={() => setTopic(null)}
        />
      )}
    </>
  );
}

function itemKey(item: TimelineItem): string {
  return item.type === "comment"
    ? `c:${item.comment?.id}`
    : `a:${item.activity?.id}`;
}

function TimelineRow({
  item,
  orgId,
  task,
  book,
  locale,
  me,
  openTopic,
  drafts,
}: {
  item: TimelineItem;
  orgId: string;
  task: Task;
  book: NameBook;
  locale: string;
  me?: Me;
  openTopic: (id: string, focus: boolean) => void;
  drafts: Map<string, ReplyDraft>;
}) {
  const t = useT();
  if (item.type === "activity" && item.activity) {
    const a = item.activity;
    return (
      <li className="flex items-center gap-2 text-body-sm text-fg-muted">
        <Avatar name={a.actor_name || "?"} size={16} />
        <span className="min-w-0 flex-1">{activityText(t, a, book)}</span>
        <time dateTime={a.created_at} className="shrink-0 text-caption">
          {formatRelativeTime(a.created_at, locale)}
        </time>
      </li>
    );
  }
  const c = item.comment;
  if (!c) return null;
  if (c.resolved_at)
    return (
      <ResolvedCard
        orgId={orgId}
        task={task}
        book={book}
        comment={c}
        openTopic={() => openTopic(c.id, false)}
      />
    );
  return (
    <li className="overflow-hidden rounded-lg border border-line-default">
      <div className="px-4 pt-2.5 pb-4">
        <CommentContent comment={c} orgId={orgId} task={task} book={book} />
      </div>
      {c.reply_count ? (
        <div className="mx-4 mb-3 flex flex-col gap-2 rounded-lg bg-surface-hover p-3">
          <button
            type="button"
            aria-label={t("tasks.detail.viewTopic")}
            onClick={() => openTopic(c.id, false)}
            className="flex w-full items-center gap-2 text-left text-caption text-fg-muted"
          >
            <span>
              {c.reply_count > 5
                ? t("tasks.detail.earlierReplyCount", {
                    count: c.reply_count - 5,
                  })
                : t("tasks.detail.replyCount", { count: c.reply_count })}
            </span>
            <span className="h-px flex-1 bg-line-default" />
          </button>
          {c.recent_replies?.map((r) => (
            <button
              key={r.id}
              type="button"
              onClick={() => openTopic(c.id, false)}
              className="flex min-w-0 items-start gap-2 rounded text-left text-caption leading-5 hover:bg-surface-hover"
            >
              <Author comment={r} book={book} size={16} preview={r.body} />
            </button>
          ))}
          <button
            type="button"
            className="mt-1 rounded-lg border border-line-default bg-surface-card px-3 py-1.5 text-left text-body-sm text-fg-muted hover:border-line-strong"
            onClick={() => openTopic(c.id, true)}
          >
            {t("tasks.detail.replyPlaceholder")}
          </button>
        </div>
      ) : (
        <CommentBox
          orgId={orgId}
          task={task}
          people={book.people}
          replyToId={c.id}
          inline
          me={me}
          drafts={drafts}
        />
      )}
    </li>
  );
}

function ResolvedCard({
  orgId,
  task,
  book,
  comment,
  openTopic,
}: {
  orgId: string;
  task: Task;
  book: NameBook;
  comment: TimelineComment;
  openTopic: () => void;
}) {
  const t = useT();
  const [expanded, setExpanded] = useState(false);
  const person = book.people.find((p) => p.id === comment.author_user_id);
  const name = person ? personName(person) : comment.author_name;
  return (
    <li className="overflow-hidden rounded-lg border border-line-default">
      <button
        type="button"
        aria-expanded={comment.reply_count ? undefined : expanded}
        aria-haspopup={comment.reply_count ? "dialog" : undefined}
        onClick={() =>
          comment.reply_count ? openTopic() : setExpanded(!expanded)
        }
        className="group flex w-full items-center gap-2.5 px-4 py-2 text-left text-caption text-fg-muted hover:bg-surface-hover"
      >
        {!expanded && <Icon name="openfox-circle-check" size={14} />}
        <span className="min-w-0 flex-1 truncate">
          {expanded
            ? t("tasks.detail.collapseThread")
            : t("tasks.detail.resolvedSummary", {
                name,
                count: 1 + (comment.reply_count ?? 0),
              })}
        </span>
        <Icon
          name={expanded ? "chevron-down" : "chevron-right"}
          size={14}
          className="opacity-0 group-hover:opacity-100 group-focus-visible:opacity-100 [@media(hover:none)]:opacity-100"
        />
      </button>
      {expanded && (
        <div className="border-t border-line-default px-4 py-3">
          <CommentContent
            orgId={orgId}
            task={task}
            book={book}
            comment={comment}
          />
        </div>
      )}
    </li>
  );
}

type CommentAuthor = Pick<
  TimelineComment,
  "author_name" | "author_user_id" | "author_agent_id"
>;
function Author({
  comment,
  book,
  size,
  preview,
}: {
  comment: CommentAuthor;
  book: NameBook;
  size: 16 | 20 | 32;
  preview?: string;
}) {
  const person = book.people.find((p) => p.id === comment.author_user_id);
  const agent = book.agents.find((a) => a.id === comment.author_agent_id);
  const name = person ? personName(person) : comment.author_name;
  return (
    <>
      {comment.author_agent_id ? (
        <AgentAvatar name={name} avatar={agent?.avatar} size={size} />
      ) : (
        <Avatar name={name} src={person?.avatar_url} size={size} />
      )}
      {preview !== undefined ? (
        <span className="line-clamp-2 min-w-0 flex-1 break-words text-fg-muted">
          <span className="mr-1.5 font-medium text-fg-default">{name}</span>
          {preview}
        </span>
      ) : (
        <span className="max-w-[50%] truncate font-medium">{name}</span>
      )}
    </>
  );
}

/** Render a reference to the task already in view without looking up other tasks. */
function CommentText({
  text,
  orgId,
  task,
}: {
  text: string;
  orgId: string;
  task: Task;
}) {
  const id = task.display_id.toUpperCase();
  return (
    <Markdown
      source={text}
      renderText={(plain, key) =>
        plain.split(/(\b[A-Z][A-Z0-9_]*-[0-9]+\b)/gi).map((part, i) =>
          part.toUpperCase() === id ? (
            <Link
              key={`${key}-${i}`}
              to={taskPagePath(orgId, id)}
              title={id + " " + task.title}
              className="inline-flex items-center gap-1 rounded-md border border-line-default px-1.5 align-baseline text-caption text-fg-secondary hover:bg-surface-muted"
            >
              <StatusIcon
                category={task.status_category}
                color={task.status_color}
                size={12}
              />
              {id}
            </Link>
          ) : (
            part
          ),
        )
      }
    />
  );
}

function CommentContent({
  comment: c,
  orgId,
  task,
  book,
  topic = false,
  resolutionId = c.resolved_comment_id,
}: {
  comment: TimelineComment;
  orgId: string;
  task: Task;
  book: NameBook;
  topic?: boolean;
  resolutionId?: string;
}) {
  const { locale, t } = useI18n();
  const [editing, setEditing] = useState(false);
  const readOnly = useReadOnly();
  const showEditor = editing && c.can_edit && !readOnly;
  return (
    <div className="group relative">
      <div className="flex min-w-0 items-center gap-2 text-body-sm">
        <Author comment={c} book={book} size={topic ? 32 : 20} />
        {c.author_kind === "AGENT" && (
          <Badge tone="neutral">{t("tasks.detail.agentTag")}</Badge>
        )}
        <time dateTime={c.created_at} className="text-caption text-fg-muted">
          {formatRelativeTime(c.created_at, locale)}
        </time>
        {c.edited_at && (
          <span
            className="text-caption text-fg-muted"
            title={new Date(c.edited_at).toLocaleString(locale)}
          >
            {t("tasks.detail.editedComment")}
          </span>
        )}
        {resolutionId === c.id && (
          <span className="shrink-0 text-caption font-medium text-success">
            Resolution
          </span>
        )}
        {!showEditor && (
          <div className="ml-auto flex items-center gap-1 opacity-0 focus-within:opacity-100 group-hover:opacity-100 [@media(hover:none)]:opacity-100">
            {!c.reactions.length && (
              <Reactions
                orgId={orgId}
                task={task}
                commentId={c.id}
                reactions={c.reactions}
              />
            )}
            <CommentMenu
              orgId={orgId}
              task={task}
              comment={c}
              isResolution={resolutionId === c.id}
              onEdit={() => setEditing(true)}
            />
          </div>
        )}
      </div>
      <div
        className={cn(
          "mt-1 min-w-0 text-body break-words",
          topic && "ml-11 mt-2",
        )}
      >
        {showEditor ? (
          <CommentEditor
            orgId={orgId}
            task={task}
            comment={c}
            onClose={() => setEditing(false)}
          />
        ) : (
          <CommentText text={c.body} orgId={orgId} task={task} />
        )}
        {c.reactions.length > 0 && (
          <Reactions
            orgId={orgId}
            task={task}
            commentId={c.id}
            reactions={c.reactions}
          />
        )}
      </div>
    </div>
  );
}

function Topic({
  orgId,
  task,
  book,
  root,
  focus,
  drafts,
  onClose,
}: {
  orgId: string;
  task: Task;
  book: NameBook;
  root: TimelineComment;
  focus: boolean;
  drafts: Map<string, ReplyDraft>;
  onClose: () => void;
}) {
  const t = useT();
  const thread = useTimeline(orgId, task, root.id);
  const scroll = useRef<HTMLDivElement>(null);
  const latest = thread.data?.pages[0]?.items[0]?.comment?.id;
  useEffect(() => {
    if (latest && scroll.current)
      scroll.current.scrollTop = scroll.current.scrollHeight;
  }, [latest]);
  const replies = thread.data?.pages.flatMap((p) => p.items).reverse() ?? [];
  return (
    <Drawer
      open
      modal={false}
      title={
        <span className="border-l border-line-default pl-2">
          {t("tasks.detail.topic")}
        </span>
      }
      width="min(800px, calc(100vw - 18px))"
      onClose={onClose}
    >
      <div className="flex h-full min-h-0 flex-col bg-surface-canvas">
        <div
          className="min-h-0 flex-1 overflow-y-auto px-6 pt-9 pb-5"
          ref={scroll}
        >
          <CommentContent
            comment={root}
            orgId={orgId}
            task={task}
            book={book}
            topic
          />
          <div className="mt-8 mb-6 flex items-center gap-3 text-body-sm text-fg-muted">
            {t("tasks.detail.replyCount", { count: root.reply_count ?? 0 })}
            <span className="h-px flex-1 bg-line-default" />
          </div>
          {thread.hasNextPage ? (
            <Button
              variant="ghost-muted"
              size="28"
              loading={thread.isFetchingNextPage}
              onClick={() => void thread.fetchNextPage()}
            >
              {t("tasks.detail.olderReplies")}
            </Button>
          ) : (
            thread.isError && (
              <Button
                size="28"
                variant="ghost-muted"
                onClick={() => void thread.refetch()}
              >
                {t("common.retry")}
              </Button>
            )
          )}
          {thread.isPending && (
            <span role="status" className="text-body-sm text-fg-muted">
              {t("common.loading")}
            </span>
          )}
          <ol className="flex flex-col gap-5">
            {replies.map(
              (i) =>
                i.comment && (
                  <li key={i.comment.id}>
                    <CommentContent
                      resolutionId={root.resolved_comment_id}
                      comment={i.comment}
                      orgId={orgId}
                      task={task}
                      book={book}
                      topic
                    />
                  </li>
                ),
            )}
          </ol>
        </div>
        {!root.resolved_at && (
          <div className="shrink-0 px-6 pt-2 pb-6">
            <CommentBox
              orgId={orgId}
              task={task}
              people={book.people}
              replyToId={root.id}
              focusOnOpen={focus}
              drafts={drafts}
            />
          </div>
        )}
      </div>
    </Drawer>
  );
}
