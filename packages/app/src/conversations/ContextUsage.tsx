import { isTerminalRunState, type Run, type RunEvent } from "@openfox/api";
import { useI18n } from "@openfox/i18n";
import { Icon } from "@openfox/ui";

import { contextSample, requestedContextModel } from "./contextUsageModel";
import { stepOf } from "./steps/model";
import { useRun, useRunEvents } from "./steps/queries";

/** Share the timeline's authenticated event query; do not scan older Runs. */
export function ContextUsage({
  orgId,
  runId,
  effective,
}: {
  orgId: string;
  runId?: string;
  effective?: { runtime: string; model: string } | null;
}) {
  const { t, locale } = useI18n();
  const run = useRun(orgId, runId ?? "");
  const events = useRunEvents(orgId, runId ?? "");
  const terminal = isTerminalRunState(run.data?.state);
  const snapshot =
    run.data &&
    events.isSuccess &&
    !events.isFetching &&
    !events.isError &&
    !run.isError &&
    !events.data.truncated
      ? events.data.events
      : null;
  const latest =
    run.data && snapshot ? contextSample(run.data, snapshot) : null;
  const changed =
    terminal &&
    effective &&
    latest &&
    (effective.runtime !== latest.runtime ||
      requestedContextModel(effective.model) !== latest.requestedModel);
  const sample = changed ? null : latest;
  const current = sample?.current ?? null;
  const window = sample?.window ?? null;
  const percent =
    current !== null && window !== null ? (current / window) * 100 : null;
  const number = (value: number) =>
    value.toLocaleString(locale, { maximumFractionDigits: 1 });
  const compact = (value: number) =>
    value >= 1_000_000
      ? `${number(value / 1_000_000)}M`
      : value >= 1_000
        ? `${number(value / 1_000)}K`
        : number(value);
  const usage = !run.isError ? run.data?.usage : null;
  const input = validCounter(usage?.input) ? usage.input : null;
  const output = validCounter(usage?.output) ? usage.output : null;
  const cacheRead = validCounter(usage?.cached_input)
    ? usage.cached_input
    : null;
  const cacheWrite = validCounter(usage?.cache_write_input)
    ? usage.cache_write_input
    : null;
  // The preparation snapshot identifies the actual Run, even after next-run
  // settings change. Never infer historical usage from the current config.
  const runtime =
    run.data && snapshot
      ? (preparedRuntime(run.data, snapshot) ?? latest?.runtime)
      : null;
  // Claude Code, OpenCode, Pi and CodeBuddy report fresh input separately.
  // Codex includes cached input. Other adapters have no verified cache split.
  const separateCache = ["claude-code", "opencode", "pi", "codebuddy"].includes(
    runtime ?? "",
  );
  const inputTotal =
    runtime === "codex"
      ? input
      : separateCache &&
          input !== null &&
          cacheRead !== null &&
          cacheWrite !== null
        ? input + cacheRead + cacheWrite
        : null;
  const total =
    inputTotal !== null && output !== null && validCounter(inputTotal + output)
      ? inputTotal + output
      : null;
  const cacheHit =
    validCounter(inputTotal) &&
    inputTotal > 0 &&
    cacheRead !== null &&
    cacheRead <= inputTotal
      ? (cacheRead / inputTotal) * 100
      : null;
  const sampleLabel = t(
    terminal ? "chat.contextUsage.previous" : "chat.contextUsage.current",
  );
  const contextDetails = [
    `${t("chat.contextUsage.label")}：${current !== null ? t("chat.contextUsage.tokens", { count: number(current) }) : t("chat.contextUsage.unknown")}`,
    current !== null
      ? window === null
        ? t("chat.contextUsage.windowUnknown")
        : t("chat.contextUsage.window", {
            count: number(window),
            percent: number(percent!),
          })
      : null,
    sample ? sampleLabel : null,
    sample ? (sample.model ?? t("chat.contextUsage.modelUnknown")) : null,
    sample ? new Date(sample.sampledAt).toLocaleString(locale) : null,
    sample ? t("chat.contextUsage.estimate") : null,
    changed ? t("chat.contextUsage.changed") : null,
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <div
      className="mb-2 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 px-1 text-caption text-fg-muted tabular-nums"
      aria-label={t("chat.contextUsage.stats")}
    >
      {(input !== null || output !== null) && (
        <span
          className="inline-flex min-w-0 max-w-full flex-wrap items-center gap-x-1.5 gap-y-1"
          title={sampleLabel}
          aria-label={sampleLabel}
        >
          <Icon name="chart" size={16} />
          {total !== null ? (
            <span
              title={t("chat.contextUsage.tokens", { count: number(total) })}
            >
              {compact(total)} tok
            </span>
          ) : (
            <>
              {input !== null && (
                <span title={t("chat.contextUsage.input")}>
                  ↓ {compact(input)} tok
                </span>
              )}
              {output !== null && (
                <span title={t("chat.contextUsage.output")}>
                  ↑ {compact(output)} tok
                </span>
              )}
            </>
          )}
          {cacheHit !== null && (
            <>
              <span aria-hidden="true">·</span>
              <span>
                {t("chat.contextUsage.cacheHit", { percent: number(cacheHit) })}
              </span>
            </>
          )}
        </span>
      )}
      <span
        className="ml-auto inline-flex min-w-0 max-w-full items-center gap-1.5 whitespace-nowrap"
        title={contextDetails}
        aria-label={contextDetails}
      >
        {percent !== null ? (
          <svg
            role="progressbar"
            aria-label={t("chat.contextUsage.label")}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.min(percent, 100)}
            aria-valuetext={`${number(percent)}%`}
            width="16"
            height="16"
            viewBox="0 0 16 16"
            className="shrink-0 -rotate-90"
          >
            <circle
              cx="8"
              cy="8"
              r="6"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              opacity="0.2"
            />
            <circle
              cx="8"
              cy="8"
              r="6"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              pathLength="100"
              strokeDasharray={`${Math.min(percent, 100)} 100`}
            />
          </svg>
        ) : (
          <Icon name="circle" size={16} />
        )}
        <span className="truncate">
          {percent !== null
            ? t("chat.contextUsage.percent", { percent: number(percent) })
            : t("chat.contextUsage.summary", {
                value:
                  current !== null
                    ? t("chat.contextUsage.tokens", { count: compact(current) })
                    : t("chat.contextUsage.unknown"),
              })}
        </span>
      </span>
    </div>
  );
}

function validCounter(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

/** Supervisor writes the adapter name into the current generation's prep step. */
function preparedRuntime(run: Run, events: readonly RunEvent[]): string | null {
  let sequence = -1;
  let runtime: string | null = null;
  for (const event of events) {
    if (
      event.run_id !== run.id ||
      event.generation !== run.generation ||
      event.type !== "step" ||
      !Number.isSafeInteger(event.sequence) ||
      event.sequence <= sequence
    )
      continue;
    const step = stepOf(event);
    if (step?.kind !== "prep_env" || step.id !== "prep") continue;
    sequence = event.sequence;
    const substep = Array.isArray(step.substeps)
      ? step.substeps.find((item) => item?.kind === "runtime")
      : null;
    runtime = typeof substep?.label === "string" ? substep.label : null;
  }
  return runtime;
}
