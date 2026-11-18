import { AppErrorBoundary } from "../ErrorBoundary";
import { useQueryClient } from "@tanstack/react-query";
import { lazy, Suspense, useEffect, useRef, useState } from "react";

import {
  isApiError,
  type DocNodeDetail,
  type ArtifactReviewFile,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Skeleton } from "@openfox/ui";

import { useApi } from "../context";
import { docPageWidths, type DocPageWidth } from "./pagePreferences";
import { DOC_TITLE_MAX } from "./model";
import {
  DocSaveConflict,
  saveDocPatch,
  putDocNode,
  useDebouncedSave,
} from "./queries";
import { NativeReviewSetup } from "./NativeReviewSetup";
import { ArtifactReview } from "../conversations/artifacts/ArtifactReview";
import { documentTabKey } from "../conversations/artifacts/params";

// 阶段 5b · 文档编辑页（Figma 3.8）：标题可以直接编辑；正文是 TipTap，以 Markdown 存取。
// TipTap 编辑器体积较大，只在打开文档时加载（懒加载，不进入主包）。

const BlockBody = lazy(() => import("./BlockBody"));

interface DocPatch {
  title?: string;
  body?: string;
}

function DocEditorContent({
  orgId,
  spaceId,
  node,
  onFlushReady,
  pageWidth = "standard",
}: {
  orgId: string;
  spaceId: string;
  node: DocNodeDetail;
  onFlushReady: (flush: () => Promise<boolean>) => void;
  pageWidth?: DocPageWidth;
}) {
  const t = useT();
  const api = useApi();
  const queryClient = useQueryClient();
  const [title, setTitle] = useState(node.title);
  const titleFocused = useRef(false);
  const versionRef = useRef(node.version);
  const savedRef = useRef(node);
  // 正文只在打开时取一次：TipTap 自己持有编辑状态，之后的刷新不再覆盖它
  const initialBody = useRef(node.body ?? "");
  // A refreshed node must never advance the version of a still-dirty editor.
  // Only this editor's successful save or an explicit review exit advances it.
  const [editorKey, setEditorKey] = useState(0);
  const [setup, setSetup] = useState(false);
  const [review, setReview] = useState<{
    conversation: string;
    file: ArtifactReviewFile;
    text: string;
    nodeVersion: number;
  } | null>(null);
  const [reviewBusy, setReviewBusy] = useState(false);
  const [reviewError, setReviewError] = useState(false);
  // 其他地方改了标题（重命名）时同步到输入框；输入框聚焦时不打断用户输入
  useEffect(() => {
    if (!titleFocused.current) setTitle(node.title);
  }, [node.title]);

  const save = useDebouncedSave<DocPatch>(async (patch) => {
    const saved = await saveDocPatch(
      () =>
        api.docNodes.update(orgId, spaceId, node.id, {
          version: versionRef.current,
          ...patch,
        }),
      () => api.docNodes.get(orgId, spaceId, node.id),
      { title: savedRef.current.title, body: savedRef.current.body, ...patch },
    );
    savedRef.current = saved;
    versionRef.current = saved.version;
    putDocNode(queryClient, orgId, spaceId, saved);
  });
  const editable = node.can_edit && !reviewBusy && !review;
  const enterReview = async (conversation: string) => {
    if (reviewBusy) return;
    setReviewBusy(true);
    setReviewError(false);
    try {
      if (!(await save.flush())) return;
      const fresh = await api.docNodes.get(orgId, spaceId, node.id);
      const text = fresh.body ?? "";
      const bytes = new TextEncoder().encode(text);
      const digest = await crypto.subtle.digest("SHA-256", bytes);
      const sha = Array.from(new Uint8Array(digest))
        .map((b) => b.toString(16).padStart(2, "0"))
        .join("");
      setReview({
        conversation,
        text,
        nodeVersion: fresh.version,
        file: {
          organization_id: orgId,
          id: node.id,
          name: fresh.title + ".md",
          mime_type: "text/markdown",
          sha256: sha,
          size_bytes: bytes.length,
          state: "AVAILABLE",
          native: true,
          pinned: false,
          version: fresh.version,
          created_at: new Date().toISOString(),
        },
      });
      setSetup(false);
    } catch {
      setReviewError(true);
    } finally {
      setReviewBusy(false);
    }
  };
  const leaveReview = async () => {
    if (reviewBusy) return;
    setReviewBusy(true);
    setReviewError(false);
    try {
      const fresh = await api.docNodes.get(orgId, spaceId, node.id);
      versionRef.current = fresh.version;
      initialBody.current = fresh.body ?? "";
      setTitle(fresh.title);
      putDocNode(queryClient, orgId, spaceId, fresh);
      setEditorKey((k) => k + 1);
      setReview(null);
    } catch {
      setReviewError(true);
    } finally {
      setReviewBusy(false);
    }
  };
  useEffect(() => {
    onFlushReady(save.flush);
  }, [onFlushReady, save.flush]);

  return (
    // 左侧留出 64px 槽位给块手柄（块手柄定位在块左侧 52px，宽约 24px），标题与正文共用同一左缘
    <div
      data-doc-page-width={pageWidth}
      style={{ maxWidth: docPageWidths[pageWidth] }}
      className="mx-auto flex min-w-0 w-full flex-col gap-4 py-6 pr-6 pl-16"
    >
      <input
        aria-label={t("docs.editor.titlePlaceholder")}
        value={title}
        readOnly={!editable}
        maxLength={DOC_TITLE_MAX}
        placeholder={t("docs.editor.titlePlaceholder")}
        onFocus={() => {
          titleFocused.current = true;
        }}
        onBlur={() => {
          titleFocused.current = false;
        }}
        onChange={(event) => {
          setTitle(event.target.value);
          if (editable) save.schedule({ title: event.target.value.trim() });
        }}
        className="w-full bg-transparent text-[24px] leading-8 font-bold outline-none placeholder:text-fg-muted read-only:cursor-default"
      />
      {node.can_edit && !review && (
        <Button
          variant="ghost"
          size="28"
          disabled={reviewBusy}
          onClick={() => setSetup((v) => !v)}
        >
          {t("artifacts.review.nativeEnter")}
        </Button>
      )}
      {setup && !review && (
        <NativeReviewSetup
          orgId={orgId}
          spaceId={spaceId}
          busy={reviewBusy}
          onStart={(id) => void enterReview(id)}
        />
      )}
      {reviewError && <p role="alert">{t("artifacts.review.nativeFailed")}</p>}
      {review ? (
        <>
          <a
            className="text-caption text-accent"
            href={`/o/${orgId}/chat/${review.conversation}?artifact=${encodeURIComponent(documentTabKey(spaceId, node.id))}`}
          >
            {t("artifacts.canvas.open")}
          </a>
          <Button
            variant="ghost"
            size="28"
            disabled={reviewBusy}
            onClick={() => void leaveReview()}
          >
            {t("artifacts.review.nativeClose")}
          </Button>
          <ArtifactReview
            annotate
            key={review.file.id}
            orgId={orgId}
            conversationId={review.conversation}
            artifact={review.file}
            nativeSource={{
              spaceId,
              nodeId: node.id,
              nodeVersion: review.nodeVersion,
              text: review.text,
            }}
          />
        </>
      ) : (
        <Suspense fallback={<BodySkeleton />}>
          <BlockBody
            key={editorKey}
            orgId={orgId}
            spaceId={spaceId}
            nodeId={node.id}
            initialMarkdown={initialBody.current}
            editable={editable}
            onChange={(markdown) => {
              if (editable) save.schedule({ body: markdown });
            }}
          />
        </Suspense>
      )}
      <SaveStatus
        error={save.error}
        onMerge={() => {
          if (!(save.error instanceof DocSaveConflict)) return;
          versionRef.current = save.error.remote.version;
          savedRef.current = save.error.remote;
          void save.flush();
        }}
      />
    </div>
  );
}

function BodySkeleton() {
  return (
    <div aria-busy="true" className="flex flex-col gap-2">
      <Skeleton className="h-4 w-full" />
      <Skeleton className="h-4 w-5/6" />
    </div>
  );
}

/** 保存状态：冲突时展示远端内容，保留编辑器中的本地内容供手动合并。 */
export function SaveStatus({
  error,
  onMerge,
}: {
  error: unknown;
  onMerge?: () => void;
}) {
  const t = useT();
  if (!error) return null;
  const conflict = isApiError(error, "VERSION_CONFLICT");
  return (
    <div
      role="alert"
      className="flex items-center gap-2 text-body-sm text-danger"
    >
      <span>
        {conflict ? t("docs.editor.conflict") : t("docs.editor.saveFailed")}
      </span>
      {error instanceof DocSaveConflict && (
        <div className="flex min-w-0 flex-col gap-2">
          <label>
            {t("docs.editor.remoteTitle")}
            <input readOnly value={error.remote.title} />
          </label>
          <label>
            {t("docs.editor.remoteBody")}
            <textarea readOnly value={error.remote.body ?? ""} rows={6} />
          </label>
          <Button variant="ghost" size="28" onClick={onMerge}>
            {t("docs.editor.mergeSave")}
          </Button>
        </div>
      )}
    </div>
  );
}

export function DocEditor(props: Parameters<typeof DocEditorContent>[0]) {
  return (
    <AppErrorBoundary key={props.node.id}>
      <DocEditorContent {...props} />
    </AppErrorBoundary>
  );
}
