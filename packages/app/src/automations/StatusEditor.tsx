import type { Label, Project, SpaceAgent, TaskStatus } from "@openfox/api";
import { useT, type MessageKey, type Translate } from "@openfox/i18n";
import { Button, Field, Icon, IconButton, Select, cn } from "@openfox/ui";

import { Picker, statusGroups } from "../tasks/pickers";
import { personName, type Person } from "../tasks/queries";
import { MAX_FILTERS, type FilterDraft, type TriggerDraft } from "./model";

// 任务状态变更触发器。

/** 编辑弹窗需要的候选数据（都由弹窗统一加载，触发器只读取）。 */
export interface TriggerOptions {
  statuses: TaskStatus[];
  types: { id: string; name: string }[];
  projects: Pick<Project, "id" | "name">[];
  labels: Pick<Label, "id" | "name">[];
  people: Person[];
  agents: Pick<SpaceAgent, "id" | "name">[];
}

const FIELDS: FilterDraft["field"][] = [
  "project",
  "task_type",
  "assignee",
  "agent",
  "priority",
  "label",
];

const FIELD_LABEL: Record<FilterDraft["field"], MessageKey> = {
  project: "automations.status.field.project",
  task_type: "automations.status.field.task_type",
  assignee: "automations.status.field.assignee",
  agent: "automations.status.field.agent",
  priority: "automations.status.field.priority",
  label: "automations.status.field.label",
};

const PRIORITY_VALUES = ["URGENT", "HIGH", "MEDIUM", "LOW", "NONE"] as const;

/** 标签字段用「包含 / 不包含」，其余用「等于 / 不等于」。 */
function opsFor(field: FilterDraft["field"]): FilterDraft["op"][] {
  return field === "label" ? ["includes", "excludes"] : ["is", "is_not"];
}

function valuesFor(
  field: FilterDraft["field"],
  options: TriggerOptions,
  t: Translate,
): { value: string; label: string }[] {
  switch (field) {
    case "project":
      return options.projects.map((item) => ({
        value: item.id,
        label: item.name,
      }));
    case "task_type":
      return options.types.map((item) => ({
        value: item.id,
        label: item.name,
      }));
    case "assignee":
      return options.people.map((item) => ({
        value: item.id,
        label: personName(item),
      }));
    case "agent":
      return options.agents.map((item) => ({
        value: item.id,
        label: item.name,
      }));
    case "label":
      return options.labels.map((item) => ({
        value: item.id,
        label: item.name,
      }));
    case "priority":
      return PRIORITY_VALUES.map((value) => ({
        value,
        label: t(`automations.status.priority.${value}` as MessageKey),
      }));
  }
}

export function StatusEditor({
  value,
  options,
  onChange,
}: {
  value: TriggerDraft;
  options: TriggerOptions;
  onChange: (next: TriggerDraft) => void;
}) {
  const t = useT();
  const statusNames = options.statuses
    .filter((status) => value.statusIds.includes(status.id))
    .map((status) => status.name)
    .join("、");
  const canAdd = value.filters.length < MAX_FILTERS;

  const setFilter = (index: number, next: FilterDraft) =>
    onChange({
      ...value,
      filters: value.filters.map((item, position) =>
        position === index ? next : item,
      ),
    });

  return (
    <div className="flex flex-col gap-3">
      <Field label={t("automations.status.label")}>
        <Picker
          label={t("automations.status.label")}
          groups={statusGroups(options.statuses)}
          selected={value.statusIds}
          multiple
          placeholder={t("automations.status.placeholder")}
          onSelect={(id) =>
            onChange({
              ...value,
              statusIds: value.statusIds.includes(id)
                ? value.statusIds.filter((item) => item !== id)
                : [...value.statusIds, id],
            })
          }
          trigger={({ ref, toggle }) => (
            <button
              ref={ref}
              type="button"
              onClick={toggle}
              className="flex h-9 w-full items-center rounded-lg border border-line-default bg-surface-card px-3 text-left text-body hover:bg-surface-hover"
            >
              <span
                className={cn(
                  "min-w-0 flex-1 truncate",
                  !statusNames && "text-fg-subtle",
                )}
              >
                {statusNames || t("automations.status.placeholder")}
              </span>
              <Icon name="chevron-down" size={14} className="text-fg-muted" />
            </button>
          )}
        />
      </Field>

      <div className="flex flex-col gap-2 rounded-[10px] bg-surface-muted/20 p-3">
        <div className="flex items-center">
          <span className="text-body-sm font-medium text-fg-secondary">
            {t("automations.status.filterLabel")}
          </span>
          <span className="ml-auto text-body-sm text-fg-muted">
            {value.filters.length === 0 && t("automations.status.unset")}
          </span>
        </div>
        {value.filters.map((filter, index) => (
          <div key={index} className="flex flex-col gap-1">
            {index > 0 && (
              <span className="text-caption text-fg-muted">
                {t("automations.status.and")}
              </span>
            )}
            <div className="grid grid-cols-[96px_88px_140px_32px] items-center gap-2">
              <Select<FilterDraft["field"]>
                value={filter.field}
                aria-label={t("automations.status.filterLabel")}
                onChange={(field) =>
                  setFilter(index, {
                    field,
                    op: opsFor(field)[0]!,
                    value: "",
                  })
                }
                options={FIELDS.map((field) => ({
                  value: field,
                  label: t(FIELD_LABEL[field]),
                }))}
              />
              <Select<FilterDraft["op"]>
                value={filter.op}
                aria-label={t("automations.status.filterLabel")}
                onChange={(op) => setFilter(index, { ...filter, op })}
                options={opsFor(filter.field).map((op) => ({
                  value: op,
                  label: t(`automations.status.op.${op}` as MessageKey),
                }))}
              />
              <Select<string>
                value={filter.value || null}
                placeholder={
                  valuesFor(filter.field, options, t).length === 0
                    ? t("automations.status.noOptions")
                    : undefined
                }
                disabled={valuesFor(filter.field, options, t).length === 0}
                aria-label={t(FIELD_LABEL[filter.field])}
                onChange={(next) =>
                  setFilter(index, { ...filter, value: next })
                }
                options={valuesFor(filter.field, options, t)}
              />
              <IconButton
                label={t("automations.status.removeCondition")}
                icon={<Icon name="x" />}
                size="28"
                onClick={() =>
                  onChange({
                    ...value,
                    filters: value.filters.filter(
                      (_, position) => position !== index,
                    ),
                  })
                }
              />
            </div>
          </div>
        ))}
        <div>
          <Button
            variant="ghost"
            size="32"
            disabled={!canAdd}
            leading={<Icon name="plus" />}
            onClick={() =>
              onChange({
                ...value,
                filters: [
                  ...value.filters,
                  { field: "project", op: "is", value: "" },
                ],
              })
            }
          >
            {t("automations.status.addCondition")}
          </Button>
        </div>
      </div>
    </div>
  );
}
