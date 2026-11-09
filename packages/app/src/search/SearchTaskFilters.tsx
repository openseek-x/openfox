import { Fragment, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { CustomFieldKind } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Calendar, Icon, Popover, TextInput, cn } from "@openfox/ui";
import {
  noOperand,
  searchTaskFields,
  taskFilterOperators,
  twoOperands,
  type SearchTaskCondition,
  type SearchTaskField,
  type SearchTaskOperator,
} from "./taskFilterModel";
import {
  useTaskFilterOptions,
  type FilterMenu,
  type ScopeMode,
  type TaskFilterOption,
} from "./taskFilterOptions";

export {
  searchTaskFilterParams,
  type SearchTaskCondition,
  type TaskFilterParams,
} from "./taskFilterModel";
export interface SearchTaskFiltersProps {
  orgId: string;
  spaceIds: string[];
  spaceSelection?: ScopeMode;
  value: SearchTaskCondition[];
  onChange: (next: SearchTaskCondition[]) => void;
}
type Candidates = ReturnType<typeof useTaskFilterOptions>;
const pill =
  "flex h-7 shrink-0 items-center gap-1.5 rounded-full px-2.5 text-caption hover:text-fg-default focus-visible:outline-accent";

export function SearchTaskFilters({
  orgId,
  spaceIds,
  spaceSelection = spaceIds.length ? "subset" : "all",
  value,
  onChange,
}: SearchTaskFiltersProps) {
  const t = useT();
  const [menu, setMenu] = useState<FilterMenu>(null);
  const [dateTab, setDateTab] = useState<"dueDate" | "createdAt">("dueDate");
  const [edges, setEdges] = useState({ left: false, right: false });
  const viewport = useRef<HTMLDivElement>(null);
  const moreButton = useRef<HTMLButtonElement>(null);
  const candidates = useTaskFilterOptions(
    orgId,
    spaceIds,
    spaceSelection,
    menu,
    value,
  );
  const custom = value
    .filter((c) => c.field.startsWith("cf_"))
    .map((c) => c.field);
  function measure() {
    const el = viewport.current;
    if (!el) return;
    const left = el.scrollLeft > 1,
      right = el.scrollLeft + el.clientWidth < el.scrollWidth - 1;
    setEdges((prev) =>
      left === prev.left && right === prev.right ? prev : { left, right },
    );
  }
  useLayoutEffect(() => {
    measure();
    const el = viewport.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    if (el.firstElementChild) observer.observe(el.firstElementChild);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    if (!menu || menu === "more") return;
    const button = Array.from(
      viewport.current?.querySelectorAll<HTMLElement>("[data-search-field]") ??
        [],
    ).find((e) => e.dataset.searchField === menu);
    button?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  }, [menu, value]);
  function setCondition(next: SearchTaskCondition) {
    onChange([...value.filter((c) => c.field !== next.field), next]);
  }
  function arrow(side: "left" | "right") {
    return (
      (edges.left || edges.right) && (
        <button
          type="button"
          disabled={!edges[side]}
          aria-label={t(`searchTaskFilters.scroll_${side}`)}
          onClick={() =>
            viewport.current?.scrollBy({
              left: side === "left" ? -280 : 280,
              behavior: "smooth",
            })
          }
          className="shrink-0 rounded p-1 text-fg-muted hover:bg-surface-hover disabled:invisible"
        >
          <Icon
            name={side === "left" ? "chevron-left" : "chevron-right"}
            size={16}
          />
        </button>
      )
    );
  }
  return (
    <div className="flex min-w-0 flex-1 items-center gap-1">
      {arrow("left")}
      <div
        ref={viewport}
        onScroll={measure}
        className="min-w-0 flex-1 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
      >
        <div className="flex w-max items-center gap-1 pr-1">
          {[...searchTaskFields, ...custom].map((field) => (
            <FilterPill
              key={field}
              field={field}
              menu={menu}
              setMenu={setMenu}
              dateTab={dateTab}
              setDateTab={setDateTab}
              conditions={value}
              setCondition={setCondition}
              remove={() =>
                onChange(
                  value.filter(
                    (c) =>
                      c.field !== field &&
                      !(field === "dueDate" && c.field === "createdAt"),
                  ),
                )
              }
              candidates={candidates}
            />
          ))}
        </div>
      </div>
      {arrow("right")}
      <button
        type="button"
        ref={moreButton}
        aria-expanded={menu === "more"}
        className={cn(pill, "text-fg-muted hover:bg-surface-hover")}
        onClick={() => setMenu(menu === "more" ? null : "more")}
      >
        <Icon name="list-filter" size={14} />
        {t("searchTaskFilters.more_filters")}
      </button>
      <Popover
        open={menu === "more"}
        onOpenChange={(open) => {
          if (!open) setMenu(null);
        }}
        anchorRef={moreButton}
        placement="bottom-end"
        role="menu"
        className="w-60"
      >
        <DataState candidates={candidates}>
          {candidates.customFields.length ? (
            candidates.customFields.map((f) => (
              <button
                type="button"
                key={f.id}
                role="menuitem"
                className="flex w-full rounded-md px-2 py-1.5 text-left text-body-sm hover:bg-surface-hover"
                onClick={() => {
                  const field = `cf_${f.id}` as const;
                  if (!value.some((c) => c.field === field))
                    setCondition({
                      field,
                      op: f.kind === "BOOLEAN" ? "is_true" : "is",
                      values: [],
                    });
                  setMenu(field);
                }}
              >
                {f.name}
              </button>
            ))
          ) : (
            <div className="p-2 text-caption text-fg-muted">
              {t("searchTaskFilters.no_custom_fields")}
            </div>
          )}
        </DataState>
      </Popover>
    </div>
  );
}
function DataState({
  candidates,
  children,
}: {
  candidates: Candidates;
  children: React.ReactNode;
}) {
  const t = useT();
  if (candidates.failed)
    return (
      <div role="alert" className="p-3 text-caption text-fg-muted">
        {t("searchTaskFilters.error")}{" "}
        <button
          type="button"
          onClick={candidates.retry}
          className="ml-1 underline"
        >
          {t("common.retry")}
        </button>
      </div>
    );
  if (candidates.loading)
    return (
      <div role="status" className="p-3 text-caption text-fg-muted">
        {t("searchTaskFilters.loading")}
      </div>
    );
  return children;
}
function FilterPill({
  field,
  menu,
  setMenu,
  dateTab,
  setDateTab,
  conditions,
  setCondition,
  remove,
  candidates,
}: {
  field: SearchTaskField;
  menu: FilterMenu;
  setMenu: (menu: FilterMenu) => void;
  dateTab: "dueDate" | "createdAt";
  setDateTab: (tab: "dueDate" | "createdAt") => void;
  conditions: SearchTaskCondition[];
  setCondition: (c: SearchTaskCondition) => void;
  remove: () => void;
  candidates: Candidates;
}) {
  const t = useT(),
    anchor = useRef<HTMLButtonElement>(null);
  const isDate = field === "dueDate",
    currentField = isDate ? dateTab : field;
  const definition = candidates.customFields.find(
    (f) => "cf_" + f.id === currentField,
  );
  const ops = taskFilterOperators(currentField, definition?.kind);
  const condition = conditions.find((c) => c.field === currentField) ?? {
    field: currentField,
    op: ops[0]!,
    values: [],
  };
  const applied = conditions.filter(
    (c) =>
      (c.values.length || noOperand(c.op)) &&
      (isDate
        ? c.field === "dueDate" || c.field === "createdAt"
        : c.field === field),
  );
  const primary = applied.find((c) => c.field === currentField) ?? applied[0];
  const active = !!primary;
  const label = isDate
    ? t("searchTaskFilters.date")
    : field.startsWith("cf_")
      ? (definition?.name ?? t("settings.customFields.title"))
      : t(
          `searchTaskFilters.fields.${field as (typeof searchTaskFields)[number]}`,
        );
  const options = active ? candidates.options(primary.field) : [];
  const first = primary?.values[0];
  const firstOption = options.find(
    (o) => o.value === first || (first && o.aliases?.includes(first)),
  );
  const unknownDate =
    primary?.field === "createdAt" && first?.includes(":")
      ? first.split(":")[1]?.replace("|", " – ")
      : undefined;
  const summary =
    firstOption?.label ??
    unknownDate ??
    (first?.startsWith("invitation:") ? t("tasks.invitedGroup") : first);
  const count =
    primary?.values.filter(
      (v) => !firstOption?.aliases?.includes(v) && v !== first,
    ).length ?? 0;
  const opened = menu === field,
    removable = active || field.startsWith("cf_");
  return (
    <div
      className={cn(
        "flex h-7 shrink-0 items-center rounded-full",
        active
          ? "bg-surface-active text-fg-default"
          : "bg-surface-muted text-fg-muted",
        "hover:bg-surface-hover",
      )}
    >
      <button
        ref={anchor}
        type="button"
        data-search-field={field}
        aria-label={label}
        aria-expanded={opened}
        aria-pressed={active}
        disabled={field.startsWith("cf_") && !definition}
        onClick={() => setMenu(opened ? null : field)}
        className={cn(pill, "max-w-64", removable && "pr-1")}
      >
        <span className="shrink-0">{label}</span>
        {primary && (
          <>
            {primary.op !== "is" && (
              <span className="shrink-0">
                {t(`searchTaskFilters.operators.${primary.op}`)}
              </span>
            )}
            {isDate && (
              <span className="shrink-0">
                {t(
                  `searchTaskFilters.fields.${primary.field as "dueDate" | "createdAt"}`,
                )}
              </span>
            )}
            <span className="truncate">{summary}</span>
            {(isDate ? applied.length - 1 : count) > 0 && (
              <span>+{isDate ? applied.length - 1 : count}</span>
            )}
          </>
        )}
        {!removable && <Icon name="chevron-down" size={12} />}
      </button>
      {removable && (
        <button
          type="button"
          aria-label={t("searchTaskFilters.remove", { name: label })}
          onClick={() => {
            remove();
            if (opened) setMenu(null);
          }}
          className="mr-1 flex size-5 items-center justify-center rounded-full hover:bg-surface-active"
        >
          <Icon name="x" size={12} />
        </button>
      )}
      {opened && (
        <Popover
          open
          anchorRef={anchor}
          onOpenChange={(open) => {
            if (!open) setMenu(null);
          }}
          aria-label={label}
          className="w-72 p-0"
        >
          {isDate && (
            <div className="flex gap-1 border-b border-line-default p-2">
              {(["dueDate", "createdAt"] as const).map((tab) => (
                <button
                  key={tab}
                  type="button"
                  aria-pressed={dateTab === tab}
                  onClick={() => setDateTab(tab)}
                  className={cn(
                    "rounded px-2 py-1 text-caption",
                    dateTab === tab && "bg-surface-active",
                  )}
                >
                  {t(`searchTaskFilters.fields.${tab}`)}
                </button>
              ))}
            </div>
          )}
          <DataState candidates={candidates}>
            <ValueEditor
              key={currentField}
              condition={condition}
              setCondition={setCondition}
              kind={definition?.kind}
              options={candidates.options(currentField)}
              close={() => setMenu(null)}
              more={candidates.more}
              loadingMore={candidates.loadingMore}
            />
          </DataState>
        </Popover>
      )}
    </div>
  );
}
function ValueEditor({
  condition,
  setCondition,
  kind,
  options,
  close,
  more,
  loadingMore,
}: {
  condition: SearchTaskCondition;
  setCondition: (c: SearchTaskCondition) => void;
  kind?: CustomFieldKind;
  options: TaskFilterOption[];
  close: () => void;
  more?: () => void;
  loadingMore?: boolean;
}) {
  const t = useT(),
    { field, op, values } = condition;
  const [opOpen, setOpOpen] = useState(false),
    [search, setSearch] = useState("");
  const [customDate, setCustomDate] = useState(
    () => field === "createdAt" && !!values[0]?.includes(":"),
  );
  const ops = taskFilterOperators(field, kind);
  const searchable =
    field === "assignee" ||
    field === "creator" ||
    field === "crew" ||
    kind === "MEMBER" ||
    kind === "SELECT" ||
    kind === "MULTI_SELECT";
  const filtered = options.filter(
    (o) =>
      !search ||
      (
        o.label +
        " " +
        (o.keywords ?? "") +
        " " +
        (typeof o.description === "string" ? o.description : "")
      )
        .toLowerCase()
        .includes(search.trim().toLowerCase()),
  );
  const commit = (newValues: string[]) =>
    setCondition({ ...condition, values: newValues });
  const freeText =
    field === "content" ||
    kind === "TEXT" ||
    kind === "URL" ||
    kind === "RICH_TEXT" ||
    kind === "NUMBER" ||
    kind === "DATE";
  const single =
    field === "dueDate" || field === "createdAt" || field === "relation";
  function pick(option: TaskFilterOption) {
    const group = option.aliases ?? [option.value];
    const without = values.filter((v) => !group.includes(v));
    const remove = group.every((v) => values.includes(v));
    if (single) {
      commit(remove && field !== "relation" ? [] : group);
      return;
    }
    commit(remove ? without : [...without, ...group]);
  }
  return (
    <div onKeyDown={(e) => e.stopPropagation()}>
      {ops.length > 1 && (
        <div className="relative border-b border-line-default px-2 py-1">
          <button
            type="button"
            aria-label={t("tasks.filters.operator")}
            aria-expanded={opOpen}
            onClick={() => setOpOpen(!opOpen)}
            className="flex w-full items-center justify-between rounded px-2 py-1 text-caption hover:bg-surface-hover"
          >
            {t(`searchTaskFilters.operators.${op}`)}
            <Icon name="chevron-down" size={12} />
          </button>
          {opOpen && (
            <div
              role="menu"
              className="absolute left-2 top-full z-10 max-h-64 min-w-36 overflow-auto rounded-lg border border-line-default bg-surface-card p-1 shadow-popover"
            >
              {ops.map((next) => (
                <button
                  type="button"
                  role="menuitemradio"
                  aria-checked={op === next}
                  key={next}
                  onClick={() => {
                    setCondition({
                      ...condition,
                      op: next,
                      values:
                        noOperand(next) || twoOperands(next) !== twoOperands(op)
                          ? []
                          : values,
                    });
                    setOpOpen(false);
                  }}
                  className="flex w-full items-center justify-between gap-2 rounded px-2 py-1 text-left text-caption hover:bg-surface-hover"
                >
                  {t(`searchTaskFilters.operators.${next}`)}
                  {op === next && <Icon name="check" size={12} />}
                </button>
              ))}
            </div>
          )}
        </div>
      )}
      {noOperand(op) ? null : freeText ? (
        <OperandInputs
          key={op}
          field={field}
          kind={kind}
          op={op}
          values={values}
          onClear={() => commit([])}
          onCommit={(next) => {
            commit(next);
            close();
          }}
        />
      ) : customDate ? (
        <CreatedCalendar
          initial={values[0]}
          onSelect={(v) => {
            commit([v]);
            close();
          }}
        />
      ) : (
        <>
          {searchable && (
            <div className="border-b border-line-default p-1.5">
              <TextInput
                autoFocus
                aria-label={t("searchTaskFilters.search_values_placeholder")}
                placeholder={t("searchTaskFilters.search_values_placeholder")}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                className="h-8"
              />
            </div>
          )}
          <div
            className="max-h-72 overflow-y-auto p-1"
            role="listbox"
            aria-multiselectable={!single}
          >
            {filtered.map((option, index) => {
              const selected = (option.aliases ?? [option.value]).some((v) =>
                values.includes(v),
              );
              return (
                <Fragment key={option.value}>
                  {option.groupLabel &&
                    filtered[index - 1]?.groupLabel !== option.groupLabel && (
                      <div className="px-2 pt-2 pb-1 text-caption text-fg-muted">
                        {option.groupLabel}
                      </div>
                    )}
                  <div
                    role="option"
                    tabIndex={0}
                    aria-selected={selected}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault();
                        pick(option);
                      }
                    }}
                    onClick={() => {
                      pick(option);
                      close();
                    }}
                    className="flex h-7 cursor-pointer items-center gap-2 rounded-md px-2 text-[13px] hover:bg-surface-hover focus-visible:bg-surface-hover"
                  >
                    <button
                      type="button"
                      aria-label={option.label}
                      role="checkbox"
                      aria-checked={selected}
                      tabIndex={-1}
                      onClick={(e) => {
                        e.stopPropagation();
                        pick(option);
                      }}
                      className={cn(
                        "flex size-4 shrink-0 items-center justify-center rounded-[5px] border",
                        selected
                          ? "border-accent bg-accent text-white"
                          : "border-line-default",
                      )}
                    >
                      {selected && <Icon name="check" size={12} />}
                    </button>
                    {option.icon}
                    <span className="truncate">{option.label}</span>
                    {option.description && (
                      <span className="ml-auto truncate text-caption text-fg-muted">
                        {option.description}
                      </span>
                    )}
                  </div>
                </Fragment>
              );
            })}
            {!filtered.length && (
              <p className="p-3 text-caption text-fg-muted">
                {t("searchTaskFilters.no_results")}
              </p>
            )}
            {field === "createdAt" && (
              <button
                type="button"
                onClick={() => setCustomDate(true)}
                className="flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-[13px] hover:bg-surface-hover"
              >
                <Icon name="calendar" size={14} />
                {t("searchTaskFilters.created_date_values.custom")}
                <Icon name="chevron-right" size={12} className="ml-auto" />
              </button>
            )}
            {more && (
              <button
                type="button"
                disabled={loadingMore}
                onClick={more}
                className="w-full rounded py-1.5 text-caption hover:bg-surface-hover"
              >
                {t("commandPalette.more")}
              </button>
            )}
          </div>
        </>
      )}
    </div>
  );
}
function OperandInputs({
  field,
  kind,
  op,
  values,
  onCommit,
  onClear,
}: {
  field: SearchTaskField;
  kind?: CustomFieldKind;
  op: SearchTaskOperator;
  values: string[];
  onCommit: (values: string[]) => void;
  onClear: () => void;
}) {
  const t = useT();
  const [first, setFirst] = useState(values[0] ?? ""),
    [last, setLast] = useState(values[1] ?? "");
  const pair = twoOperands(op),
    content = field === "content";
  const incomplete = pair && (!first.trim() || !last.trim());
  function apply(e: React.FormEvent) {
    e.preventDefault();
    if (incomplete || (content && !first.trim())) return;
    onCommit(
      pair ? [first.trim(), last.trim()] : first.trim() ? [first.trim()] : [],
    );
  }
  function clear() {
    setFirst("");
    setLast("");
    onClear();
  }
  function pickDate(day: string) {
    if (!pair) {
      onCommit([day]);
      return;
    }
    if (!first || last) {
      setFirst(day);
      setLast("");
      return;
    }
    const sorted = [first, day].sort();
    setFirst(sorted[0]!);
    setLast(sorted[1]!);
  }
  const label = content
    ? t("searchTaskFilters.content_input_aria")
    : t("searchTaskFilters.custom_text_placeholder");
  return (
    <form className="grid gap-2 p-2" onSubmit={apply}>
      {kind === "DATE" ? (
        <Calendar
          value={last || first}
          range={first && last ? [first, last] : undefined}
          onChange={pickDate}
        />
      ) : (
        <>
          {pair && (
            <span className="text-caption text-fg-muted">
              {t("searchTaskFilters.range")}
            </span>
          )}
          <div className={pair ? "grid grid-cols-2 gap-2" : undefined}>
            <TextInput
              autoFocus
              type={kind === "NUMBER" ? "number" : "text"}
              step={kind === "NUMBER" ? "any" : undefined}
              aria-label={pair ? t("searchTaskFilters.range_min") : label}
              placeholder={
                pair
                  ? t("searchTaskFilters.range_min")
                  : content
                    ? t("searchTaskFilters.content_placeholder")
                    : label
              }
              maxLength={100}
              value={first}
              onChange={(e) => setFirst(e.target.value)}
              className="h-8"
            />
            {pair && (
              <TextInput
                type="number"
                step="any"
                aria-label={t("searchTaskFilters.range_max")}
                placeholder={t("searchTaskFilters.range_max")}
                value={last}
                onChange={(e) => setLast(e.target.value)}
                className="h-8"
              />
            )}
          </div>
          {!content && !pair && (
            <p className="text-[11px] text-fg-muted">
              {t("searchTaskFilters.custom_text_hint")}
            </p>
          )}
        </>
      )}
      {!content && (kind !== "DATE" || pair) && (
        <div className="flex justify-end gap-2">
          <Button type="button" size="28" variant="ghost-muted" onClick={clear}>
            {t("searchTaskFilters.clear")}
          </Button>
          <Button type="submit" size="28" disabled={incomplete}>
            {t("searchTaskFilters.apply")}
          </Button>
        </div>
      )}
    </form>
  );
}
function CreatedCalendar({
  initial,
  onSelect,
}: {
  initial?: string;
  onSelect: (value: string) => void;
}) {
  const t = useT();
  const [kind, setKind] = useState<"after" | "before" | "between">(
    initial?.startsWith("before:")
      ? "before"
      : initial?.startsWith("between:")
        ? "between"
        : "after",
  );
  const [start, setStart] = useState("");
  return (
    <div>
      <div className="flex gap-1 border-b border-line-default p-1">
        {(["after", "before", "between"] as const).map((mode) => (
          <button
            type="button"
            key={mode}
            aria-pressed={mode === kind}
            onClick={() => {
              setKind(mode);
              setStart("");
            }}
            className={cn(
              "rounded px-2 py-1 text-caption",
              kind === mode && "bg-surface-active",
            )}
          >
            {t(`searchTaskFilters.created_date_values.${mode}`)}
          </button>
        ))}
      </div>
      <Calendar
        value={start || initial?.split(":")[1]?.split("|")[0]}
        onChange={(date) => {
          if (kind !== "between") onSelect(kind + ":" + date);
          else if (!start) setStart(date);
          else onSelect("between:" + [start, date].sort().join("|"));
        }}
      />
    </div>
  );
}
