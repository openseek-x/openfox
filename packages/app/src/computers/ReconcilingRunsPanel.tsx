import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import type { ReconcilingRun } from "@openfox/api";
import { useI18n, useT, type MessageKey } from "@openfox/i18n";
import { Button, ConfirmDialog, useToast } from "@openfox/ui";

import { keys, useApi } from "../context";
import { useCanManage } from "../settings/common";

// 阶段 12e：管理员在电脑页处理待核对的运行。
// 普通成员的私有会话卡在「待核对」时，会话外的组织管理员看不到该会话，发起人又不是管理员，
// 没有人能解除。这里给组织所有者与管理员一个不依赖会话可见性的入口：列表只含运维字段
// （Agent、发起人、时间、原因），不含任何会话内容；只提供「标记为失败」。
// 「接受已有结果」仍只在会话内的时间线里提供。

/** 有文案的原因码（与时间线的待核对原因共用文案）；其他原因用通用文案 */
const KNOWN_REASONS = ["LEASE_EXPIRED", "UNKNOWN_PROCESS", "PID_REUSE"];

const reasonKey = (reason: string): MessageKey =>
  KNOWN_REASONS.includes(reason)
    ? (`steps.reconciliation.reason.${reason}` as MessageKey)
    : "steps.reconciliation.reason.generic";

export const reconcilingRunsKey = (orgId: string, deviceId?: string) =>
  ["org", orgId, "reconciling-runs", deviceId ?? "all"] as const;

/** 没有实时事件；与电脑列表同样 30 秒刷新 */
const POLL_MS = 30_000;

export interface ReconcilingRunsPanelProps {
  orgId: string;
  /** 给出时只列这台电脑上的运行；不给时列全组织的，并多显示「电脑」一列 */
  deviceId?: string;
}

/**
 * 仅组织所有者与管理员渲染并发请求；没有待核对的运行、加载中或加载失败时不显示
 * （这是辅助入口，不打扰正常使用）。
 */
export function ReconcilingRunsPanel({
  orgId,
  deviceId,
}: ReconcilingRunsPanelProps) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const { formatDate } = useI18n();
  const canManage = useCanManage(orgId);
  const [target, setTarget] = useState<ReconcilingRun | null>(null);
  const runs = useQuery({
    queryKey: reconcilingRunsKey(orgId, deviceId),
    queryFn: ({ signal }) => api.runs.reconcilingRuns(orgId, deviceId, signal),
    enabled: canManage && Boolean(orgId),
    refetchInterval: POLL_MS,
    retry: false,
  });
  const refresh = () => {
    // 前缀匹配：同时刷新单台电脑与全组织的列表，以及电脑列表与详情上的「核对中」计数
    void queryClient.invalidateQueries({
      queryKey: ["org", orgId, "reconciling-runs"],
    });
    void queryClient.invalidateQueries({ queryKey: keys.devices(orgId) });
    if (deviceId)
      void queryClient.invalidateQueries({
        queryKey: keys.device(orgId, deviceId),
      });
  };
  const markFailed = useMutation({
    mutationFn: (run: ReconcilingRun) =>
      api.runs.markReconciliationFailed(orgId, run.run_id, run.case_id, {
        version: run.case_version,
      }),
    onSuccess: (result) => {
      // 被他人或电脑端的自动核对先处理：没有任何修改，提示并刷新
      toast(
        t(
          result.already_resolved
            ? "governance.computer.pending.alreadyResolved"
            : "governance.computer.pending.marked",
        ),
      );
    },
    onError: () =>
      toast(t("governance.computer.pending.failed"), { tone: "error" }),
    onSettled: () => {
      setTarget(null);
      refresh();
    },
  });

  if (!canManage || !runs.data || runs.data.length === 0) return null;
  return (
    <section
      aria-label={t("governance.computer.pending.title")}
      className="flex flex-col gap-2 rounded-xl border border-line-default p-4"
    >
      <h2 className="text-title font-medium text-fg-default">
        {t("governance.computer.pending.title")}
      </h2>
      <p className="text-body-sm text-fg-muted">
        {t("governance.computer.pending.desc")}
      </p>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[560px] text-left text-body-sm">
          <thead className="text-fg-muted">
            <tr>
              {(
                [
                  ...(deviceId ? [] : (["computer"] as const)),
                  "agent",
                  "actor",
                  "since",
                  "reason",
                  "action",
                ] as const
              ).map((column) => (
                <th
                  key={column}
                  scope="col"
                  className="py-1.5 pr-3 font-normal"
                >
                  {t(`governance.computer.pending.columns.${column}`)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {runs.data.map((run) => (
              <tr key={run.run_id} className="border-t border-line-default">
                {!deviceId && (
                  <td className="py-1.5 pr-3">{run.computer_name}</td>
                )}
                <td className="py-1.5 pr-3">{run.agent_name}</td>
                <td className="py-1.5 pr-3">{run.actor_name}</td>
                <td className="py-1.5 pr-3 whitespace-nowrap">
                  <time dateTime={run.pending_since}>
                    {formatDate(run.pending_since, {
                      dateStyle: "short",
                      timeStyle: "short",
                    })}
                  </time>
                </td>
                <td className="py-1.5 pr-3">{t(reasonKey(run.reason_code))}</td>
                <td className="py-1.5">
                  {run.case_id ? (
                    <Button
                      variant="ghost"
                      size="28"
                      disabled={markFailed.isPending}
                      onClick={() => setTarget(run)}
                    >
                      {t("governance.computer.pending.markFailed")}
                    </Button>
                  ) : (
                    // 有待核对标记但没有开放的核对案：无法人工处理，等电脑端上报或下一轮扫描
                    <span className="text-fg-muted">
                      {t("governance.computer.pending.noCase")}
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <ConfirmDialog
        open={target !== null}
        danger
        loading={markFailed.isPending}
        title={t("governance.computer.pending.confirmTitle")}
        description={
          target
            ? t("governance.computer.pending.confirmDesc", {
                agent: target.agent_name,
                actor: target.actor_name,
              })
            : undefined
        }
        confirmLabel={t("governance.computer.pending.confirmAction")}
        onCancel={() => setTarget(null)}
        onConfirm={() => target && markFailed.mutate(target)}
      />
    </section>
  );
}
