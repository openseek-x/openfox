import { Link } from "react-router-dom";

import { isApiError } from "@openfox/api";
import { useI18n, useT, type MessageKey, type Translate } from "@openfox/i18n";
import { Button, Drawer, Icon, Spinner, useToast } from "@openfox/ui";

import {
  formatDateTime,
  RunStatusIcon,
  runStatusLabel,
  triggerReason,
} from "./parts";
import { useAutomationRunDetail } from "./queries";

// 运行详情抽屉：宽 800，贴右，与任务详情共用层叠规则。

/** 「建议下一步」：电脑离线一类 → 检查电脑；Agent 相关 → 检查 Agent；其他 → 编辑自动化。 */
export function nextStepFor(
  failureCode: string,
): "computer" | "agent" | "edit" {
  if (/DEVICE|COMPUTER|OFFLINE/.test(failureCode)) return "computer";
  if (/AGENT/.test(failureCode)) return "agent";
  return "edit";
}

export function RunDetailDrawer({
  orgId,
  spaceId,
  automationId,
  runId,
  onClose,
  onEdit,
}: {
  orgId: string;
  spaceId: string;
  automationId: string;
  runId: string | null;
  onClose: () => void;
  onEdit: () => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const toast = useToast();
  const detail = useAutomationRunDetail(orgId, spaceId, automationId, runId);
  const run = detail.data;
  const notFound = isApiError(detail.error) && detail.error.status === 404;

  return (
    <Drawer
      open={runId !== null}
      onClose={onClose}
      title={t("automations.run.title")}
      width="min(800px, calc(100vw - 48px))"
    >
      {detail.isLoading ? (
        <div className="flex items-center gap-2 text-body text-fg-muted">
          <Spinner />
          {t("automations.run.loading")}
        </div>
      ) : notFound ? (
        <p className="text-body text-fg-muted">
          {t("automations.run.notFound")}
        </p>
      ) : detail.isError || !run ? (
        <p className="text-body text-fg-muted">
          {t("automations.run.loadFailed")}
        </p>
      ) : (
        <div className="mx-auto flex max-w-[896px] flex-col gap-5">
          <div className="flex flex-col gap-2">
            <div className="flex items-center gap-2 text-body-sm">
              <RunStatusIcon status={run.status} />
              <span className="font-medium">
                {runStatusLabel(t, run.status)}
              </span>
              <span className="text-fg-muted">
                {formatDateTime(locale, run.created_at)}
              </span>
            </div>
            <h2 className="text-[24px] font-bold leading-tight">
              {run.trigger_title || triggerReason(t, run.trigger_kind)}
            </h2>
          </div>

          <section className="flex flex-col gap-2">
            <h3 className="text-body font-medium">
              {t("automations.run.resultDetail")}
            </h3>
            {run.failure_message && (
              <p className="text-body text-danger">{run.failure_message}</p>
            )}
            {run.reason && (
              <p className="text-body text-fg-secondary">{run.reason}</p>
            )}
            {run.failure_code && (
              <div className="flex flex-wrap items-center gap-2 text-body-sm">
                <span className="text-fg-muted">
                  {t("automations.run.nextStep")}
                </span>
                <NextStepAction
                  step={nextStepFor(run.failure_code)}
                  orgId={orgId}
                  spaceId={spaceId}
                  onEdit={onEdit}
                  t={t}
                />
              </div>
            )}
          </section>

          <section className="flex flex-col gap-1">
            <h3 className="text-body font-medium">
              {t("automations.run.runId")}
            </h3>
            <div className="flex items-center gap-2">
              <code className="min-w-0 flex-1 truncate rounded-md bg-surface-muted px-2.5 py-1.5 font-mono text-[12px]">
                {run.id}
              </code>
              <Button
                variant="ghost"
                size="32"
                leading={<Icon name="copy" />}
                onClick={() => {
                  void navigator.clipboard?.writeText(run.id);
                  toast(t("automations.webhook.copied"));
                }}
              >
                {t("automations.run.copyId")}
              </Button>
            </div>
          </section>

          <section className="flex flex-col gap-1">
            <h3 className="text-body font-medium">
              {t("automations.run.triggerDetail")}
            </h3>
            <p className="text-body text-fg-secondary">
              {triggerReason(t, run.trigger_kind)}
            </p>
          </section>

          <CodeSection
            title={t("automations.run.rawResult")}
            value={run.result ?? ""}
            empty={t("automations.run.noResult")}
          />
          <CodeSection
            title={t("automations.run.rawPayload")}
            value={JSON.stringify(run.trigger_payload ?? {}, null, 2)}
          />
        </div>
      )}
    </Drawer>
  );
}

function NextStepAction({
  step,
  orgId,
  spaceId,
  onEdit,
  t,
}: {
  step: "computer" | "agent" | "edit";
  orgId: string;
  spaceId: string;
  onEdit: () => void;
  t: Translate;
}) {
  if (step === "edit") {
    return (
      <Button variant="secondary" size="28" onClick={onEdit}>
        {t("automations.run.nextEdit")}
      </Button>
    );
  }
  const to =
    step === "computer"
      ? `/o/${orgId}/settings/computers`
      : `/o/${orgId}/spaces/${spaceId}/agents`;
  const label: MessageKey =
    step === "computer"
      ? "automations.run.nextCheckComputer"
      : "automations.run.nextCheckAgent";
  return (
    <Link to={to} className="text-accent hover:underline">
      {t(label)}
    </Link>
  );
}

function CodeSection({
  title,
  value,
  empty,
}: {
  title: string;
  value: string;
  empty?: string;
}) {
  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-body font-medium">{title}</h3>
      <pre className="max-h-72 overflow-auto rounded-md bg-surface-muted p-3 font-mono text-[12px] whitespace-pre-wrap">
        {value || empty}
      </pre>
    </section>
  );
}
