import { useRef, useState, useSyncExternalStore } from "react";
import { useT } from "@openfox/i18n";
import type { BrowserSelection } from "../../desktop";
import { workspaceStore } from "../workspaceStorage";
import { addBrowserComment } from "../drafts";
import { AnnotationInput } from "../artifacts/AnnotationInput";

export type UnfinishedAnnotation = {
  selection: Omit<BrowserSelection, "image" | "screenshot">;
  body: string;
  screenshot?: File;
};
export const unfinishedKey = (
  conversationId: string,
  selection: BrowserSelection,
) =>
  `unfinished-browser:${conversationId}:${JSON.stringify(selection.anchor.live)}`;
export function regionFile(selection: BrowserSelection) {
  if (!selection.screenshot) return undefined;
  const bytes = Uint8Array.from(
    atob(selection.screenshot.split(",")[1]!),
    (c) => c.charCodeAt(0),
  );
  return new File([bytes], `browser-region-${selection.id}.png`, {
    type: "image/png",
  });
}
export function saveUnfinished(
  conversationId: string,
  selection: BrowserSelection,
  body: string,
) {
  const { image, screenshot, ...context } = selection;
  workspaceStore().write(
    unfinishedKey(conversationId, selection),
    body
      ? ({
          selection: context,
          body,
          screenshot: regionFile(selection),
        } satisfies UnfinishedAnnotation)
      : undefined,
  );
}

/** Recover the user's text and captured source, without reviving a stale live selection or browser permission. */
export function UnfinishedBrowserAnnotation({
  conversationId,
  disabled,
}: {
  conversationId: string;
  disabled: boolean;
}) {
  const store = workspaceStore();
  useSyncExternalStore(
    (listener) => {
      store.listeners.add(listener);
      return () => {
        store.listeners.delete(listener);
      };
    },
    () => store.version,
  );
  const keys = [...store.restored].filter(
    (key) =>
      key.startsWith(`unfinished-browser:${conversationId}:`) &&
      store.read(key),
  );
  return (
    <>
      {keys.map((key) => (
        <RecoveredInput
          key={key}
          storageKey={key}
          conversationId={conversationId}
          disabled={disabled}
        />
      ))}
    </>
  );
}
function RecoveredInput({
  storageKey: key,
  conversationId,
  disabled,
}: {
  storageKey: string;
  conversationId: string;
  disabled: boolean;
}) {
  const store = workspaceStore(),
    t = useT();
  const value = store.read<UnfinishedAnnotation>(key);
  const [error, setError] = useState(false);
  const details = useRef<HTMLDetailsElement>(null);
  // Newly typed input remains in its floating editor; only a recovered record needs a separate entry.
  if (!store.restored.has(key) || !value) return null;
  const clear = () => {
    store.restored.delete(key);
    store.write(key, undefined);
  };
  const add = () => {
    const { selection: s, body, screenshot } = value;
    if (!body.trim() || new TextEncoder().encode(body).length > 8192) {
      setError(true);
      return;
    }
    if (
      addBrowserComment(
        conversationId,
        {
          source_url: s.source_url,
          title: s.title,
          captured_at: s.captured_at,
        },
        {
          id: s.id,
          anchor: s.anchor,
          body: body.trim(),
          ...(screenshot && { screenshot }),
        },
      )
    )
      clear();
    else setError(true);
  };
  return (
    <details
      ref={details}
      className="mb-2 rounded-xl border border-line-default px-3 py-2 text-body-sm"
    >
      <summary className="cursor-pointer truncate">
        {t("artifacts.persistence.unfinished")} ·{" "}
        {value.selection.title || value.selection.source_url}
      </summary>
      <p className="my-2 text-caption text-fg-muted">
        {t("artifacts.persistence.capturedSource")}
      </p>
      <fieldset disabled={disabled}>
        <AnnotationInput
          value={value.body}
          onChange={(body) => {
            store.write(key, { ...value, body });
            setError(false);
          }}
          onSubmit={add}
          onClose={() => {
            if (details.current) details.current.open = false;
          }}
          label={t("artifacts.browser.annotationBody")}
          placeholder={t("artifacts.browser.annotationPlaceholder")}
          submitLabel={t("artifacts.browser.addToChat")}
          closeLabel={t("artifacts.review.closeDraft")}
          error={error ? t("artifacts.browser.annotationLimit") : undefined}
        />
        <button
          type="button"
          className="mt-2 text-caption text-fg-muted underline"
          onClick={clear}
        >
          {t("artifacts.persistence.discard")}
        </button>
      </fieldset>
    </details>
  );
}
