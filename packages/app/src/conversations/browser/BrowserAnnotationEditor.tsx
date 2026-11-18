import { BrowserRebindEditor } from "./BrowserRebindEditor";
import {
  useStoredValue,
  rebindKey,
  type Rebind,
} from "../comments/resolutionState";
import {
  regionFile,
  saveUnfinished,
  unfinishedKey,
  type UnfinishedAnnotation,
} from "./UnfinishedAnnotation";
import { workspaceStore } from "../workspaceStorage";
import { useId, useLayoutEffect, useRef, useState } from "react";
import { useT } from "@openfox/i18n";
import { AnnotationInput } from "../artifacts/AnnotationInput";
import type { BrowserSelection } from "../../desktop";
import { addBrowserComment } from "../drafts";
import type { BrowserSession } from "./useBrowserSession";

export function BrowserAnnotationEditor({
  selection,
  session,
}: {
  selection: BrowserSelection;
  session: BrowserSession;
}) {
  const t = useT();
  const binding = useStoredValue<Rebind>(rebindKey(session.conversationId));
  const [body, setBody] = useState(() => {
    const saved = workspaceStore().read<UnfinishedAnnotation>(
      unfinishedKey(session.conversationId, selection),
    );
    return saved?.body ?? "";
  });
  const [error, setError] = useState(false);
  const container = useRef<HTMLDivElement>(null);
  const form = useRef<HTMLFormElement>(null);
  const quoteId = useId();
  const [top, setTop] = useState(8);
  useLayoutEffect(() => {
    if (!selection.image || !container.current || !form.current) return;
    const place = () => {
      const height = container.current!.clientHeight;
      const editorHeight = form.current!.offsetHeight;
      const { rect, height: pageHeight } = selection.anchor.visual;
      const above = (rect.y / pageHeight) * height - editorHeight - 8;
      const below = ((rect.y + rect.height) / pageHeight) * height + 8;
      setTop(
        Math.max(
          8,
          Math.min(above >= 8 ? above : below, height - editorHeight - 8),
        ),
      );
    };
    const observer = new ResizeObserver(place);
    observer.observe(container.current);
    observer.observe(form.current);
    place();
    return () => observer.disconnect();
  }, [selection]);
  const cancel = () => {
    void session.command({ action: "annotation-cancel" });
  };
  const add = () => {
    if (!body.trim()) return;
    if (new TextEncoder().encode(body).length > 8192) {
      setError(true);
      return;
    }
    const screenshot = regionFile(selection);
    const ok = addBrowserComment(
      session.conversationId,
      {
        source_url: selection.source_url,
        title: selection.title,
        captured_at: selection.captured_at,
      },
      {
        id: selection.id,
        body: body.trim(),
        anchor: selection.anchor,
        ...(screenshot && { screenshot }),
      },
    );
    if (ok) {
      saveUnfinished(session.conversationId, selection, "");
      cancel();
    } else setError(true);
  };
  const rect = selection.anchor.visual.rect,
    visual = selection.anchor.visual;
  const frozen = Boolean(selection.image);
  if (binding)
    return (
      <BrowserRebindEditor
        selection={selection}
        session={session}
        binding={binding}
      />
    );
  return (
    <div
      ref={container}
      className={frozen ? "absolute inset-0 z-10" : "shrink-0 px-2 py-2"}
    >
      {selection.image && (
        <img
          src={selection.image}
          alt=""
          className="absolute inset-0 h-full w-full"
          onLoad={() =>
            void session.command({
              action: "annotation-ready",
              selectionId: selection.id,
            })
          }
          onError={() => setError(true)}
        />
      )}
      {frozen && (
        <div
          className="pointer-events-none absolute border-2 border-blue-500 bg-blue-500/10"
          style={{
            left: `${(rect.x / visual.width) * 100}%`,
            top: `${(rect.y / visual.height) * 100}%`,
            width: `${(rect.width / visual.width) * 100}%`,
            height: `${(rect.height / visual.height) * 100}%`,
          }}
        />
      )}
      <form
        ref={form}
        aria-label={t("artifacts.browser.annotationEditor")}
        aria-describedby={quoteId}
        className={`${frozen ? "absolute w-75 max-w-[calc(100%-16px)]" : "mx-auto max-w-75"} rounded-[22px] border border-line-default bg-surface-card p-[7px] text-body-sm shadow-lg focus-within:border-line-strong`}
        style={
          frozen
            ? {
                top,
                left: `clamp(8px, ${((rect.x + rect.width / 2) / visual.width) * 100}% - 150px, max(8px, 100% - 308px))`,
              }
            : undefined
        }
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            cancel();
          }
        }}
        onSubmit={(event) => {
          event.preventDefault();
          add();
        }}
      >
        <blockquote id={quoteId} className="sr-only">
          {selection.anchor.snapshot.quote || t("artifacts.browser.region")}
        </blockquote>
        <AnnotationInput
          value={body}
          onChange={(value) => {
            setBody(value);
            saveUnfinished(session.conversationId, selection, value);
            setError(false);
          }}
          onSubmit={add}
          onClose={cancel}
          label={t("artifacts.browser.annotationBody")}
          placeholder={t("artifacts.browser.annotationPlaceholder")}
          closeLabel={t("artifacts.review.closeDraft")}
          submitLabel={t(
            selection.screenshot
              ? "artifacts.browser.addImageToChat"
              : "artifacts.browser.addToChat",
          )}
          error={error ? t("artifacts.browser.annotationLimit") : undefined}
        />
      </form>
    </div>
  );
}
