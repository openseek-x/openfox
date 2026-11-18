import {
  useEffect,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { useT } from "@openfox/i18n";
import { Button } from "@openfox/ui";
import { useMe } from "../agents/queries";
import {
  bindWorkspaceStore,
  openWorkspaceDB,
  workspaceStore,
  WorkspaceStore,
} from "./workspaceStorage";

/** Hydrate before mounting composers; an empty initial render must not overwrite saved input. */
export function WorkspacePersistence({
  orgId,
  children,
}: {
  orgId: string;
  children: ReactNode;
}) {
  const me = useMe(),
    t = useT();
  const scope = me.data?.id ? JSON.stringify([me.data.id, orgId]) : "";
  const [loaded, setLoaded] = useState("");
  const [error, setError] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!scope) return;
    let cancelled = false,
      store: WorkspaceStore | undefined;
    setError(false);
    void (async () => {
      try {
        store = new WorkspaceStore(scope, await openWorkspaceDB());
        await store.hydrate();
        if (cancelled) {
          await store.close();
          return;
        }
        bindWorkspaceStore(store);
        setLoaded(scope);
      } catch {
        if (!cancelled) setError(true);
      }
    })();
    return () => {
      cancelled = true;
      if (store) void store.close().catch(() => {});
    };
  }, [scope, attempt]);
  if (loaded !== scope || !scope)
    return (
      <div className="p-4 text-body-sm" aria-busy={!error}>
        {t(
          error
            ? "artifacts.persistence.loadFailed"
            : "artifacts.persistence.loading",
        )}
        {error && (
          <Button onClick={() => setAttempt((n) => n + 1)}>
            {t("common.retry")}
          </Button>
        )}
      </div>
    );
  return <>{children}</>;
}

export function DraftStorageStatus() {
  const t = useT(),
    store = workspaceStore();
  const status = useSyncExternalStore(
    (listener) => {
      store.listeners.add(listener);
      return () => {
        store.listeners.delete(listener);
      };
    },
    () => store.status,
  );
  useEffect(() => {
    const flush = () => {
      void store.flush().catch(() => {});
    };
    const leave = (event: BeforeUnloadEvent) => {
      if (store.status === "saved") return;
      flush();
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("pagehide", flush);
    window.addEventListener("beforeunload", leave);
    document.addEventListener("visibilitychange", flush);
    return () => {
      window.removeEventListener("pagehide", flush);
      window.removeEventListener("beforeunload", leave);
      document.removeEventListener("visibilitychange", flush);
    };
  }, [store]);
  if (!store.durable || status === "saved") return null;
  return (
    <div
      role={status === "saving" ? "status" : "alert"}
      className="mb-2 text-caption text-fg-secondary"
    >
      {t(`artifacts.persistence.${status}`)}
      {status === "failed" && (
        <button
          className="ml-2 underline"
          onClick={() => void store.flush().catch(() => {})}
        >
          {t("common.retry")}
        </button>
      )}
    </div>
  );
}
