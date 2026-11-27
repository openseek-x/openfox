import { takeFileLocation } from "./fileLocation";
import {
  capturePreviewSelection,
  previewCapabilities,
  previewSourceKey,
  type PreviewSelection,
  type PreviewSource,
} from "./previewProtocol";
import { useViewState, usePreviewScroll } from "./viewState";
import { useEffect, useMemo, useRef, useState } from "react";
import type { ArtifactReviewAnchor, ArtifactReviewFile } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, cn } from "@openfox/ui";
import { Link } from "react-router-dom";
import { useApi } from "../../context";
import { MarkdownView, TextView } from "../../preview/FileViews";
import {
  addFileComment,
  removeFileComment,
  useDraftFileContexts,
} from "../drafts";
import { reviewCSV, utf8Length } from "./anchors";
import { artifactPreviewKind, ARTIFACT_LARGE_TEXT_BYTES } from "./model";
import { useArtifactContent } from "./queries";
import { OfficeReview } from "./OfficeReview";
import { VisualReview } from "./VisualReview";
import { HtmlPreview } from "./HtmlPreview";
import { ChatMarkdown } from "../ChatMarkdown";
import { SelectionComment, type CommentSelection } from "./SelectionComment";
import { domTextAnchor, type SelectionRect } from "./selection";
const button =
  "rounded-md border border-line-default px-2 py-1 text-body-sm hover:bg-surface-hover disabled:opacity-50";
export type NativeCommentSource = {
  spaceId: string;
  nodeId: string;
  nodeVersion: number;
  text: string;
};
export function ArtifactReview({
  orgId,
  conversationId,
  artifact,
  nativeSource,
  embedded = false,
  sourceMode = false,
  annotate = false,
}: {
  orgId: string;
  conversationId: string;
  artifact: ArtifactReviewFile;
  nativeSource?: NativeCommentSource;
  embedded?: boolean;
  sourceMode?: boolean;
  annotate?: boolean;
}) {
  const t = useT();
  const api = useApi();
  const load = useArtifactContent(orgId, artifact, !nativeSource);
  const nativeText = nativeSource?.text;
  const data = useMemo(
    () =>
      nativeText !== undefined
        ? { text: nativeText, blob: null, office: undefined }
        : load.data,
    [nativeText, load.data],
  );
  const hasContent = Boolean(data);
  const text = data?.text ?? "";
  const contexts = useDraftFileContexts(conversationId);
  const context = contexts.find(
    (c) =>
      c.sha256 === artifact.sha256 &&
      (nativeSource
        ? c.node_id === nativeSource.nodeId &&
          c.node_version === nativeSource.nodeVersion
        : c.artifact_id === artifact.id),
  );
  const previewSource: PreviewSource = nativeSource
    ? {
        kind: "document",
        spaceId: nativeSource.spaceId,
        nodeId: nativeSource.nodeId,
        nodeVersion: nativeSource.nodeVersion,
        sha256: artifact.sha256,
      }
    : { kind: "artifact", artifactId: artifact.id, sha256: artifact.sha256 };
  const sourceKey = previewSourceKey(previewSource);
  const capabilities = previewCapabilities(artifact);
  const comments = context?.comments ?? [];
  const annotations = comments.map((c) => ({
    id: c.thread_id,
    body: c.body,
    anchor: c.anchor,
  }));
  const [selection, setSelection] = useState<
    (CommentSelection & PreviewSelection) | null
  >(null);
  const [error, setError] = useState("");
  const [source, setSource] = useState(sourceMode);
  const [located, setLocated] = useState<ArtifactReviewAnchor | null>(null);
  useEffect(() => {
    if (!annotate) {
      setSelection(null);
      setError("");
      setLocated(null);
    }
  }, [annotate]);
  useEffect(() => setSource(sourceMode), [sourceMode]);
  useEffect(() => {
    if (!hasContent) return;
    const locate = () => {
      const a = takeFileLocation(conversationId, artifact.id, artifact.sha256);
      if (!a) return;
      if (a.kind === "text") setSource(true);
      setLocated(a);
    };
    locate();
    window.addEventListener("openfox:file-locate", locate);
    return () => window.removeEventListener("openfox:file-locate", locate);
  }, [conversationId, artifact.id, artifact.sha256, hasContent]);
  const [page, setPage] = useViewState("csv.pageIndex", 0);
  const previewRef = useRef<HTMLDivElement>(null);
  const cellsRef = useRef<HTMLDivElement>(null);
  usePreviewScroll(cellsRef, Boolean(data), "cells");
  const downloadUrl = artifact.run_id
    ? api.runs.artifactUrl(orgId, artifact.run_id, artifact.id)
    : "";
  useEffect(() => {
    if (!located || !data) return;
    if (located.kind === "cell") {
      const targetPage = Math.floor((located.row ?? 0) / 50);
      if (targetPage !== page) {
        setPage(targetPage);
        return;
      }
      cellsRef.current
        ?.querySelector<HTMLButtonElement>(
          `[data-cell="${located.row ?? 0}:${located.column ?? 0}"]`,
        )
        ?.focus();
      setLocated(null);
    } else if (located.kind === "text" && source && previewRef.current) {
      const code = previewRef.current.querySelector("pre > code");
      if (!code) return;
      const bytes = new TextEncoder().encode(text);
      const offset = (n: number) =>
        new TextDecoder("utf-8", { ignoreBOM: true }).decode(bytes.slice(0, n))
          .length;
      const start = offset(located.start),
        end = offset(located.end);
      const walker = document.createTreeWalker(code, NodeFilter.SHOW_TEXT);
      const range = document.createRange();
      let at = 0,
        node: Node | null,
        begun = false;
      while ((node = walker.nextNode())) {
        const next = at + (node.textContent?.length ?? 0);
        if (!begun && start >= at && start < next) {
          range.setStart(node, start - at);
          begun = true;
        }
        if (begun && end <= next) {
          range.setEnd(node, end - at);
          previewRef.current.focus({ preventScroll: true });
          window.getSelection()?.removeAllRanges();
          window.getSelection()?.addRange(range);
          node.parentElement?.scrollIntoView?.({ block: "nearest" });
          break;
        }
        at = next;
      }
      setLocated(null);
    }
  }, [located, text, source, page, setPage, data]);
  const csv = useMemo(() => {
    if (artifactPreviewKind(artifact) !== "table" || !data) return null;
    try {
      return reviewCSV(text);
    } catch {
      return null;
    }
  }, [artifact, text, data]);
  const choose = (anchor: ArtifactReviewAnchor, rect: SelectionRect) => {
    if (!annotate || !capabilities.selection.includes(anchor.kind)) return;
    if (utf8Length(anchor.quote) > 8192) {
      setError(t("artifacts.review.selectionLimit"));
      return;
    }
    setSelection({
      ...capturePreviewSelection(previewSource, anchor),
      rect: {
        left: rect.left,
        top: rect.top,
        width: rect.width,
        height: rect.height,
      },
    });
    setError("");
  };
  const selectText = () => {
    if (!annotate) return;
    const selected = window.getSelection();
    const root = previewRef.current;
    if (!root || !selected || selected.isCollapsed || selected.rangeCount !== 1)
      return;
    const range = selected.getRangeAt(0);
    if (!root.contains(range.commonAncestorContainer)) return;
    const anchor = domTextAnchor(root, text, range);
    if (anchor) choose(anchor, range.getBoundingClientRect());
    else setError(t("artifacts.review.selectionUnmapped"));
  };
  const kind = artifactPreviewKind(artifact);
  const large = artifact.size_bytes > ARTIFACT_LARGE_TEXT_BYTES;
  return (
    <div
      className={
        embedded ? "flex h-full min-h-0 flex-col" : "flex flex-col gap-3 p-3"
      }
    >
      {!embedded && (
        <p className="text-body-sm text-fg-muted">
          {t("artifacts.review.chatHint")}
        </p>
      )}
      {error && <p role="alert">{error}</p>}
      {load.isError ? (
        <p role="alert">{t("artifacts.review.unavailable")}</p>
      ) : !data ? (
        <p>{t("artifacts.review.loading")}</p>
      ) : data.office ? (
        <OfficeReview
          key={artifact.id}
          view={data.office}
          blob={data.blob!}
          name={artifact.name}
          editable={annotate}
          onSelect={choose}
          located={located}
          annotations={annotations}
          loadFallback={(signal) =>
            api.artifactReviews.officePreview(
              orgId,
              artifact.run_id!,
              artifact.id,
              signal,
            )
          }
        />
      ) : data.blob ? (
        <VisualReview
          key={artifact.id}
          blob={data.blob}
          name={artifact.name}
          pdf={artifact.mime_type === "application/pdf"}
          editable={annotate}
          annotations={annotations}
          located={located}
          onSelect={choose}
        />
      ) : kind === "table" && !source && !large ? (
        <>
          {csv ? (
            <div className="flex min-h-0 flex-1 flex-col">
              <div
                ref={cellsRef}
                className={cn(
                  "overflow-auto",
                  embedded
                    ? "min-h-0 flex-1"
                    : "max-h-80 rounded-md border border-line-default",
                )}
              >
                <table className="w-full border-collapse text-body-sm">
                  <tbody>
                    {csv.slice(page * 50, page * 50 + 50).map((row, r) => (
                      <tr
                        key={page * 50 + r}
                        className="border-b border-line-subtle hover:bg-surface-hover/50"
                      >
                        <th
                          className="border-r border-line-subtle bg-surface-muted/60 px-2 text-caption font-normal text-fg-muted"
                          scope="row"
                        >
                          {page * 50 + r + 1}
                        </th>
                        {row.map((cell, c) => (
                          <td
                            key={c}
                            className="border-r border-line-subtle last:border-r-0"
                          >
                            <button
                              className="min-h-8 min-w-12 whitespace-pre-wrap px-2 py-1 text-left focus-visible:ring-2"
                              aria-disabled={!annotate}
                              tabIndex={annotate ? 0 : -1}
                              data-cell={`${page * 50 + r}:${c}`}
                              aria-label={t("artifacts.review.cell", {
                                row: page * 50 + r + 1,
                                column: c + 1,
                              })}
                              onClick={(e) =>
                                choose(
                                  cell.anchor,
                                  e.currentTarget.getBoundingClientRect(),
                                )
                              }
                            >
                              {cell.value || "\u00a0"}
                            </button>
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <div className="flex shrink-0 items-center justify-between gap-2 border-t border-line-subtle bg-surface-card px-3 py-1.5 text-caption text-fg-muted">
                <div className="flex items-center gap-1.5">
                  <Button
                    size="28"
                    variant="outline"
                    disabled={page === 0}
                    onClick={() => setPage((p) => p - 1)}
                  >
                    {t("artifacts.review.previous")}
                  </Button>
                  <span className="px-1.5 font-mono text-fg-secondary">
                    {page + 1} / {Math.max(1, Math.ceil(csv.length / 50))}
                  </span>
                  <Button
                    size="28"
                    variant="outline"
                    disabled={(page + 1) * 50 >= csv.length}
                    onClick={() => setPage((p) => p + 1)}
                  >
                    {t("artifacts.review.next")}
                  </Button>
                </div>
              </div>
            </div>
          ) : (
            <p role="alert">{t("artifacts.review.csvInvalid")}</p>
          )}
        </>
      ) : (
        <>
          {!embedded && (kind === "markdown" || kind === "html") && (
            <button className={button} onClick={() => setSource((v) => !v)}>
              {t(
                source
                  ? "artifacts.preview.rendered"
                  : "artifacts.preview.source",
              )}
            </button>
          )}
          {kind === "html" && !source && !large ? (
            <HtmlPreview
              text={text}
              name={artifact.name}
              annotate={annotate}
              onSelect={choose}
              located={located}
              onUnmapped={() => setSource(true)}
            />
          ) : (
            <div
              ref={previewRef}
              tabIndex={0}
              aria-label={t("artifacts.review.sourceText")}
              data-comment-plain={kind !== "markdown" || source || large}
              className="min-h-0 flex-1 outline-none"
              onMouseUp={selectText}
              onKeyUp={(e) => {
                if (
                  e.key === "Shift" ||
                  e.shiftKey ||
                  ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "a")
                )
                  selectText();
              }}
            >
              {kind === "markdown" && !source && !large ? (
                <MarkdownView
                  text={text}
                  renderBody={(body) => (
                    <div
                      data-comment-body-offset={
                        text.replace(/^\ufeff/, "").replace(/\r\n/g, "\n")
                          .length - body.length
                      }
                    >
                      <ChatMarkdown source={body} selectable />
                    </div>
                  )}
                />
              ) : (
                <TextView
                  path={artifact.name}
                  content={text}
                  large={large}
                  kind={kind === "markdown" || kind === "table" ? kind : "code"}
                  source={source}
                  downloadUrl={downloadUrl}
                />
              )}
            </div>
          )}
        </>
      )}
      {annotate &&
        selection &&
        previewSourceKey(selection.source) === sourceKey && (
          <SelectionComment
            key={JSON.stringify(selection)}
            selection={selection}
            draftKey={JSON.stringify([
              "file",
              conversationId,
              nativeSource?.nodeId ?? artifact.id,
              nativeSource?.nodeVersion ?? artifact.sha256,
              selection.anchor,
            ])}
            onClose={() => {
              setSelection(null);
              window.getSelection()?.removeAllRanges();
            }}
            onAdd={(body) =>
              previewSourceKey(selection.source) === sourceKey &&
              addFileComment(
                conversationId,
                {
                  name: artifact.name,
                  sha256: artifact.sha256,
                  ...(nativeSource
                    ? {
                        space_id: nativeSource.spaceId,
                        node_id: nativeSource.nodeId,
                        node_version: nativeSource.nodeVersion,
                      }
                    : { artifact_id: artifact.id }),
                },
                {
                  thread_id: crypto.randomUUID(),
                  anchor: selection.anchor,
                  body,
                },
              )
            }
          />
        )}
      {!embedded && (
        <>
          <h3 className="text-body-sm font-medium">
            {t("artifacts.review.pending", { count: comments.length })}
          </h3>
          {comments.map((a) => (
            <div
              key={a.thread_id}
              className="rounded-md border border-line-default p-2"
            >
              <blockquote className="max-h-20 overflow-auto whitespace-pre-wrap text-caption text-fg-muted">
                {a.anchor.quote ||
                  t("artifacts.review.regionLabel", {
                    page: a.anchor.page ?? 1,
                  })}
              </blockquote>
              <p className="whitespace-pre-wrap text-body-sm">{a.body}</p>
              <div className="mt-2 flex gap-2">
                <button
                  className={button}
                  onClick={() => {
                    setSource(artifactPreviewKind(artifact) !== "html");
                    setPage(Math.floor((a.anchor.row ?? 0) / 50));
                    setLocated({ ...a.anchor });
                  }}
                >
                  {t("artifacts.review.locate")}
                </button>
                <button
                  className={button}
                  onClick={() =>
                    removeFileComment(conversationId, context!.id, a.thread_id)
                  }
                >
                  {t("artifacts.review.removeComment")}
                </button>
              </div>
            </div>
          ))}
        </>
      )}
      {nativeSource && (
        <Link className={button} to={`/o/${orgId}/chat/${conversationId}`}>
          {t("artifacts.review.openChat")}
        </Link>
      )}
    </div>
  );
}
