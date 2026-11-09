import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";

import type { SkillImportItem, SkillRuntimeImport } from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import { Button, Callout, Icon, Spinner, cn } from "@openfox/ui";

import { keys } from "../context";
import { summarizeImport } from "./model";
import { useCancelRuntimeImport, useRuntimeImportProgress } from "./queries";

// 导入执行与汇总：进行中显示转圈、「正在导入 x / N...」、进度条与已完成列表，
// 底栏「取消」；结束后汇总格 + 明细，底栏「完成」。只导入成功 1 个时打开该技能详情。

const REASON_CODES = [
  "SKILL_NAME_TAKEN",
  "SKILL_INVALID",
  "SKILL_NAME_INVALID",
  "SKILL_TOO_LARGE",
  "NOT_FOUND",
  "SKILL_LIMIT",
  "REQUESTER_INACTIVE",
  "FORBIDDEN",
  "SPACE_NOT_FOUND",
  "SPACE_RETIRED",
  "COMMAND_EXPIRED",
  "COMMAND_FAILED",
  "UPLOAD_REJECTED",
  "NOT_UPLOADED",
];

/** 已知原因码显示译文，未知的码显示原文。 */
function reasonText(t: ReturnType<typeof useT>, reason: string): string {
  return REASON_CODES.includes(reason)
    ? t(`skills.runtime.reasons.${reason}` as MessageKey)
    : reason;
}

const STATUS_ICON: Record<
  SkillImportItem["status"],
  { icon: Parameters<typeof Icon>[0]["name"]; tone: string; label: MessageKey }
> = {
  imported: {
    icon: "circle-check",
    tone: "text-accent",
    label: "skills.runtime.statusImported",
  },
  skipped: {
    icon: "skip-forward",
    tone: "text-fg-muted",
    label: "skills.runtime.statusSkipped",
  },
  failed: {
    icon: "circle-x",
    tone: "text-danger",
    label: "skills.runtime.statusFailed",
  },
  canceled: {
    icon: "circle-slash",
    tone: "text-fg-muted",
    label: "skills.runtime.statusCanceled",
  },
  pending: {
    icon: "clock",
    tone: "text-fg-muted",
    label: "skills.runtime.statusPending",
  },
};

export function ImportProgress({
  orgId,
  spaceId,
  initial,
  onClose,
  onOpenSkill,
  onImported,
}: {
  orgId: string;
  spaceId: string;
  initial: SkillRuntimeImport;
  onClose: () => void;
  onOpenSkill: (skillId: string) => void;
  /** 导入结束（含取消）时回传成功导入的技能 id，只调用一次；空间技能页不传 */
  onImported?: (skillIds: string[]) => void;
}) {
  const t = useT();
  const queryClient = useQueryClient();
  const progress = useRuntimeImportProgress(
    orgId,
    spaceId,
    initial.command_id,
    initial,
  );
  const cancel = useCancelRuntimeImport(orgId, spaceId);
  const data = progress.data ?? initial;
  const summary = summarizeImport(data.items);
  const running = data.status === "running";
  const total = data.items.length;

  // 结束时（不论用户是否点「完成」）回传一次成功项，避免关掉弹窗后漏加
  const reported = useRef(false);
  const importedIds = running
    ? ""
    : data.items
        .filter((item) => item.status === "imported" && item.skill_id)
        .map((item) => item.skill_id!)
        .join(",");
  useEffect(() => {
    if (running || reported.current || !onImported) return;
    reported.current = true;
    onImported(importedIds ? importedIds.split(",") : []);
  }, [running, importedIds, onImported]);

  const finish = () => {
    void queryClient.invalidateQueries({
      queryKey: keys.skills(orgId, spaceId),
    });
    const only =
      summary.imported === 1
        ? data.items.find((item) => item.status === "imported")
        : undefined;
    if (only?.skill_id) onOpenSkill(only.skill_id);
    else onClose();
  };

  return (
    <>
      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-4">
        {progress.isError && (
          <Callout tone="warning">{t("skills.runtime.pollFailed")}</Callout>
        )}
        {running ? (
          <>
            <div role="status" className="flex items-center gap-2 text-body">
              <Spinner />
              <span>
                {t("skills.runtime.importing", {
                  done: summary.finished,
                  total,
                })}
              </span>
            </div>
            <div
              role="progressbar"
              aria-valuemin={0}
              aria-valuemax={total}
              aria-valuenow={summary.finished}
              className="h-1.5 overflow-hidden rounded-full bg-surface-muted"
            >
              <div
                className="h-full bg-accent transition-[width]"
                style={{
                  width: `${total === 0 ? 0 : (summary.finished / total) * 100}%`,
                }}
              />
            </div>
            <ItemList
              items={data.items.filter((item) => item.status !== "pending")}
            />
          </>
        ) : (
          <>
            <div className="grid grid-cols-3 gap-2">
              <SummaryCell
                label={t("skills.runtime.summaryImported")}
                value={summary.imported}
              />
              <SummaryCell
                label={t("skills.runtime.summarySkipped")}
                value={summary.skipped}
              />
              <SummaryCell
                label={t("skills.runtime.summaryFailed")}
                value={summary.failed}
                danger={summary.failed > 0}
              />
            </div>
            {summary.canceled > 0 && (
              <p className="text-caption text-fg-muted">
                {t("skills.runtime.summaryCanceled")} {summary.canceled}
              </p>
            )}
            <ItemList items={data.items} />
          </>
        )}
      </div>
      <div className="flex items-center gap-3 border-t border-line-default bg-surface-muted/30 px-4 py-3">
        <span className="flex-1 text-body-sm text-fg-muted">
          {running
            ? ""
            : data.status === "canceled"
              ? t("skills.runtime.canceled")
              : t("skills.runtime.done")}
        </span>
        {running ? (
          <Button
            variant="outline"
            loading={cancel.isPending}
            onClick={() => cancel.mutate(data.command_id)}
          >
            {t("skills.runtime.cancel")}
          </Button>
        ) : (
          <Button onClick={finish}>{t("skills.runtime.finish")}</Button>
        )}
      </div>
    </>
  );
}

function SummaryCell({
  label,
  value,
  danger,
}: {
  label: string;
  value: number;
  danger?: boolean;
}) {
  return (
    <div className="flex flex-col gap-0.5 rounded-xl border border-line-default px-3 py-2">
      <span className="text-caption text-fg-muted">{label}</span>
      <span
        className={cn("text-title font-medium", danger && "text-danger")}
        data-testid={`summary-${label}`}
      >
        {value}
      </span>
    </div>
  );
}

function ItemList({ items }: { items: SkillImportItem[] }) {
  const t = useT();
  if (items.length === 0) return null;
  return (
    <ul className="flex flex-col divide-y divide-line-soft rounded-xl border border-line-default">
      {items.map((item) => {
        const status = STATUS_ICON[item.status];
        return (
          <li key={item.name} className="flex items-center gap-2 px-3 py-2">
            <Icon
              name={status.icon}
              className={status.tone}
              label={t(status.label)}
            />
            <span className="min-w-0 flex-1 truncate font-mono text-body-sm">
              {item.name}
            </span>
            {item.reason && (
              <span className="text-caption text-fg-muted">
                {reasonText(t, item.reason)}
              </span>
            )}
          </li>
        );
      })}
    </ul>
  );
}
