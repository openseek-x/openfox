import { lazy, Suspense, useRef, useState } from "react";
import type { Editor } from "@tiptap/core";
import type { DocNodeDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, IconButton, Popover } from "@openfox/ui";
import { Link } from "react-router-dom";
import { useDocNode } from "../../docs/queries";
import { SaveStatus } from "../../docs/DocEditor";
import { DocHistoryDialog } from "../../docs/DocHistoryDialog";
import {
  canvasMarkdownAnchor,
  canvasSelection,
  selectCanvasRange,
} from "../../docs/editor/annotationSelection";
import { AnnotationInput } from "../artifacts/AnnotationInput";
import {
  addFileComment,
  readAnnotationBody,
  saveAnnotationBody,
} from "../drafts";
import { useCanvasDocument } from "./documentDraft";
import { useConversation } from "../queries";
import { useMe } from "../../agents/queries";
import { utf8Length } from "../artifacts/anchors";
const BlockBody = lazy(() => import("../../docs/BlockBody"));

export function NativeCanvas({
  orgId,
  conversationId,
  spaceId,
  nodeId,
}: {
  orgId: string;
  conversationId: string;
  spaceId: string;
  nodeId: string;
}) {
  const query = useDocNode(orgId, spaceId, nodeId),
    t = useT();
  const chat = useConversation(orgId, conversationId);
  const me = useMe();
  if (query.isError)
    return (
      <div role="alert" className="p-4">
        <p>{t("docs.loadFailed")}</p>
        <button onClick={() => void query.refetch()}>
          {t("common.retry")}
        </button>
      </div>
    );
  if (!query.data)
    return (
      <p className="p-4" role="status">
        {t("artifacts.review.loading")}
      </p>
    );
  if (query.data.kind !== "doc")
    return (
      <Link className="p-4" to={`/o/${orgId}/spaces/${spaceId}/docs/${nodeId}`}>
        {t("artifacts.canvas.openLibrary")}
      </Link>
    );
  const canAnnotate =
    chat.data?.space_id === spaceId &&
    chat.data?.visibility === "PRIVATE" &&
    chat.data?.owner_user_id === me.data?.id &&
    !chat.data?.task &&
    !chat.data?.crew_id &&
    chat.data?.lifecycle === "OPEN" &&
    chat.data?.effective?.runtime === "codex";
  return (
    <Canvas
      key={nodeId}
      orgId={orgId}
      conversationId={conversationId}
      spaceId={spaceId}
      node={query.data}
      canAnnotate={Boolean(canAnnotate)}
    />
  );
}
function Canvas({
  orgId,
  conversationId,
  spaceId,
  node,
  canAnnotate,
}: {
  orgId: string;
  conversationId: string;
  spaceId: string;
  node: DocNodeDetail;
  canAnnotate: boolean;
}) {
  const t = useT(),
    doc = useCanvasDocument(orgId, spaceId, node);
  const editor = useRef<Editor | null>(null);
  const [annotate, setAnnotate] = useState(false),
    [history, setHistory] = useState(false);
  const [selection, setSelection] = useState<{
    left: number;
    top: number;
    width: number;
    height: number;
  } | null>(null);
  const [error, setError] = useState("");
  const [adding, setAdding] = useState(false);
  const anchor = useRef<HTMLSpanElement>(null);
  const draftKey = `canvas-comment:${conversationId}:${spaceId}:${node.id}`;
  const [comment, setComment] = useState(() => readAnnotationBody(draftKey));
  const clear = () => {
    setSelection(null);
    setError("");
    if (editor.current && !editor.current.isDestroyed)
      editor.current.view.dispatch(
        editor.current.state.tr.setMeta(canvasSelection, null),
      );
  };
  const pick = (e: Editor) => {
    if (!annotate || adding || !selectCanvasRange(e)) return;
    const range = canvasSelection.getState(e.state)!;
    const from = e.view.coordsAtPos(range.from);
    setSelection({
      left: from.left,
      top: from.top,
      width: Math.min(300, e.view.dom.clientWidth),
      height: from.bottom - from.top,
    });
    setError("");
  };
  const submit = async () => {
    const e = editor.current;
    if (adding || !e || !comment.trim()) return;
    if (utf8Length(comment.trim()) > 8192) {
      setError(t("artifacts.review.draftLimit"));
      return;
    }
    const location = canvasMarkdownAnchor(e);
    if (!location) {
      setError(t("artifacts.canvas.selectionChanged"));
      return;
    }
    // Canonical Markdown is the exact saved snapshot used by the source anchor.
    const markdown = e.getMarkdown().trimEnd();
    setAdding(true);
    setError("");
    try {
      if (markdown !== doc.body.current) doc.change(markdown);
      if (!(await doc.flush())) return;
      if (doc.base.current.body !== markdown) {
        setError(t("artifacts.canvas.selectionChanged"));
        return;
      }
      const bytes = new TextEncoder().encode(markdown);
      const digest = await crypto.subtle.digest("SHA-256", bytes);
      const sha = Array.from(new Uint8Array(digest), (x) =>
        x.toString(16).padStart(2, "0"),
      ).join("");
      if (
        !addFileComment(
          conversationId,
          {
            space_id: spaceId,
            node_id: node.id,
            node_version: doc.base.current.version,
            sha256: sha,
            name: node.title + ".md",
          },
          {
            thread_id: crypto.randomUUID(),
            anchor: location,
            body: comment.trim(),
          },
        )
      ) {
        setError(t("artifacts.review.draftLimit"));
        return;
      }
      saveAnnotationBody(draftKey, "");
      setComment("");
      clear();
    } catch {
      setError(t("artifacts.canvas.saveFailed"));
    } finally {
      setAdding(false);
    }
  };
  return (
    <section
      aria-label={t("artifacts.canvas.label")}
      className="flex min-h-0 min-w-0 flex-1 flex-col"
    >
      <div
        aria-label={t("artifacts.preview.toolbar")}
        className="flex h-[52px] shrink-0 items-center gap-2 border-b border-line-default px-3"
      >
        <IconButton
          label={t("artifacts.review.annotate")}
          icon={<Icon name="hifox-scan-eye" size={17} />}
          size="28"
          active={annotate}
          aria-pressed={annotate}
          disabled={!node.can_edit || !canAnnotate || adding}
          onClick={() => {
            clear();
            setAnnotate((v) => !v);
          }}
        />
        <span className="min-w-0 flex-1 truncate text-body-sm">
          {node.title}
        </span>
        <span role="status" className="text-caption text-fg-muted">
          {t(
            doc.busy
              ? "artifacts.canvas.saving"
              : doc.error || doc.localError
                ? "artifacts.canvas.saveFailed"
                : doc.dirty
                  ? "artifacts.canvas.unsaved"
                  : "artifacts.canvas.saved",
          )}
        </span>
        <IconButton
          label={t("artifacts.canvas.save")}
          icon={<Icon name="check" size={16} />}
          size="28"
          disabled={doc.busy || adding || !node.can_edit}
          onClick={() => void doc.flush()}
        />
        <IconButton
          label={t("docs.history.title")}
          icon={<Icon name="history" size={16} />}
          size="28"
          disabled={adding || doc.busy}
          onClick={() =>
            void doc.flush().then((ok) => {
              if (ok) {
                clear();
                setHistory(true);
              }
            })
          }
        />
      </div>
      <div className="min-h-0 flex-1 overflow-auto px-6 py-5">
        <SaveStatus error={doc.error} onMerge={() => void doc.merge()} />
        {doc.localError && (
          <p role="alert">{t("artifacts.canvas.saveFailed")}</p>
        )}
        {!node.can_edit && <p>{t("docs.editor.viewOnly")}</p>}
        <Suspense fallback={<p>{t("artifacts.review.loading")}</p>}>
          <BlockBody
            key={doc.epoch}
            orgId={orgId}
            spaceId={spaceId}
            nodeId={node.id}
            initialMarkdown={doc.body.current}
            editable={node.can_edit && !adding && !history}
            onChange={doc.change}
            onReady={(value) => {
              editor.current = value;
            }}
            annotationMode={annotate}
            onPick={pick}
            onTransaction={(e) => {
              if (selection && !canvasSelection.getState(e.state))
                setError(t("artifacts.canvas.selectionChanged"));
            }}
          />
        </Suspense>
      </div>
      {history && (
        <DocHistoryDialog
          orgId={orgId}
          spaceId={spaceId}
          node={doc.base.current}
          onClose={() => setHistory(false)}
        />
      )}
      {selection && annotate && (
        <>
          <span
            ref={anchor}
            aria-hidden
            style={{ position: "fixed", pointerEvents: "none", ...selection }}
          />
          <Popover
            open
            onOpenChange={(open) => {
              if (!open && !adding) clear();
            }}
            anchorRef={anchor}
            placement="top-start"
            offset={8}
            aria-label={t("artifacts.review.comment")}
            className="w-75 max-w-[calc(100vw-16px)] rounded-[22px] p-[7px] shadow-lg"
          >
            <div
              aria-busy={adding}
              className={adding ? "pointer-events-none opacity-60" : ""}
            >
              <AnnotationInput
                label={t("artifacts.review.comment")}
                placeholder={t("artifacts.browser.annotationPlaceholder")}
                closeLabel={t("artifacts.review.closeDraft")}
                submitLabel={t("artifacts.review.addToChat")}
                value={comment}
                onChange={(text) => {
                  setComment(text);
                  saveAnnotationBody(draftKey, text);
                }}
                onClose={clear}
                onSubmit={() => void submit()}
                error={error}
              />
            </div>
          </Popover>
        </>
      )}
    </section>
  );
}
