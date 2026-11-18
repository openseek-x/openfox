import { useState } from "react";
import {
  isApiError,
  type WorkspaceCommentThread,
  type AnchorResolution,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useApi } from "../../context";
import { workspaceStore } from "../workspaceStorage";
import { useOpenArtifactList, BROWSER_PANEL } from "../artifacts/params";
import { requestFileLocation } from "../artifacts/fileLocation";
import { requestBrowserLocation } from "../browser/BrowserReferences";
import { beginRebind, resolutionKey } from "./resolutionState";
import { documentTabKey } from "../artifacts/params";

export function ResolutionActions({
  orgId,
  conversationId,
  thread,
  resolution,
  disabled,
}: {
  orgId: string;
  conversationId: string;
  thread: WorkspaceCommentThread;
  resolution?: AnchorResolution;
  disabled: boolean;
}) {
  const api = useApi(),
    t = useT(),
    store = workspaceStore();
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(false);
  const file = resolution?.target?.file;
  const open = useOpenArtifactList(
      file?.node_id
        ? documentTabKey(file.space_id!, file.node_id)
        : file?.artifact_id,
    ),
    browser = useOpenArtifactList(BROWSER_PANEL);
  const resolve = async () => {
    if (busy) return;
    setBusy(true);
    setError(false);
    const key = `anchor-request:${conversationId}:${thread.id}`;
    const old = store.read<{ id: string; version: number }>(key);
    const request = old ?? { id: crypto.randomUUID(), version: thread.version };
    try {
      store.write(key, request);
      await store.flush();
      const result = await api.workspaceComments.resolve(
        orgId,
        conversationId,
        thread.id,
        request.id,
        { version: request.version },
      );
      store.write(resolutionKey(conversationId, thread.id), result);
      store.write(key, undefined);
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
  const locate = () => {
    const f = resolution?.target?.file,
      b = resolution?.target?.browser;
    if (f?.node_id) open();
    if (f?.artifact_id)
      requestFileLocation(
        conversationId,
        f.artifact_id,
        f.sha256,
        f.comments[0]!.anchor,
        open,
      );
    if (b)
      requestBrowserLocation(
        conversationId,
        b.comments[0]!.anchor.live,
        browser,
      );
  };
  return (
    <div className="my-2 space-y-1 text-caption">
      <button
        type="button"
        className="text-fg-secondary hover:text-fg-default disabled:opacity-50"
        disabled={disabled || busy}
        onClick={() =>
          thread.source.browser
            ? beginRebind(conversationId, thread, browser)
            : void resolve()
        }
      >
        {t(
          thread.source.browser
            ? "artifacts.resolution.rebind"
            : "artifacts.resolution.resolve",
        )}
      </button>
      {resolution && (
        <p role="status">
          {t(`artifacts.resolution.${resolution.status}`)}
          {resolution.status === "exact" &&
            (resolution.target?.file?.artifact_id ||
              resolution.target?.file?.node_id ||
              resolution.target?.browser) && (
              <button
                type="button"
                className="ml-2 text-accent"
                onClick={locate}
              >
                {t(
                  file?.node_id
                    ? "artifacts.canvas.open"
                    : "artifacts.resolution.open",
                )}
              </button>
            )}
        </p>
      )}
      {error && <p role="alert">{t("artifacts.resolution.failed")}</p>}
    </div>
  );
}
