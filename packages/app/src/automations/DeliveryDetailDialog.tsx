import type { WebhookDeliveryDetail } from "@openfox/api";
import { useI18n, useT, type MessageKey, type Translate } from "@openfox/i18n";
import { Button, Dialog, Icon, Spinner, cn, useToast } from "@openfox/ui";

import {
  DeliveryStatusIcon,
  deliveryStatusLabel,
  formatDateTime,
} from "./parts";
import {
  automationErrorMessage,
  useReplayDelivery,
  useWebhookDelivery,
} from "./queries";

// Webhook 投递详情：max-w-2xl，元数据、签名状态、重放、原始内容。

/** 原始 body / Headers / 响应 body 的预览上限（字符），超出时显示截断提示。 */
const PREVIEW_CHARS = 8000;

export function DeliveryDetailDialog({
  orgId,
  spaceId,
  automationId,
  deliveryId,
  onClose,
}: {
  orgId: string;
  spaceId: string;
  automationId: string;
  deliveryId: string | null;
  onClose: () => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const toast = useToast();
  const detail = useWebhookDelivery(orgId, spaceId, automationId, deliveryId);
  const replay = useReplayDelivery(orgId, spaceId, automationId);
  const delivery = detail.data;

  const replayHandler = () => {
    if (!delivery) return;
    replay.mutate(delivery.id, {
      onSuccess: () => toast(t("automations.delivery.replayed")),
      onError: (error) =>
        toast(
          automationErrorMessage(t, error, "automations.delivery.replayFailed"),
          {
            tone: "error",
          },
        ),
    });
  };

  return (
    <Dialog
      open={deliveryId !== null}
      onClose={onClose}
      width={672}
      title={t("automations.delivery.title")}
      footer={
        <Button variant="secondary" onClick={onClose}>
          {t("automations.cancel")}
        </Button>
      }
    >
      {detail.isLoading ? (
        <div className="flex items-center gap-2 text-body text-fg-muted">
          <Spinner />
        </div>
      ) : detail.isError || !delivery ? (
        <p className="text-body text-fg-muted">
          {t("automations.delivery.loadFailed")}
        </p>
      ) : (
        <div className="flex max-h-[85vh] flex-col gap-5 overflow-y-auto">
          <div className="flex flex-wrap items-center gap-3 text-body-sm">
            <DeliveryStatusIcon status={delivery.status} />
            <span className="font-medium">
              {deliveryStatusLabel(t, delivery.status)}
            </span>
            <span className="text-fg-muted">{delivery.provider}</span>
            <span className="font-mono text-fg-secondary">
              {delivery.event}
            </span>
          </div>

          <dl className="grid grid-cols-2 gap-x-6 gap-y-3 text-body-sm">
            <Meta
              label={t("automations.delivery.received")}
              value={formatDateTime(locale, delivery.received_at)}
            />
            <Meta
              label={t("automations.delivery.lastAttempt")}
              value={formatDateTime(locale, delivery.last_attempt_at)}
            />
            <Meta
              label={t("automations.delivery.attemptCount")}
              value={String(delivery.attempts)}
            />
            <Meta
              label={t("automations.delivery.response")}
              value={
                delivery.response_status ? String(delivery.response_status) : ""
              }
            />
            <Meta
              label={t("automations.delivery.sourceIp")}
              value={delivery.source_ip}
              mono
            />
            <Meta
              label={t("automations.delivery.dedupeKey")}
              value={delivery.dedupe_key}
              mono
            />
            <Meta
              label={t("automations.delivery.dedupeSource")}
              value={dedupeSourceLabel(t, delivery.dedupe_source)}
            />
            <Meta
              label={t("automations.delivery.contentType")}
              value={delivery.content_type}
              mono
            />
            {delivery.replay_of && (
              <Meta
                label={t("automations.delivery.replaySource")}
                value={delivery.replay_of}
                mono
              />
            )}
          </dl>

          {delivery.error && (
            <div className="rounded-lg border border-danger/40 bg-danger/5 p-3 text-body-sm text-danger">
              <div className="mb-1 font-medium">
                {t("automations.delivery.errorTitle")}
              </div>
              {delivery.error}
            </div>
          )}

          <section className="flex flex-col gap-2">
            <div className="flex flex-wrap items-center gap-2 text-body-sm">
              <span className="text-fg-muted">
                {t("automations.delivery.signature")}
              </span>
              <span className="font-medium">
                {t(
                  `automations.delivery.signatureStatus.${delivery.signature_status}` as MessageKey,
                )}
              </span>
              <Button
                size="28"
                variant="secondary"
                leading={<Icon name="refresh-cw" />}
                disabled={!delivery.replayable || replay.isPending}
                loading={replay.isPending}
                onClick={replayHandler}
              >
                {t("automations.detail.replay")}
              </Button>
            </div>
            <p className="text-caption text-fg-muted">
              {delivery.replayable
                ? t("automations.delivery.replayHint")
                : notReplayableReason(t, delivery.not_replayable_reason)}
            </p>
          </section>

          <CodeBlock
            title={t("automations.delivery.body")}
            value={delivery.body}
            truncated={delivery.body_truncated}
            copyLabel={t("automations.delivery.copy")}
            t={t}
          />
          <CodeBlock
            title={t("automations.delivery.headers")}
            value={JSON.stringify(delivery.headers ?? {}, null, 2)}
            copyLabel={t("automations.delivery.copy")}
            t={t}
          />
          <CodeBlock
            title={t("automations.delivery.responseBody")}
            value={delivery.response_body}
            copyLabel={t("automations.delivery.copy")}
            t={t}
          />
        </div>
      )}
    </Dialog>
  );
}

function notReplayableReason(
  t: Translate,
  reason: WebhookDeliveryDetail["not_replayable_reason"],
): string {
  if (!reason) return "";
  return t(`automations.delivery.replayNot.${reason}` as MessageKey);
}

function dedupeSourceLabel(
  t: Translate,
  source: WebhookDeliveryDetail["dedupe_source"],
): string {
  return source === "header"
    ? t("automations.delivery.dedupeSourceHeader")
    : t("automations.delivery.dedupeSourceBody");
}

function Meta({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <dt className="text-caption text-fg-muted">{label}</dt>
      <dd className={cn("truncate text-body", mono && "font-mono text-[12px]")}>
        {value || "—"}
      </dd>
    </div>
  );
}

function CodeBlock({
  title,
  value,
  truncated = false,
  copyLabel,
  t,
}: {
  title: string;
  value: string;
  truncated?: boolean;
  copyLabel: string;
  t: Translate;
}) {
  const toast = useToast();
  const preview =
    value.length > PREVIEW_CHARS ? value.slice(0, PREVIEW_CHARS) : value;
  const cut = truncated || value.length > PREVIEW_CHARS;
  return (
    <section className="flex flex-col gap-1.5">
      <div className="flex items-center gap-2">
        <span className="text-body-sm font-medium">{title}</span>
        <Button
          variant="ghost"
          size="28"
          leading={<Icon name="copy" />}
          className="ml-auto"
          onClick={() => {
            void navigator.clipboard?.writeText(value);
            toast(t("automations.delivery.copied"));
          }}
        >
          {copyLabel}
        </Button>
      </div>
      <pre className="max-h-48 overflow-auto rounded-md bg-surface-muted p-3 font-mono text-[11px] whitespace-pre-wrap break-all">
        {preview || t("automations.delivery.empty")}
        {cut && `\n${t("automations.delivery.truncated")}`}
      </pre>
    </section>
  );
}
