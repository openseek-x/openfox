import { useRef, useState } from "react";
import type {
  TriageAction,
  TriageCondition,
  TriageConditionGroup,
  TriageRule,
} from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  DateFieldPopover,
  Icon,
  IconButton,
  Select,
  Switch,
  TextInput,
  cn,
  type SearchMenuGroup,
} from "@openfox/ui";

import type { FilterOptions } from "../../tasks/filters/Filters";
import { Picker } from "../../tasks/pickers";
import {
  useTriageRuleCreate,
  useTriageRuleDelete,
  useTriageRuleEnabled,
  useTriageRuleReorder,
  useTriageRuleUpdate,
} from "../../triage/queries";
import { SettingsSection } from "./parts";

// 9.6 分拣规则：规则列表与内联编辑器。条件字段与运算符和后端一致（与任务筛选对齐，另加来源）。
type Field = TriageCondition["field"];
type Op = TriageCondition["op"];
type ActionType = TriageAction["type"];

const OPS: Record<Field, Op[]> = {
  source: ["is", "is_not"],
  priority: ["is", "is_not"],
  type: ["is", "is_not"],
  creator: ["is", "is_not"],
  assignee: ["is", "is_not"],
  crew: ["is", "is_not"],
  label: ["has_any", "has_all", "has_none"],
  content: ["contains", "not_contains", "starts_with", "ends_with", "exactly"],
  due_date: ["before", "after", "between", "none"],
  project: ["is", "is_not"],
  iteration: ["is", "is_not"],
};
const FIELDS = Object.keys(OPS) as Field[];
const ACTIONS: ActionType[] = [
  "move_space",
  "set_status",
  "assign",
  "add_labels",
  "set_project",
  "set_priority",
];
const LIST_ACTIONS: ActionType[] = ["assign", "add_labels"];
const SOURCES = ["member", "guest", "agent", "integration"] as const;
const SOURCE_KEY = {
  member: "spaces.triage.sourceMember",
  guest: "spaces.triage.sourceGuest",
  agent: "spaces.triage.sourceAgent",
  integration: "spaces.triage.sourceIntegration",
} as const;

export interface RuleSources {
  options: FilterOptions;
  spaces: { id: string; name: string }[];
}

/** 处理人候选的值是 user:<id> / agent:<id>，规则里存裸 id。 */
const bareIds = (
  groups: SearchMenuGroup[] = [],
  keep?: (group: SearchMenuGroup) => boolean,
) =>
  groups
    .filter((group) => group.id !== "none" && (!keep || keep(group)))
    .map((group) => ({
      ...group,
      options: group.options.map((option) => ({
        ...option,
        value: option.value.slice(option.value.indexOf(":") + 1),
      })),
    }));

const withoutValues = (groups: SearchMenuGroup[] = [], drop: string[]) =>
  groups.map((group) => ({
    ...group,
    options: group.options.filter((option) => !drop.includes(option.value)),
  }));

/** 条件字段或动作的候选值，复用任务筛选的选项来源。 */
function choices(
  t: Translate,
  key: Field | ActionType,
  sources: RuleSources,
): SearchMenuGroup[] {
  const { options, spaces } = sources;
  switch (key) {
    case "source":
      return [
        {
          options: SOURCES.map((value) => ({
            value,
            label: t(SOURCE_KEY[value]),
          })),
        },
      ];
    case "assignee":
      return bareIds(options.assignee);
    case "assign":
      return bareIds(options.assignee, (group) => group.id === "people");
    case "iteration":
      return withoutValues(options.iteration, ["current"]);
    case "set_project":
      return withoutValues(options.project, ["none"]);
    case "set_status":
      return options.status ?? [];
    case "set_priority":
      return options.priority ?? [];
    case "add_labels":
      return options.label ?? [];
    case "move_space":
      return [
        {
          options: spaces.map((space) => ({
            value: space.id,
            label: space.name,
          })),
        },
      ];
    default:
      return options[key as import("../../tasks/model").FilterField] ?? [];
  }
}

function labelOf(groups: SearchMenuGroup[], value: string) {
  for (const group of groups)
    for (const option of group.options)
      if (option.value === value) return option.label;
  return value;
}

/** 两条启用规则条件完全相同、却对同一动作设置了不同的值。 */
function conflicts(rule: TriageRule, rules: TriageRule[]) {
  if (!rule.enabled) return false;
  const same = JSON.stringify(rule.conditions);
  return rules.some(
    (other) =>
      other.id !== rule.id &&
      other.enabled &&
      JSON.stringify(other.conditions) === same &&
      other.actions.some((action) =>
        rule.actions.some(
          (mine) =>
            mine.type === action.type &&
            !LIST_ACTIONS.includes(mine.type) &&
            JSON.stringify(mine.value) !== JSON.stringify(action.value),
        ),
      ),
  );
}

export function TriageRules({
  orgId,
  spaceId,
  rules,
  editable,
  sources,
}: {
  orgId: string;
  spaceId: string;
  rules: TriageRule[];
  editable: boolean;
  sources: RuleSources;
}) {
  const t = useT();
  const create = useTriageRuleCreate(orgId, spaceId);
  const reorder = useTriageRuleReorder(orgId, spaceId);
  const [editing, setEditing] = useState<string | null>(null);
  // 新规则先停用：在用户填好条件并启用之前，不影响进入分拣台的任务。
  const add = () =>
    create.mutate(
      {
        name: t("spaces.triage.addRule"),
        enabled: false,
        conditions: [],
        actions: [{ type: "set_priority", value: "NORMAL" }],
      },
      { onSuccess: (rule) => setEditing((rule as TriageRule).id) },
    );
  const move = (index: number, delta: number) => {
    const ids = rules.map((rule) => rule.id);
    [ids[index], ids[index + delta]] = [ids[index + delta]!, ids[index]!];
    reorder.mutate(ids);
  };
  return (
    <SettingsSection
      title={t("spaces.triage.rules")}
      description={t("spaces.triage.rulesDescription")}
      action={
        <Button
          size="28"
          disabled={!editable || create.isPending}
          leading={<Icon name="plus" />}
          onClick={add}
        >
          {t("spaces.triage.addRule")}
        </Button>
      }
    >
      <div className="flex flex-col gap-3">
        {rules.map((rule, index) => (
          <RuleCard
            key={`${rule.id}:${rule.version}`}
            orgId={orgId}
            spaceId={spaceId}
            rule={rule}
            index={index}
            last={index === rules.length - 1}
            editable={editable}
            editing={editing === rule.id}
            conflict={conflicts(rule, rules)}
            sources={sources}
            onEdit={(open) => setEditing(open ? rule.id : null)}
            onMove={(delta) => move(index, delta)}
          />
        ))}
        {rules.length === 0 && (
          <div className="rounded-xl border border-dashed border-line-default p-8 text-center text-body-sm text-fg-muted">
            {t("spaces.triage.noRules")}
          </div>
        )}
      </div>
    </SettingsSection>
  );
}

function RuleCard({
  orgId,
  spaceId,
  rule,
  index,
  last,
  editable,
  editing,
  conflict,
  sources,
  onEdit,
  onMove,
}: {
  orgId: string;
  spaceId: string;
  rule: TriageRule;
  index: number;
  last: boolean;
  editable: boolean;
  editing: boolean;
  conflict: boolean;
  sources: RuleSources;
  onEdit: (open: boolean) => void;
  onMove: (delta: number) => void;
}) {
  const t = useT();
  const enabled = useTriageRuleEnabled(orgId, spaceId);
  const remove = useTriageRuleDelete(orgId, spaceId);
  const [confirm, setConfirm] = useState(false);
  return (
    <div
      className={cn(
        "rounded-xl border border-line-default bg-surface-card",
        !rule.enabled && "opacity-70",
      )}
    >
      <div className="flex items-center gap-2 px-4 py-3">
        <span className="w-6 text-caption text-fg-muted">#{index + 1}</span>
        <span className="min-w-0 flex-1 truncate text-body font-medium">
          {rule.name}
        </span>
        {!rule.enabled && (
          <span className="rounded bg-surface-muted px-1.5 py-0.5 text-caption text-fg-muted">
            {t("spaces.triage.disabled")}
          </span>
        )}
        <IconButton
          label={t("spaces.triage.moveUp")}
          icon={<Icon name="chevron-up" />}
          size="28"
          disabled={!editable || index === 0}
          onClick={() => onMove(-1)}
        />
        <IconButton
          label={t("spaces.triage.moveDown")}
          icon={<Icon name="chevron-down" />}
          size="28"
          disabled={!editable || last}
          onClick={() => onMove(1)}
        />
        <Switch
          checked={rule.enabled}
          disabled={!editable || enabled.isPending}
          label={rule.name}
          onCheckedChange={(next) =>
            enabled.mutate({
              id: rule.id,
              version: rule.version,
              enabled: next,
            })
          }
        />
        <IconButton
          label={t("spaces.triage.editRule")}
          icon={<Icon name="pencil" />}
          size="28"
          disabled={!editable}
          onClick={() => onEdit(!editing)}
        />
        <IconButton
          label={t("spaces.triage.deleteRule")}
          icon={<Icon name="trash-2" />}
          size="28"
          disabled={!editable}
          onClick={() => setConfirm(true)}
        />
      </div>
      <div className="border-t border-line-soft px-4 py-3 text-body-sm">
        {conflict && (
          <div className="mb-2 rounded-md bg-warning-soft px-2 py-1 text-warning">
            {t("spaces.triage.conflict")}
          </div>
        )}
        {editing ? (
          <RuleEditor
            orgId={orgId}
            spaceId={spaceId}
            rule={rule}
            sources={sources}
            onDone={() => onEdit(false)}
          />
        ) : (
          <RuleSummary rule={rule} sources={sources} />
        )}
      </div>
      <ConfirmDialog
        open={confirm}
        title={t("spaces.triage.deleteTitle")}
        description={rule.name}
        confirmLabel={t("spaces.triage.deleteRule")}
        danger
        loading={remove.isPending}
        onCancel={() => setConfirm(false)}
        onConfirm={() =>
          remove.mutate(
            { id: rule.id, version: rule.version },
            { onSuccess: () => setConfirm(false) },
          )
        }
      />
    </div>
  );
}

function valueText(
  t: Translate,
  key: Field | ActionType,
  values: string[],
  sources: RuleSources,
) {
  if (key === "content" || key === "due_date") return values.join(" – ");
  const groups = choices(t, key, sources);
  return values.map((value) => labelOf(groups, value)).join("、");
}

function RuleSummary({
  rule,
  sources,
}: {
  rule: TriageRule;
  sources: RuleSources;
}) {
  const t = useT();
  const chip = (text: string, key: string) => (
    <span key={key} className="rounded-md bg-surface-muted px-2 py-1">
      {text}
    </span>
  );
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="w-8 text-fg-muted">{t("spaces.triage.when")}</span>
        {rule.conditions.length === 0
          ? chip(t("spaces.triage.allRequests"), "all")
          : rule.conditions.map((group, groupIndex) => [
              groupIndex > 0 && (
                <span key={`or-${groupIndex}`} className="text-fg-muted">
                  {t("spaces.triage.or")}
                </span>
              ),
              ...group.conditions.map((condition, index) =>
                chip(
                  [
                    t(`spaces.triage.fields.${condition.field}`),
                    t(`spaces.triage.ops.${condition.op}`),
                    valueText(t, condition.field, condition.values, sources),
                  ]
                    .filter(Boolean)
                    .join(" "),
                  `${groupIndex}-${index}`,
                ),
              ),
            ])}
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="w-8 text-fg-muted">{t("spaces.triage.then")}</span>
        {rule.actions.map((action, index) =>
          chip(
            `${t(`spaces.triage.actions.${action.type}`)} | ${valueText(t, action.type, toList(action.value), sources)}`,
            String(index),
          ),
        )}
      </div>
    </div>
  );
}

const toList = (value: TriageAction["value"]) =>
  Array.isArray(value) ? value : [value];

function RuleEditor({
  orgId,
  spaceId,
  rule,
  sources,
  onDone,
}: {
  orgId: string;
  spaceId: string;
  rule: TriageRule;
  sources: RuleSources;
  onDone: () => void;
}) {
  const t = useT();
  const update = useTriageRuleUpdate(orgId, spaceId);
  const [name, setName] = useState(rule.name);
  const [groups, setGroups] = useState<TriageConditionGroup[]>(rule.conditions);
  const [actions, setActions] = useState<TriageAction[]>(rule.actions);
  const blank: TriageCondition = { field: "source", op: "is", values: [] };
  const setCondition = (
    groupIndex: number,
    index: number,
    next: TriageCondition | null,
  ) =>
    setGroups(
      groups
        .map((group, g) =>
          g !== groupIndex
            ? group
            : {
                conditions: next
                  ? group.conditions.map((current, i) =>
                      i === index ? next : current,
                    )
                  : group.conditions.filter((_, i) => i !== index),
              },
        )
        .filter((group) => group.conditions.length > 0),
    );
  const save = () =>
    update.mutate(
      {
        id: rule.id,
        version: rule.version,
        name: name.trim() || rule.name,
        conditions: groups,
        actions,
      },
      { onSuccess: onDone },
    );
  return (
    <div className="flex flex-col gap-3">
      <TextInput
        aria-label={t("spaces.triage.ruleName")}
        value={name}
        onChange={(event) => setName(event.target.value)}
      />
      <div className="flex gap-2">
        <span className="w-8 shrink-0 pt-1.5 text-fg-muted">
          {t("spaces.triage.when")}
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-2">
          {groups.length === 0 && (
            <span className="text-fg-muted">
              {t("spaces.triage.allRequests")}
            </span>
          )}
          {groups.map((group, groupIndex) => (
            <div key={groupIndex} className="flex flex-col gap-1.5">
              {groupIndex > 0 && (
                <span className="text-fg-muted">{t("spaces.triage.or")}</span>
              )}
              <div className="flex flex-col gap-1.5 rounded-lg border border-line-soft p-2">
                {group.conditions.map((condition, index) => (
                  <ConditionRow
                    key={index}
                    condition={condition}
                    sources={sources}
                    onChange={(next) => setCondition(groupIndex, index, next)}
                  />
                ))}
                <Button
                  size="28"
                  variant="ghost-muted"
                  className="self-start"
                  leading={<Icon name="plus" />}
                  onClick={() =>
                    setGroups(
                      groups.map((item, g) =>
                        g === groupIndex
                          ? { conditions: [...item.conditions, blank] }
                          : item,
                      ),
                    )
                  }
                >
                  {t("spaces.triage.addAnd")}
                </Button>
              </div>
            </div>
          ))}
          <Button
            size="28"
            variant="ghost-muted"
            className="self-start"
            leading={<Icon name="plus" />}
            onClick={() => setGroups([...groups, { conditions: [blank] }])}
          >
            {t("spaces.triage.addOr")}
          </Button>
        </div>
      </div>
      <div className="flex gap-2">
        <span className="w-8 shrink-0 pt-1.5 text-fg-muted">
          {t("spaces.triage.then")}
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          {actions.map((action, index) => (
            <ActionRow
              key={index}
              action={action}
              sources={sources}
              onChange={(next) =>
                setActions(
                  next
                    ? actions.map((current, i) =>
                        i === index ? next : current,
                      )
                    : actions.filter((_, i) => i !== index),
                )
              }
            />
          ))}
          <Button
            size="28"
            variant="ghost-muted"
            className="self-start"
            leading={<Icon name="plus" />}
            onClick={() =>
              setActions([
                ...actions,
                { type: "set_priority", value: "NORMAL" },
              ])
            }
          >
            {t("spaces.triage.addAction")}
          </Button>
        </div>
      </div>
      <div className="flex justify-end gap-2">
        <Button size="28" variant="ghost-muted" onClick={onDone}>
          {t("common.cancel")}
        </Button>
        <Button size="28" onClick={save} loading={update.isPending}>
          {t("spaces.triage.save")}
        </Button>
      </div>
    </div>
  );
}

function ConditionRow({
  condition,
  sources,
  onChange,
}: {
  condition: TriageCondition;
  sources: RuleSources;
  onChange: (condition: TriageCondition | null) => void;
}) {
  const t = useT();
  const { field, op, values } = condition;
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <Select
        aria-label={t("spaces.triage.field")}
        className="!w-32 shrink-0"
        value={field}
        options={FIELDS.map((value) => ({
          value,
          label: t(`spaces.triage.fields.${value}`),
        }))}
        onChange={(next) =>
          onChange({ field: next, op: OPS[next][0]!, values: [] })
        }
      />
      <Select
        aria-label={t("spaces.triage.operator")}
        className="!w-32 shrink-0"
        value={op}
        options={OPS[field].map((value) => ({
          value,
          label: t(`spaces.triage.ops.${value}`),
        }))}
        onChange={(next) =>
          onChange({
            ...condition,
            op: next,
            values: field === "due_date" ? [] : values,
          })
        }
      />
      {field === "content" ? (
        <TextInput
          className="!w-56"
          aria-label={t("spaces.triage.fields.content")}
          value={values[0] ?? ""}
          onChange={(event) =>
            onChange({ ...condition, values: [event.target.value] })
          }
        />
      ) : field === "due_date" ? (
        op !== "none" &&
        (op === "between" ? [0, 1] : [0]).map((slot) => (
          <DateValue
            key={slot}
            value={values[slot] ?? null}
            onChange={(date) => {
              const next = [...values];
              next[slot] = date;
              onChange({ ...condition, values: next });
            }}
          />
        ))
      ) : (
        <ValuePicker
          label={t(`spaces.triage.fields.${field}`)}
          groups={choices(t, field, sources)}
          multiple={field !== "source"}
          values={values}
          onChange={(next) => onChange({ ...condition, values: next })}
        />
      )}
      <IconButton
        label={t("spaces.triage.remove")}
        icon={<Icon name="x" />}
        size="28"
        onClick={() => onChange(null)}
      />
    </div>
  );
}

function ActionRow({
  action,
  sources,
  onChange,
}: {
  action: TriageAction;
  sources: RuleSources;
  onChange: (action: TriageAction | null) => void;
}) {
  const t = useT();
  const multiple = LIST_ACTIONS.includes(action.type);
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <Select
        aria-label={t("spaces.triage.action")}
        className="!w-32 shrink-0"
        value={action.type}
        options={ACTIONS.map((value) => ({
          value,
          label: t(`spaces.triage.actions.${value}`),
        }))}
        onChange={(type) =>
          onChange({ type, value: LIST_ACTIONS.includes(type) ? [] : "" })
        }
      />
      <ValuePicker
        label={t(`spaces.triage.actions.${action.type}`)}
        groups={choices(t, action.type, sources)}
        multiple={multiple}
        values={toList(action.value).filter(Boolean)}
        onChange={(next) =>
          onChange({ ...action, value: multiple ? next : (next[0] ?? "") })
        }
      />
      <IconButton
        label={t("spaces.triage.remove")}
        icon={<Icon name="x" />}
        size="28"
        onClick={() => onChange(null)}
      />
    </div>
  );
}

function ValuePicker({
  label,
  groups,
  multiple,
  values,
  onChange,
}: {
  label: string;
  groups: SearchMenuGroup[];
  multiple: boolean;
  values: string[];
  onChange: (values: string[]) => void;
}) {
  const t = useT();
  return (
    <Picker
      label={label}
      multiple={multiple}
      groups={groups}
      selected={values}
      onSelect={(value) =>
        onChange(
          !multiple
            ? [value]
            : values.includes(value)
              ? values.filter((item) => item !== value)
              : [...values, value],
        )
      }
      trigger={({ ref, toggle, open }) => (
        <Button
          ref={ref}
          size="28"
          variant="outline"
          onClick={toggle}
          aria-expanded={open}
        >
          {values.length
            ? values.map((value) => labelOf(groups, value)).join("、")
            : t("spaces.triage.value")}
        </Button>
      )}
    />
  );
}

function DateValue({
  value,
  onChange,
}: {
  value: string | null;
  onChange: (value: string) => void;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLButtonElement | null>(null);
  return (
    <>
      <Button
        ref={anchor}
        size="28"
        variant="outline"
        onClick={() => setOpen(!open)}
      >
        {value ?? t("spaces.triage.value")}
      </Button>
      <DateFieldPopover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        value={value}
        onChange={(next) => next && onChange(next)}
      />
    </>
  );
}
