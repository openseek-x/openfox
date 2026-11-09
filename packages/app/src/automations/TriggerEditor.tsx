import type { AutomationTriggerKind } from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import { Field, Icon, IconButton, Select, TextInput } from "@openfox/ui";

import { ScheduleEditor } from "./ScheduleEditor";
import { StatusEditor, type TriggerOptions } from "./StatusEditor";
import { WebhookSecurity } from "./WebhookSecurity";
import {
  MAX_COMMENT_LENGTH,
  newTriggerDraft,
  type TriggerDraft,
} from "./model";
import { TRIGGER_KIND_ORDER, triggerIcon } from "./parts";

// 单个触发器卡片：先选触发方式，再显示对应配置区。

export function TriggerEditor({
  draft,
  index,
  timezone,
  orgId,
  spaceId,
  automationId,
  options,
  onChange,
  onRemove,
  onUrlRegenerated,
}: {
  draft: TriggerDraft;
  index: number;
  timezone: string;
  orgId: string;
  spaceId: string;
  /** 已保存的自动化才有 id，安全设置区依赖它 */
  automationId?: string;
  options: TriggerOptions;
  onChange: (next: TriggerDraft) => void;
  onRemove: () => void;
  onUrlRegenerated: (triggerId: string, url: string) => void;
}) {
  const t = useT();
  const savedId = draft.id;
  const kindLabel = (kind: AutomationTriggerKind) =>
    t(`automations.trigger.kind.${kind}` as MessageKey);

  // 换触发方式时重新生成草稿：原来的服务端 id 不再适用，作为新触发器提交
  const changeKind = (kind: AutomationTriggerKind) => {
    if (kind === draft.kind) return;
    onChange(newTriggerDraft(kind, draft.key, timezone));
  };

  return (
    <section
      aria-label={`${t("automations.form.triggersLabel")} ${index + 1}`}
      className="flex flex-col gap-3 rounded-xl border border-line-default p-4"
    >
      {/* 头部：序号与删除；下方的触发方式 Select 与卡片内容等宽，弹层宽度跟随它 */}
      <div className="flex items-center gap-2">
        <span className="text-body-sm font-medium text-fg-secondary">
          {t("automations.form.triggersLabel")} {index + 1}
        </span>
        <IconButton
          className="ml-auto"
          label={t("automations.form.removeTrigger")}
          icon={<Icon name="trash-2" />}
          size="28"
          onClick={onRemove}
        />
      </div>
      <Select<AutomationTriggerKind>
        value={draft.kind}
        aria-label={t("automations.form.triggerKindLabel")}
        onChange={changeKind}
        options={TRIGGER_KIND_ORDER.map((kind) => ({
          value: kind,
          label: kindLabel(kind),
          icon: <Icon name={triggerIcon(kind)} size={14} />,
        }))}
      />

      {draft.kind === "schedule" && (
        <ScheduleEditor
          value={draft.schedule}
          onChange={(schedule) => onChange({ ...draft, schedule })}
        />
      )}

      {draft.kind === "task_status" && (
        <StatusEditor value={draft} options={options} onChange={onChange} />
      )}

      {draft.kind === "comment" && (
        <Field label={t("automations.comment.label")}>
          <TextInput
            value={draft.contains}
            maxLength={MAX_COMMENT_LENGTH}
            placeholder={t("automations.comment.placeholder")}
            onChange={(event) =>
              onChange({ ...draft, contains: event.target.value })
            }
          />
        </Field>
      )}

      {draft.kind === "webhook" && (
        <div className="flex flex-col gap-2 text-body-sm text-fg-muted">
          {!savedId || !automationId ? (
            <>
              <p>{t("automations.webhook.createHint")}</p>
              <p>{t("automations.webhook.saveFirst")}</p>
            </>
          ) : (
            <WebhookSecurity
              orgId={orgId}
              spaceId={spaceId}
              automationId={automationId}
              triggerId={savedId}
              draft={draft}
              onChange={onChange}
              onUrlRegenerated={(url) => onUrlRegenerated(savedId, url)}
            />
          )}
        </div>
      )}
    </section>
  );
}
