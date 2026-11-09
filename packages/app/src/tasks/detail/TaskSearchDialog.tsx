import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useState, type ReactNode } from "react";

import { isApiError, type Task } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Avatar,
  Button,
  Dialog,
  Icon,
  IconButton,
  Spinner,
  TextInput,
  cn,
} from "@openfox/ui";

import { useMe } from "../../agents/queries";
import { recentTaskIds } from "../recentVisits";
import { useApi } from "../../context";
import { displayIdOf } from "../parts";
import { TaskTypeIcon } from "../primitives/TaskTypeIcon";
import { StatusIcon } from "../primitives/StatusIcon";
import { taskErrorMessage } from "../queries";

/** 只解析编号，不访问粘贴的 URL；任务仍从当前组织的受权接口读取。 */
function displayIdFromSearch(value: string): string | null {
  if (/^[a-z0-9_]+-\d+$/i.test(value)) return value;
  try {
    const url = new URL(value);
    const legacyId = url.searchParams.get("task");
    if (legacyId && /^[a-z0-9_]+-\d+$/i.test(legacyId)) return legacyId;
    return (
      /\/(?:task|tasks)\/([a-z0-9_]+-\d+)\/?$/i.exec(url.pathname)?.[1] ?? null
    );
  } catch {
    return null;
  }
}

/** 按标题、编号或任务链接选择任务，支持方向键和 Enter。 */
export function TaskSearchDialog({
  orgId,
  spaceId,
  title,
  excludeIds,
  onPick,
  onClose,
  header,
  busy = false,
  duplicatePicker = false,
}: {
  orgId: string;
  spaceId: string;
  title: string;
  excludeIds: string[];
  onPick: (task: Task) => void;
  onClose: () => void;
  /** 列表上方的额外控件（如关联类型选择） */
  header?: ReactNode;
  busy?: boolean;
  duplicatePicker?: boolean;
}) {
  const t = useT();
  const api = useApi();
  const userId = useMe().data?.id;
  const listId = useId();
  const [q, setQ] = useState("");
  const [debounced, setDebounced] = useState("");
  const [active, setActive] = useState(0);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(q.trim()), 200);
    return () => window.clearTimeout(timer);
  }, [q]);
  const results = useQuery({
    queryKey: [
      "org",
      orgId,
      "task-search",
      spaceId,
      debounced,
      duplicatePicker,
    ],
    queryFn: async () => {
      const displayId = displayIdFromSearch(debounced);
      if (displayId) {
        try {
          const task = await api.tasks.byDisplayId(orgId, displayId);
          return { tasks: task.space_id === spaceId ? [task] : [] };
        } catch (error) {
          if (isApiError(error) && error.status === 404) return { tasks: [] };
          throw error;
        }
      }
      return api.tasks.list(orgId, spaceId, {
        q: debounced || undefined,
        limit: duplicatePicker ? 100 : 20,
        sort: "updated",
        order: "desc",
      });
    },
  });
  const candidates = (results.data?.tasks ?? []).filter(
    (task) =>
      !excludeIds.includes(task.id) &&
      (!duplicatePicker ||
        (!task.duplicate_of && task.status_category !== "DUPLICATE")),
  );
  let tasks = candidates;
  if (duplicatePicker && !debounced) {
    const byId = new Map(candidates.map((task) => [task.id, task]));
    const seen = recentTaskIds(orgId, userId).flatMap((id) => {
      const task = byId.get(id);
      return task ? [task] : [];
    });
    tasks = (seen.length ? seen : candidates).slice(0, 5);
  }
  const current = Math.min(active, Math.max(0, tasks.length - 1));
  const waiting = q.trim() !== debounced || results.isFetching;
  const disabled = busy || waiting;
  useEffect(() => {
    document
      .getElementById(`${listId}-${current}`)
      ?.scrollIntoView?.({ block: "nearest" });
  }, [listId, current]);
  return (
    <Dialog
      open
      onClose={onClose}
      title={duplicatePicker ? t("spaces.triage.chooseDuplicate") : title}
      hideHeader={duplicatePicker}
      width={duplicatePicker ? 720 : 520}
      className={
        duplicatePicker
          ? "relative -top-[8vh] gap-0! rounded-[14px]! p-2!"
          : undefined
      }
      footer={
        duplicatePicker ? (
          <div className="mt-1 flex w-full items-center gap-1.5 border-t border-line-default px-2 pt-2 pb-1 text-caption text-fg-muted">
            <kbd className="rounded bg-surface-muted px-1.5 py-0.5">Enter</kbd>
            {t("spaces.triage.select")}
          </div>
        ) : (
          <span className="text-caption text-fg-muted">
            {t("tasks.detail.enterSelect")}
          </span>
        )
      }
    >
      <div className={cn("flex flex-col", duplicatePicker ? "gap-1" : "gap-3")}>
        {header}
        <TextInput
          className={
            duplicatePicker
              ? "rounded-lg border-transparent! bg-surface-muted! focus-within:border-transparent! [&_input:focus-visible]:shadow-none"
              : undefined
          }
          trailing={
            duplicatePicker && (
              <IconButton
                icon={<Icon name="x" size={14} />}
                label={t("common.close")}
                size="28"
                onClick={onClose}
              />
            )
          }
          data-autofocus
          role="combobox"
          aria-expanded
          aria-controls={listId}
          aria-activedescendant={
            !disabled && tasks[current] ? `${listId}-${current}` : undefined
          }
          aria-label={t(
            duplicatePicker
              ? "spaces.triage.chooseDuplicate"
              : "tasks.detail.searchTasks",
          )}
          placeholder={t(
            duplicatePicker
              ? "spaces.triage.duplicatePlaceholder"
              : "tasks.detail.searchTasks",
          )}
          leading={<Icon name="search" />}
          value={q}
          maxLength={2048}
          disabled={busy}
          onChange={(event) => {
            setQ(event.target.value);
            setActive(0);
          }}
          onKeyDown={(event) => {
            if (disabled || !tasks.length || event.nativeEvent.isComposing)
              return;
            if (event.key === "ArrowDown" || event.key === "ArrowUp") {
              event.preventDefault();
              setActive(
                (current + (event.key === "ArrowDown" ? 1 : tasks.length - 1)) %
                  tasks.length,
              );
            } else if (event.key === "Enter") {
              event.preventDefault();
              onPick(tasks[current]!);
            }
          }}
        />
        {results.error && (
          <div
            role="alert"
            className="flex items-center gap-2 text-body-sm text-danger"
          >
            {taskErrorMessage(t, results.error)}
            <Button
              variant="ghost-muted"
              size="28"
              onClick={() => void results.refetch()}
            >
              {t("common.retry")}
            </Button>
          </div>
        )}
        {duplicatePicker && tasks.length > 0 && !waiting && (
          <div className="px-2 pt-1.5 pb-0.5 text-caption text-fg-muted">
            {t(debounced ? "spaces.triage.results" : "spaces.triage.recent")}
          </div>
        )}
        <ul
          id={listId}
          role="listbox"
          aria-label={title}
          className="max-h-[320px] overflow-y-auto"
        >
          {waiting && (
            <li className="flex justify-center py-3">
              <Spinner />
            </li>
          )}
          {!waiting && results.data && tasks.length === 0 && (
            <li className="py-3 text-center text-body-sm text-fg-muted">
              {t("tasks.detail.noTaskResults")}
            </li>
          )}
          {tasks.map((task, index) => (
            <li
              key={task.id}
              id={`${listId}-${index}`}
              role="option"
              aria-selected={index === current}
            >
              <button
                type="button"
                disabled={disabled}
                onMouseMove={() => setActive(index)}
                onClick={() => onPick(task)}
                className={cn(
                  "flex h-9 w-full items-center gap-2 rounded-lg px-2 text-left text-body hover:bg-surface-active disabled:opacity-50",
                  index === current && "bg-surface-active",
                )}
              >
                {duplicatePicker ? (
                  <TaskTypeIcon size={14} icon={task.type_icon} />
                ) : (
                  <StatusIcon
                    category={task.status_category}
                    color={task.status_color}
                  />
                )}
                <span
                  title={displayIdOf(task)}
                  className={cn(
                    "shrink-0 truncate text-caption text-fg-muted",
                    duplicatePicker ? "max-w-40" : "w-28",
                  )}
                >
                  {displayIdOf(task)}
                </span>
                <span className="truncate">{task.title}</span>
                {duplicatePicker && (
                  <span className="ml-auto flex shrink-0 items-center gap-1.5 text-caption text-fg-muted">
                    <StatusIcon
                      category={task.status_category}
                      color={task.status_color}
                      size={14}
                    />
                    {task.status_name}
                    {task.assignees.slice(0, 1).map((a) => (
                      <Avatar key={a.id} name={a.name ?? a.id} size={16} />
                    ))}
                  </span>
                )}
              </button>
            </li>
          ))}
        </ul>
      </div>
    </Dialog>
  );
}
