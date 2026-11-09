import type { ExportJob } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Badge, useToast } from "@openfox/ui";

import { useApi } from "../context";
import { SettingsCard, SettingsSection } from "../spaces/settings/parts";
import { formatRelativeTime } from "../tasks/primitives/dates";
import { exportStatusKey, exportStatusTone } from "./export-model";
import { useExportList } from "./queries";

// 导出记录：没有记录时整块不渲染；下载链接由浏览器带登录 cookie 直接访问。

export function ExportRecords({ orgId }: { orgId: string }) {
  const t = useT();
  const list = useExportList(orgId);
  const jobs = list.data?.exports ?? [];
  if (jobs.length === 0) return null;
  return (
    <SettingsSection
      title={t("imports.export.records.title")}
      description={t("imports.export.records.description")}
    >
      <SettingsCard>
        {jobs.map((job) => (
          <ExportRow key={job.id} orgId={orgId} job={job} />
        ))}
      </SettingsCard>
    </SettingsSection>
  );
}

function ExportRow({ orgId, job }: { orgId: string; job: ExportJob }) {
  const t = useT();
  const { locale } = useI18n();
  const toast = useToast();
  const api = useApi();
  const copy = () => {
    void navigator.clipboard
      ?.writeText(job.operation_id)
      .then(() => toast(t("imports.records.copied")));
  };
  return (
    <div
      data-testid={`export-${job.id}`}
      className="flex items-center gap-3 px-4 py-3"
    >
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex items-center gap-2">
          <span className="truncate text-body font-medium">
            {job.file_name || t("imports.export.dialog.title")}
          </span>
          <Badge tone={exportStatusTone(job.status)} small>
            {t(exportStatusKey(job.status))}
          </Badge>
        </div>
        <span className="truncate text-caption text-fg-muted">
          {t("imports.export.records.meta", {
            format: job.format.toUpperCase(),
            spaces: job.space_count,
            tasks: job.task_count,
            time: formatRelativeTime(job.created_at, locale),
          })}
        </span>
        {job.status === "FAILED" && (
          <span className="flex items-center gap-1 text-caption text-danger">
            {t("imports.export.records.failedSummary", {
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
      {job.downloadable && (
        <a
          href={api.exportTasks.downloadUrl(orgId, job.id)}
          download
          aria-label={t("imports.export.records.downloadLabel", {
            name: job.file_name,
          })}
          className="rounded-lg border border-line-default px-3 py-1 text-body-sm font-medium hover:bg-surface-hover"
        >
          {t("imports.export.records.download")}
        </a>
      )}
    </div>
  );
}
