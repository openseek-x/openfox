import { useMutation, useQueryClient } from "@tanstack/react-query";
import { keys, useApi } from "../context";
import { useState } from "react";

import type { ImportJob } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Dialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  useToast,
} from "@openfox/ui";

import { SettingsCard, SettingsSection } from "../spaces/settings/parts";
import { formatRelativeTime } from "../tasks/primitives/dates";
import { rollbackReasonKey, statusLabelKey, statusTone } from "./model";
import { importErrorText, useImportList } from "./queries";
import { RollbackDialog } from "./RollbackDialog";

// 导入记录：没有记录时整块不渲染。

export function ImportRecords({
  orgId,
  canManage,
  onReimport,
}: {
  orgId: string;
  canManage: boolean;
  onReimport: (job: ImportJob) => void;
}) {
  const t = useT();
  const list = useImportList(orgId);
  const [rollbackJob, setRollbackJob] = useState<ImportJob | null>(null);
  const pages = list.data?.pages ?? [];
  const jobs = pages.flatMap((page) => page.imports);
  const total = pages[0]?.total ?? 0;
  if (jobs.length === 0) return null;
  return (
    <SettingsSection
      title={t("imports.records.title", { total })}
      description={t("imports.records.description")}
    >
      <SettingsCard>
        {jobs.map((job) => (
          <RecordRow
            key={job.id}
            orgId={orgId}
            job={job}
            canManage={canManage}
            onRollback={() => setRollbackJob(job)}
            onReimport={() => onReimport(job)}
          />
        ))}
      </SettingsCard>
      {list.hasNextPage && (
        <div className="flex justify-center">
          <Button
            variant="ghost-muted"
            size="32"
            loading={list.isFetchingNextPage}
            onClick={() => void list.fetchNextPage()}
          >
            {t("imports.records.loadMore")}
          </Button>
        </div>
      )}
      {rollbackJob && (
        <RollbackDialog
          orgId={orgId}
          job={rollbackJob}
          onClose={() => setRollbackJob(null)}
        />
      )}
    </SettingsSection>
  );
}

function RecordRow({
  orgId,
  job,
  canManage,
  onRollback,
  onReimport,
}: {
  orgId: string;
  job: ImportJob;
  canManage: boolean;
  onRollback: () => void;
  onReimport: () => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const toast = useToast();
  const api = useApi();
  const client = useQueryClient();
  const [confirmStop, setConfirmStop] = useState(false);
  const sync = useMutation({
    mutationFn: (state: NonNullable<ImportJob["sync"]>["state"]) =>
      api.imports.setSync(orgId, job.id, state, job.sync!.version),
    onSuccess: () => setConfirmStop(false),
    onError: (error) => toast(importErrorText(t, error)),
    onSettled: () =>
      client.invalidateQueries({ queryKey: keys.imports(orgId) }),
  });
  const linked = job.sync && job.sync.state !== "DISCONNECTED";
  const failed = job.status === "FAILED";
  const reasonKey = rollbackReasonKey(job.rollback.reason);
  const copy = () => {
    void navigator.clipboard
      ?.writeText(job.operation_id)
      .then(() => toast(t("imports.records.copied")));
  };
  return (
    <div
      data-testid={`import-${job.id}`}
      className="flex items-center gap-3 px-4 py-3"
    >
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex items-center gap-2">
          <span className="truncate text-body font-medium">
            {job.source_label} → {job.space_name}
          </span>
          <Badge tone={statusTone(job.status)} small>
            {t(statusLabelKey(job.status))}
          </Badge>
        </div>
        <span className="truncate text-caption text-fg-muted">
          {t("imports.records.meta", {
            file: job.file_name || job.source_label,
            time: formatRelativeTime(job.created_at, locale),
            processed: job.processed_count,
            total: job.total_count,
            created: job.created_count,
          })}
        </span>
        {job.sync && (
          <span className="text-caption text-fg-muted">
            {t(
              job.sync.error_code === "CONFLICT"
                ? "imports.options.syncConflict"
                : job.sync.error_code
                  ? "imports.options.syncError"
                  : job.sync.state === "PAUSED"
                    ? "imports.options.syncPaused"
                    : job.sync.state === "DISCONNECTED"
                      ? "imports.options.syncStopped"
                      : "imports.options.syncActive",
            )}
            {job.sync.last_success &&
              " · " +
                t("imports.options.syncLast", {
                  time: formatRelativeTime(job.sync.last_success, locale),
                })}
          </span>
        )}
        {failed && (
          <span className="flex items-center gap-1 text-caption text-danger">
            {t("imports.records.failedSummary", {
              message: job.error_message || job.error_code,
            })}
            <button
              type="button"
              onClick={copy}
              aria-label={t("imports.records.copyOperation", {
                id: job.operation_id,
              })}
              className="rounded px-1 font-mono hover:bg-danger-soft"
            >
              {job.operation_id}
            </button>
          </span>
        )}
      </div>
      {canManage && (
        <Menu
          label={t("imports.records.menu")}
          placement="bottom-end"
          trigger={({ ref, toggle }) => (
            <IconButton
              ref={ref}
              label={t("imports.records.menu")}
              icon={<Icon name="ellipsis" />}
              size="28"
              onClick={toggle}
            />
          )}
        >
          {linked && (
            <>
              <MenuItem
                disabled={sync.isPending}
                onSelect={() =>
                  sync.mutate(
                    job.sync?.state === "ACTIVE" ? "PAUSED" : "ACTIVE",
                  )
                }
              >
                {t(
                  job.sync?.state === "ACTIVE"
                    ? "imports.options.pauseSync"
                    : "imports.options.resumeSync",
                )}
              </MenuItem>
              <MenuItem danger onSelect={() => setConfirmStop(true)}>
                {t("imports.options.stopSync")}
              </MenuItem>
            </>
          )}
          <MenuItem
            danger
            disabled={!job.rollback.eligible}
            onSelect={onRollback}
          >
            {t("imports.records.rollback")}
          </MenuItem>
          <MenuItem onSelect={onReimport}>
            {t("imports.records.reimport")}
          </MenuItem>
          {!job.rollback.eligible && reasonKey && (
            <>
              <MenuSeparator />
              <p className="px-2.5 py-1.5 text-caption text-fg-muted">
                {t(reasonKey)}
              </p>
            </>
          )}
        </Menu>
      )}
      {confirmStop && (
        <Dialog
          open
          title={t("imports.options.stopSync")}
          description={t("imports.options.stopSyncDesc")}
          onClose={() => setConfirmStop(false)}
          footer={
            <>
              <Button variant="outline" onClick={() => setConfirmStop(false)}>
                {t("common.cancel")}
              </Button>
              <Button
                loading={sync.isPending}
                onClick={() => sync.mutate("DISCONNECTED")}
              >
                {t("imports.options.stopSync")}
              </Button>
            </>
          }
        >
          <></>
        </Dialog>
      )}
    </div>
  );
}
