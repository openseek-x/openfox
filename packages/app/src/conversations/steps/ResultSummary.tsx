import { useId } from "react";
import type { Run, RunResult } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { ChainOfThoughtTrigger, Icon } from "@openfox/ui";
import { useExpandAllSignal } from "./rows";

// Git 统计作为可展开的辅助信息；普通聊天不展示空卡片，警告始终可见。

type Reason = "UNCOMMITTED_CHANGES" | "GIT_SUMMARY_UNAVAILABLE" | "RECONCILED";

/** 合并 daemon 上报的未验证原因与中心推导的 RECONCILED（run.reconciled），去重并保持顺序。 */
export function unverifiedReasons(
  result: RunResult | null | undefined,
  run: Pick<Run, "reconciled"> | undefined,
): Reason[] {
  const list: Reason[] = [...(result?.git_summary?.unverified_reasons ?? [])];
  if (run?.reconciled && !list.includes("RECONCILED")) list.push("RECONCILED");
  return list;
}

export function ResultSummary({
  result,
  run,
}: {
  result?: RunResult | null;
  run?: Pick<Run, "reconciled">;
}) {
  const t = useT();
  const contentId = useId();
  const [expanded, setExpanded] = useExpandAllSignal(false);
  if (!result) return null;
  const git = result.git_summary;
  const reasons = unverifiedReasons(result, run);
  const available = git?.repo && git.available;
  const unavailable = git?.repo && !git.available;
  if (!available && !unavailable && reasons.length === 0) return null;

  const rows: { label: string; value: string }[] = [];
  const highlights: string[] = [];
  if (available) {
    rows.push({
      label: t("steps.result.branch"),
      value: git.detached
        ? t("steps.result.detached", { head: git.head ?? "" })
        : git.branch || "-",
    });
    const total = git.commit_total ?? 0;
    if (total > 0) {
      rows.push({
        label: t("steps.result.commits"),
        value: t("steps.result.commitCount", { count: total }),
      });
      highlights.push(
        `${t("steps.result.commits")} ${t("steps.result.commitCount", { count: total })}`,
      );
    }
    if (git.impact) {
      const impact = t("steps.result.impactValue", {
        files: git.impact.files,
        added: git.impact.added,
        deleted: git.impact.deleted,
      });
      rows.push({
        label: t("steps.result.impact"),
        value: impact,
      });
      highlights.unshift(
        git.impact.files === 0 ? t("steps.result.noChanges") : impact,
      );
    }
    rows.push({
      label: t("steps.result.uncommitted"),
      value: t("steps.result.uncommittedCount", {
        count: git.uncommitted ?? 0,
      }),
    });
    if (git.uncommitted && git.uncommitted > 0)
      highlights.push(
        `${t("steps.result.uncommitted")} ${t("steps.result.uncommittedCount", { count: git.uncommitted })}`,
      );
  }
  return (
    <section
      aria-label={t("steps.result.title")}
      className="flex min-w-0 flex-col gap-2 text-body-sm"
    >
      {available && (
        <div className="max-w-full self-start rounded-lg border border-line-default bg-surface-muted/40 p-1">
          <ChainOfThoughtTrigger
            expanded={Boolean(expanded)}
            aria-controls={expanded ? contentId : undefined}
            onClick={() => setExpanded(!expanded)}
            icon={<Icon name="git-branch" size={14} />}
            className="text-fg-secondary"
          >
            <span className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
              <span className="font-medium">{t("steps.result.title")}</span>
              {highlights.map((text) => (
                <span key={text} className="text-caption text-fg-muted">
                  {text}
                </span>
              ))}
            </span>
          </ChainOfThoughtTrigger>
          {expanded && (
            <div
              id={contentId}
              className="flex min-w-0 flex-col gap-2 border-t border-line-default px-2 py-2 [overflow-wrap:anywhere]"
            >
              <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
                {rows.map((row) => (
                  <div key={row.label} className="contents">
                    <dt className="text-fg-muted">{row.label}</dt>
                    <dd>{row.value}</dd>
                  </div>
                ))}
              </dl>
              {git.commits && git.commits.length > 0 && (
                <ul className="ml-4 list-disc text-fg-secondary">
                  {git.commits.map((c) => (
                    <li key={c.hash}>
                      <code>{c.hash}</code> {c.title}
                    </li>
                  ))}
                </ul>
              )}
              {git.impact && git.impact.paths.length > 0 && (
                <p className="text-caption text-fg-muted">
                  {git.impact.paths.slice(0, 5).join("、")}
                  {git.impact.paths_total > 5
                    ? t("steps.result.morePaths", {
                        count: git.impact.paths_total - 5,
                      })
                    : ""}
                </p>
              )}
            </div>
          )}
        </div>
      )}
      {(unavailable || reasons.length > 0) && (
        <p className="flex items-start gap-1.5 text-caption text-warning">
          <Icon name="triangle-alert" size={14} className="mt-0.5" />
          <span>
            {unavailable && t("steps.result.unavailable")}
            {unavailable && reasons.length > 0 && " · "}
            {reasons.length > 0 && t("steps.result.unverified")}
            {reasons.map((r) => t(`steps.result.reasons.${r}`)).join("；")}
          </span>
        </p>
      )}
    </section>
  );
}

/** “重试自 …”：跳到同页原运行的时间线；找不到（不在当前页）时不动作。 */
export function RetryOfNote({ retryOf }: { retryOf?: string }) {
  const t = useT();
  if (!retryOf) return null;
  const jump = () => {
    const el = document.querySelector(`[data-run-id="${retryOf}"]`);
    if (el && "scrollIntoView" in el)
      (el as HTMLElement).scrollIntoView({ block: "center" });
  };
  return (
    <p className="text-caption text-fg-muted">
      {t("steps.result.retryOf")}{" "}
      <button type="button" className="underline" onClick={jump}>
        {retryOf.slice(0, 8)}
      </button>
    </p>
  );
}
