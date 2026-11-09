import { useEffect, useRef } from "react";
import {
  useLocation,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";

import type { Task } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Button, Icon, cn, useToast } from "@openfox/ui";

import { LabelDot } from "./pickers";
import { TaskAssigneeAvatar } from "./invitationAssignees";
import {
  DRAWER_STATE,
  isDrawerState,
  taskPagePath,
  useTaskPageId,
} from "./navigation";
import { formatMonthDay, isOverdue } from "./primitives/dates";

/**
 * 读写地址栏 ?task=<DISPLAY_ID>，详情抽屉据此打开，刷新后保持。
 * 历史栈：没开抽屉时 open 是 push 并打上标记，close 对带标记的项后退一步（后退不会再打开刚关掉的抽屉）；
 * 抽屉已打开时（切到别的任务）和没有标记时（深链接、刷新后）都用 replace，不增加历史条数。
 * 在任务全页里 open 是跳到另一个任务的全页（replace）；全页上 displayId 恒为 null，不会再叠一层抽屉。
 */
export function useTaskParam() {
  const { orgId = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const location = useLocation();
  const navigate = useNavigate();
  const onPage = useTaskPageId();
  const value = onPage ? null : params.get("task");
  const open = (displayId: string) => {
    if (onPage) {
      navigate(taskPagePath(orgId, displayId), {
        replace: true,
        state: location.state,
      });
      return;
    }
    const opened = params.has("task");
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.set("task", displayId.toUpperCase());
        return next;
      },
      opened
        ? { replace: true, state: location.state }
        : { replace: false, state: DRAWER_STATE },
    );
  };
  const close = () => {
    if (isDrawerState(location.state)) {
      navigate(-1);
      return;
    }
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.delete("task");
        return next;
      },
      { replace: true },
    );
  };
  return { displayId: value ? value.toUpperCase() : null, open, close };
}

/** 任务的可分享链接：指向所在空间的任务页并带 ?task= */
export function taskUrl(
  orgId: string,
  task: Pick<Task, "space_id" | "display_id">,
) {
  const origin = typeof window === "undefined" ? "" : window.location.origin;
  return `${origin}/o/${orgId}/spaces/${task.space_id}/tasks?task=${encodeURIComponent(task.display_id.toUpperCase())}`;
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

/** 复制并提示 */
export function useCopy() {
  const toast = useToast();
  const t = useT();
  return async (text: string, message: string) => {
    if (await copyText(text)) toast(message);
    else toast(t("tasks.errors.failed"), { tone: "error" });
  };
}

export function displayIdOf(task: Pick<Task, "display_id">) {
  return task.display_id.toUpperCase();
}

/** 「复制标题 & 链接」：第一行「OPEN-1 任务标题」，第二行任务链接。 */
export function taskTitleLink(
  orgId: string,
  task: Pick<Task, "space_id" | "display_id" | "title">,
) {
  return `${displayIdOf(task)} ${task.title}\n${taskUrl(orgId, task)}`;
}

/**
 * 「复制 git commit message」：标题、空一行、`Refs: OPEN-1`。
 * Git 集成按 `前缀-序号` 识别任务（internal/integrations/taskref.go），`Refs:` 只是约定写法，不影响识别。
 */
export function taskCommitMessage(task: Pick<Task, "display_id" | "title">) {
  return `${task.title}\n\nRefs: ${displayIdOf(task)}`;
}

export function AssigneeStack({
  task,
  size = 20,
}: {
  task: Pick<Task, "assignees">;
  size?: 16 | 20 | 24;
}) {
  const list = task.assignees.slice(0, 3);
  if (list.length === 0) return null;
  return (
    <span className="flex -space-x-1">
      {list.map((a) => (
        <TaskAssigneeAvatar
          key={`${a.kind}:${a.id}`}
          name={a.name}
          kind={a.kind}
          size={size}
          className="ring-2 ring-surface-card"
        />
      ))}
    </span>
  );
}

export function LabelPills({
  task,
  max = 3,
}: {
  task: Pick<Task, "label_items">;
  max?: number;
}) {
  if (task.label_items.length === 0) return null;
  const rest = task.label_items.length - max;
  return (
    <>
      {task.label_items.slice(0, max).map((label) => (
        <Pill key={label.id}>
          <LabelDot color={label.color} />
          {label.name}
        </Pill>
      ))}
      {rest > 0 && <Pill>+{rest}</Pill>}
    </>
  );
}

export function Pill({
  children,
  tone,
  className,
}: {
  children: React.ReactNode;
  tone?: "danger";
  className?: string;
}) {
  return (
    <span
      className={cn(
        "inline-flex h-5 max-w-[160px] items-center gap-1 truncate rounded-full border border-line-default px-1.5 text-caption",
        tone === "danger"
          ? "border-danger-border text-danger"
          : "text-fg-secondary",
        className,
      )}
    >
      {children}
    </span>
  );
}

export function DueChip({ date }: { date: string | null }) {
  const { locale } = useI18n();
  if (!date) return null;
  return (
    <Pill tone={isOverdue(date) ? "danger" : undefined}>
      <Icon name="calendar" size={12} />
      {formatMonthDay(date, locale)}
    </Pill>
  );
}

export function SubtaskChip({
  task,
}: {
  task: Pick<Task, "subtask_count" | "subtask_done_count">;
}) {
  const t = useT();
  if (!task.subtask_count) return null;
  return (
    <Pill>
      <Icon name="list-checks" size={12} />
      <span
        aria-label={t("tasks.subtasksProgress", {
          done: task.subtask_done_count,
          total: task.subtask_count,
        })}
      >
        {task.subtask_done_count}/{task.subtask_count}
      </span>
    </Pill>
  );
}

/** 滚动到底自动加载下一页；不支持 IntersectionObserver 时显示按钮。 */
export function LoadMore({
  hasMore,
  loading,
  onLoad,
}: {
  hasMore: boolean;
  loading: boolean;
  onLoad: () => void;
}) {
  const t = useT();
  const ref = useRef<HTMLDivElement | null>(null);
  const onLoadRef = useRef(onLoad);
  onLoadRef.current = onLoad;
  const supported = typeof IntersectionObserver !== "undefined";
  useEffect(() => {
    if (!supported || !hasMore || !ref.current) return;
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) onLoadRef.current();
    });
    observer.observe(ref.current);
    return () => observer.disconnect();
  }, [supported, hasMore]);
  if (!hasMore) return null;
  if (!supported)
    return (
      <Button
        variant="ghost-muted"
        size="28"
        loading={loading}
        onClick={onLoad}
        className="mx-auto my-1"
      >
        {t("tasks.loadMore")}
      </Button>
    );
  return <div ref={ref} aria-hidden className="h-4 shrink-0" />;
}
