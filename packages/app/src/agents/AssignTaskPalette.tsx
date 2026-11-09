import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";

import type { Task, TaskAssigneeRef } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, Spinner, cn, useModalBehavior, useToast } from "@openfox/ui";

import { keys, useApi } from "../context";
import { useCreateTaskDialog } from "../tasks/create/CreateTaskProvider";
import { displayIdOf, useTaskParam } from "../tasks/parts";
import { StatusIcon } from "../tasks/primitives/StatusIcon";
import {
  applyTaskToCache,
  latestTask,
  taskErrorMessage,
} from "../tasks/queries";

export interface AssignTarget {
  kind: "agent" | "crew";
  id: string;
  name: string;
  spaceId: string;
}

const DISPLAY_ID = /\b([A-Za-z][A-Za-z0-9]*-\d+)\b/;

/** 选中的任务追加处理人（已在其中则保持不变）。 */
export function withAssignee(
  task: Task,
  target: AssignTarget,
): TaskAssigneeRef[] {
  const kind = target.kind === "agent" ? "AGENT" : "CREW";
  const refs = task.assignees.map(({ kind: k, id }) => ({ kind: k, id }));
  return refs.some((ref) => ref.kind === kind && ref.id === target.id)
    ? refs
    : [...refs, { kind, id: target.id }];
}

/**
 * 指派任务命令面板：搜索或粘贴任务链接、「+ 新建任务」（预填执行者），
 * 「最近任务」取该空间任务列表的前 5 条；选中后把 Agent / 小队加入处理人并打开任务详情。
 */
export function AssignTaskPalette({
  orgId,
  target,
  onClose,
}: {
  orgId: string;
  target: AssignTarget;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const { openCreate } = useCreateTaskDialog();
  const taskParam = useTaskParam();
  const panel = useRef<HTMLDivElement | null>(null);
  const listId = useId();
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  const [active, setActive] = useState(0);
  const [busy, setBusy] = useState(false);
  useModalBehavior(true, panel, onClose);

  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(query.trim()), 200);
    return () => window.clearTimeout(timer);
  }, [query]);

  const pasted = DISPLAY_ID.exec(debounced)?.[1]?.toUpperCase();
  const tasks = useQuery({
    // 挂在任务公共前缀下随任务写操作失效，但不放在 list 下（形状不同）
    queryKey: [
      ...keys.tasks(orgId),
      "assign",
      target.spaceId,
      pasted ? `id:${pasted}` : debounced,
    ],
    queryFn: async (): Promise<Task[]> => {
      if (pasted) {
        try {
          return [await api.tasks.byDisplayId(orgId, pasted)];
        } catch {
          return [];
        }
      }
      const page = await api.tasks.list(orgId, target.spaceId, {
        limit: 5,
        ...(debounced ? { q: debounced } : {}),
      });
      return page.tasks.slice(0, 5);
    },
  });
  const list = tasks.data ?? [];
  const current = Math.min(active, Math.max(list.length - 1, 0));

  const assign = async (task: Task) => {
    if (busy) return;
    setBusy(true);
    try {
      const latest = latestTask(queryClient, orgId, task.id) ?? task;
      const saved = await api.tasks.update(orgId, latest.space_id, latest.id, {
        assignees: withAssignee(latest, target),
        version: latest.version,
      });
      applyTaskToCache(queryClient, orgId, saved);
      void queryClient.invalidateQueries({ queryKey: keys.taskLists(orgId) });
      toast(
        t("agents.toast.assigned", {
          id: displayIdOf(saved),
          name: target.name,
        }),
      );
      onClose();
      taskParam.open(saved.display_id);
    } catch (error) {
      toast(taskErrorMessage(t, error), { tone: "error" });
      setBusy(false);
    }
  };

  const createNew = () => {
    onClose();
    openCreate({
      spaceId: target.spaceId,
      assignee: { value: `${target.kind}:${target.id}`, name: target.name },
      openAfterCreate: true,
    });
  };

  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-scrim/25 p-6 pt-[22vh]"
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-label={t("agents.assign.label", { name: target.name })}
        className="flex w-[512px] max-w-full flex-col rounded-2xl border border-line-default bg-surface-card p-2 shadow-dialog"
      >
        <div className="flex items-center gap-2 border-b border-line-default px-3 pt-1 pb-2.5">
          <Icon name="search" className="text-fg-muted" />
          <input
            data-autofocus
            role="combobox"
            aria-expanded
            aria-controls={listId}
            aria-activedescendant={
              list[current] ? `${listId}-${list[current]!.id}` : undefined
            }
            aria-label={t("agents.assign.search")}
            placeholder={t("agents.assign.search")}
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setActive(0);
            }}
            onKeyDown={(event) => {
              if (event.key === "ArrowDown" && list.length) {
                event.preventDefault();
                setActive((current + 1) % list.length);
              } else if (event.key === "ArrowUp" && list.length) {
                event.preventDefault();
                setActive((current - 1 + list.length) % list.length);
              } else if (event.key === "Enter" && list[current]) {
                event.preventDefault();
                void assign(list[current]!);
              }
            }}
            className="min-w-0 flex-1 bg-transparent text-body outline-none placeholder:text-fg-subtle focus-visible:shadow-none"
          />
          {busy && <Spinner />}
        </div>
        <button
          type="button"
          onClick={createNew}
          className="mx-1 mt-1 flex h-8 items-center justify-center gap-1.5 rounded-lg bg-surface-muted text-body font-medium hover:bg-surface-active"
        >
          <Icon name="plus" />
          {t("agents.assign.newTask")}
        </button>
        <div className="mx-1 mt-2 border-t border-line-default pt-3">
          <h3 className="px-1 text-body font-medium">
            {t("agents.assign.existing")}
          </h3>
          <div className="px-1 pt-2 pb-1 text-caption text-fg-muted">
            {t("agents.assign.recent")}
          </div>
          <div
            id={listId}
            role="listbox"
            aria-label={t("agents.assign.recent")}
          >
            {tasks.isLoading ? (
              <Spinner className="mx-auto my-3" />
            ) : list.length === 0 ? (
              <p className="px-2 py-3 text-center text-body-sm text-fg-muted">
                {t("agents.assign.empty")}
              </p>
            ) : (
              list.map((task, index) => (
                <div
                  key={task.id}
                  id={`${listId}-${task.id}`}
                  role="option"
                  aria-selected={index === current}
                  onMouseDown={(event) => event.preventDefault()}
                  onMouseMove={() => setActive(index)}
                  onClick={() => void assign(task)}
                  className={cn(
                    "flex h-9 cursor-pointer items-center gap-2.5 rounded-lg px-2 text-body",
                    index === current && "bg-surface-active",
                  )}
                >
                  <StatusIcon category={task.status_category} />
                  <span className="font-mono text-caption text-fg-muted">
                    {displayIdOf(task)}
                  </span>
                  <span className="min-w-0 flex-1 truncate">{task.title}</span>
                </div>
              ))
            )}
          </div>
        </div>
      </div>
    </div>,
    document.body,
  );
}
