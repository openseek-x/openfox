import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import { isApiError, type DeletedTask } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  ConfirmDialog,
  Icon,
  Spinner,
  Tooltip,
  useToast,
} from "@openfox/ui";

import { useSpaceRetired } from "../../spaces/readonly";
import { formatRelativeTime } from "../primitives/dates";
import { StatusIcon } from "../primitives/StatusIcon";
import { TaskTypeIcon } from "../primitives/TaskTypeIcon";
import { taskErrorMessage } from "../queries";
import { useDeletedTasks, useRestoreTask } from "./queries";

// 空间「最近删除」：空间管理员看全部，其他人只看自己删除的（后端按 scope 返回）。
// 恢复先确认；成功提示带「打开」；409 时提示已被恢复或修改（列表由 restoreDeletedTask 刷新）。
// 停用空间只能查看，恢复按钮禁用并提示原因。

const GRID =
  "grid grid-cols-[minmax(0,1fr)_120px_120px_140px_120px_88px] items-center gap-3";

export function TrashPage() {
  const t = useT();
  const { locale } = useI18n();
  const { orgId = "", spaceId = "" } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const retired = useSpaceRetired(orgId, spaceId);
  const list = useDeletedTasks(orgId, spaceId);
  const restore = useRestoreTask(orgId);
  const [pending, setPending] = useState<DeletedTask | null>(null);

  if (list.isLoading)
    return (
      <div className="flex flex-1 items-center justify-center">
        <Spinner />
      </div>
    );
  if (list.error)
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center">
        <p className="text-body text-danger">{t("tasks.trash.loadFailed")}</p>
        <Button variant="outline" size="28" onClick={() => void list.refetch()}>
          {t("common.retry")}
        </Button>
      </div>
    );

  const tasks = list.data?.pages.flatMap((page) => page.tasks) ?? [];
  const mine = list.data?.pages[0]?.scope !== "all";
  const pendingId = pending?.display_id.toUpperCase() ?? "";

  const confirmRestore = () => {
    if (!pending) return;
    restore.mutate(
      { spaceId, taskId: pending.id, version: pending.version },
      {
        onSuccess: (task) => {
          setPending(null);
          const id = task.display_id.toUpperCase();
          toast(t("tasks.trash.restored", { id }), {
            action: {
              label: t("tasks.trash.open"),
              onClick: () =>
                navigate(
                  `/o/${orgId}/spaces/${task.space_id}/tasks?task=${encodeURIComponent(id)}`,
                ),
            },
          });
        },
        onError: (error) => {
          setPending(null);
          toast(
            isApiError(error, "VERSION_CONFLICT")
              ? t("tasks.trash.conflict")
              : taskErrorMessage(t, error),
            { tone: "error" },
          );
        },
      },
    );
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex h-12 shrink-0 items-center gap-2 border-b border-line-default px-4">
        <h1 className="text-title font-medium">{t("tasks.trash.title")}</h1>
      </header>
      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 py-3">
        {mine ? (
          <div className="flex flex-col gap-0.5">
            <h2 className="text-body font-medium">
              {t("tasks.trash.mineTitle")}
            </h2>
            <p className="text-caption text-fg-muted">
              {t("tasks.trash.mineDescription")}
            </p>
          </div>
        ) : (
          <p className="text-caption text-fg-muted">
            {t("tasks.trash.allDescription")}
          </p>
        )}
        {retired && (
          <Callout tone="warning">{t("tasks.trash.retired")}</Callout>
        )}
        {tasks.length === 0 ? (
          <div className="flex flex-1 flex-col items-center justify-center gap-2 p-10 text-center">
            <Icon name="trash-2" size={28} className="text-fg-muted" />
            <p className="text-body font-medium">{t("tasks.trash.empty")}</p>
            <p className="text-caption text-fg-muted">
              {t("tasks.trash.emptyDescription")}
            </p>
          </div>
        ) : (
          <div
            role="table"
            aria-label={t("tasks.trash.listLabel")}
            className="flex flex-col overflow-x-auto"
          >
            <div role="rowgroup">
              <div
                role="row"
                className={`${GRID} min-w-[720px] border-b border-line-default px-2 py-1.5 text-caption text-fg-muted`}
              >
                <span role="columnheader">{t("tasks.trash.columns.task")}</span>
                <span role="columnheader">{t("tasks.trash.columns.type")}</span>
                <span role="columnheader">
                  {t("tasks.trash.columns.status")}
                </span>
                <span role="columnheader">
                  {t("tasks.trash.columns.deletedBy")}
                </span>
                <span role="columnheader">
                  {t("tasks.trash.columns.deletedAt")}
                </span>
                <span role="columnheader" className="sr-only">
                  {t("tasks.trash.columns.actions")}
                </span>
              </div>
            </div>
            <div role="rowgroup">
              {tasks.map((task) => (
                <TrashRow
                  key={task.id}
                  task={task}
                  locale={locale}
                  retired={retired}
                  onRestore={() => setPending(task)}
                />
              ))}
            </div>
            {list.hasNextPage && (
              <div className="flex justify-center py-3">
                <Button
                  variant="ghost-muted"
                  size="28"
                  loading={list.isFetchingNextPage}
                  onClick={() => void list.fetchNextPage()}
                >
                  {t("tasks.trash.loadMore")}
                </Button>
              </div>
            )}
          </div>
        )}
      </div>
      <ConfirmDialog
        open={pending !== null}
        title={t("tasks.trash.restoreTitle", { id: pendingId })}
        description={t("tasks.trash.restoreDesc")}
        confirmLabel={t("tasks.trash.restore")}
        loading={restore.isPending}
        onCancel={() => setPending(null)}
        onConfirm={confirmRestore}
      />
    </div>
  );
}

function TrashRow({
  task,
  locale,
  retired,
  onRestore,
}: {
  task: DeletedTask;
  locale: string;
  retired: boolean;
  onRestore: () => void;
}) {
  const t = useT();
  const id = task.display_id.toUpperCase();
  const button = (
    <Button
      variant="outline"
      size="28"
      leading={<Icon name="rotate-ccw" size={14} />}
      aria-label={t("tasks.trash.restoreLabel", { id })}
      disabled={retired}
      onClick={onRestore}
    >
      {t("tasks.trash.restore")}
    </Button>
  );
  return (
    <div
      role="row"
      className={`${GRID} min-w-[720px] rounded-lg px-2 py-2 text-body hover:bg-surface-hover`}
    >
      <span role="cell" className="flex min-w-0 items-center gap-2">
        <span className="shrink-0 text-caption text-fg-muted tabular-nums">
          {id}
        </span>
        <span className="truncate">{task.title}</span>
      </span>
      <span role="cell" className="flex min-w-0 items-center gap-1.5">
        <TaskTypeIcon
          icon={task.type_icon}
          className="shrink-0 text-fg-muted"
        />
        <span className="truncate">{task.type_name}</span>
      </span>
      <span role="cell" className="flex min-w-0 items-center gap-1.5">
        <StatusIcon
          category={task.status_category}
          color={task.status_color}
          className="shrink-0"
        />
        <span className="truncate">{task.status_name}</span>
      </span>
      <span role="cell" className="truncate text-fg-muted">
        {task.deleted_by?.name || t("tasks.trash.unknownUser")}
      </span>
      <span
        role="cell"
        className="truncate text-fg-muted"
        title={new Date(task.deleted_at).toLocaleString(locale)}
      >
        {formatRelativeTime(task.deleted_at, locale)}
      </span>
      <span role="cell" className="flex justify-end">
        {retired ? (
          <Tooltip content={t("tasks.trash.retired")}>
            <span
              tabIndex={0}
              title={t("tasks.trash.retired")}
              aria-label={t("tasks.trash.retired")}
            >
              <span className="pointer-events-none inline-flex">{button}</span>
            </span>
          </Tooltip>
        ) : (
          button
        )}
      </span>
    </div>
  );
}
