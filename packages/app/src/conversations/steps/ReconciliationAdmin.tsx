import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import type { ReconciliationCase, ReconciliationEvidence } from "@openfox/api";
import { useI18n, useT, type MessageKey } from "@openfox/i18n";
import { Button, useToast } from "@openfox/ui";

import { keys, useApi } from "../../context";

// 阶段 12e：待核对面板的管理员部分——证据列表（来源、类型、时间、说明）与「接受已有结果」。
// 只对组织所有者与管理员渲染（由 RunTimeline 判断）；服务端同样只对这两种角色返回证据。

const KNOWN_SOURCES = ["DAEMON", "CENTER", "OPERATOR"] as const;
const KNOWN_KINDS = [
  "PROCESS_STARTED",
  "PROCESS_RUNNING",
  "PROCESS_EXITED",
  "PROCESS_NOT_FOUND",
  "RESULT_CONFIRMED",
  "HEARTBEAT",
  "REVOCATION",
] as const;

const sourceKey = (source: string): MessageKey =>
  (KNOWN_SOURCES as readonly string[]).includes(source)
    ? (`governance.reconcile.source.${source}` as MessageKey)
    : "governance.reconcile.source.unknown";

const kindKey = (kind: string): MessageKey =>
  (KNOWN_KINDS as readonly string[]).includes(kind)
    ? (`governance.reconcile.kind.${kind}` as MessageKey)
    : "governance.reconcile.kind.unknown";

const evidenceKey = (orgId: string, runId: string) =>
  [...keys.run(orgId, runId), "reconciliation", "evidence"] as const;

export function useReconciliationEvidence(orgId: string, runId: string) {
  const api = useApi();
  return useQuery({
    queryKey: evidenceKey(orgId, runId),
    queryFn: ({ signal }) =>
      api.runs.reconciliationEvidence(orgId, runId, signal),
    enabled: Boolean(orgId && runId),
    // 证据由电脑端随时补传；与核对案保持同样的刷新间隔
    refetchInterval: 30_000,
  });
}

export interface ReconciliationAdminPanelProps {
  orgId: string;
  runId: string;
  /** 当前开放的核对案（裁决需要它的 id 与 version） */
  openCase: Pick<ReconciliationCase, "id" | "version">;
}

export function ReconciliationAdminPanel({
  orgId,
  runId,
  openCase,
}: ReconciliationAdminPanelProps) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const evidence = useReconciliationEvidence(orgId, runId);
  const [confirming, setConfirming] = useState(false);
  // 只有后端允许 RESULT_ACCEPTED 的证据（已受理的结果确认，且属于仍开放的案）才给入口，
  // 避免用户点了才报错。列表按时间倒序，取最新的一条。
  const acceptable = evidence.data?.find(
    (item) => item.can_accept_result && item.case_id === openCase.id,
  );
  const accept = useMutation({
    mutationFn: (evidenceId: string) =>
      api.runs.acceptReconciliationResult(orgId, runId, openCase.id, {
        version: openCase.version,
        evidence_id: evidenceId,
      }),
    onSuccess: (result) => {
      // 被他人或自动裁决先处理：没有任何修改，提示并刷新
      if (result.already_resolved)
        toast(t("governance.reconcile.alreadyResolved"));
    },
    onSettled: () => {
      setConfirming(false);
      // keys.run 是核对案与证据查询键的前缀，一并刷新
      void queryClient.invalidateQueries({ queryKey: keys.run(orgId, runId) });
    },
  });

  return (
    <div className="flex flex-col gap-2 text-body-sm text-fg-subtle">
      <p className="text-fg-secondary">
        {t("governance.reconcile.evidenceTitle")}
      </p>
      {evidence.isError ? (
        <p className="text-danger">
          {t("governance.reconcile.evidenceLoadFailed")}
        </p>
      ) : !evidence.data ? (
        <p aria-busy="true">…</p>
      ) : evidence.data.length === 0 ? (
        <p>{t("governance.reconcile.evidenceEmpty")}</p>
      ) : (
        <EvidenceTable items={evidence.data} />
      )}
      {acceptable && !confirming && (
        <div>
          <Button
            variant="ghost"
            size="28"
            disabled={accept.isPending}
            onClick={() => setConfirming(true)}
          >
            {t("governance.reconcile.acceptResult")}
          </Button>
        </div>
      )}
      {acceptable && confirming && (
        <div className="flex flex-col gap-1">
          <p>{t("governance.reconcile.acceptConfirm")}</p>
          <div className="flex gap-2">
            <Button
              variant="ghost"
              size="28"
              disabled={accept.isPending}
              onClick={() => accept.mutate(acceptable.id)}
            >
              {t("governance.reconcile.acceptConfirmAction")}
            </Button>
            <Button
              variant="ghost"
              size="28"
              disabled={accept.isPending}
              onClick={() => setConfirming(false)}
            >
              {t("common.cancel")}
            </Button>
          </div>
        </div>
      )}
      {accept.isError && (
        <p role="alert" className="text-danger">
          {t("governance.reconcile.acceptFailed")}
        </p>
      )}
    </div>
  );
}

function EvidenceTable({ items }: { items: ReconciliationEvidence[] }) {
  const t = useT();
  const { formatDate } = useI18n();
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[480px] text-left">
        <thead className="text-fg-muted">
          <tr>
            {(["source", "kind", "time", "detail"] as const).map((column) => (
              <th key={column} scope="col" className="py-1 pr-3 font-normal">
                {t(`governance.reconcile.columns.${column}`)}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {items.map((item) => (
            <tr key={item.id} className="border-t border-line-default">
              <td className="py-1 pr-3 whitespace-nowrap">
                {t(sourceKey(item.source))}
              </td>
              <td className="py-1 pr-3 whitespace-nowrap">
                {t(kindKey(item.kind))}
                {item.accepted_state !== "ACCEPTED" && (
                  <span className="text-fg-muted">
                    {" "}
                    · {t("governance.reconcile.notAccepted")}
                  </span>
                )}
              </td>
              <td className="py-1 pr-3 whitespace-nowrap">
                <time dateTime={item.observed_at}>
                  {formatDate(item.observed_at, {
                    dateStyle: "short",
                    timeStyle: "medium",
                  })}
                </time>
              </td>
              <td className="py-1 break-words">
                {item.detail || t("governance.reconcile.noDetail")}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
