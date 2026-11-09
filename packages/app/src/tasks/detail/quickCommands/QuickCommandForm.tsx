import { useState } from "react";

import {
  QUICK_COMMAND_NAME_MAX,
  QUICK_COMMAND_PROMPT_MAX,
  type QuickCommand,
  type QuickCommandVisibility,
} from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import {
  Avatar,
  Button,
  Field,
  Radio,
  Select,
  TextInput,
  Textarea,
  type SelectOption,
} from "@openfox/ui";

import { AgentAvatar } from "../../../agents/AgentAvatar";
import { ViewIconPicker, type ViewIconValue } from "../../../views/ViewIcon";
import { useQuickCommandTargets } from "./queries";

// 新建 / 编辑快捷指令表单。目标值编码为 "agent:<id>" / "crew:<id>"。

export interface QuickCommandDraft {
  name: string;
  icon: ViewIconValue;
  target: string | null;
  prompt: string;
  visibility: QuickCommandVisibility;
}

export const emptyDraft = (): QuickCommandDraft => ({
  name: "",
  icon: { kind: null, icon: null, url: null },
  target: null,
  prompt: "",
  visibility: "PRIVATE",
});

export const draftOf = (item: QuickCommand): QuickCommandDraft => ({
  name: item.name,
  icon: { kind: item.icon_kind, icon: item.icon, url: item.icon_url },
  target: `${item.target.kind === "CREW" ? "crew" : "agent"}:${item.target.id}`,
  prompt: item.prompt,
  visibility: item.visibility,
});

/** 目标选项：Agent 在前、小队在后；编辑时原目标已不可选，作为禁用项保留，避免显示为空 */
function useTargetOptions(
  orgId: string,
  spaceId: string,
  editing: QuickCommand | null,
): SelectOption<string>[] {
  const t = useT();
  const targets = useQuickCommandTargets(orgId, spaceId);
  const options: SelectOption<string>[] = [
    ...targets.agents.map((agent) => ({
      value: `agent:${agent.id}`,
      label: agent.name,
      icon: <AgentAvatar name={agent.name} avatar={agent.avatar} size={20} />,
      description: t("tasks.quickCommands.form.agentsGroup"),
    })),
    ...targets.crews.map((crew) => ({
      value: `crew:${crew.id}`,
      label: crew.name,
      icon: <Avatar name={crew.name} kind="agent" size={20} />,
      description: t("tasks.quickCommands.form.crewsGroup"),
    })),
  ];
  const current = editing && draftOf(editing).target;
  if (editing && current && !options.some((o) => o.value === current)) {
    const reason = unavailableText(t, editing);
    options.push({
      value: current,
      label: editing.target.name || reason,
      description: editing.target.name ? reason : undefined,
      disabled: true,
    });
  }
  return options;
}

/** 新建 / 编辑表单；必填（功能名、目标）未填时「保存」禁用。可见性默认私有。 */
export function QuickCommandForm({
  orgId,
  spaceId,
  editing,
  busy,
  onBack,
  onSubmit,
}: {
  orgId: string;
  spaceId: string;
  editing: QuickCommand | null;
  busy: boolean;
  onBack: () => void;
  onSubmit: (draft: QuickCommandDraft) => void;
}) {
  const t = useT();
  const [draft, setDraft] = useState<QuickCommandDraft>(() =>
    editing ? draftOf(editing) : emptyDraft(),
  );
  const options = useTargetOptions(orgId, spaceId, editing);
  const set = (patch: Partial<QuickCommandDraft>) =>
    setDraft((current) => ({ ...current, ...patch }));
  const name = draft.name.trim();
  const valid = name.length > 0 && draft.target !== null;
  const nameLength = Array.from(draft.name).length;
  return (
    <form
      className="flex flex-col gap-4"
      onSubmit={(event) => {
        event.preventDefault();
        if (valid && !busy) onSubmit(draft);
      }}
    >
      <p className="text-body text-fg-muted">
        {t("tasks.quickCommands.form.description")}
      </p>
      <div className="flex flex-col gap-2">
        <div className="flex items-center justify-between">
          <span className="text-body font-medium">
            {t("tasks.quickCommands.form.name")}
          </span>
          <span className="text-caption text-fg-muted" aria-hidden>
            {nameLength}/{QUICK_COMMAND_NAME_MAX}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <ViewIconPicker
            orgId={orgId}
            value={draft.icon}
            onChange={(icon) => set({ icon })}
          />
          <TextInput
            data-autofocus
            aria-label={t("tasks.quickCommands.form.name")}
            value={draft.name}
            maxLength={QUICK_COMMAND_NAME_MAX}
            placeholder={t("tasks.quickCommands.form.namePlaceholder")}
            onChange={(event) => set({ name: event.target.value })}
            className="flex-1"
          />
        </div>
      </div>
      <QuickCommandFormRest
        draft={draft}
        options={options}
        set={set}
        busy={busy}
        valid={valid}
        onBack={onBack}
      />
    </form>
  );
}

function QuickCommandFormRest({
  draft,
  options,
  set,
  busy,
  valid,
  onBack,
}: {
  draft: QuickCommandDraft;
  options: SelectOption<string>[];
  set: (patch: Partial<QuickCommandDraft>) => void;
  busy: boolean;
  valid: boolean;
  onBack: () => void;
}) {
  const t = useT();
  const visibility = (
    value: QuickCommandVisibility,
    title: string,
    hint: string,
  ) => (
    <label className="flex cursor-pointer items-center gap-2 text-body">
      <Radio
        name="quick-command-visibility"
        value={value}
        checked={draft.visibility === value}
        onChange={() => set({ visibility: value })}
      />
      <span>{title}</span>
      <span className="text-fg-muted">{hint}</span>
    </label>
  );
  return (
    <>
      <Field
        label={t("tasks.quickCommands.form.target")}
        helper={t("tasks.quickCommands.form.targetHint")}
      >
        <Select
          aria-label={t("tasks.quickCommands.form.target")}
          value={draft.target}
          options={options}
          placeholder={
            options.length === 0
              ? t("tasks.quickCommands.form.targetEmpty")
              : t("tasks.quickCommands.form.targetPlaceholder")
          }
          onChange={(target) => set({ target })}
        />
      </Field>
      <Field label={t("tasks.quickCommands.form.prompt")}>
        <Textarea
          rows={4}
          value={draft.prompt}
          maxLength={QUICK_COMMAND_PROMPT_MAX}
          placeholder={t("tasks.quickCommands.form.promptPlaceholder")}
          onChange={(event) => set({ prompt: event.target.value })}
        />
      </Field>
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-2 text-body font-medium">
          {t("tasks.quickCommands.form.visibility")}
        </legend>
        {visibility(
          "PRIVATE",
          t("tasks.quickCommands.form.private"),
          t("tasks.quickCommands.form.privateHint"),
        )}
        {visibility(
          "SHARED",
          t("tasks.quickCommands.form.shared"),
          t("tasks.quickCommands.form.sharedHint"),
        )}
      </fieldset>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" size="28" onClick={onBack}>
          {t("tasks.quickCommands.form.cancel")}
        </Button>
        <Button type="submit" size="28" disabled={!valid || busy}>
          {t("tasks.quickCommands.form.save")}
        </Button>
      </div>
    </>
  );
}

/** 目标不可用的原因文案 */
export function unavailableText(t: Translate, item: QuickCommand): string {
  return t(
    `tasks.quickCommands.unavailable.${item.target.unavailable_reason ?? "NOT_VISIBLE"}`,
  );
}
