import { useRef, useState } from "react";
import { useT } from "@openfox/i18n";
import { Icon, IconButton, Popover, Select, Switch, cn } from "@openfox/ui";
import { readLocal, writeLocal } from "../storage";

const defaults = {
  sort: "entered" as "entered" | "priority" | "due",
  order: "desc" as "asc" | "desc",
  snoozed: false,
  identifier: true,
  dueDate: false,
};
export function useTriageDisplay(user: string, org: string, space: string) {
  const key = ["openfox:triage-display", user, org, space]
    .map(encodeURIComponent)
    .join(":");
  const [value, setValue] = useState(() => {
    try {
      const obj = JSON.parse(readLocal(key) ?? "{}") ?? {};
      return {
        sort:
          obj.sort === "priority" || obj.sort === "due"
            ? (obj.sort as "priority" | "due")
            : defaults.sort,
        order: obj.order === "asc" ? ("asc" as const) : defaults.order,
        snoozed: obj.snoozed === true,
        identifier: obj.identifier !== false,
        dueDate: obj.dueDate === true,
      };
    } catch {
      return defaults;
    }
  });
  const update = (patch: Partial<typeof defaults>) => {
    const next = { ...value, ...patch };
    setValue(next);
    writeLocal(key, JSON.stringify(next));
  };
  return { value, update };
}
export function TriageDisplay({
  value,
  update,
}: ReturnType<typeof useTriageDisplay>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLButtonElement>(null);
  const options = [
    { value: "entered", label: t("spaces.triage.sortEntered") },
    { value: "priority", label: t("tasks.fields.priority") },
    { value: "due", label: t("tasks.fields.dueDate") },
  ] as const;
  return (
    <>
      <IconButton
        ref={anchor}
        label={t("spaces.triage.viewOptions")}
        icon={<Icon name="sliders-horizontal" />}
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      />
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        role="dialog"
        aria-label={t("spaces.triage.viewOptions")}
        placement="bottom-end"
        className="w-80 p-0"
      >
        <div className="flex min-h-14 items-center gap-2 border-b border-line-soft px-4 py-3">
          <span className="mr-auto text-body text-fg-muted">
            {t("spaces.triage.sort")}
          </span>
          <IconButton
            label={t(
              value.order === "asc"
                ? "spaces.triage.ascending"
                : "spaces.triage.descending",
            )}
            icon={
              <Icon
                name="arrow-up"
                className={value.order === "asc" ? undefined : "rotate-180"}
              />
            }
            onClick={() =>
              update({ order: value.order === "asc" ? "desc" : "asc" })
            }
          />
          <Select
            value={value.sort}
            aria-label={t("spaces.triage.sort")}
            options={options}
            className="w-40"
            onChange={(sort) => update({ sort })}
          />
        </div>
        <div className="flex min-h-14 items-center justify-between gap-2 border-b border-line-soft px-4 py-3 text-body">
          <label htmlFor="triage-snoozed">
            {t("spaces.triage.showSnoozed")}
          </label>
          <Switch
            id="triage-snoozed"
            label={t("spaces.triage.showSnoozed")}
            checked={value.snoozed}
            onCheckedChange={(snoozed) => update({ snoozed })}
          />
        </div>
        <div className="px-4 py-3">
          <span className="text-body text-fg-muted">
            {t("spaces.triage.fieldsLabel")}
          </span>
          <div className="mt-3 flex gap-1.5">
            {(["identifier", "dueDate"] as const).map((field) => (
              <button
                key={field}
                type="button"
                aria-pressed={value[field]}
                className={cn(
                  "h-7 rounded-full border border-line-default px-3 text-body-sm hover:bg-surface-hover",
                  value[field]
                    ? "bg-surface-active text-fg-default"
                    : "text-fg-muted",
                )}
                onClick={() => update({ [field]: !value[field] })}
              >
                {t(
                  field === "identifier"
                    ? "spaces.triage.identifier"
                    : "tasks.fields.dueDate",
                )}
              </button>
            ))}
          </div>
        </div>
      </Popover>
    </>
  );
}
