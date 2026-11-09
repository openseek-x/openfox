import { useInfiniteQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";

import {
  isApiError,
  isTerminalRunState,
  type AgentRunSummary,
  type RunState,
} from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Button, Dialog, Skeleton } from "@openfox/ui";

import { Markdown } from "../../conversations/Markdown";
import {
  ExpandAllContext,
  RunTimelineView,
  foldRunEvents,
  turnTranscript,
  useRun,
  useRunArtifacts,
  useRunResult,
} from "../../conversations/steps";
import { keys, useApi } from "../../context";
import { useCopy } from "../../tasks/parts";

// 运行历史里「查看记录」的弹窗：在当前页展示一次 Run 的执行记录（复用对话里的执行时间线），
// 支持全部展开、复制全部、关闭。事件按页加载（每页 ≤ 100 条），不一次拉全。

/** 列表行上已有的运行摘要（标题、任务编号）；刷新后列表里找不到这条运行时为 undefined。 */
export type RunTranscriptSummary = Pick<
  AgentRunSummary,
  "conversation_title" | "task_display_id"
>;

/** 每页事件数（接口上限 100）；满页超过客户端响应上限时改用小页重试，与 api.runs.allEvents 一致。 */
const PAGE_SIZE = 100;
const SMALL_PAGE_SIZE = 20;

const renderMarkdown = (text: string) => <Markdown source={text} />;

/** next_cursor 为空、0 或缺省表示没有下一页。 */
function nextCursor(page: { next_cursor?: unknown }): string | undefined {
  const raw = page.next_cursor;
  if (raw === undefined || raw === null) return undefined;
  const value = String(raw);
  return value === "" || value === "0" ? undefined : value;
}

/** 分页拉一次 Run 的事件；只在用户点「加载更多」时取下一页。 */
function useRunEventPages(orgId: string, runId: string) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: [...keys.run(orgId, runId), "transcript"] as const,
    queryFn: async ({ pageParam }) => {
      try {
        return await api.runs.events(orgId, runId, pageParam, PAGE_SIZE);
      } catch (error) {
        if (!isApiError(error, "INVALID_RESPONSE")) throw error;
        return api.runs.events(orgId, runId, pageParam, SMALL_PAGE_SIZE);
      }
    },
    initialPageParam: "0",
    getNextPageParam: (last) => nextCursor(last),
    enabled: Boolean(orgId && runId),
    retry: false,
  });
}

/** 403 / 404 / 其他错误。 */
function errorKind(error: unknown): "forbidden" | "notFound" | "failed" {
  const status = (error as { status?: number } | null)?.status;
  if (status === 403) return "forbidden";
  if (status === 404) return "notFound";
  return "failed";
}

export function RunTranscriptDialog({
  orgId,
  runId,
  summary,
  onClose,
}: {
  orgId: string;
  runId: string;
  summary?: RunTranscriptSummary;
  onClose: () => void;
}) {
  const t = useT();
  const { formatDate } = useI18n();
  const api = useApi();
  const copy = useCopy();
  const [expandSignal, setExpandSignal] = useState(0);
  const run = useRun(orgId, runId);
  const pages = useRunEventPages(orgId, runId);
  const terminal = isTerminalRunState(run.data?.state);
  const result = useRunResult(orgId, runId, terminal);
  const artifacts = useRunArtifacts(orgId, runId, terminal);
  const events = useMemo(
    () => pages.data?.pages.flatMap((page) => page.events) ?? [],
    [pages.data],
  );
  const turn = useMemo(
    () => foldRunEvents(events, { terminal }),
    [events, terminal],
  );

  const baseTitle = summary
    ? summary.conversation_title || t("agents.runs.untitled")
    : t("agents.runTranscript.fallbackTitle");
  const title = summary?.task_display_id
    ? `${summary.task_display_id} ${baseTitle}`
    : baseTitle;
  const state: RunState | undefined = run.data?.state;
  const startedAt = run.data
    ? (run.data.started_at ?? run.data.created_at)
    : null;
  const meta = [
    state ? t(`agents.runs.states.${state}`) : null,
    startedAt
      ? formatDate(startedAt, { dateStyle: "medium", timeStyle: "short" })
      : null,
  ].filter((part): part is string => Boolean(part));

  const error = run.error ?? pages.error;
  const kind = error ? errorKind(error) : null;
  const loading =
    !error &&
    (run.isPending || pages.isPending || (terminal && result.isPending));
  const finalText = turn.finalText ?? (result.data?.output || null);
  const errorDetail =
    turn.error?.detail || turn.error?.text || result.data?.summary || null;
  const empty =
    !loading &&
    !error &&
    turn.items.length === 0 &&
    !finalText &&
    !turn.error &&
    !pages.hasNextPage;
  const ready = !loading && !error && !empty;

  const copyAll = () =>
    void copy(
      turnTranscript(turn, t, {
        header: [title, meta.join(" · ")].filter(Boolean),
        errorDetail: terminal ? errorDetail : null,
        finalText: terminal ? finalText : null,
        truncated: pages.hasNextPage,
      }),
      t("agents.runTranscript.copied"),
    );

  return (
    <Dialog
      open
      onClose={onClose}
      width={760}
      title={title}
      description={meta.length ? meta.join(" · ") : undefined}
      footer={
        <>
          <Button
            variant="outline"
            disabled={!ready}
            onClick={() => setExpandSignal((n) => n + 1)}
          >
            {t("agents.runTranscript.expandAll")}
          </Button>
          <Button variant="outline" disabled={!ready} onClick={copyAll}>
            {t("agents.runTranscript.copyAll")}
          </Button>
          <Button onClick={onClose}>{t("common.close")}</Button>
        </>
      }
    >
      <div className="min-h-[120px]">
        {loading ? (
          <div
            role="status"
            aria-busy="true"
            aria-label={t("agents.runTranscript.loading")}
            className="flex flex-col gap-2"
          >
            <Skeleton className="h-5 w-40" />
            <Skeleton className="h-5 w-64" />
            <Skeleton className="h-5 w-52" />
          </div>
        ) : kind ? (
          <div
            role="alert"
            className="flex flex-col items-center gap-2 py-10 text-center"
          >
            <p className="text-body text-fg-muted">
              {t(
                kind === "forbidden"
                  ? "agents.runTranscript.forbidden"
                  : kind === "notFound"
                    ? "agents.runTranscript.notFound"
                    : "agents.runTranscript.loadFailed",
              )}
            </p>
            {kind === "failed" && (
              <Button
                variant="outline"
                onClick={() => {
                  if (run.isError) void run.refetch();
                  if (pages.isError) void pages.refetch();
                }}
              >
                {t("common.retry")}
              </Button>
            )}
          </div>
        ) : empty ? (
          <p
            role="status"
            className="py-10 text-center text-body text-fg-muted"
          >
            {t("agents.runTranscript.empty")}
          </p>
        ) : (
          <ExpandAllContext.Provider value={expandSignal}>
            <RunTimelineView
              turn={turn}
              run={run.data}
              result={result.data}
              artifacts={artifacts.data}
              artifactHref={(artifact) =>
                api.runs.artifactUrl(orgId, runId, artifact.id)
              }
              truncated={pages.hasNextPage}
              renderText={renderMarkdown}
              defaultExpanded
            />
            {pages.hasNextPage && (
              <Button
                variant="ghost"
                className="mx-auto mt-2 flex"
                loading={pages.isFetchingNextPage}
                onClick={() => void pages.fetchNextPage()}
              >
                {t("agents.runTranscript.loadMore")}
              </Button>
            )}
          </ExpandAllContext.Provider>
        )}
      </div>
    </Dialog>
  );
}
