import { useState } from "react";
import { useT } from "@openfox/i18n";
import { isApiError, type RunArtifact } from "@openfox/api";
import { useApi } from "../../context";
import { workspaceStore } from "../workspaceStorage";
import { useSearchParams } from "react-router-dom";
import { ARTIFACT_PARAM, proposalTabKey } from "../artifacts/params";
export function RestoreProposal({
  orgId,
  conversationId,
  artifact: a,
}: {
  orgId: string;
  conversationId: string;
  artifact: RunArtifact;
}) {
  const api = useApi(),
    t = useT(),
    store = workspaceStore();
  const [, setParams] = useSearchParams();
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const restore = async () => {
    if (busy) return;
    setBusy(true);
    setError("");
    const key = `proposal-restore:${conversationId}:${a.id}`;
    const request = store.read<{
      id: string;
      source_artifact_id: string;
      base_artifact_id: string;
    }>(key) ?? {
      id: crypto.randomUUID(),
      source_artifact_id: a.id,
      base_artifact_id: a.head_artifact_id,
    };
    try {
      store.write(key, request);
      await store.flush();
      const { id, ...body } = request;
      const result = await api.workspaceProposals.restore(
        orgId,
        conversationId,
        id,
        body,
      );
      store.write(key, undefined);
      if (workspaceStore() === store)
        setParams((current) => {
          const next = new URLSearchParams(current);
          next.set(ARTIFACT_PARAM, proposalTabKey(result.id));
          return next;
        });
    } catch (reason) {
      if (
        isApiError(reason) &&
        reason.status >= 400 &&
        reason.status < 500 &&
        ![408, 429].includes(reason.status)
      )
        store.write(key, undefined);
      setError(
        t(
          isApiError(reason) && reason.status === 409
            ? "artifacts.proposals.conflict"
            : "artifacts.proposals.restoreFailed",
        ),
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <div>
      <button
        type="button"
        className="text-accent hover:underline disabled:opacity-50"
        disabled={busy}
        onClick={() => void restore()}
      >
        {t("artifacts.proposals.restore")}
      </button>
      {error && (
        <p role="alert" className="text-danger">
          {error}
        </p>
      )}
    </div>
  );
}
