import { useRef, useState } from "react";

import { useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  Icon,
  IconButton,
  Popover,
  Segmented,
  Switch,
  cn,
} from "@openfox/ui";

import {
  BOARD_COLUMN_FIELDS,
  BOARD_ROW_FIELDS,
  LIST_SUBGROUP_FIELDS,
  SORT_KEYS,
  TASK_PROPERTIES,
  type BoardRowField,
  type DisplaySettings,
  type Layout,
  type SortKey,
  type TaskProperty,
} from "../model";

/** 分组下拉（列 / 行 / 二级分组） */
function GroupSelect<F extends BoardRowField>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: F;
  options: F[];
  onChange: (next: F) => void;
}) {
  const t = useT();
  return (
    <label className="flex min-h-8 items-center justify-between gap-3 text-body">
      <span className="text-fg-muted">{label}</span>
      <select
        aria-label={label}
        value={value}
        onChange={(event) => onChange(event.target.value as F)}
        className="h-7 rounded-md border border-line-default bg-surface-card px-2 text-body-sm"
      >
        {options.map((field) => (
          <option key={field} value={field}>
            {t(`tasks.displayMenu.groupBy.${field}`)}
          </option>
        ))}
      </select>
    </label>
  );
}

const PROPERTY_LABEL: Record<TaskProperty, MessageKey> = {
  id: "tasks.fields.id",
  status: "tasks.fields.status",
  assignee: "tasks.fields.assignee",
  priority: "tasks.fields.priority",
  due: "tasks.fields.dueDate",
  labels: "tasks.fields.label",
  created: "tasks.fields.created",
  updated: "tasks.fields.updated",
  start: "tasks.fields.startDate",
  subtasks: "tasks.fields.subtasks",
  estimate: "spaces.task.estimate",
  iteration: "spaces.sections.iterations",
  project: "projects.title",
};
export function DisplayMenu({
  value,
  onChange,
  onReset,
  manualSort,
  estimation = false,
  iterationEnabled = false,
  projectEnabled = false,
}: {
  value: DisplaySettings;
  onChange: (next: DisplaySettings) => void;
  onReset: () => void;
  /** 我的任务不支持手动排序 */
  manualSort: boolean;
  estimation?: boolean;
  /** 是否显示迭代排序与属性 */
  iterationEnabled?: boolean;
  /** 是否显示专项排序与属性 */
  projectEnabled?: boolean;
}) {
  const t = useT();
  const anchor = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  const set = <K extends keyof DisplaySettings>(
    key: K,
    v: DisplaySettings[K],
  ) => onChange({ ...value, [key]: v });
  const layouts: Array<{
    id: Layout;
    icon: "kanban" | "list" | "file-spreadsheet" | "square-chart-gantt";
  }> = [
    { id: "board", icon: "kanban" },
    { id: "list", icon: "list" },
    { id: "table", icon: "file-spreadsheet" },
    { id: "gantt", icon: "square-chart-gantt" },
  ];
  const row = "flex min-h-8 items-center justify-between gap-3 text-body";
  return (
    <>
      <IconButton
        ref={anchor}
        label={t("tasks.display")}
        icon={<Icon name="sliders-horizontal" />}
        variant="round"
        active={open}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      />
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        placement="bottom-end"
        aria-label={t("tasks.display")}
        className="w-[340px] p-3"
      >
        <div className="flex flex-col gap-3">
          <div
            role="radiogroup"
            aria-label={t("tasks.displayMenu.layout")}
            className="grid grid-cols-4 gap-1.5"
          >
            {layouts.map((item) => {
              const selected = item.id === value.layout;
              return (
                <button
                  key={item.id}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  onClick={() => set("layout", item.id)}
                  className={cn(
                    "flex h-14 flex-col items-center justify-center gap-1 rounded-lg border text-caption",
                    selected
                      ? "border-line-focus bg-surface-active font-medium"
                      : "border-line-default hover:bg-surface-hover",
                  )}
                >
                  <Icon name={item.icon} />
                  {t(`tasks.displayMenu.${item.id}`)}
                </button>
              );
            })}
          </div>
          {value.layout === "board" && (
            <>
              <GroupSelect
                label={t("tasks.displayMenu.columns")}
                value={value.boardColumns}
                options={BOARD_COLUMN_FIELDS.filter(
                  (f) => iterationEnabled || f !== "iteration",
                )}
                onChange={(next) =>
                  onChange({
                    ...value,
                    boardColumns: next,
                    // 行不能与列相同
                    boardRows:
                      value.boardRows === next ? "none" : value.boardRows,
                  })
                }
              />
              <GroupSelect
                label={t("tasks.displayMenu.rows")}
                value={value.boardRows}
                options={BOARD_ROW_FIELDS.filter(
                  (f) =>
                    f !== value.boardColumns &&
                    (iterationEnabled || f !== "iteration"),
                )}
                onChange={(next) => set("boardRows", next)}
              />
            </>
          )}
          {value.layout === "list" && (
            <GroupSelect
              label={t("tasks.displayMenu.subgroup")}
              value={value.listSubgroup}
              options={LIST_SUBGROUP_FIELDS.filter(
                (f) => iterationEnabled || f !== "iteration",
              )}
              onChange={(next) => set("listSubgroup", next)}
            />
          )}
          <label className={row}>
            <span className="text-fg-muted">{t("tasks.displayMenu.sort")}</span>
            <select
              value={value.sort}
              onChange={(event) => set("sort", event.target.value as SortKey)}
              className="h-7 rounded-md border border-line-default bg-surface-card px-2 text-body-sm"
            >
              {SORT_KEYS.filter(
                (key) =>
                  (manualSort || key !== "manual") &&
                  (estimation || key !== "estimate") &&
                  (iterationEnabled || key !== "iteration") &&
                  (projectEnabled || key !== "project"),
              ).map((key) => (
                <option key={key} value={key}>
                  {key === "estimate"
                    ? t("spaces.task.estimate")
                    : t(`tasks.displayMenu.sortKeys.${key}`)}
                </option>
              ))}
            </select>
          </label>
          <div className={row}>
            <span className="text-fg-muted">
              {t("tasks.displayMenu.completed")}
            </span>
            <Segmented
              label={t("tasks.displayMenu.completed")}
              value={value.completed}
              onChange={(v) => set("completed", v)}
              className="h-7"
              options={[
                { value: "all", label: t("tasks.displayMenu.completedAll") },
                { value: "hide", label: t("tasks.displayMenu.completedHide") },
              ]}
            />
          </div>
          <div className={row}>
            <span>{t("tasks.displayMenu.showSubtasks")}</span>
            <Switch
              label={t("tasks.displayMenu.showSubtasks")}
              checked={value.showSubtasks}
              onCheckedChange={(v) => set("showSubtasks", v)}
            />
          </div>
          <div className="border-t border-line-default pt-3">
            <div className="mb-1 text-caption text-fg-muted">
              {t("tasks.displayMenu.boardOptions")}
            </div>
            <div className={row}>
              <span>{t("tasks.displayMenu.showEmptyColumns")}</span>
              <Switch
                label={t("tasks.displayMenu.showEmptyColumns")}
                checked={value.showEmptyColumns}
                onCheckedChange={(v) => set("showEmptyColumns", v)}
              />
            </div>
            <div className={row}>
              <span
                className={cn(value.boardRows === "none" && "text-fg-muted")}
              >
                {t("tasks.displayMenu.showEmptyRows")}
              </span>
              <Switch
                label={t("tasks.displayMenu.showEmptyRows")}
                checked={value.showEmptyRows}
                disabled={value.boardRows === "none"}
                onCheckedChange={(v) => set("showEmptyRows", v)}
              />
            </div>
          </div>
          <div className="border-t border-line-default pt-3">
            <div className="mb-2 text-caption text-fg-muted">
              {t("tasks.displayMenu.properties")}
            </div>
            <div className="flex flex-wrap gap-1.5">
              {TASK_PROPERTIES.filter(
                (p) =>
                  (estimation || p !== "estimate") &&
                  (iterationEnabled || p !== "iteration") &&
                  (projectEnabled || p !== "project"),
              ).map((p) => {
                const on = value.properties.includes(p);
                return (
                  <button
                    key={p}
                    type="button"
                    aria-pressed={on}
                    onClick={() =>
                      set(
                        "properties",
                        on
                          ? value.properties.filter((x) => x !== p)
                          : [...value.properties, p],
                      )
                    }
                    className={cn(
                      "h-6 rounded-full border px-2.5 text-caption",
                      on
                        ? "border-primary bg-primary text-fg-inverse"
                        : "border-line-default text-fg-secondary hover:bg-surface-hover",
                    )}
                  >
                    {t(PROPERTY_LABEL[p])}
                  </button>
                );
              })}
            </div>
          </div>
          <div className="flex justify-end border-t border-line-default pt-3">
            <Button variant="ghost-muted" size="28" onClick={onReset}>
              {t("tasks.displayMenu.reset")}
            </Button>
          </div>
        </div>
      </Popover>
    </>
  );
}
