import { PreviewStateKey } from "./viewState";
import { workspaceStore } from "../workspaceStorage";
import { useEffect, useMemo, useRef, useContext } from "react";
import type { ArtifactReviewAnchor } from "@openfox/api";
import { useT } from "@openfox/i18n";
import type { SelectionRect } from "./selection";
import {
  htmlPreviewDocument,
  htmlSelection,
  htmlTextSelection,
} from "./htmlDocument";

export function HtmlPreview({
  text,
  name,
  annotate = false,
  onSelect,
  located,
  onUnmapped,
}: {
  text: string;
  name: string;
  annotate?: boolean;
  onSelect?: (anchor: ArtifactReviewAnchor, rect: SelectionRect) => void;
  located?: ArtifactReviewAnchor | null;
  onUnmapped?: () => void;
}) {
  const t = useT();
  const frame = useRef<HTMLIFrameElement>(null);
  const scope = useContext(PreviewStateKey),
    store = workspaceStore(),
    key = `view:${scope}`;
  const interactive = Boolean(onSelect || scope);
  const preview = useMemo(() => {
    try {
      const session = interactive ? crypto.randomUUID() : "";
      return { ...htmlPreviewDocument(text, session), session };
    } catch {
      return null;
    }
  }, [text, interactive]);
  useEffect(() => {
    if (!preview || !annotate) return;
    const receive = (event: MessageEvent) => {
      const anchor = htmlSelection(
        event,
        frame.current?.contentWindow ?? null,
        preview.session,
        preview.targets,
      );
      const selected =
        anchor ??
        htmlTextSelection(
          event,
          frame.current?.contentWindow ?? null,
          preview.session,
          preview.textTargets,
          text,
        );
      if (!selected || !frame.current) return;
      const rect = event.data?.rect;
      if (
        !rect ||
        ![rect.left, rect.top, rect.width, rect.height].every(
          Number.isFinite,
        ) ||
        rect.width < 0 ||
        rect.height < 0
      )
        return;
      const box = frame.current.getBoundingClientRect();
      onSelect?.(selected, {
        left: box.left + Math.max(0, Math.min(rect.left, box.width)),
        top: box.top + Math.max(0, Math.min(rect.top, box.height)),
        width: Math.min(rect.width, box.width),
        height: Math.min(rect.height, box.height),
      });
    };
    window.addEventListener("message", receive);
    return () => window.removeEventListener("message", receive);
  }, [preview, onSelect, annotate, text]);
  useEffect(() => {
    if (!scope || !preview) return;
    const receive = (event: MessageEvent) => {
      const data = event.data;
      if (
        event.source !== frame.current?.contentWindow ||
        data?.session !== preview.session ||
        data.type !== "openfox-html-scroll"
      )
        return;
      if (
        ![data.x, data.y].every(
          (n) => Number.isFinite(n) && n >= 0 && n <= 10_000_000,
        )
      )
        return;
      store.write(key, {
        ...store.read<object>(key),
        htmlScroll: { x: data.x, y: data.y },
      });
    };
    window.addEventListener("message", receive);
    return () => window.removeEventListener("message", receive);
  }, [preview, scope, store, key]);
  const syncMode = () => {
    frame.current?.contentWindow?.postMessage(
      {
        type: "openfox-html-mode",
        session: preview?.session,
        enabled: annotate,
      },
      "*",
    );
  };
  useEffect(syncMode, [preview, annotate]);
  const locate = () => {
    if (!annotate || !located) return;
    if (!preview) {
      onUnmapped?.();
      return;
    }
    const target = preview.targets.find(
      ({ anchor }) =>
        anchor.start === located.start &&
        anchor.end === located.end &&
        anchor.quote === located.quote,
    );
    if (target)
      frame.current?.contentWindow?.postMessage(
        {
          type: "openfox-html-locate",
          session: preview.session,
          id: target.id,
        },
        "*",
      );
    else onUnmapped?.();
  };
  useEffect(locate, [preview, located, annotate, onUnmapped]);
  if (!preview)
    return (
      <p role="status" className="p-3 text-body-sm">
        {t("artifacts.review.htmlUnavailable")}
      </p>
    );
  return (
    <div className={"flex h-full min-h-0 flex-col"}>
      <iframe
        ref={frame}
        title={t("artifacts.preview.htmlTitle", { name })}
        sandbox={interactive ? "allow-scripts" : ""}
        referrerPolicy="no-referrer"
        srcDoc={preview.document}
        onLoad={() => {
          syncMode();
          const position = scope
            ? store.read<{ htmlScroll?: { x: number; y: number } }>(key)
                ?.htmlScroll
            : undefined;
          if (position)
            frame.current?.contentWindow?.postMessage(
              {
                type: "openfox-html-restore-scroll",
                session: preview.session,
                ...position,
              },
              "*",
            );
          locate();
        }}
        className="block h-full min-h-64 w-full flex-1 border-0 bg-white"
      />
    </div>
  );
}
