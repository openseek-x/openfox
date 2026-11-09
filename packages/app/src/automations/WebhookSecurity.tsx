import { useState } from "react";

import { useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Icon,
  IconButton,
  Switch,
  Tabs,
  Textarea,
  cn,
  useToast,
} from "@openfox/ui";

import { useApi } from "../context";
import { WEBHOOK_EXAMPLES, type WebhookExample } from "./webhookExamples";
import { validateIpAllowlist, type TriggerDraft } from "./model";
import {
  automationErrorMessage,
  useRegenerateWebhookURL,
  useResetWebhookSecret,
  useWebhookSigningSecret,
} from "./queries";

// 编辑已有 Webhook 触发器时的安全设置。
// 只有已保存的触发器（有 id）才会出现；新建时由弹窗提示「保存触发器后可设置」。

const RULE_KEYS = [1, 2, 3, 4, 5] as const;

export function WebhookSecurity({
  orgId,
  spaceId,
  automationId,
  triggerId,
  draft,
  onChange,
  onUrlRegenerated,
}: {
  orgId: string;
  spaceId: string;
  automationId: string;
  /** 已保存触发器的服务端 id */
  triggerId: string;
  draft: TriggerDraft;
  onChange: (next: TriggerDraft) => void;
  /** 重新生成 URL 后交给弹窗展示（只显示一次） */
  onUrlRegenerated: (url: string) => void;
}) {
  const t = useT();
  const toast = useToast();
  const api = useApi();
  const regenerate = useRegenerateWebhookURL(orgId, spaceId);
  const reset = useResetWebhookSecret(orgId, spaceId, automationId, triggerId);
  const [revealed, setRevealed] = useState(false);
  const [confirmRegenerate, setConfirmRegenerate] = useState(false);
  const [confirmReset, setConfirmReset] = useState(false);
  const secret = useWebhookSigningSecret(
    orgId,
    spaceId,
    automationId,
    triggerId,
    revealed,
  );
  const [ipOn, setIpOn] = useState(draft.ipAllowlist.trim() !== "");
  const [howOpen, setHowOpen] = useState(false);
  const [examplesOpen, setExamplesOpen] = useState(false);
  const [tab, setTab] = useState<WebhookExample["id"]>("curl");

  const hasSecret = Boolean(draft.webhook?.has_secret);
  const allowlist = validateIpAllowlist(draft.ipAllowlist);
  const example =
    WEBHOOK_EXAMPLES.find((item) => item.id === tab) ?? WEBHOOK_EXAMPLES[0]!;

  // 开启签名前没有密钥时先生成，否则开关打开后无法验证请求
  const enableSignature = () => {
    if (hasSecret) {
      onChange({ ...draft, signatureRequired: true });
      return;
    }
    reset.mutate(undefined, {
      onSuccess: () => {
        onChange({
          ...draft,
          signatureRequired: true,
          webhook: draft.webhook
            ? { ...draft.webhook, has_secret: true }
            : draft.webhook,
        });
        setRevealed(true);
      },
      onError: (error) =>
        toast(
          automationErrorMessage(t, error, "automations.webhook.secretFailed"),
          {
            tone: "error",
          },
        ),
    });
  };

  const copySecret = async () => {
    try {
      const value = await api.automations.signingSecret(
        orgId,
        spaceId,
        automationId,
        triggerId,
      );
      await navigator.clipboard?.writeText(value.secret);
      toast(t("automations.webhook.secretCopied"));
    } catch (error) {
      toast(
        automationErrorMessage(t, error, "automations.webhook.secretFailed"),
        {
          tone: "error",
        },
      );
    }
  };

  return (
    <div className="flex flex-col gap-4 rounded-[10px] border border-line-default p-4">
      <div className="text-body font-medium">
        {t("automations.webhook.securityTitle")}
      </div>

      {/* Webhook URL：只在生成时返回一次，这里只提供重新生成 */}
      <div className="flex items-center gap-3">
        <div className="min-w-0 flex-1">
          <div className="text-body-sm font-medium text-fg-secondary">
            {t("automations.webhook.urlLabel")}
          </div>
        </div>
        <Button
          size="32"
          variant="secondary"
          leading={<Icon name="refresh-cw" />}
          onClick={() => setConfirmRegenerate(true)}
        >
          {t("automations.webhook.regenerate")}
        </Button>
      </div>

      {/* 签名校验 */}
      <div className="flex flex-col gap-3">
        <div className="flex items-start gap-3">
          <div className="min-w-0 flex-1">
            <div className="text-body-sm font-medium text-fg-secondary">
              {t("automations.webhook.signatureLabel")}
            </div>
            <div className="text-caption text-fg-muted">
              {t("automations.webhook.signatureDesc")}
            </div>
          </div>
          <Switch
            checked={draft.signatureRequired}
            disabled={reset.isPending}
            aria-label={t("automations.webhook.signatureLabel")}
            onCheckedChange={(on) =>
              on
                ? enableSignature()
                : onChange({ ...draft, signatureRequired: false })
            }
          />
        </div>

        {draft.signatureRequired && (
          <div className="flex flex-col gap-2">
            <div className="text-caption text-fg-muted">
              {t("automations.webhook.secretLabel")}
            </div>
            <div className="flex items-center gap-2">
              <code className="min-w-0 flex-1 truncate rounded-md bg-surface-muted px-2.5 py-1.5 font-mono text-[12px]">
                {revealed && secret.data
                  ? secret.data
                  : t("automations.webhook.secretHidden")}
              </code>
              <IconButton
                label={
                  revealed
                    ? t("automations.webhook.hideSecret")
                    : t("automations.webhook.showSecret")
                }
                icon={<Icon name={revealed ? "eye-off" : "eye"} />}
                size="28"
                onClick={() => setRevealed((value) => !value)}
              />
              <IconButton
                label={t("automations.webhook.copySecret")}
                icon={<Icon name="copy" />}
                size="28"
                onClick={() => void copySecret()}
              />
              <IconButton
                label={t("automations.webhook.resetSecret")}
                icon={<Icon name="rotate-ccw" />}
                size="28"
                onClick={() => setConfirmReset(true)}
              />
            </div>
            {secret.isError && (
              <div className="text-caption text-danger">
                {t("automations.webhook.secretFailed")}
              </div>
            )}
          </div>
        )}

        {/* 如何签名：默认折叠 */}
        <div className="flex flex-col gap-2">
          <button
            type="button"
            aria-expanded={howOpen}
            onClick={() => setHowOpen((value) => !value)}
            className="flex items-center gap-1 self-start text-body-sm text-fg-secondary hover:text-fg-default"
          >
            <Icon name={howOpen ? "chevron-down" : "chevron-right"} size={14} />
            {t("automations.webhook.howToSign")}
          </button>
          {howOpen && (
            <div className="flex flex-col gap-3 rounded-[10px] bg-surface-muted/40 p-3 text-body-sm">
              <ol className="flex list-decimal flex-col gap-1.5 pl-5">
                {RULE_KEYS.map((key) => (
                  <li key={key}>
                    {t(
                      `automations.webhook.howToSignRules.${key}` as MessageKey,
                    )}
                  </li>
                ))}
              </ol>
              <button
                type="button"
                aria-expanded={examplesOpen}
                onClick={() => setExamplesOpen((value) => !value)}
                className="flex items-center gap-1 self-start text-fg-secondary hover:text-fg-default"
              >
                <Icon
                  name={examplesOpen ? "chevron-down" : "chevron-right"}
                  size={14}
                />
                {t("automations.webhook.examples")}
              </button>
              {examplesOpen && (
                <div className="flex flex-col gap-2">
                  <p className="text-caption text-fg-muted">
                    {t("automations.webhook.examplesHint")}
                  </p>
                  <Tabs
                    label={t("automations.webhook.examples")}
                    tabs={WEBHOOK_EXAMPLES.map((item) => ({
                      id: item.id,
                      label: item.label,
                    }))}
                    value={tab}
                    onChange={(id) => setTab(id as WebhookExample["id"])}
                  />
                  <div className="relative">
                    <pre className="max-h-72 overflow-auto rounded-md bg-surface-card p-3 font-mono text-[11px] leading-relaxed">
                      {example.code}
                    </pre>
                    <IconButton
                      label={t("automations.webhook.copyCode")}
                      icon={<Icon name="copy" />}
                      size="28"
                      className="absolute top-2 right-2"
                      onClick={() => {
                        void navigator.clipboard?.writeText(example.code);
                        toast(t("automations.webhook.copied"));
                      }}
                    />
                  </div>
                </div>
              )}
            </div>
          )}
        </div>
      </div>

      {/* IP 白名单 */}
      <div className="flex flex-col gap-3">
        <div className="flex items-start gap-3">
          <div className="min-w-0 flex-1">
            <div className="text-body-sm font-medium text-fg-secondary">
              {t("automations.webhook.ipLabel")}
            </div>
            <div className="text-caption text-fg-muted">
              {t("automations.webhook.ipDesc")}
            </div>
          </div>
          <Switch
            checked={ipOn}
            aria-label={t("automations.webhook.ipLabel")}
            onCheckedChange={(on) => {
              setIpOn(on);
              if (!on) onChange({ ...draft, ipAllowlist: "" });
            }}
          />
        </div>
        {ipOn && (
          <div className="flex flex-col gap-1.5">
            <Textarea
              aria-label={t("automations.webhook.ipLabel")}
              value={draft.ipAllowlist}
              invalid={allowlist.invalid !== null || allowlist.tooMany}
              placeholder={t("automations.webhook.ipPlaceholder")}
              rows={4}
              className={cn(
                "font-mono text-[12px] [&_textarea]:max-h-60 [&_textarea]:min-h-24",
              )}
              onChange={(event) =>
                onChange({ ...draft, ipAllowlist: event.target.value })
              }
            />
            {allowlist.invalid ? (
              <div className="text-caption text-danger">
                {t("automations.webhook.ipInvalid", {
                  line: allowlist.invalid.line,
                  value: allowlist.invalid.value,
                })}
              </div>
            ) : allowlist.tooMany ? (
              <div className="text-caption text-danger">
                {t("automations.webhook.ipTooMany")}
              </div>
            ) : (
              <div className="text-caption text-fg-muted">
                {t("automations.webhook.ipHint")}
              </div>
            )}
            <div className="text-caption text-fg-muted">
              {draft.webhook?.last_source_ip
                ? t("automations.webhook.lastSourceIp", {
                    ip: draft.webhook.last_source_ip,
                  })
                : t("automations.webhook.noRequests")}
            </div>
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirmRegenerate}
        onCancel={() => setConfirmRegenerate(false)}
        onConfirm={() =>
          regenerate.mutate(
            { automationId, triggerId },
            {
              onSuccess: ({ url }) => {
                setConfirmRegenerate(false);
                onUrlRegenerated(url);
                toast(t("automations.webhook.regenerated"));
              },
              onError: (error) => {
                setConfirmRegenerate(false);
                toast(
                  automationErrorMessage(
                    t,
                    error,
                    "automations.webhook.regenerateFailed",
                  ),
                  { tone: "error" },
                );
              },
            },
          )
        }
        title={t("automations.webhook.regenerateTitle")}
        description={t("automations.webhook.regenerateDesc")}
        confirmLabel={t("automations.webhook.regenerateConfirm")}
        loading={regenerate.isPending}
      />
      <ConfirmDialog
        open={confirmReset}
        danger
        onCancel={() => setConfirmReset(false)}
        onConfirm={() =>
          reset.mutate(undefined, {
            onSuccess: () => {
              setConfirmReset(false);
              setRevealed(true);
            },
            onError: (error) => {
              setConfirmReset(false);
              toast(
                automationErrorMessage(
                  t,
                  error,
                  "automations.webhook.secretFailed",
                ),
                {
                  tone: "error",
                },
              );
            },
          })
        }
        title={t("automations.webhook.resetSecretTitle")}
        description={t("automations.webhook.resetSecretDesc")}
        confirmLabel={t("automations.webhook.resetSecret")}
        loading={reset.isPending}
      />
    </div>
  );
}
