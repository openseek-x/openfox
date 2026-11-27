import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  isApiError,
  type WorkspaceProposal,
  type WorkspaceProposalDecision,
  type ProposalSide,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, IconButton } from "@openfox/ui";
import { useApi, keys } from "../../context";
import { LoadFailed, TextView } from "../../preview/FileViews";
import { HtmlPreview } from "../artifacts/HtmlPreview";
import { OfficeReview } from "../artifacts/OfficeReview";
import { VisualReview } from "../artifacts/VisualReview";
import { PreviewStateKey } from "../artifacts/viewState";
import { artifactPreviewKind } from "../artifacts/model";
import { workspaceStore } from "../workspaceStorage";
import { proposalKey, proposalsKey } from "./WorkspaceProposals";
import { changedLines } from "./diff";
import { flushCanvasSource } from "../canvas/documentDraft";

export function ProposalPreview({
  orgId,
  conversationId,
  id,
}: {
  orgId: string;
  conversationId: string;
  id: string;
}) {
  const api = useApi();
  const query = useQuery({
    queryKey: proposalKey(orgId, conversationId, id),
    queryFn: ({ signal }) =>
      api.workspaceProposals.get(orgId, conversationId, id, signal),
    refetchInterval: 5000,
    retry: false,
  });
  const t = useT();
  if (query.isError)
    return <LoadFailed onReload={() => void query.refetch()} />;
  if (!query.data)
    return (
      <p role="status" className="p-4">
        {t("artifacts.review.loading")}
      </p>
    );
  return (
    <Review
      orgId={orgId}
      conversationId={conversationId}
      proposal={query.data}
    />
  );
}
function Review({
  orgId,
  conversationId,
  proposal: p,
}: {
  orgId: string;
  conversationId: string;
  proposal: WorkspaceProposal;
}) {
  const api = useApi(),
    client = useQueryClient(),
    t = useT(),
    store = workspaceStore();
  const pendingKey = `proposal-decision:${conversationId}:${p.id}`;
  const [pending, setPending] = useState(() =>
    store.read<WorkspaceProposalDecision>(pendingKey),
  );
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [side, setSide] = useState<ProposalSide | "diff">("candidate");
  const kind = artifactPreviewKind(p.base);
  const textDiff = !["office", "pdf", "image", "video", "binary"].includes(
    kind,
  );
  useEffect(() => {
    if (p.status !== "PENDING" && pending) {
      store.write(pendingKey, undefined);
      setPending(undefined);
      if (p.decision_id !== pending.operation_id)
        setError(t("artifacts.proposals.decidedElsewhere"));
      void client.invalidateQueries({
        queryKey: keys.conversationArtifacts(orgId, conversationId),
      });
    }
  }, [
    p.status,
    p.decision_id,
    pending,
    store,
    pendingKey,
    client,
    orgId,
    conversationId,
    t,
  ]);
  useEffect(() => {
    if (p.status === "ACCEPTED" && p.source.space_id)
      void client.invalidateQueries({
        queryKey: keys.docs(orgId, p.source.space_id),
      });
  }, [client, orgId, p.status, p.source.space_id]);
  const decide = async (action: WorkspaceProposalDecision["action"]) => {
    if (busy) return;
    setBusy(true);
    setError("");
    const request = pending ?? {
      operation_id: crypto.randomUUID(),
      version: p.version,
      action,
    };
    try {
      if (
        !pending &&
        request.action === "accept" &&
        p.source.node_id &&
        !(await flushCanvasSource(p.source.space_id!, p.source.node_id))
      ) {
        setError(t("artifacts.canvas.finishEditing"));
        return;
      }
      store.write(pendingKey, request);
      setPending(request);
      await store.flush();
      const result = await api.workspaceProposals.decide(
        orgId,
        conversationId,
        p.id,
        request,
      );
      store.write(pendingKey, undefined);
      setPending(undefined);
      client.setQueryData(proposalKey(orgId, conversationId, p.id), result);
      await Promise.all([
        client.invalidateQueries({
          queryKey: proposalsKey(orgId, conversationId),
          exact: true,
        }),
        client.invalidateQueries({
          queryKey: keys.conversationArtifacts(orgId, conversationId),
        }),
      ]);
    } catch (reason) {
      const known =
        isApiError(reason) &&
        reason.status >= 400 &&
        reason.status < 500 &&
        reason.status !== 408 &&
        reason.status !== 429;
      if (known) {
        store.write(pendingKey, undefined);
        setPending(undefined);
        void client.invalidateQueries({
          queryKey: proposalKey(orgId, conversationId, p.id),
          exact: true,
        });
      }
      setError(
        t(
          isApiError(reason) && reason.status === 409
            ? "artifacts.proposals.conflict"
            : "artifacts.proposals.decisionFailed",
        ),
      );
    } finally {
      setBusy(false);
    }
  };
  const download = async () => {
    setError("");
    try {
      const blob = await api.workspaceProposals.content(
        orgId,
        conversationId,
        p.id,
        side === "base" ? "base" : "candidate",
      );
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = p.base.name;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch {
      setError(t("artifacts.proposals.loadFailed"));
    }
  };
  return (
    <section
      aria-label={t("artifacts.proposals.review")}
      className="flex min-h-0 flex-1 flex-col"
    >
      <div className="flex min-h-[52px] shrink-0 flex-wrap items-center gap-2 border-b border-line-default px-3 py-2">
        <div
          role="group"
          aria-label={t("artifacts.proposals.view")}
          className="flex rounded-full border border-line-default bg-surface-muted p-0.5"
        >
          {(["base", "candidate", ...(textDiff ? ["diff"] : [])] as const).map(
            (value) => (
              <button
                key={value}
                type="button"
                aria-pressed={side === value}
                className={`rounded-full px-3 py-1 text-caption ${side === value ? "bg-surface-active text-fg-default" : "text-fg-muted"}`}
                onClick={() => setSide(value as typeof side)}
              >
                {t(
                  `artifacts.proposals.${value as "base" | "candidate" | "diff"}`,
                )}
              </button>
            ),
          )}
        </div>
        <span
          className="min-w-0 flex-1 truncate text-caption text-fg-muted"
          title={p.base.name}
        >
          {p.base.name}
        </span>
        <IconButton
          label={t("artifacts.proposals.download")}
          size="28"
          icon={<Icon name="download" size={16} />}
          onClick={() => void download()}
        />
        {p.status === "PENDING" ? (
          pending ? (
            <Button
              size="28"
              variant="outline"
              disabled={busy}
              onClick={() => void decide(pending.action)}
            >
              {t("artifacts.proposals.retryDecision")}
            </Button>
          ) : (
            <>
              <Button
                size="28"
                variant="ghost"
                disabled={busy}
                onClick={() => void decide("reject")}
              >
                {t("artifacts.proposals.reject")}
              </Button>
              <Button
                size="28"
                disabled={busy}
                onClick={() => void decide("accept")}
              >
                {t("artifacts.proposals.accept")}
              </Button>
            </>
          )
        ) : (
          <span role="status" className="text-caption text-fg-muted">
            {t(`artifacts.proposals.${p.status}`)}
          </span>
        )}
      </div>
      <div className="shrink-0 border-b border-line-default px-3 py-2 text-caption text-fg-muted">
        <p>
          {t(
            p.status === "PENDING"
              ? "artifacts.proposals.pendingHint"
              : p.status === "ACCEPTED"
                ? "artifacts.proposals.acceptedHint"
                : "artifacts.proposals.rejectedHint",
          )}
        </p>
        {p.summary && (
          <p className="mt-1 max-h-16 overflow-auto whitespace-pre-wrap">
            {p.summary}
          </p>
        )}
        {error && (
          <p role="alert" className="mt-1 text-danger">
            {error}
          </p>
        )}
      </div>
      <PreviewStateKey.Provider
        key={`${p.id}:${side}`}
        value={`${conversationId}:proposal:${p.id}:${side}`}
      >
        <Content
          orgId={orgId}
          conversationId={conversationId}
          proposal={p}
          side={side}
        />
      </PreviewStateKey.Provider>
    </section>
  );
}
function Content({
  orgId,
  conversationId,
  proposal: p,
  side,
}: {
  orgId: string;
  conversationId: string;
  proposal: WorkspaceProposal;
  side: ProposalSide | "diff";
}) {
  const api = useApi(),
    t = useT(),
    kind = artifactPreviewKind(p.base);
  const query = useQuery({
    queryKey: [...proposalKey(orgId, conversationId, p.id), "content", side],
    staleTime: Infinity,
    retry: false,
    queryFn: async ({ signal }) => {
      const load = (s: ProposalSide) =>
        api.workspaceProposals.content(orgId, conversationId, p.id, s, signal);
      if (side === "diff") {
        const [a, b] = await Promise.all([load("base"), load("candidate")]);
        return { diff: changedLines(await a.text(), await b.text()) };
      }
      if (kind === "office") {
        const office = await api.workspaceProposals.office(
          orgId,
          conversationId,
          p.id,
          side,
          signal,
        );
        if (office.sha256 !== (side === "base" ? p.base.sha256 : p.sha256))
          throw new Error("Candidate content mismatch");
        const blob =
          office.kind === "xlsx"
            ? await api.workspaceProposals.officePreview(
                orgId,
                conversationId,
                p.id,
                side,
                signal,
                office.spreadsheet ? "xlsx" : "pdf",
              )
            : await load(side);
        return { office, blob };
      }
      const blob = await load(side);
      return {
        blob,
        text: ["pdf", "image"].includes(kind) ? "" : await blob.text(),
      };
    },
  });
  const blob = query.data?.blob;
  const url = useMemo(() => (blob ? URL.createObjectURL(blob) : ""), [blob]);
  useEffect(
    () => () => {
      if (url) URL.revokeObjectURL(url);
    },
    [url],
  );
  if (query.isError)
    return <LoadFailed onReload={() => void query.refetch()} />;
  if (!query.data)
    return (
      <p role="status" className="p-4">
        {t("artifacts.review.loading")}
      </p>
    );
  const data = query.data;
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-auto">
      {data.diff ? (
        <div className="p-4">
          <p className="mb-3 text-caption text-fg-muted">
            {t("artifacts.proposals.diffHint")}
          </p>
          {data.diff.truncated && (
            <p role="status" className="mb-2 text-caption text-fg-muted">
              {t("artifacts.proposals.diffLimited")}
            </p>
          )}
          {!data.diff.lines.length && (
            <p>{t("artifacts.proposals.identical")}</p>
          )}
          <pre className="text-body-sm whitespace-pre-wrap break-all font-mono">
            {data.diff.lines.map((line, i) => (
              <span
                key={i}
                className={`block ${line.kind === "remove" ? "bg-danger-soft text-danger" : line.kind === "add" ? "bg-success-soft text-success" : "text-fg-muted"}`}
              >
                {line.kind === "remove"
                  ? "− "
                  : line.kind === "add"
                    ? "+ "
                    : "  "}
                {line.text || " "}
              </span>
            ))}
          </pre>
        </div>
      ) : data.office && blob ? (
        <OfficeReview
          view={data.office}
          blob={blob}
          name={p.base.name}
          loadFallback={(signal) =>
            api.workspaceProposals.officePreview(
              orgId,
              conversationId,
              p.id,
              side as ProposalSide,
              signal,
            )
          }
        />
      ) : blob && (kind === "pdf" || kind === "image") ? (
        <VisualReview blob={blob} name={p.base.name} pdf={kind === "pdf"} />
      ) : kind === "html" ? (
        <HtmlPreview text={data.text ?? ""} name={p.base.name} />
      ) : (
        <TextView
          path={p.base.name}
          content={data.text ?? ""}
          large={(data.text?.length ?? 0) > 256 * 1024}
          kind={
            kind === "markdown"
              ? "markdown"
              : kind === "table"
                ? "table"
                : "code"
          }
          source={false}
          downloadUrl={url}
        />
      )}
    </div>
  );
}
