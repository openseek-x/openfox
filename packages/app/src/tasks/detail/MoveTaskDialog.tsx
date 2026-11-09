import {
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type QueryClient,
} from "@tanstack/react-query";
import { useState } from "react";

import {
  isApiError,
  newIdempotencyKey,
  type SpaceDetail,
  type Task,
  type TaskMovePreview,
  type TaskMoveResult,
  type TaskPageResult,
} from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import { Button, Callout, Dialog, Skeleton, useToast } from "@openfox/ui";

import { keys, useApi } from "../../context";
import { useOrgRole, useSpaceList } from "../../spaces/queries";
import { latestTask } from "../queries";

type TaskPages = InfiniteData<TaskPageResult<Task>, string>;
import {
  MOVE_SEARCH_THRESHOLD,
  MoveSpaceList,
  filterSpaces,
  moveTargetOptions,
} from "./moveToSpace";

// 「移动到空间」确认对话框：选择目标（快捷键进入时）→ 预览 → 确认移动。

/** 预览默认展开的任务行数，其余折叠。 */
export const PREVIEW_ROWS = 20;

/** 移动预览与执行的错误文案。 */
export function moveErrorMessage(
  t: Translate,
  error: unknown,
  fallback: "failed" | "previewFailed" = "failed",
): string {
  if (!isApiError(error)) return t(`tasks.move.errors.${fallback}`);
  if (error.status === 403) return t("tasks.move.errors.forbidden");
  if (error.status === 404) return t("tasks.move.errors.notFound");
  switch (error.code) {
    case "INVALID_TARGET_SPACE":
      return t("tasks.move.errors.invalidTarget");
    case "SPACE_RETIRED":
      return t("tasks.move.errors.retired");
    case "TOO_MANY_TASKS":
      return t("tasks.move.errors.tooMany");
    case "VERSION_CONFLICT":
      return t("tasks.move.errors.versionConflict");
    case "IDEMPOTENCY_CONFLICT":
      return t("tasks.move.errors.idempotencyConflict");
    default:
      return t(`tasks.move.errors.${fallback}`);
  }
}

/** 预览缓存键：挂在任务子资源下，任务缓存失效时一起刷新。 */
export const movePreviewKey = (
  orgId: string,
  taskId: string,
  targetId: string,
) => [...keys.taskSub(orgId, taskId, "move-preview"), targetId] as const;

/**
 * 移动成功后立即从源空间的任务列表里去掉移走的任务（不等轮询或事件），再按前缀失效：
 * - keys.tasks：所有任务列表、计数、统计、任务详情（含整棵子树的旧编号）、子资源（父任务的子任务列表）；
 * - 搜索结果（命令面板，键前缀 ["org", orgId, "search"]）；
 * - keys.spaces 前缀：空间列表（task_count）与源 / 目标空间的分拣台（keys.triage，任务可能在源空间分拣台里）。
 * 「我的任务」等跨空间列表不在这里去掉（任务仍属于自己），由失效后的重新加载更新。
 */
export function applyMoveToCache(
  queryClient: QueryClient,
  orgId: string,
  sourceSpaceId: string,
  movedIds: readonly string[],
  movedTask?: Task,
) {
  const moved = new Set(movedIds);
  const sourceScope = `space:${sourceSpaceId}`;
  // 子任务、动态、Git 关联的缓存键只有任务 id，请求却按 task.space_id 发。若在详情还是旧空间时失效它们，
  // 会用旧空间重新请求（子任务得到空列表、动态和 Git 关联 404）。所以：先把新任务（新空间）写进详情缓存，
  // 再直接移除移走任务的子资源缓存（不触发请求），界面按新空间重新渲染时再各自重新加载。
  if (movedTask) {
    for (const [key, data] of queryClient.getQueriesData<{ id?: string }>({
      queryKey: keys.taskDetails(orgId),
    })) {
      // canonical_display_id 保持旧值：去向由调用方 onMoved 主动跳转；若这里先写成新编号，
      // 旧编号重定向（useCanonicalDisplayId）会按旧页面地址改 ?task=，把跳到目标空间的导航覆盖掉。
      if (data?.id === movedTask.id)
        queryClient.setQueryData(key, {
          ...data,
          ...movedTask,
          canonical_display_id: (data as { canonical_display_id?: string })
            .canonical_display_id,
        });
    }
  }
  queryClient.removeQueries({
    predicate: (query) =>
      query.queryKey[0] === "org" &&
      query.queryKey[1] === orgId &&
      query.queryKey[3] === "sub" &&
      moved.has(String(query.queryKey[4])),
  });
  for (const [key, data] of queryClient.getQueriesData<TaskPages>({
    queryKey: keys.taskLists(orgId),
  })) {
    if (key[4] !== sourceScope || !data?.pages) continue;
    queryClient.setQueryData<TaskPages>(key, {
      ...data,
      pages: data.pages.map((page) => ({
        ...page,
        tasks: page.tasks.filter((task) => !moved.has(task.id)),
      })),
    });
  }
  void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
  void queryClient.invalidateQueries({ queryKey: ["org", orgId, "search"] });
  // 空间前缀下含空间列表（task_count）与两个空间的分拣台（keys.triage）
  void queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
}

/** 409 TASK_HAS_ACTIVE_RUNS 的 error.details（预览之后又有人指派了 Agent）。 */
interface StaleRuns {
  active_runs: number;
  agent_assignees: number;
}

/**
 * 移动对话框。initialTarget 为 null 时先显示「选择目标」一步（快捷键进入）。
 * Idempotency-Key 在每次打开（挂载）时生成一次，同一个对话框里的连点、重试都复用它。
 * onMoved 由调用方决定是否跳转（任务详情跳到新编号；看板卡片不跳转）。
 */
export function MoveTaskDialog({
  orgId,
  task,
  initialTarget,
  onClose,
  onMoved,
}: {
  orgId: string;
  task: Task;
  initialTarget: SpaceDetail | null;
  onClose: () => void;
  onMoved?: (result: TaskMoveResult) => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [target, setTarget] = useState(initialTarget);
  const [idempotencyKey] = useState(newIdempotencyKey);
  const [staleRuns, setStaleRuns] = useState<StaleRuns | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [expanded, setExpanded] = useState(false);
  const preview = useQuery({
    queryKey: movePreviewKey(orgId, task.id, target?.id ?? ""),
    queryFn: () =>
      api.tasks.movePreview(orgId, task.space_id, task.id, {
        target_space_id: target!.id,
      }),
    enabled: Boolean(target),
    staleTime: 0,
    retry: false,
  });
  const confirmRuns =
    Boolean(preview.data?.requires_confirmation) || staleRuns !== null;
  const move = useMutation({
    scope: { id: `task-write:${orgId}` },
    mutationFn: (cancelRuns: boolean) =>
      api.tasks.move(
        orgId,
        task.space_id,
        task.id,
        {
          target_space_id: target!.id,
          version:
            latestTask(queryClient, orgId, task.id)?.version ?? task.version,
          ...(cancelRuns ? { cancel_runs: true } : {}),
        },
        idempotencyKey,
      ),
    onSuccess: (result) => {
      const moved = preview.data?.tasks.map((item) => item.task_id) ?? [];
      applyMoveToCache(
        queryClient,
        orgId,
        task.space_id,
        [task.id, ...moved],
        result.task,
      );
      queryClient.removeQueries({
        queryKey: keys.taskSub(orgId, task.id, "move-preview"),
      });
      toast(
        t("tasks.move.moved", { name: target!.name, id: result.display_id }),
      );
      onClose();
      onMoved?.(result);
    },
    onError: (error) => {
      setNotice(moveErrorMessage(t, error));
      if (isApiError(error, "VERSION_CONFLICT")) {
        // 重新拉任务详情（拿到新 version）与预览，用户再点一次即可
        void queryClient.invalidateQueries({
          queryKey: keys.taskDetails(orgId),
        });
        void queryClient.invalidateQueries({ queryKey: keys.taskLists(orgId) });
        void preview.refetch();
      } else if (isApiError(error, "TASK_HAS_ACTIVE_RUNS")) {
        // 不自动重发：用 details 刷新确认信息，按钮切到「取消运行并移动」
        const details = error.details ?? {};
        setNotice(null);
        setStaleRuns({
          active_runs: Number(details.active_runs ?? 0),
          agent_assignees: Number(details.agent_assignees ?? 0),
        });
        void preview.refetch();
      }
    },
  });

  if (!target)
    return (
      <PickTargetDialog
        orgId={orgId}
        task={task}
        onClose={onClose}
        onPick={setTarget}
      />
    );
  const data = preview.data;
  return (
    <Dialog
      open
      width={560}
      onClose={move.isPending ? undefined : onClose}
      title={t("tasks.move.title", { name: target.name })}
      description={
        data
          ? t("tasks.move.description", { count: data.tasks_count })
          : undefined
      }
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={move.isPending}>
            {t("common.cancel")}
          </Button>
          <Button
            variant={confirmRuns ? "danger" : "primary"}
            disabled={!data}
            loading={move.isPending}
            onClick={() => {
              setNotice(null);
              move.mutate(confirmRuns);
            }}
          >
            {move.isPending
              ? t("tasks.move.moving")
              : confirmRuns
                ? t("tasks.move.submitCancelRuns")
                : t("tasks.move.submit")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        {notice && (
          <p role="alert" className="text-body-sm text-danger">
            {notice}
          </p>
        )}
        {staleRuns && (
          <Callout tone="warning">
            {t("tasks.move.staleRuns", {
              agents: staleRuns.agent_assignees,
              runs: staleRuns.active_runs,
            })}
          </Callout>
        )}
        {preview.isLoading ? (
          <PreviewSkeleton />
        ) : preview.isError && !data ? (
          <div className="flex flex-col items-start gap-2">
            <p role="alert" className="text-body-sm text-danger">
              {moveErrorMessage(t, preview.error, "previewFailed")}
            </p>
            <Button
              variant="outline"
              size="28"
              onClick={() => void preview.refetch()}
            >
              {t("common.retry")}
            </Button>
          </div>
        ) : data ? (
          <PreviewBody
            preview={data}
            parentDisplayId={
              task.parent && task.parent.id === data.detached_parent?.id
                ? task.parent.display_id
                : undefined
            }
            expanded={expanded}
            onToggle={() => setExpanded((value) => !value)}
          />
        ) : null}
      </div>
    </Dialog>
  );
}

/** 「选择目标」一步：与菜单相同的空间列表，选中后进入预览。 */
function PickTargetDialog({
  orgId,
  task,
  onClose,
  onPick,
}: {
  orgId: string;
  task: Task;
  onClose: () => void;
  onPick: (space: SpaceDetail) => void;
}) {
  const t = useT();
  const spaces = useSpaceList(orgId);
  const orgRole = useOrgRole(orgId);
  const [query, setQuery] = useState("");
  const all = moveTargetOptions(spaces.data ?? [], task.space_id, orgRole);
  const hasTargets = all.some((item) => !item.current);
  return (
    <Dialog
      open
      width={420}
      onClose={onClose}
      title={t("tasks.move.pickTitle")}
      description={t("tasks.move.pickDescription", {
        id: task.display_id.toUpperCase(),
      })}
      footer={
        <Button variant="outline" onClick={onClose}>
          {t("common.cancel")}
        </Button>
      }
    >
      <div role="group" aria-label={t("tasks.move.pickTitle")}>
        {spaces.isLoading ? (
          <PreviewSkeleton />
        ) : (
          <MoveSpaceList
            items={filterSpaces(all, query)}
            searchable={(spaces.data?.length ?? 0) > MOVE_SEARCH_THRESHOLD}
            query={query}
            onQuery={setQuery}
            empty={
              hasTargets ? t("tasks.move.noMatch") : t("tasks.move.noTargets")
            }
            onPick={onPick}
          />
        )}
      </div>
    </Dialog>
  );
}

/**
 * 一行：旧编号 → 新编号（预计）、标题；类型 / 状态 / 估时有变化时「A → B」；清空字段与丢弃标签用醒目样式。
 * 「父任务」不在行内列出，由下方「将脱离父任务」统一说明。
 */
function PreviewRow({ row }: { row: TaskMovePreview["tasks"][number] }) {
  const t = useT();
  const sep = t("tasks.move.separator");
  const changes: string[] = [];
  // 任务类型按空间各自一份，id 必然不同；名称相同（按名称映射成功）时不算变化
  if (row.type_from.name !== row.type_to.name)
    changes.push(
      t("tasks.move.typeChange", {
        from: row.type_from.name,
        to: row.type_to.name,
      }),
    );
  if (row.status_from.id !== row.status_to.id)
    changes.push(
      t("tasks.move.statusChange", {
        from: row.status_from.name,
        to: row.status_to.name,
      }),
    );
  const cleared = row.cleared.filter((field) => field !== "parent");
  if (
    row.estimate_to !== null &&
    row.estimate_from !== null &&
    row.estimate_from !== row.estimate_to
  )
    changes.push(
      t("tasks.move.estimateChange", {
        from: estimateText(row.estimate_from),
        to: estimateText(row.estimate_to),
      }),
    );
  const lost = [
    ...(cleared.length > 0
      ? [
          t("tasks.move.cleared", {
            fields: cleared
              .map((field) => t(`tasks.move.clearedFields.${field}`))
              .join(sep),
          }),
        ]
      : []),
    ...(row.dropped_labels.length > 0
      ? [
          t("tasks.move.droppedLabels", {
            names: row.dropped_labels.map((label) => label.name).join(sep),
          }),
        ]
      : []),
  ];
  return (
    <li className="flex flex-col gap-1 px-3 py-2">
      <span className="flex min-w-0 items-center gap-1.5 text-body">
        <span className="shrink-0 text-fg-muted">
          {row.display_id.toUpperCase()}
        </span>
        <span aria-hidden className="shrink-0 text-fg-subtle">
          →
        </span>
        <span className="shrink-0 font-medium">
          {row.display_id_hint.toUpperCase()}
        </span>
        <span className="shrink-0 rounded bg-surface-muted px-1 text-caption text-fg-muted">
          {t("tasks.move.estimated")}
        </span>
        <span className="min-w-0 truncate">{row.title}</span>
      </span>
      {changes.length > 0 && (
        <span className="text-body-sm text-fg-muted">
          {changes.join(" · ")}
        </span>
      )}
      {lost.map((text) => (
        <span key={text} className="text-body-sm font-medium text-warning">
          {text}
        </span>
      ))}
    </li>
  );
}

/** 估时显示：null 显示「—」。 */
const estimateText = (value: number | null) =>
  value === null ? "—" : String(value);

/** 预览内容：任务列表（逐行映射与丢失项）、脱离父任务、移除 Agent / 取消运行、解除 Issue、固定提示。 */
export function PreviewBody({
  preview,
  parentDisplayId,
  expanded,
  onToggle,
}: {
  preview: TaskMovePreview;
  /** 预览的 detached_parent 只有 id / name，编号取自任务自己的 parent */
  parentDisplayId?: string;
  expanded: boolean;
  onToggle: () => void;
}) {
  const t = useT();
  const sep = t("tasks.move.separator");
  const rows = expanded ? preview.tasks : preview.tasks.slice(0, PREVIEW_ROWS);
  const hidden = preview.tasks.length - PREVIEW_ROWS;
  const assignees = [
    ...new Map(
      preview.removed_assignees.map((a) => [`${a.kind}:${a.id}`, a.name]),
    ).values(),
  ];
  const issues = preview.unlinked_issues.map(
    (issue) => `${issue.repository}#${issue.number}`,
  );
  const parent = preview.detached_parent;
  return (
    <div className="flex flex-col gap-3">
      <ul
        aria-label={t("tasks.move.previewList")}
        className="flex max-h-[320px] flex-col divide-y divide-line-default overflow-y-auto rounded-xl border border-line-default"
      >
        {rows.map((row) => (
          <PreviewRow key={row.task_id} row={row} />
        ))}
      </ul>
      {hidden > 0 && (
        <Button variant="ghost" size="28" onClick={onToggle}>
          {expanded
            ? t("tasks.move.showLess")
            : t("tasks.move.showMore", { count: hidden })}
        </Button>
      )}
      {(parent ||
        assignees.length > 0 ||
        preview.active_runs > 0 ||
        issues.length > 0) && (
        <Callout tone="warning">
          <ul className="flex flex-col gap-1">
            {parent && (
              <li>
                {t("tasks.move.detachedParent", {
                  id: parentDisplayId?.toUpperCase() ?? "",
                  title: parent.name,
                }).replace(/ {2,}/g, " ")}
              </li>
            )}
            {assignees.length > 0 && (
              <li>
                {t("tasks.move.removedAssignees", {
                  names: assignees.join(sep),
                })}
              </li>
            )}
            {preview.active_runs > 0 && (
              <li>
                {t("tasks.move.activeRuns", { count: preview.active_runs })}
              </li>
            )}
            {issues.length > 0 && (
              <li>
                {t("tasks.move.unlinkedIssues", { issues: issues.join(sep) })}
              </li>
            )}
          </ul>
        </Callout>
      )}
      <p className="text-body-sm text-fg-muted">{t("tasks.move.keepNote")}</p>
    </div>
  );
}

function PreviewSkeleton() {
  return (
    <div data-testid="move-preview-loading" className="flex flex-col gap-2">
      <Skeleton className="h-5 w-3/4" />
      <Skeleton className="h-5 w-full" />
      <Skeleton className="h-5 w-2/3" />
    </div>
  );
}
