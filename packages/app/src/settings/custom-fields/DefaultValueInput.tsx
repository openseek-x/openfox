import { useRef, useState } from "react";

import type { CustomFieldKind, CustomFieldMemberScope } from "@openfox/api";
import { useI18n } from "@openfox/i18n";
import {
  Avatar,
  DateFieldPopover,
  Icon,
  Select,
  TextInput,
  Textarea,
  cn,
  formatDateLabel,
} from "@openfox/ui";

import { useAgentDirectory, useOrgPeople } from "../../tasks/queries";
import {
  RICH_TEXT_MAX,
  TEXT_MAX,
  URL_MAX,
  type DefaultDraft,
  type OptionRow,
} from "./kinds";

const NONE = "__none__";

/** 默认值输入：按字段类型使用不同控件。 */
export function DefaultValueInput({
  orgId,
  kind,
  draft,
  onChange,
  options,
  memberScope,
  invalid,
}: {
  orgId: string;
  kind: CustomFieldKind;
  draft: DefaultDraft;
  onChange: (draft: DefaultDraft) => void;
  options: OptionRow[];
  memberScope: CustomFieldMemberScope;
  invalid?: boolean;
}) {
  const { t } = useI18n();
  const setScalar = (scalar: string) => onChange({ ...draft, scalar });
  const placeholder = t("settings.customFields.dialog.defaultEmpty");

  switch (kind) {
    case "NUMBER":
      return (
        <TextInput
          inputMode="decimal"
          value={draft.scalar}
          invalid={invalid}
          placeholder={placeholder}
          onChange={(event) => setScalar(event.target.value)}
        />
      );
    case "URL":
      return (
        <TextInput
          type="url"
          value={draft.scalar}
          maxLength={URL_MAX}
          invalid={invalid}
          placeholder="https://"
          onChange={(event) => setScalar(event.target.value)}
        />
      );
    case "RICH_TEXT":
      return (
        <Textarea
          value={draft.scalar}
          maxLength={RICH_TEXT_MAX}
          rows={3}
          placeholder={placeholder}
          onChange={(event) => setScalar(event.target.value)}
        />
      );
    case "BOOLEAN":
      return (
        <Select
          value={draft.scalar || NONE}
          onChange={(value) => setScalar(value === NONE ? "" : value)}
          options={[
            { value: NONE, label: placeholder },
            { value: "true", label: t("settings.customFields.boolean.true") },
            { value: "false", label: t("settings.customFields.boolean.false") },
          ]}
        />
      );
    case "DATE":
      return <DateDefault value={draft.scalar} onChange={setScalar} />;
    case "MEMBER":
      return (
        <MemberDefault
          orgId={orgId}
          scope={memberScope}
          value={draft.scalar}
          onChange={setScalar}
        />
      );
    case "SELECT": {
      const named = options.filter((row) => row.label.trim());
      return (
        <Select
          value={draft.keys[0] ?? NONE}
          onChange={(value) =>
            onChange({ ...draft, keys: value === NONE ? [] : [value] })
          }
          options={[
            { value: NONE, label: placeholder },
            ...named.map((row) => ({
              value: row.key,
              label: row.label.trim(),
              icon: <OptionDot color={row.color} />,
            })),
          ]}
        />
      );
    }
    case "MULTI_SELECT": {
      const named = options.filter((row) => row.label.trim());
      if (named.length === 0)
        return (
          <p className="text-body-sm text-fg-muted">
            {t("settings.customFields.dialog.defaultNeedsOptions")}
          </p>
        );
      return (
        <div
          role="group"
          aria-label={t("settings.customFields.dialog.default")}
          className="flex flex-wrap gap-1.5"
        >
          {named.map((row) => {
            const checked = draft.keys.includes(row.key);
            return (
              <button
                key={row.key}
                type="button"
                aria-pressed={checked}
                onClick={() =>
                  onChange({
                    ...draft,
                    keys: checked
                      ? draft.keys.filter((key) => key !== row.key)
                      : [...draft.keys, row.key],
                  })
                }
                className={cn(
                  "flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-body-sm",
                  checked
                    ? "border-line-focus bg-surface-selected"
                    : "border-line-default hover:bg-surface-hover",
                )}
              >
                <OptionDot color={row.color} />
                {row.label.trim()}
                {checked && <Icon name="check" size={12} />}
              </button>
            );
          })}
        </div>
      );
    }
    default:
      return (
        <TextInput
          value={draft.scalar}
          maxLength={TEXT_MAX}
          placeholder={placeholder}
          onChange={(event) => setScalar(event.target.value)}
        />
      );
  }
}

export function OptionDot({ color }: { color: string }) {
  return (
    <span
      aria-hidden
      className="size-2.5 shrink-0 rounded-full"
      style={{ backgroundColor: color }}
    />
  );
}

function DateDefault({
  value,
  onChange,
}: {
  value: string;
  onChange: (value: string) => void;
}) {
  const { t, locale } = useI18n();
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLButtonElement | null>(null);
  return (
    <>
      <button
        ref={anchor}
        type="button"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="flex h-8 w-full items-center gap-2 rounded-md border border-line-default bg-surface-card px-2.5 text-left text-body hover:bg-surface-hover"
      >
        <Icon name="calendar" className="text-fg-muted" />
        <span className={cn("flex-1", !value && "text-fg-subtle")}>
          {value
            ? formatDateLabel(value, locale)
            : t("settings.customFields.dialog.defaultEmpty")}
        </span>
      </button>
      <DateFieldPopover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        value={value || null}
        onChange={(next) => onChange(next ?? "")}
      />
    </>
  );
}

function MemberDefault({
  orgId,
  scope,
  value,
  onChange,
}: {
  orgId: string;
  scope: CustomFieldMemberScope;
  value: string;
  onChange: (value: string) => void;
}) {
  const { t } = useI18n();
  const people = useOrgPeople(orgId);
  const directory = useAgentDirectory(orgId, scope !== "HUMANS");
  const options = [
    { value: NONE, label: t("settings.customFields.dialog.defaultEmpty") },
    ...(scope === "AGENTS"
      ? []
      : (people.data ?? []).map((person) => ({
          value: `HUMAN:${person.id}`,
          label: person.email,
          icon: <Avatar name={person.email} size={16} />,
        }))),
    ...(scope === "HUMANS"
      ? []
      : (directory.data?.entries ?? [])
          .filter((entry) => entry.type === "agent")
          .map((entry) => ({
            value: `AGENT:${entry.id}`,
            label: entry.name,
            description: entry.space_name,
            icon: <Avatar name={entry.name} kind="agent" size={16} />,
          }))),
  ];
  return (
    <Select
      value={options.some((option) => option.value === value) ? value : NONE}
      onChange={(next) => onChange(next === NONE ? "" : next)}
      options={options}
    />
  );
}
