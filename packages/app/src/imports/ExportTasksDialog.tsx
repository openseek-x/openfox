import { useEffect, useMemo, useRef, useState } from "react";

import type { ExportFormat } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Checkbox,
  Dialog,
  Radio,
  Spinner,
  useToast,
} from "@openfox/ui";

import {
  MAX_EXPORT_SPACES,
  MAX_EXPORT_TASKS,
  exportErrorMessage,
  newExportKey,
  selectionSummary,
} from "./export-model";
import { useExportSpaces, useStartExport } from "./queries";

// 导出任务数据：选择空间与格式，后台生成，结果在「导出记录」里下载。

export function ExportTasksDialog({
  orgId,
  canManage,
  onClose,
}: {
  orgId: string;
  /** 所有者 / 管理员默认全选，成员默认不选。 */
  canManage: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const spacesQuery = useExportSpaces(orgId, true);
  const start = useStartExport(orgId);
  const spaces = useMemo(
    () => spacesQuery.data?.spaces ?? [],
    [spacesQuery.data],
  );
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [format, setFormat] = useState<ExportFormat>("csv");
  const [error, setError] = useState<ReturnType<
    typeof exportErrorMessage
  > | null>(null);
  // 同一次打开期间复用幂等键：网络重试不会重复创建
  const keyRef = useRef(newExportKey());
  const initialised = useRef(false);

  useEffect(() => {
    if (initialised.current || !spacesQuery.data) return;
    initialised.current = true;
    if (canManage) setSelected(new Set(spaces.map((space) => space.id)));
  }, [spacesQuery.data, spaces, canManage]);

  const summary = selectionSummary(spaces, selected);
  const allSelected = spaces.length > 0 && summary.spaces === spaces.length;
  const canStart = summary.spaces > 0 && !summary.over && !start.isPending;

  const toggle = (id: string) => {
    keyRef.current = newExportKey();
    setError(null);
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };
  const toggleAll = () => {
    keyRef.current = newExportKey();
    setError(null);
    setSelected(allSelected ? new Set() : new Set(spaces.map((s) => s.id)));
  };

  const submit = () => {
    setError(null);
    start.mutate(
      {
        request: { space_ids: [...selected], format },
        idempotencyKey: keyRef.current,
      },
      {
        onSuccess: () => {
          toast(t("imports.export.started"));
          onClose();
        },
        onError: (e) => setError(exportErrorMessage(e)),
      },
    );
  };

  return (
    <Dialog
      open
      width={560}
      className="max-h-[85vh]"
      onClose={start.isPending ? undefined : onClose}
      title={t("imports.export.dialog.title")}
      description={t("imports.export.dialog.description")}
      footer={
        <div className="flex justify-end gap-2">
          <Button
            variant="secondary"
            size="28"
            disabled={start.isPending}
            onClick={onClose}
          >
            {t("imports.cancel")}
          </Button>
          <Button
            size="28"
            disabled={!canStart}
            loading={start.isPending}
            onClick={submit}
          >
            {start.isPending
              ? t("imports.export.starting")
              : t("imports.export.start")}
          </Button>
        </div>
      }
    >
      <div className="flex flex-col gap-4">
        <section className="flex flex-col gap-2">
          <div className="flex items-center justify-between">
            <span className="text-body font-medium">
              {t("imports.export.spaces.label")}
            </span>
            {spaces.length > 0 && (
              <label className="flex items-center gap-2 text-body-sm text-fg-muted">
                <Checkbox
                  checked={allSelected}
                  indeterminate={summary.spaces > 0 && !allSelected}
                  onChange={toggleAll}
                />
                {t("imports.export.spaces.selectAll")}
              </label>
            )}
          </div>
          {spacesQuery.isLoading ? (
            <div
              role="status"
              aria-label={t("imports.export.spaces.loading")}
              className="flex justify-center py-4"
            >
              <Spinner />
            </div>
          ) : spacesQuery.isError ? (
            <p role="alert" className="text-body-sm text-danger">
              {t("imports.export.spaces.failed")}
            </p>
          ) : spaces.length === 0 ? (
            <p className="text-body-sm text-fg-muted">
              {t("imports.export.spaces.empty")}
            </p>
          ) : (
            <ul className="flex max-h-[240px] flex-col overflow-y-auto rounded-xl border border-line-default">
              {spaces.map((space) => (
                <li key={space.id}>
                  <label className="flex cursor-pointer items-center gap-3 px-3 py-2 hover:bg-surface-hover">
                    <Checkbox
                      checked={selected.has(space.id)}
                      onChange={() => toggle(space.id)}
                    />
                    <span className="min-w-0 flex-1 truncate text-body">
                      {space.name}
                    </span>
                    <span className="font-mono text-caption text-fg-muted">
                      {space.prefix}
                    </span>
                    <span className="text-caption text-fg-muted">
                      {t("imports.export.spaces.taskCount", {
                        count: space.task_count,
                      })}
                    </span>
                  </label>
                </li>
              ))}
            </ul>
          )}
        </section>

        <fieldset className="flex flex-col gap-2">
          <legend className="mb-1 text-body font-medium">
            {t("imports.export.format.label")}
          </legend>
          {(["csv", "json"] as const).map((value) => (
            <label
              key={value}
              className="flex cursor-pointer items-start gap-3 rounded-xl border border-line-default px-3 py-2 hover:bg-surface-hover"
            >
              <Radio
                name="export-format"
                value={value}
                checked={format === value}
                onChange={() => {
                  keyRef.current = newExportKey();
                  setFormat(value);
                }}
                className="mt-1"
              />
              <span className="flex flex-col gap-0.5">
                <span className="text-body font-medium">
                  {t(`imports.export.format.${value}`)}
                </span>
                <span className="text-caption text-fg-muted">
                  {t(`imports.export.format.${value}Desc`)}
                </span>
              </span>
            </label>
          ))}
        </fieldset>

        <p aria-live="polite" className="text-body-sm text-fg-muted">
          {t("imports.export.spaces.selected", {
            spaces: summary.spaces,
            tasks: summary.tasks,
          })}
        </p>
        {summary.over && (
          <p role="alert" className="text-body-sm text-danger">
            {t("imports.export.overLimit", {
              maxSpaces: MAX_EXPORT_SPACES,
              maxTasks: MAX_EXPORT_TASKS.toLocaleString("en-US"),
            })}
          </p>
        )}
        {error && (
          <p role="alert" className="text-body-sm text-danger">
            {"key" in error ? t(error.key) : error.text}
          </p>
        )}
      </div>
    </Dialog>
  );
}
