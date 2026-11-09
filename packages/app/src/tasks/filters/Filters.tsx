import { useQuery } from "@tanstack/react-query";
import { useRef, useState, type RefObject } from "react";

import { useT, type MessageKey, type Translate } from "@openfox/i18n";
import {
  Button,
  Icon,
  IconButton,
  MenuItem,
  Popover,
  SearchMenu,
  Segmented,
  TextInput,
  handleMenuKeyDown,
  type SearchMenuGroup,
  type Placement,
} from "@openfox/ui";

import {
  POSITIVE_ONLY,
  conditionActive,
  type FilterCondition,
  filtersToQuery,
  type FilterQuery,
  type FilterField,
  type CustomFilterField,
} from "../model";
import { parseDuration } from "../../spaces/estimation";

/** 每个可选值字段的候选项（content / date 不需要） */
export type FilterOptions = Partial<Record<FilterField, SearchMenuGroup[]>> & {
  customNames?: Record<string, string>;
  triage?: {
    key: readonly unknown[];
    conditions: FilterCondition[];
    load: (
      field: string,
      query: FilterQuery,
    ) => Promise<{ counts: Record<string, number>; complete: boolean }>;
  };
};

const FIELD_LABEL: Record<
  Exclude<FilterField, CustomFilterField>,
  MessageKey
> = {
  subscriber: "spaces.triage.subscriber",
  relation: "spaces.triage.relation",
  status: "tasks.fields.status",
  priority: "tasks.fields.priority",
  type: "tasks.fields.type",
  assignee: "tasks.fields.assignee",
  crew: "tasks.fields.crew",
  label: "tasks.fields.label",
  content: "tasks.fields.content",
  date: "tasks.fields.date",
  creator: "tasks.fields.creator",
  iteration: "spaces.sections.iterations",
  project: "projects.title",
  estimate: "spaces.task.estimate",
};

const FIELD_ICON = {
  subscriber: "bell",
  relation: "link",
  status: "circle-dot",
  priority: "bar-chart",
  type: "shapes",
  assignee: "user",
  crew: "users",
  label: "tag",
  content: "type",
  date: "calendar",
  creator: "user-plus",
  iteration: "repeat",
  project: "folder",
  estimate: "clock",
} as const;

function custom(field: FilterField): field is CustomFilterField {
  return field.startsWith("cf_");
}
function fieldLabel(t: Translate, field: FilterField, options: FilterOptions) {
  return custom(field)
    ? (options.customNames?.[field] ?? t("settings.customFields.title"))
    : t(FIELD_LABEL[field]);
}
function fieldIcon(field: FilterField) {
  return custom(field) ? "type" : FIELD_ICON[field];
}
function upsert(
  conditions: FilterCondition[],
  next: FilterCondition,
): FilterCondition[] {
  const rest = conditions.filter((c) => c.field !== next.field);
  return conditionActive(next) ? [...rest, next] : rest;
}

/** 工具栏的「筛选」按钮：第一级选字段，第二级选值。 */
export function FilterButton({
  fields,
  conditions,
  onChange,
  options,
}: {
  fields: FilterField[];
  conditions: FilterCondition[];
  onChange: (next: FilterCondition[]) => void;
  options: FilterOptions;
}) {
  const t = useT();
  const anchor = useRef<HTMLButtonElement | null>(null);
  const [stage, setStage] = useState<null | "fields" | FilterField>(null);
  const row = useRef<HTMLElement | null>(null);
  const cascade = Boolean(options.triage);
  const active = conditions.some(conditionActive);
  return (
    <>
      <IconButton
        ref={anchor}
        label={t("tasks.filter")}
        icon={<Icon name="list-filter" />}
        variant="round"
        active={stage !== null || active}
        aria-expanded={stage !== null}
        onClick={() => setStage((s) => (s ? null : "fields"))}
      />
      <Popover
        open={stage === "fields" || (cascade && !!stage)}
        onOpenChange={(open) => {
          if (open || !cascade || stage === "fields")
            setStage(open ? "fields" : null);
        }}
        anchorRef={anchor}
        placement="bottom-end"
        role="menu"
        aria-label={t("tasks.filters.add")}
        className="min-w-[200px] max-h-[min(560px,80vh)] overflow-y-auto"
      >
        <div onKeyDown={handleMenuKeyDown}>
          {fields.map((field) => (
            <div
              key={field}
              onPointerEnter={(e) => {
                if (cascade && e.pointerType !== "touch") {
                  row.current = e.currentTarget;
                  setStage(field);
                }
              }}
              onKeyDown={(e) => {
                if (cascade && e.key === "ArrowRight") {
                  row.current = e.currentTarget;
                  setStage(field);
                  e.preventDefault();
                }
              }}
              onClickCapture={(e) => {
                row.current = e.currentTarget;
              }}
            >
              <MenuItem
                icon={<Icon name={fieldIcon(field)} />}
                trailing={<Icon name="chevron-right" />}
                onSelect={() => setStage(field)}
              >
                {fieldLabel(t, field, options)}
              </MenuItem>
            </div>
          ))}
        </div>
      </Popover>
      {stage && stage !== "fields" && (
        <ConditionEditor
          key={stage}
          field={stage}
          placement={cascade ? "right-start" : "bottom-end"}
          anchorRef={cascade ? row : anchor}
          onClose={() => setStage(null)}
          condition={
            conditions.find((c) => c.field === stage) ?? {
              field: stage,
              op: "is",
              values: [],
            }
          }
          onChange={(next) => onChange(upsert(conditions, next))}
          options={options}
        />
      )}
    </>
  );
}

/** 条件胶囊行：属性 + 运算符 + 值 + 删除，带匹配数与「清除筛选」。 */
export function FilterChips({
  conditions,
  onChange,
  options,
  matchCount,
}: {
  conditions: FilterCondition[];
  onChange: (next: FilterCondition[]) => void;
  options: FilterOptions;
  matchCount?: number;
}) {
  const t = useT();
  const active = conditions.filter(conditionActive);
  if (active.length === 0) return null;
  return (
    <div className="flex flex-wrap items-center gap-2 px-4 pb-2">
      {active.map((condition) => (
        <Chip
          key={condition.field}
          condition={condition}
          options={options}
          onChange={(next) => onChange(upsert(conditions, next))}
          onRemove={() =>
            onChange(conditions.filter((c) => c.field !== condition.field))
          }
        />
      ))}
      {matchCount !== undefined && (
        <span className="text-body-sm text-fg-muted" aria-live="polite">
          {t("tasks.filters.matches", { count: matchCount })}
        </span>
      )}
      <Button variant="ghost-muted" size="28" onClick={() => onChange([])}>
        {t("tasks.filters.clear")}
      </Button>
    </div>
  );
}

function optionLabel(
  options: FilterOptions,
  field: FilterField,
  value: string,
): string {
  for (const group of options[field] ?? [])
    for (const option of group.options)
      if (option.value === value) return option.label;
  return value;
}

function Chip({
  condition,
  options,
  onChange,
  onRemove,
}: {
  condition: FilterCondition;
  options: FilterOptions;
  onChange: (next: FilterCondition) => void;
  onRemove: () => void;
}) {
  const t = useT();
  const anchor = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  const { field, op, values } = condition;
  let summary: string;
  if (field === "content") summary = values[0] ?? "";
  else if (field === "date")
    summary = `${values[0] === "start" ? t("tasks.fields.startDate") : t("tasks.fields.dueDate")} ${values[1] || "…"} – ${values[2] || "…"}`;
  else if (field === "estimate")
    summary =
      values[0] === "none"
        ? t("spaces.task.estimateNone")
        : `${values[0] || "…"} – ${values[1] || "…"}`;
  else if (values.length === 1)
    summary = optionLabel(options, field, values[0]!);
  else summary = t("tasks.filters.valuesCount", { count: values.length });
  const segment =
    "flex h-7 items-center gap-1 px-2 text-body-sm hover:bg-surface-hover";
  return (
    <span
      role="group"
      aria-label={fieldLabel(t, field, options)}
      className="inline-flex items-center divide-x divide-line-default overflow-hidden rounded-lg border border-line-default bg-surface-card"
    >
      <span className={`${segment} text-fg-muted hover:bg-transparent`}>
        <Icon name={fieldIcon(field)} size={14} />
        {fieldLabel(t, field, options)}
      </span>
      <button
        type="button"
        aria-label={t("tasks.filters.operator")}
        disabled={POSITIVE_ONLY.has(field)}
        onClick={() =>
          onChange({ ...condition, op: op === "is" ? "is_not" : "is" })
        }
        className={`${segment} text-fg-muted disabled:cursor-default disabled:hover:bg-transparent`}
      >
        {op === "is" ? t("tasks.filters.is") : t("tasks.filters.isNot")}
      </button>
      <button
        ref={anchor}
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className={`${segment} max-w-[220px] font-medium`}
      >
        <span className="truncate">{summary}</span>
      </button>
      <button
        type="button"
        aria-label={t("tasks.filters.remove")}
        onClick={onRemove}
        className={`${segment} text-fg-muted`}
      >
        <Icon name="x" size={14} />
      </button>
      {open && (
        <ConditionEditor
          field={field}
          anchorRef={anchor}
          condition={condition}
          onChange={onChange}
          onClose={() => setOpen(false)}
          options={options}
        />
      )}
    </span>
  );
}

/** 第二级：多选值（带搜索）、内容关键字或日期范围。 */
function ConditionEditor({
  placement = "bottom-end",
  field,
  anchorRef,
  condition,
  onChange,
  onClose,
  options,
}: {
  field: FilterField;
  placement?: Placement;
  anchorRef: RefObject<HTMLElement | null>;
  condition: FilterCondition;
  onChange: (next: FilterCondition) => void;
  onClose: () => void;
  options: FilterOptions;
}) {
  const t = useT();
  const [text, setText] = useState(condition.values[0] ?? "");
  const [which, setWhich] = useState<"start" | "due">(
    condition.values[0] === "start" ? "start" : "due",
  );
  const [from, setFrom] = useState(condition.values[1] ?? "");
  const [to, setTo] = useState(condition.values[2] ?? "");
  const onOpenChange = (open: boolean) => {
    if (!open) onClose();
  };

  const facets = options.triage;
  const facetQuery = filtersToQuery(
    facets?.conditions.filter((c) => c.field !== field) ?? [],
  );
  const choices = options[field];
  const counts = useQuery({
    queryKey: [...(facets?.key ?? []), "candidates", field, facetQuery],
    queryFn: () => facets!.load(field, facetQuery),
    enabled: !!facets && !!choices && field !== "relation",
  });
  const [expanded, setExpanded] = useState(false);
  const countsByValue = counts.data?.counts;
  const hasMatch = (v: string) =>
    condition.values.includes(v) ||
    !counts.data?.complete ||
    !!countsByValue?.[v];
  const hidden = (choices ?? [])
    .flatMap((g) => g.options)
    .filter((o) => !hasMatch(o.value)).length;
  const groups = (choices ?? []).map((g) => ({
    ...g,
    options: g.options
      .filter((o) => expanded || hasMatch(o.value))
      .map((o) => ({ ...o, count: countsByValue?.[o.value] })),
  }));
  if (field === "content" || (custom(field) && !options[field]))
    return (
      <Popover
        open
        onOpenChange={onOpenChange}
        anchorRef={anchorRef}
        placement={placement}
        aria-label={fieldLabel(t, field, options)}
        className="w-[280px] p-2"
      >
        <form
          onSubmit={(event) => {
            event.preventDefault();
            onChange({ field, op: condition.op, values: [text] });
            onClose();
          }}
        >
          <TextInput
            autoFocus
            aria-label={fieldLabel(t, field, options)}
            placeholder={t(
              custom(field)
                ? "spaces.triage.enterValue"
                : "tasks.filters.contentPlaceholder",
            )}
            value={text}
            maxLength={100}
            onChange={(event) => setText(event.target.value)}
          />
          {custom(field) && (
            <div className="mt-2 flex justify-end gap-2">
              <Button
                size="28"
                variant="ghost-muted"
                onClick={() => {
                  onChange({ ...condition, values: [] });
                  onClose();
                }}
              >
                {t("spaces.triage.clearValue")}
              </Button>
              <Button size="28" type="submit">
                {t("tasks.filters.apply")}
              </Button>
            </div>
          )}
        </form>
      </Popover>
    );

  if (field === "date")
    return (
      <Popover
        open
        onOpenChange={onOpenChange}
        anchorRef={anchorRef}
        placement={placement}
        aria-label={t("tasks.fields.date")}
        className="w-[300px] p-3"
      >
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            onChange({ field, op: condition.op, values: [which, from, to] });
            onClose();
          }}
        >
          <Segmented
            label={t("tasks.fields.date")}
            value={which}
            onChange={setWhich}
            options={[
              { value: "start", label: t("tasks.fields.startDate") },
              { value: "due", label: t("tasks.fields.dueDate") },
            ]}
          />
          <label className="flex items-center gap-2 text-body-sm">
            <span className="w-8 text-fg-muted">{t("tasks.filters.from")}</span>
            <TextInput
              type="date"
              value={from}
              onChange={(event) => setFrom(event.target.value)}
            />
          </label>
          <label className="flex items-center gap-2 text-body-sm">
            <span className="w-8 text-fg-muted">{t("tasks.filters.to")}</span>
            <TextInput
              type="date"
              value={to}
              onChange={(event) => setTo(event.target.value)}
            />
          </label>
          <Button type="submit" size="28" className="self-end">
            {t("tasks.filters.apply")}
          </Button>
        </form>
      </Popover>
    );

  if (field === "estimate")
    return (
      <EstimateFilterEditor
        anchorRef={anchorRef}
        placement={placement}
        condition={condition}
        onChange={onChange}
        onClose={onClose}
      />
    );

  return (
    <SearchMenu
      open
      onOpenChange={onOpenChange}
      anchorRef={anchorRef}
      placement={placement}
      label={fieldLabel(t, field, options)}
      multiple
      groups={groups}
      moreOptions={
        hidden > 0
          ? {
              label: t(
                expanded
                  ? "spaces.triage.hideUnavailable"
                  : "spaces.triage.showUnavailable",
                { count: hidden },
              ),
              onClick: () => setExpanded(!expanded),
            }
          : undefined
      }
      selected={condition.values}
      onSelect={(value) =>
        onChange({
          ...condition,
          values: condition.values.includes(value)
            ? condition.values.filter((v) => v !== value)
            : [...condition.values, value],
        })
      }
    />
  );
}

/**
 * 估时范围：最小值 / 最大值（时间刻度可输入「2d 4h」，按秒提交）或「未估时」。
 */
function EstimateFilterEditor({
  placement,
  anchorRef,
  condition,
  onChange,
  onClose,
}: {
  placement?: Placement;
  anchorRef: RefObject<HTMLElement | null>;
  condition: FilterCondition;
  onChange: (next: FilterCondition) => void;
  onClose: () => void;
}) {
  const t = useT();
  const none = condition.values[0] === "none";
  const [mode, setMode] = useState<"range" | "none">(none ? "none" : "range");
  const [min, setMin] = useState(none ? "" : (condition.values[0] ?? ""));
  const [max, setMax] = useState(condition.values[1] ?? "");
  const toValue = (text: string) => {
    const value = text.trim();
    if (!value) return "";
    const seconds = parseDuration(value);
    if (seconds !== null) return String(seconds);
    const number = Number(value);
    return Number.isFinite(number) && number >= 0 ? String(number) : "";
  };
  return (
    <Popover
      open
      onOpenChange={(open) => !open && onClose()}
      anchorRef={anchorRef}
      placement={placement ?? "bottom-end"}
      aria-label={t("spaces.task.estimate")}
      className="w-[280px] p-3"
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          onChange({
            field: "estimate",
            op: "is",
            values: mode === "none" ? ["none"] : [toValue(min), toValue(max)],
          });
          onClose();
        }}
      >
        <Segmented
          label={t("spaces.task.estimate")}
          value={mode}
          onChange={setMode}
          options={[
            { value: "range", label: t("spaces.task.estimate") },
            { value: "none", label: t("spaces.task.estimateNone") },
          ]}
        />
        {mode === "range" && (
          <>
            <label className="flex items-center gap-2 text-body-sm">
              <span className="w-8 text-fg-muted">
                {t("tasks.filters.from")}
              </span>
              <TextInput
                aria-label={`${t("spaces.task.estimate")} ${t("tasks.filters.from")}`}
                value={min}
                onChange={(event) => setMin(event.target.value)}
              />
            </label>
            <label className="flex items-center gap-2 text-body-sm">
              <span className="w-8 text-fg-muted">{t("tasks.filters.to")}</span>
              <TextInput
                aria-label={`${t("spaces.task.estimate")} ${t("tasks.filters.to")}`}
                value={max}
                onChange={(event) => setMax(event.target.value)}
              />
            </label>
          </>
        )}
        <Button type="submit" size="28" className="self-end">
          {t("tasks.filters.apply")}
        </Button>
      </form>
    </Popover>
  );
}
