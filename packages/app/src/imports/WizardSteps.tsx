import { readImportWorkbook } from "./excel";
import { useRef, useState, type ReactNode } from "react";

import type {
  ImportAnalysis,
  ImportColumnMapping,
  ImportField,
  ImportJob,
  ImportNewSpace,
  ImportOptions,
  ImportStatusMapping,
  ImportUserMapping,
  SpaceDetail,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import {
  Badge,
  Button,
  Callout,
  Checkbox,
  Icon,
  Select,
  Switch,
  TextInput,
  Tooltip,
  cn,
} from "@openfox/ui";

import { StatusIcon } from "../tasks/primitives/StatusIcon";
import { personName, useOrgPeople, useStatuses } from "../tasks/queries";
import {
  FIELD_ORDER,
  MAX_FILE_BYTES,
  NEW_CATEGORIES,
  RUN_STEPS,
  UNSUPPORTED_FIELDS,
  defaultCategory,
  migrationCounts,
  newStatusCount,
  runStepIndex,
  sortedStatuses,
  validateColumns,
  type ImportMode,
  type NewSpaceDraft,
} from "./model";
import { OptionRow, SpaceTarget, StepTitle } from "./parts";

// 向导各步骤的界面。

const NEW_STATUS = "__new__";

const tableClass = "w-full border-collapse text-left text-body-sm";
const thClass = "px-3 py-2 text-caption font-medium text-fg-muted";
const tdClass = "px-3 py-2 align-middle";

// ---------------- 5.4 欢迎 ----------------

export function WelcomeStep({ mode }: { mode: ImportMode }) {
  const t = useT();
  const points: MessageKey[] = [
    "imports.welcome.point1",
    "imports.welcome.point2",
    mode === "api" ? "imports.welcome.apiSync" : "imports.welcome.point3",
  ];
  return (
    <div className="flex flex-col gap-4">
      <StepTitle
        title={t("imports.welcome.title")}
        description={t(
          mode === "api"
            ? "imports.welcome.apiDescription"
            : "imports.welcome.description",
        )}
      />
      <ul className="flex flex-col gap-2">
        {points.map((key) => (
          <li key={key} className="flex items-start gap-2 text-body">
            <Icon name="check" className="mt-0.5 text-accent" />
            {t(key)}
          </li>
        ))}
      </ul>
      <Callout tone="info">
        {mode === "api"
          ? t("imports.welcome.apiPath")
          : t("imports.welcome.exportPath")}
      </Callout>
    </div>
  );
}

// ---------------- 5.14 上传 ----------------

export function UploadStep({
  excel = false,
  sheet,
  onSheet,
  spaces,
  spaceId,
  onSpace,
  newSpace,
  onNewSpace,
  file,
  onFile,
  error,
}: {
  excel?: boolean;
  sheet?: string;
  onSheet?: (name: string) => void;
  spaces: readonly SpaceDetail[];
  spaceId: string;
  onSpace: (id: string) => void;
  newSpace: NewSpaceDraft;
  onNewSpace: (value: NewSpaceDraft) => void;
  file: File | null;
  onFile: (file: File | null) => void;
  error: string;
}) {
  const t = useT();
  const input = useRef<HTMLInputElement | null>(null);
  const [localError, setLocalError] = useState("");
  const [over, setOver] = useState(false);
  const [sheets, setSheets] = useState<string[]>([]);
  const generation = useRef(0);
  const pick = async (picked: File | undefined) => {
    if (!picked) return;
    const current = ++generation.current;
    onFile(null);
    onSheet?.("");
    setSheets([]);
    if (
      !(excel
        ? /\.xlsx?$/i.test(picked.name)
        : picked.name.toLowerCase().endsWith(".csv"))
    ) {
      setLocalError(
        t(excel ? "imports.upload.badExcel" : "imports.upload.notCsv"),
      );
      return;
    }
    if (picked.size > MAX_FILE_BYTES) {
      setLocalError(t("imports.errors.tooLarge"));
      return;
    }
    setLocalError("");
    if (excel) {
      const result = await readImportWorkbook(picked);
      if (generation.current !== current) return;
      if (!result.sheets?.length) {
        setLocalError(t("imports.upload.badExcel"));
        return;
      }
      setSheets(result.sheets);
      onSheet?.(result.sheets[0]!);
    }
    onFile(picked);
  };
  const shown = localError || error;
  return (
    <div className="flex flex-col gap-4">
      <StepTitle
        title={t("imports.upload.title")}
        description={t(
          excel ? "imports.upload.excelDesc" : "imports.upload.description",
        )}
      />
      <SpaceTarget
        spaces={spaces}
        spaceId={spaceId}
        onSpace={onSpace}
        newSpace={newSpace}
        onNewSpace={onNewSpace}
        prefixHint={t("imports.newSpace.prefixCsvHint")}
      />
      {sheets.length > 1 && (
        <label className="grid gap-1">
          {t("imports.upload.sheet")}
          <select value={sheet} onChange={(e) => onSheet?.(e.target.value)}>
            {sheets.map((v) => (
              <option key={v}>{v}</option>
            ))}
          </select>
        </label>
      )}
      <div
        data-testid="import-dropzone"
        onDragOver={(event) => {
          event.preventDefault();
          setOver(true);
        }}
        onDragLeave={() => setOver(false)}
        onDrop={(event) => {
          event.preventDefault();
          setOver(false);
          pick(event.dataTransfer.files[0]);
        }}
        className={cn(
          "flex flex-col items-center gap-2 rounded-xl border border-dashed border-line-strong px-6 py-8 text-center",
          over && "bg-surface-hover",
        )}
      >
        <Icon name="upload" className="text-fg-muted" />
        <p className="text-body">
          {file
            ? file.name
            : t(excel ? "imports.upload.excelDrop" : "imports.upload.drop")}
        </p>
        <p className="text-caption text-fg-muted">
          {t(excel ? "imports.upload.excelLimit" : "imports.upload.limit")}
        </p>
        <input
          ref={input}
          type="file"
          accept={excel ? ".xls,.xlsx" : ".csv,text/csv"}
          aria-label={t(
            excel ? "imports.upload.excelDrop" : "imports.upload.file",
          )}
          className="sr-only"
          onChange={(event) => {
            pick(event.target.files?.[0]);
            event.target.value = "";
          }}
        />
        <Button
          variant="secondary"
          size="32"
          onClick={() => input.current?.click()}
        >
          {t("imports.upload.choose")}
        </Button>
      </div>
      {shown && (
        <p role="alert" className="text-body-sm text-danger">
          {shown}
        </p>
      )}
    </div>
  );
}

// ---------------- 预览 ----------------

export function PreviewStep({ job }: { job: ImportJob }) {
  const t = useT();
  const preview = job.preview;
  if (!preview) return null;
  const stats: [MessageKey, number][] = [
    ["imports.preview.total", preview.total_rows],
    ["imports.preview.importable", preview.importable_rows],
    ["imports.preview.skipped", preview.skipped_rows],
    ["imports.preview.failed", preview.failed_rows],
  ];
  return (
    <div className="flex flex-col gap-4">
      <StepTitle
        title={t("imports.preview.title")}
        description={t("imports.preview.description", { file: job.file_name })}
      />
      <div className="grid grid-cols-4 gap-3">
        {stats.map(([key, value]) => (
          <div
            key={key}
            className="flex flex-col gap-0.5 rounded-xl border border-line-default bg-surface-card px-4 py-3"
          >
            <span className="text-caption text-fg-muted">{t(key)}</span>
            <span className="text-title font-bold">{value}</span>
          </div>
        ))}
      </div>
      <div className="max-h-[260px] overflow-auto rounded-xl border border-line-default">
        <table className={tableClass} aria-label={t("imports.preview.samples")}>
          <thead>
            <tr>
              <th className={thClass}>{t("imports.preview.column")}</th>
              {[1, 2, 3].map((n) => (
                <th key={n} className={thClass}>
                  {t("imports.preview.sample", { n })}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {preview.columns.map((column) => (
              <tr key={column.index} className="border-t border-line-soft">
                <td className={cn(tdClass, "font-medium")}>{column.header}</td>
                {[0, 1, 2].map((i) => (
                  <td
                    key={i}
                    className={cn(
                      tdClass,
                      "max-w-[200px] truncate text-fg-muted",
                    )}
                  >
                    {column.samples[i] ?? ""}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {preview.row_errors.length > 0 && (
        <Callout tone="warning" title={t("imports.preview.rowErrors")}>
          <ul className="flex flex-col gap-0.5">
            {preview.row_errors.slice(0, 20).map((item, index) => (
              <li key={index}>
                {t("imports.preview.rowError", {
                  row: item.row,
                  message: item.message,
                })}
              </li>
            ))}
          </ul>
        </Callout>
      )}
    </div>
  );
}

// ---------------- 5.15 映射列 ----------------

export function ColumnsStep({
  job,
  columns,
  onChange,
}: {
  job: ImportJob;
  columns: ImportColumnMapping[];
  onChange: (columns: ImportColumnMapping[]) => void;
}) {
  const t = useT();
  const issues = validateColumns(columns);
  const [filter, setFilter] = useState<"all" | "mapped" | "custom" | "skipped">(
    "all",
  );
  const group = (field: ImportField) =>
    field === "ignore" || UNSUPPORTED_FIELDS.has(field)
      ? "skipped"
      : field === "custom"
        ? "custom"
        : "mapped";
  const counts = { mapped: 0, custom: 0, skipped: 0 };
  for (const c of columns) counts[group(c.field)]++;
  const options = FIELD_ORDER.map((field) => ({
    value: field,
    label: UNSUPPORTED_FIELDS.has(field)
      ? t("imports.columns.unsupported", {
          name: t(`imports.fields.${field}`),
        })
      : t(`imports.fields.${field}`),
  }));
  return (
    <div className="flex flex-col gap-4">
      <StepTitle
        title={t("imports.columns.title")}
        description={t("imports.columns.description")}
      />
      <div className="flex flex-wrap items-center justify-between gap-2 text-body-sm text-fg-muted">
        <span>{t("imports.columns.summary", counts)}</span>
        <Select<typeof filter>
          aria-label={t("imports.columns.filter")}
          value={filter}
          onChange={setFilter}
          options={(["all", "mapped", "custom", "skipped"] as const).map(
            (value) => ({
              value,
              label: t(`imports.columns.filters.${value}`),
            }),
          )}
        />
      </div>
      <div className="max-h-[420px] overflow-auto rounded-xl border border-line-default">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>{t("imports.columns.file")}</th>
              <th className={thClass}>{t("imports.columns.field")}</th>
            </tr>
          </thead>
          <tbody>
            {job.preview?.columns.map((column) => {
              const mapping = columns.find(
                (item) => item.index === column.index,
              );
              if (
                filter !== "all" &&
                group(mapping?.field ?? "ignore") !== filter
              )
                return null;
              return (
                <tr key={column.index} className="border-t border-line-soft">
                  <td className={tdClass}>
                    <div className="font-medium">{column.header}</div>
                    <div className="max-w-[260px] truncate text-caption text-fg-muted">
                      {column.samples[0] ?? ""}
                    </div>
                  </td>
                  <td className={cn(tdClass, "w-[240px]")}>
                    <Select<ImportField>
                      aria-label={t("imports.columns.mapAria", {
                        name: column.header,
                      })}
                      value={mapping?.field ?? "ignore"}
                      options={options}
                      onChange={(field) =>
                        onChange(
                          columns.map((item) =>
                            item.index === column.index
                              ? {
                                  index: item.index,
                                  field,
                                  ...(field === "custom"
                                    ? {
                                        custom_name:
                                          item.custom_name ?? column.header,
                                      }
                                    : {}),
                                }
                              : item,
                          ),
                        )
                      }
                    />
                    {mapping?.field === "custom" && (
                      <TextInput
                        className="mt-1"
                        aria-label={t("imports.columns.customName", {
                          name: column.header,
                        })}
                        placeholder={t("imports.columns.customPlaceholder")}
                        value={mapping.custom_name ?? ""}
                        onChange={(event) =>
                          onChange(
                            columns.map((item) =>
                              item.index === column.index
                                ? { ...item, custom_name: event.target.value }
                                : item,
                            ),
                          )
                        }
                      />
                    )}
                  </td>
                </tr>
              );
            })}
            {filter !== "all" && !counts[filter] && (
              <tr>
                <td colSpan={2} className={tdClass}>
                  {t("imports.columns.empty")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      {issues.length > 0 && (
        <ul
          role="alert"
          className="flex flex-col gap-0.5 text-body-sm text-danger"
        >
          {issues.map((issue) => (
            <li key={issue.code + ("field" in issue ? issue.field : "")}>
              {issue.code === "duplicate_column_field"
                ? t("imports.columns.issue.duplicate_column_field", {
                    field: t(`imports.fields.${issue.field}`),
                  })
                : t(`imports.columns.issue.${issue.code}`)}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// ---------------- 5.8 映射状态 ----------------

export function StatusesStep({
  brand = "Jira",
  orgId,
  analysis,
  mappings,
  onChange,
}: {
  brand?: string;
  orgId: string;
  analysis: ImportAnalysis;
  mappings: ImportStatusMapping[];
  onChange: (mappings: ImportStatusMapping[]) => void;
}) {
  const t = useT();
  const statuses = useStatuses(orgId);
  const existing = sortedStatuses(statuses.data ?? []);
  const options = [
    ...existing.map((status) => ({
      value: status.id,
      label: status.name,
      description: t(`tasks.statusCategory.${status.category}`),
      icon: (
        <StatusIcon category={status.category} color={status.color} size={14} />
      ),
    })),
    { value: NEW_STATUS, label: t("imports.statuses.createNew") },
  ];
  const update = (source: string, next: ImportStatusMapping) =>
    onChange(mappings.map((item) => (item.source === source ? next : item)));
  return (
    <div className="flex flex-col gap-4">
      <StepTitle
        title={t("imports.statuses.title")}
        description={t("imports.statuses.description", { brand })}
      />
      <div className="overflow-auto rounded-xl border border-line-default">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass}>
                {t("imports.statuses.source", { brand })}
              </th>
              <th className={thClass}>{t("imports.statuses.target")}</th>
            </tr>
          </thead>
          <tbody>
            {analysis.statuses.map((item) => {
              const mapping = mappings.find((m) => m.source === item.source);
              const isNew = mapping ? !mapping.status_id : true;
              return (
                <tr key={item.source} className="border-t border-line-soft">
                  <td className={tdClass}>
                    <span className="font-medium">{item.source}</span>{" "}
                    <span className="text-fg-muted">
                      {t("imports.statuses.count", { count: item.count })}
                    </span>
                  </td>
                  <td className={cn(tdClass, "w-[340px]")}>
                    <div className="flex flex-col gap-2">
                      <Select
                        aria-label={t("imports.statuses.mapAria", {
                          name: item.source,
                        })}
                        value={
                          isNew ? NEW_STATUS : (mapping?.status_id ?? null)
                        }
                        options={options}
                        onChange={(value) =>
                          update(
                            item.source,
                            value === NEW_STATUS
                              ? {
                                  source: item.source,
                                  new_name: item.source,
                                  new_category: defaultCategory(
                                    item.source,
                                    item.done,
                                  ),
                                }
                              : { source: item.source, status_id: value },
                          )
                        }
                      />
                      {isNew && mapping && (
                        <div className="flex gap-2">
                          <TextInput
                            aria-label={t("imports.statuses.newName", {
                              name: item.source,
                            })}
                            value={mapping.new_name ?? ""}
                            onChange={(event) =>
                              update(item.source, {
                                ...mapping,
                                new_name: event.target.value,
                              })
                            }
                          />
                          <Select
                            aria-label={t("imports.statuses.newCategory", {
                              name: item.source,
                            })}
                            className="w-[140px] shrink-0"
                            value={mapping.new_category ?? null}
                            options={NEW_CATEGORIES.map((category) => ({
                              value: category,
                              label: t(`tasks.statusCategory.${category}`),
                            }))}
                            onChange={(category) =>
                              update(item.source, {
                                ...mapping,
                                new_category: category,
                              })
                            }
                          />
                        </div>
                      )}
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ---------------- 5.16 映射用户 ----------------

const MODES: ImportUserMapping["mode"][] = [
  "member",
  "invite",
  "external",
  "skip",
];

export function UsersStep({
  brand = "Jira",
  orgId,
  analysis,
  mappings,
  onChange,
}: {
  brand?: string;
  orgId: string;
  analysis: ImportAnalysis;
  mappings: ImportUserMapping[];
  onChange: (mappings: ImportUserMapping[]) => void;
}) {
  const t = useT();
  const people = useOrgPeople(orgId).data ?? [];
  const update = (id: string, next: ImportUserMapping) =>
    onChange(mappings.map((item) => (item.source_id === id ? next : item)));
  return (
    <div className="flex flex-col gap-4">
      <StepTitle
        title={t("imports.users.title")}
        description={t("imports.users.description")}
      />
      {analysis.users.length === 0 ? (
        <p className="text-body text-fg-muted">{t("imports.users.empty")}</p>
      ) : (
        <div className="max-h-[420px] overflow-auto rounded-xl border border-line-default">
          <table className={tableClass}>
            <thead>
              <tr>
                <th className={thClass}>
                  {t("imports.users.source", { brand })}
                </th>
                <th className={thClass}>{t("imports.users.mode")}</th>
              </tr>
            </thead>
            <tbody>
              {analysis.users.map((item) => {
                const mapping = mappings.find(
                  (m) => m.source_id === item.source_id,
                ) ?? { source_id: item.source_id, mode: "external" as const };
                return (
                  <tr
                    key={item.source_id}
                    className="border-t border-line-soft"
                  >
                    <td className={tdClass}>
                      <span className="font-medium">{item.name}</span>{" "}
                      <span className="text-fg-muted">
                        {t("imports.users.count", { count: item.count })}
                      </span>
                    </td>
                    <td className={cn(tdClass, "w-[380px]")}>
                      <div className="flex gap-2">
                        <Select
                          aria-label={t("imports.users.modeAria", {
                            name: item.name,
                          })}
                          className="w-[160px] shrink-0"
                          value={mapping.mode}
                          options={MODES.map((mode) => ({
                            value: mode,
                            label: t(`imports.users.modes.${mode}`),
                          }))}
                          onChange={(mode) =>
                            update(item.source_id, {
                              source_id: item.source_id,
                              mode,
                              ...(mode === "member" && item.suggested_user_id
                                ? { user_id: item.suggested_user_id }
                                : {}),
                            })
                          }
                        />
                        {mapping.mode === "member" &&
                          item.suggest_reason === "verified" &&
                          mapping.user_id === item.suggested_user_id && (
                            <Badge tone="neutral" small>
                              {t("imports.users.verifiedMatch")}
                            </Badge>
                          )}
                        {mapping.mode === "member" &&
                          item.suggest_reason === "email" &&
                          mapping.user_id === item.suggested_user_id && (
                            <Badge tone="neutral" small>
                              {t("imports.users.emailMatch")}
                            </Badge>
                          )}
                        {mapping.mode === "member" && (
                          <Select
                            aria-label={t("imports.users.memberAria", {
                              name: item.name,
                            })}
                            value={mapping.user_id ?? null}
                            placeholder={t("imports.users.memberPlaceholder")}
                            options={people.map((person) => ({
                              value: person.id,
                              label: personName(person),
                            }))}
                            onChange={(userId) =>
                              update(item.source_id, {
                                ...mapping,
                                user_id: userId,
                              })
                            }
                          />
                        )}
                        {mapping.mode === "invite" && (
                          <TextInput
                            type="email"
                            aria-label={t("imports.users.emailAria", {
                              name: item.name,
                            })}
                            placeholder={t("imports.users.emailPlaceholder")}
                            value={mapping.email ?? ""}
                            onChange={(event) =>
                              update(item.source_id, {
                                ...mapping,
                                email: event.target.value,
                              })
                            }
                          />
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

// ---------------- 5.9 选项 ----------------

export function OptionsStep({
  brand = "Jira",
  options,
  onChange,
  mode,
  capabilities,
}: {
  brand?: string;
  options: ImportOptions;
  onChange: (options: ImportOptions) => void;
  mode: ImportMode;
  capabilities: ImportAnalysis["capabilities"];
}) {
  const t = useT();
  // API 导入的「导入未完成 / 已完成」在选择任务步骤里设置
  const toggles: [keyof ImportOptions, MessageKey, MessageKey][] = [
    ...(mode === "csv"
      ? ([
          ["import_open", "imports.options.open", "imports.options.openDesc"],
          ["import_done", "imports.options.done", "imports.options.doneDesc"],
        ] as [keyof ImportOptions, MessageKey, MessageKey][])
      : []),
    [
      "migrated_label",
      "imports.options.migrated",
      "imports.options.migratedDesc",
    ],
    ["overwrite", "imports.options.overwrite", "imports.options.overwriteDesc"],
  ];
  // 可能被禁用的开关：不可用时关闭显示并给出原因
  const gated: [
    keyof ImportOptions,
    MessageKey,
    MessageKey,
    boolean,
    MessageKey,
  ][] = [
    [
      "import_sprints",
      "imports.options.iterations",
      "imports.options.iterationsDesc",
      capabilities.sprints,
      mode === "csv"
        ? "imports.options.iterationsTip"
        : "imports.options.iterationsTipApi",
    ],
    [
      "keep_numbers",
      "imports.options.keepNumbers",
      "imports.options.keepNumbersDesc",
      capabilities.keep_numbers,
      "imports.options.keepNumbersTip",
    ],
  ];
  return (
    <div className="flex flex-col gap-4">
      <StepTitle title={t("imports.options.title")} />
      <div className="flex flex-col rounded-xl border border-line-default bg-surface-card">
        {toggles.map(([key, title, desc]) => (
          <OptionRow
            key={key}
            title={t(title)}
            description={t(desc, { brand })}
            control={
              <Switch
                label={t(title)}
                checked={!!options[key]}
                onCheckedChange={(value) =>
                  onChange({ ...options, [key]: value })
                }
              />
            }
          />
        ))}
        {gated
          .filter(
            ([key]) =>
              brand === "Jira" ||
              (key === "import_sprints" && capabilities.sprint_count > 0),
          )
          .map(([key, title, desc, enabled, tip]) => (
            <OptionRow
              key={key}
              title={t(title)}
              description={t(desc, { brand })}
              control={
                enabled ? (
                  <Switch
                    label={t(title)}
                    checked={!!options[key]}
                    onCheckedChange={(value) =>
                      onChange({ ...options, [key]: value })
                    }
                  />
                ) : (
                  <Tooltip content={t(tip)}>
                    <span tabIndex={0} className="inline-flex">
                      <Switch label={t(title)} checked={false} disabled />
                    </span>
                  </Tooltip>
                )
              }
            />
          ))}
        {capabilities.project_sync && (
          <OptionRow
            title={t("imports.options.sync", { brand })}
            description={t(
              brand === "Jira" || brand === "禅道" || brand === "ZenTao"
                ? "imports.options.siteSyncDesc"
                : "imports.options.projectSyncDesc",
              { brand },
            )}
            control={
              <Switch
                label={t("imports.options.sync", { brand })}
                checked={!!options.continuous_sync}
                onCheckedChange={(continuous_sync) =>
                  onChange({ ...options, continuous_sync })
                }
              />
            }
          />
        )}
        {brand === "Jira" && !capabilities.project_sync && (
          <OptionRow
            title={t("imports.options.sync", { brand })}
            description={t("imports.options.syncDesc")}
            control={
              <Tooltip content={t("imports.options.syncTip", { brand })}>
                <span tabIndex={0} className="inline-flex">
                  <Switch
                    label={t("imports.options.sync", { brand })}
                    checked={false}
                    disabled
                  />
                </span>
              </Tooltip>
            }
          />
        )}
      </div>
      {options.overwrite && (
        <Callout tone="warning">
          {t("imports.options.overwriteWarning")}
        </Callout>
      )}
    </div>
  );
}

// ---------------- 5.10 确认 ----------------

export function ConfirmStep({
  analysis,
  options,
  statuses,
  confirmed,
  onConfirmed,
  canManage,
  newSpace,
}: {
  newSpace: ImportNewSpace | null;
  analysis: ImportAnalysis;
  options: ImportOptions;
  statuses: ImportStatusMapping[];
  confirmed: boolean;
  onConfirmed: (value: boolean) => void;
  canManage: boolean;
}) {
  const t = useT();
  const counts = migrationCounts(analysis, options);
  const rows: [string, ReactNode, ReactNode][] = [
    ...(newSpace
      ? ([
          [
            t("imports.upload.space"),
            newSpace.prefix
              ? t("imports.confirm.newSpace", {
                  name: newSpace.name,
                  prefix: newSpace.prefix,
                })
              : newSpace.name,
            "—",
          ],
        ] as [string, ReactNode, ReactNode][])
      : []),
    [t("imports.confirm.openTasks"), counts.migrate.open, counts.skip.open],
    [t("imports.confirm.doneTasks"), counts.migrate.done, counts.skip.done],
    [
      t("imports.confirm.users"),
      t("imports.confirm.usersValue", analysis.members),
      "—",
    ],
    [
      t("imports.confirm.workflow"),
      t("imports.confirm.workflowValue", { count: newStatusCount(statuses) }),
      "—",
    ],
  ];
  const overwriteNeeded = options.overwrite && analysis.overwrite.modified > 0;
  return (
    <div className="flex flex-col gap-4">
      <StepTitle
        title={t("imports.confirm.title")}
        description={t("imports.confirm.description")}
      />
      <div className="overflow-hidden rounded-xl border border-line-default">
        <table className={tableClass}>
          <thead>
            <tr>
              <th className={thClass} />
              <th className={thClass}>{t("imports.confirm.migrate")}</th>
              <th className={thClass}>{t("imports.confirm.skip")}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map(([label, migrate, skip]) => (
              <tr key={label} className="border-t border-line-soft">
                <td className={cn(tdClass, "font-medium")}>{label}</td>
                <td className={tdClass}>{migrate}</td>
                <td className={cn(tdClass, "text-fg-muted")}>{skip}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {overwriteNeeded && (
        <Callout
          tone="warning"
          title={t("imports.confirm.overwriteWarning", {
            count: analysis.overwrite.modified,
          })}
        >
          <label className="flex items-center gap-2 text-body-sm">
            <Checkbox
              checked={confirmed}
              onChange={(event) => onConfirmed(event.target.checked)}
            />
            {t("imports.confirm.overwriteAck")}
          </label>
        </Callout>
      )}
      {analysis.issues.length > 0 && (
        <Callout tone="danger" title={t("imports.confirm.issues")}>
          <ul className="flex flex-col gap-0.5">
            {analysis.issues.map((issue, index) => (
              <li key={index}>{issue.message}</li>
            ))}
          </ul>
        </Callout>
      )}
      {!canManage && <Callout tone="info">{t("imports.adminOnly")}</Callout>}
    </div>
  );
}

// ---------------- 5.11 进行中 ----------------

export function RunningView({
  job,
  onCancel,
  cancelling,
}: {
  job: ImportJob;
  onCancel: () => void;
  cancelling: boolean;
}) {
  const t = useT();
  const current = runStepIndex(job);
  const percent =
    job.total_count > 0
      ? Math.min(100, Math.round((job.processed_count / job.total_count) * 100))
      : 0;
  const counts: [MessageKey, number][] = [
    ["imports.run.created", job.created_count],
    ["imports.run.updated", job.updated_count],
    ["imports.run.skipped", job.skipped_count],
    ["imports.run.failed", job.failed_count],
  ];
  return (
    <div className="flex flex-col gap-4">
      <StepTitle
        title={
          job.status === "CANCEL_REQUESTED"
            ? t("imports.run.cancelling")
            : t("imports.run.title")
        }
        description={t("imports.run.description")}
      />
      <ol className="flex flex-col gap-2">
        {RUN_STEPS.map((step, index) => (
          <li
            key={step}
            aria-current={index === current ? "step" : undefined}
            className={cn(
              "flex items-center gap-2 text-body",
              index > current && "text-fg-muted",
            )}
          >
            <Icon
              name={index < current ? "check" : "circle"}
              className={index < current ? "text-accent" : undefined}
            />
            {t(`imports.run.steps.${step}`)}
          </li>
        ))}
      </ol>
      <div className="flex flex-col gap-1.5">
        <div
          role="progressbar"
          aria-valuemin={0}
          aria-valuemax={job.total_count}
          aria-valuenow={job.processed_count}
          className="h-2 overflow-hidden rounded-full bg-surface-muted"
        >
          <div
            className="h-full bg-accent transition-[width]"
            style={{ width: `${percent}%` }}
          />
        </div>
        <span className="text-caption text-fg-muted">
          {t("imports.run.progress", {
            processed: job.processed_count,
            total: job.total_count,
          })}
        </span>
      </div>
      <div className="grid grid-cols-4 gap-3">
        {counts.map(([key, value]) => (
          <div
            key={key}
            className="flex flex-col gap-0.5 rounded-xl border border-line-default bg-surface-card px-4 py-3"
          >
            <span className="text-caption text-fg-muted">{t(key)}</span>
            <span className="text-title font-bold">{value}</span>
          </div>
        ))}
      </div>
      <div>
        <Button
          variant="secondary"
          loading={cancelling}
          disabled={job.status === "CANCEL_REQUESTED" || cancelling}
          onClick={onCancel}
        >
          {t("imports.run.cancel")}
        </Button>
      </div>
    </div>
  );
}

// ---------------- 5.12 完成 ----------------

const RESULT_LABEL: Record<string, MessageKey> = {
  sent: "imports.done.invite.sent",
  failed: "imports.done.invite.failed",
  already_member: "imports.done.invite.already_member",
  already_invited: "imports.done.invite.already_invited",
};

export function DoneView({
  job,
  onViewSpace,
  onFinish,
}: {
  job: ImportJob;
  onViewSpace: () => void;
  onFinish: () => void;
}) {
  const t = useT();
  const counts: [MessageKey, number][] = [
    ["imports.run.created", job.created_count],
    ["imports.run.updated", job.updated_count],
    ["imports.run.skipped", job.skipped_count],
    ["imports.run.failed", job.failed_count],
  ];
  return (
    <div className="flex flex-col gap-4">
      <StepTitle
        title={
          job.status === "FAILED"
            ? t("imports.done.failedTitle")
            : job.status === "CANCELLED"
              ? t("imports.done.cancelledTitle")
              : t("imports.done.title")
        }
        description={
          job.status === "CANCELLED"
            ? t("imports.done.cancelledDesc", {
                count: job.created_count + job.updated_count,
              })
            : undefined
        }
      />
      {job.status === "FAILED" && (
        <Callout tone="danger">
          {job.error_message || job.error_code}
          {" · "}
          {job.operation_id}
        </Callout>
      )}
      <div className="grid grid-cols-4 gap-3">
        {counts.map(([key, value]) => (
          <div
            key={key}
            className="flex flex-col gap-0.5 rounded-xl border border-line-default bg-surface-card px-4 py-3"
          >
            <span className="text-caption text-fg-muted">{t(key)}</span>
            <span className="text-title font-bold">{value}</span>
          </div>
        ))}
      </div>
      {job.warnings.length > 0 && (
        <Callout tone="warning" title={t("imports.done.warnings")}>
          <ul className="flex flex-col gap-0.5">
            {job.warnings.map((warning) => (
              <li key={warning.code}>
                {warning.message}
                {warning.count > 1 && ` ×${warning.count}`}
              </li>
            ))}
          </ul>
        </Callout>
      )}
      {(job.failures?.length ?? 0) > 0 && (
        <Callout tone="danger" title={t("imports.done.failures")}>
          <ul className="flex flex-col gap-0.5">
            {job.failures!.map((failure) => (
              <li key={failure.ordinal}>
                {failure.external_key || `#${failure.ordinal + 1}`}：
                {failure.error}
              </li>
            ))}
          </ul>
        </Callout>
      )}
      {(job.invitations?.length ?? 0) > 0 && (
        <div className="flex flex-col gap-1.5">
          <h3 className="text-body font-medium">
            {t("imports.done.invitations")}
          </h3>
          <ul className="flex flex-col gap-1">
            {job.invitations!.map((item) => (
              <li
                key={item.source_id}
                className="flex items-center gap-2 text-body-sm"
              >
                <span className="truncate">{item.email}</span>
                <Badge
                  tone={item.result === "failed" ? "danger" : "neutral"}
                  small
                >
                  {t(RESULT_LABEL[item.result]!)}
                </Badge>
              </li>
            ))}
          </ul>
        </div>
      )}
      <div className="flex gap-2">
        <Button onClick={onViewSpace} disabled={!job.space_id}>
          {t("imports.done.viewSpace")}
        </Button>
        <Button variant="secondary" onClick={onFinish}>
          {t("imports.done.finish")}
        </Button>
      </div>
    </div>
  );
}
