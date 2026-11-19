import { useEffect, useId, useMemo, useState, type ReactNode } from "react";
import { useParams } from "react-router-dom";
import { ReconciliationAdminPanel } from "./ReconciliationAdmin";

import {
  isTerminalRunState,
  type Run,
  type RunArtifact,
  type RunResult,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, ChainOfThought, Skeleton } from "@openfox/ui";

import { useApi } from "../../context";
import { ScheduledRun } from "./ScheduledRun";
import { useOrgRole } from "../../settings/common";
import { ArtifactButtons } from "../artifacts/ArtifactButtons";
import { useArtifactOpener } from "../artifacts/context";
import { FileAttachmentCard, ModelErrorCard } from "./cards";
import { elapsedMs, isDuplicateRunError } from "./format";
import { ResultSummary, RetryOfNote } from "./ResultSummary";
import {
  foldRunEvents,
  hasSteps,
  type Turn,
  type TurnFinalState,
} from "./model";
import {
  useMarkReconciliationFailed,
  useRun,
  useRunArtifacts,
  useRunEvents,
  useRunReconciliation,
  useRunResult,
} from "./queries";
import {
  RenderTextContext,
  ReconciliationRow,
  RunStatusRow,
  StepText,
  TurnActivityList,
  TurnSummaryRow,
  useExpandAllSignal,
  type ReconciliationRowProps,
  type RenderText,
  type RunStatusKind,
} from "./rows";

// Run 执行时间线（Figma 页 12 分区 5 执行过程 411:25591）：
// 过程说明始终按事件顺序显示；连续工具合并摘要，异常默认展开。
// 结束后收起成功的环境准备与思考详情，最终回复与失败卡片保持可见。

/** 公共的展示参数（RunTimeline 与 RunTimelineView 共用）。 */
export interface RunTimelineDisplayProps {
  /** 正文渲染（agent_text 与最终回复）；缺省为纯文本，Markdown 由调用方提供 */
  renderText?: RenderText;
  /** 是否在末尾显示最终回复；调用方单独渲染 AGENT 消息时传 false。默认 true */
  showFinalText?: boolean;
  /** 失败卡片的「重试」；不传则不显示按钮 */
  onRetry?: () => void;
  retryScope?: Pick<Run, "agent_id" | "actor_id">;
  retrying?: boolean;
  /**
   * 阶段 12d（F-08）：WORKTREE_PREP_FAILED 时改为直接在源目录运行并重试。
   * 只有 Run 的发起人（原输入作者，actor_id）等于 actorId 时才显示入口；directory 为将改用的源目录。
   */
  directRetry?: {
    actorId: string;
    directory: string;
    onRetry: (runId: string) => void;
  };
  /** 回合结束后过程详情默认展开；默认 false（工具摘要、说明与异常仍可见） */
  defaultExpanded?: boolean;
  /** 阶段 10d：是否在末尾显示产物；回复里单独显示产物按钮时传 false。默认 true */
  showArtifacts?: boolean;
}

export interface RunTimelineViewProps extends RunTimelineDisplayProps {
  turn: Turn;
  run?: Run;
  result?: RunResult | null;
  artifacts?: RunArtifact[];
  /** 产物下载地址 */
  artifactHref?: (artifact: RunArtifact) => string;
  /** 阶段 10d：打开会话产物面板；有它时末尾显示「产物：<文件名>」按钮（运行中也显示），代替生成文件卡片 */
  onOpenArtifact?: (id: string) => void;
  /** 事件达到页数上限，只显示了前一部分 */
  truncated?: boolean;
  /** 阶段 12a：待核对时的原因与人工入口；缺省按“不可人工处理”显示联系管理员 */
  reconciliation?: Pick<
    ReconciliationRowProps,
    "reason" | "canResolve" | "onMarkFailed" | "pending" | "error"
  >;
  /** 阶段 12e：待核对时仅管理员可见的附加内容（证据列表与「接受已有结果」） */
  reconciliationAdmin?: ReactNode;
}

const RUN_FINAL: Partial<Record<Run["state"], TurnFinalState>> = {
  SUCCEEDED: "done",
  FAILED: "failed",
  CANCELED: "cancelled",
};

/** 运行中每秒刷新一次的当前时间。 */
function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [active]);
  return now;
}

function runningStatus(turn: Turn, run: Run | undefined): RunStatusKind {
  if (run?.state === "QUEUED") return "queued";
  if (run?.state === "CANCEL_REQUESTED") return "canceling";
  if (turn.activeKind === "thinking") return "thinking";
  return "processing";
}

/** 纯展示：给定折叠后的回合与 Run 数据渲染时间线。 */
export function RunTimelineView({
  turn,
  run,
  result,
  artifacts = [],
  artifactHref,
  truncated = false,
  renderText,
  showFinalText = true,
  onRetry,
  retryScope,
  retrying,
  directRetry,
  defaultExpanded = false,
  showArtifacts = true,
  onOpenArtifact,
  reconciliation,
  reconciliationAdmin,
}: RunTimelineViewProps) {
  const t = useT();
  const ended = turn.ended || isTerminalRunState(run?.state);
  const contentId = useId();
  const [expandedState, setExpanded] = useExpandAllSignal(defaultExpanded);
  const expanded = expandedState ?? defaultExpanded;
  const now = useNow(!ended);
  const finalState: TurnFinalState =
    turn.finalState ?? (run ? RUN_FINAL[run.state] : undefined) ?? "done";
  const durationMs = turn.durationMs ?? run?.duration_ms ?? null;
  const steps = hasSteps(turn);
  const compact = ended && !expanded;
  const failed =
    ended && (finalState === "failed" || Boolean(run?.failure_code));
  const finalText = turn.finalText ?? (result?.output || null);
  const errorDetail =
    turn.error?.detail || turn.error?.text || result?.summary || null;

  return (
    <RenderTextContext.Provider value={renderText ?? null}>
      <div
        className="flex flex-col gap-2"
        aria-label={t("steps.timeline.label")}
        data-run-id={run?.id}
      >
        <ChainOfThought>
          {ended && (
            <TurnSummaryRow
              state={finalState}
              durationMs={durationMs}
              expanded={expanded}
              controlsId={steps ? contentId : undefined}
              onToggle={steps ? () => setExpanded(!expanded) : undefined}
            />
          )}
          {turn.items.length > 0 && (
            <div
              id={contentId}
              className="flex min-w-0 flex-col gap-2 empty:hidden"
            >
              <TurnActivityList items={turn.items} compact={compact} />
            </div>
          )}
          {truncated && (
            <p className="text-caption text-fg-muted">
              {t("steps.timeline.truncated", { count: turn.eventCount })}
            </p>
          )}
          {!ended && run?.reconciliation_required && (
            <>
              <ReconciliationRow canResolve={false} {...reconciliation} />
              {reconciliationAdmin}
            </>
          )}
          {!ended && !run?.reconciliation_required && (
            <RunStatusRow
              kind={runningStatus(turn, run)}
              waitReason={run?.wait_reason}
              durationMs={elapsedMs(
                run?.started_at ?? run?.created_at,
                new Date(now),
              )}
            />
          )}
        </ChainOfThought>
        {failed && (
          <ModelErrorCard
            failureCode={run?.failure_code}
            detail={errorDetail}
            onRetry={
              !retryScope ||
              (run?.agent_id === retryScope.agent_id &&
                run.actor_id === retryScope.actor_id)
                ? onRetry
                : undefined
            }
            retrying={retrying}
            onRetryDirect={
              run &&
              directRetry &&
              directRetry.actorId &&
              directRetry.directory &&
              run.failure_code === "WORKTREE_PREP_FAILED" &&
              run.actor_id === directRetry.actorId
                ? () => directRetry.onRetry(run.id)
                : undefined
            }
            directDirectory={directRetry?.directory}
          />
        )}
        {ended &&
          showFinalText &&
          finalText &&
          !isDuplicateRunError(finalText, result) && (
            <StepText text={finalText} />
          )}
        {ended && finalState !== "cancelled" && (
          <ResultSummary result={result} run={run} />
        )}
        {ended && <RetryOfNote retryOf={run?.retry_of} />}
        {showArtifacts && onOpenArtifact && (
          <ArtifactButtons artifacts={artifacts} onOpen={onOpenArtifact} />
        )}
        {showArtifacts &&
          !onOpenArtifact &&
          ended &&
          artifactHref &&
          artifacts.map((artifact) => (
            <FileAttachmentCard
              key={artifact.id}
              artifact={artifact}
              href={artifactHref(artifact)}
            />
          ))}
      </div>
    </RenderTextContext.Provider>
  );
}

export interface RunTimelineProps extends RunTimelineDisplayProps {
  runId: string;
  /** 缺省取路由参数 :orgId */
  orgId?: string;
}

/**
 * 一次 Run 的执行时间线：拉 Run 详情 + 全部事件 + 结果 + 产物；
 * 实时由 useOrgEvents 维护（run.event 追加进事件缓存，run.state 更新 Run）。
 */
export function RunTimeline({
  runId,
  orgId: orgProp,
  ...display
}: RunTimelineProps) {
  const t = useT();
  const api = useApi();
  const params = useParams();
  const orgId = orgProp ?? params.orgId ?? "";
  const run = useRun(orgId, runId);
  const events = useRunEvents(orgId, runId);
  const terminal = isTerminalRunState(run.data?.state);
  const result = useRunResult(orgId, runId, terminal);
  // 阶段 10d：在会话里（有产物面板）运行中也取产物，Agent 发布后经 run.artifact 实时刷新
  const openArtifact = useArtifactOpener();
  const artifacts = useRunArtifacts(
    orgId,
    runId,
    terminal || (Boolean(openArtifact) && display.showArtifacts !== false),
  );
  const role = useOrgRole(orgId);
  const canResolve = role === "OWNER" || role === "ADMIN";
  const pendingReview = run.data?.reconciliation_required === true && !terminal;
  const cases = useRunReconciliation(orgId, runId, pendingReview);
  const openCase = cases.data?.find((item) => item.status === "OPEN");
  const markFailed = useMarkReconciliationFailed(orgId, runId);
  const list = events.data?.events;
  const turn = useMemo(
    () => foldRunEvents(list ?? [], { terminal, hideRoutinePreparation: true }),
    [list, terminal],
  );

  if (events.isError || run.isError)
    return (
      <div className="flex items-center gap-2 text-body-sm text-fg-muted">
        <span>{t("steps.timeline.loadFailed")}</span>
        <Button
          variant="ghost"
          size="28"
          onClick={() => {
            if (run.isError) void run.refetch();
            if (events.isError) void events.refetch();
          }}
        >
          {t("common.retry")}
        </Button>
      </div>
    );
  if (!list || !run.data)
    return (
      <div aria-busy="true" className="flex flex-col gap-2">
        <Skeleton className="h-5 w-40" />
        <Skeleton className="h-5 w-64" />
      </div>
    );
  return (
    <>
      <ScheduledRun orgId={orgId} run={run.data} />
      <RunTimelineView
        {...display}
        turn={turn}
        run={run.data}
        result={result.data}
        artifacts={artifacts.data}
        artifactHref={(artifact) =>
          api.runs.artifactUrl(orgId, runId, artifact.id)
        }
        onOpenArtifact={openArtifact ?? undefined}
        truncated={events.data?.truncated}
        reconciliation={{
          reason: openCase?.reason_code,
          canResolve,
          onMarkFailed: openCase
            ? () =>
                markFailed.mutate({
                  id: openCase.id,
                  version: openCase.version,
                })
            : undefined,
          pending: markFailed.isPending,
          error: markFailed.isError,
        }}
        reconciliationAdmin={
          canResolve && pendingReview && openCase ? (
            <ReconciliationAdminPanel
              orgId={orgId}
              runId={runId}
              openCase={openCase}
            />
          ) : undefined
        }
      />
    </>
  );
}
