import { isApiError, type ImportJob } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog, Skeleton } from "@openfox/ui";

import {
  importErrorText,
  useRollbackImport,
  useRollbackPreview,
} from "./queries";

// 回滚确认框：打开时读预览，确认后软删除本次导入创建的任务。

export function RollbackDialog({
  orgId,
  job,
  onClose,
}: {
  orgId: string;
  job: ImportJob;
  onClose: () => void;
}) {
  const t = useT();
  const preview = useRollbackPreview(orgId, job.id, true);
  const rollback = useRollbackImport(orgId);
  const data = preview.data;
  const error = rollback.error;
  const errorText =
    error && isApiError(error, "RUN_ACTIVE")
      ? t("imports.rollback.runActive")
      : error
        ? importErrorText(t, error)
        : "";
  const confirm = () =>
    rollback.mutate(
      { importId: job.id, version: job.version },
      { onSuccess: onClose },
    );
  return (
    <Dialog
      open
      onClose={onClose}
      title={t("imports.rollback.title")}
      width={520}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            {t("imports.cancel")}
          </Button>
          <Button
            variant="danger"
            loading={rollback.isPending}
            disabled={!data?.eligible || rollback.isPending}
            onClick={confirm}
          >
            {rollback.isPending
              ? t("imports.rollback.running")
              : t("imports.rollback.confirm")}
          </Button>
        </>
      }
    >
      {preview.isPending ? (
        <Skeleton className="h-[80px] rounded-lg" />
      ) : !data ? (
        <p role="alert" className="text-body text-fg-muted">
          {t("imports.rollback.loadFailed")}
        </p>
      ) : (
        <div className="flex flex-col gap-3">
          <p className="text-body text-fg-default">
            {t("imports.rollback.body", {
              source: job.source_label,
              space: job.space_name,
              count: data.created_count,
            })}
          </p>
          {!!data.project_count && (
            <p>
              {t("imports.rollback.projects", { count: data.project_count })}
            </p>
          )}
          {data.modified_total > 0 && (
            <div className="flex flex-col gap-1.5">
              <p className="text-body-sm text-fg-muted">
                {t("imports.rollback.modified", { count: data.modified_total })}
              </p>
              <ul
                aria-label={t("imports.rollback.modifiedList")}
                className="flex max-h-[160px] flex-col gap-1 overflow-y-auto rounded-lg bg-surface-muted p-2.5 text-body-sm"
              >
                {data.modified.map((task) => (
                  <li key={task.task_id} className="truncate">
                    <span className="text-fg-muted">{task.display_id}</span>{" "}
                    {task.title}
                  </li>
                ))}
                {data.modified_total > data.modified.length && (
                  <li className="text-fg-muted">
                    {t("imports.rollback.more", {
                      count: data.modified_total - data.modified.length,
                    })}
                  </li>
                )}
              </ul>
            </div>
          )}
          {(data.cleanup.statuses > 0 ||
            data.cleanup.labels > 0 ||
            data.cleanup.iterations > 0) && (
            <p className="text-body-sm text-fg-muted">
              {data.cleanup.iterations > 0
                ? t("imports.rollback.cleanupIterations", {
                    statuses: data.cleanup.statuses,
                    labels: data.cleanup.labels,
                    iterations: data.cleanup.iterations,
                  })
                : t("imports.rollback.cleanup", {
                    statuses: data.cleanup.statuses,
                    labels: data.cleanup.labels,
                  })}
            </p>
          )}
          {data.space_created && (
            <p className="text-body-sm text-fg-muted">
              {t("imports.rollback.spaceKept", { name: job.space_name })}
            </p>
          )}
          {!data.eligible && (
            <Callout tone="warning">
              {t("imports.rollback.unavailable")}
            </Callout>
          )}
        </div>
      )}
      {errorText && (
        <p role="alert" className="mt-3 text-body-sm text-danger">
          {t("imports.rollback.failed", { reason: errorText })}
        </p>
      )}
    </Dialog>
  );
}
