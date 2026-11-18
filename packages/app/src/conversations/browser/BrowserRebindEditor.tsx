import { useState } from "react";
import { isApiError, type WorkspaceCommentSource } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useApi } from "../../context";
import type { BrowserSelection } from "../../desktop";
import type { BrowserSession } from "./useBrowserSession";
import { regionFile } from "./UnfinishedAnnotation";
import { workspaceStore } from "../workspaceStorage";
import {
  rebindKey,
  resolutionKey,
  type Rebind,
} from "../comments/resolutionState";

export function BrowserRebindEditor({
  selection,
  session,
  binding,
}: {
  selection: BrowserSelection;
  session: BrowserSession;
  binding: Rebind;
}) {
  const api = useApi(),
    t = useT(),
    store = workspaceStore();
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(false);
  const cancel = () => {
    store.write(rebindKey(session.conversationId), undefined);
    void session.command({ action: "annotation-cancel" });
  };
  const confirm = async () => {
    if (busy) return;
    setBusy(true);
    setError(false);
    const key = `rebind-request:${session.conversationId}:${binding.id}:${selection.id}`;
    let pending = store.read<{ id: string; source: WorkspaceCommentSource }>(
      key,
    );
    try {
      if (!pending) {
        const file = regionFile(selection);
        const image = file
          ? await api.attachments.uploadToConversation(
              session.orgId,
              session.conversationId,
              file,
            )
          : undefined;
        pending = {
          id: crypto.randomUUID(),
          source: {
            name: selection.title || selection.source_url,
            browser: {
              id: crypto.randomUUID(),
              source_url: selection.source_url,
              title: selection.title,
              captured_at: selection.captured_at,
              comments: [
                {
                  id: selection.id,
                  body: binding.source.browser!.comments[0]!.body,
                  anchor: selection.anchor,
                  ...(image && { screenshot_attachment_id: image.id }),
                },
              ],
            },
          },
        };
        store.write(key, pending);
        await store.flush();
      }
      const result = await api.workspaceComments.resolve(
        session.orgId,
        session.conversationId,
        binding.id,
        pending.id,
        { version: binding.version, browser: pending.source },
      );
      store.write(resolutionKey(session.conversationId, binding.id), result);
      store.write(key, undefined);
      cancel();
    } catch (e) {
      if (
        isApiError(e) &&
        e.status >= 400 &&
        e.status < 500 &&
        e.status !== 408 &&
        e.status !== 429
      )
        store.write(key, undefined);
      setError(true);
    } finally {
      setBusy(false);
    }
  };
  const rect = selection.anchor.visual.rect,
    v = selection.anchor.visual;
  return (
    <div className={selection.image ? "absolute inset-0 z-10" : "px-2 py-2"}>
      {selection.image && (
        <>
          <img
            alt=""
            src={selection.image}
            className="absolute inset-0 h-full w-full"
            onLoad={() =>
              void session.command({
                action: "annotation-ready",
                selectionId: selection.id,
              })
            }
          />
          <div
            className="pointer-events-none absolute border-2 border-blue-500 bg-blue-500/10"
            style={{
              left: `${(rect.x / v.width) * 100}%`,
              top: `${(rect.y / v.height) * 100}%`,
              width: `${(rect.width / v.width) * 100}%`,
              height: `${(rect.height / v.height) * 100}%`,
            }}
          />
        </>
      )}
      <div
        className={`${selection.image ? "absolute left-2 right-2 top-2" : ""} mx-auto flex max-w-96 flex-wrap items-center gap-2 rounded-2xl border border-line-default bg-surface-card p-2 text-caption shadow-lg`}
      >
        <span>{t("artifacts.resolution.rebindHint")}</span>
        <button type="button" disabled={busy} onClick={() => void confirm()}>
          {t("artifacts.resolution.confirmRebind")}
        </button>
        <button type="button" disabled={busy} onClick={cancel}>
          {t("common.cancel")}
        </button>
        {error && <p role="alert">{t("artifacts.resolution.failed")}</p>}
      </div>
    </div>
  );
}
