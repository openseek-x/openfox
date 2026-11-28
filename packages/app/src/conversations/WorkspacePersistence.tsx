import {
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { useT } from "@openfox/i18n";
import { Button, cn } from "@openfox/ui";
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

const SAVED_VISIBLE_MS = 1500;
const SAVED_FADE_MS = 300;

export function DraftStorageStatus() {
  const t = useT(),
    store = workspaceStore();
  // 仅用户草稿/批注等内容的写入才显示保存中状态；失败与冲突告警覆盖所有写入
  const status = useSyncExternalStore(
    (listener) => {
      store.listeners.add(listener);
      return () => {
        store.listeners.delete(listener);
      };
    },
    () =>
      store.status === "saving" && !store.hasDirtyContent
        ? "saved"
        : store.status,
  );
  const wasSaving = useRef(false);
  const [savedPhase, setSavedPhase] = useState<"hidden" | "shown" | "fading">(
    "hidden",
  );
  useEffect(() => {
    if (status === "saving") {
      wasSaving.current = true;
      setSavedPhase("hidden");
      return undefined;
    }
    if (status === "saved" && wasSaving.current) {
      wasSaving.current = false;
      setSavedPhase("shown");
      const fadeTimer = window.setTimeout(
        () => setSavedPhase("fading"),
        SAVED_VISIBLE_MS - SAVED_FADE_MS,
      );
      const hideTimer = window.setTimeout(
        () => setSavedPhase("hidden"),
        SAVED_VISIBLE_MS,
      );
      return () => {
        window.clearTimeout(fadeTimer);
        window.clearTimeout(hideTimer);
      };
    }
    wasSaving.current = false;
    setSavedPhase("hidden");
    return undefined;
  }, [status]);
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
  if (!store.durable) return null;
  if (status === "saved") {
    if (savedPhase === "hidden") return null;
    return (
      <div
        role="status"
        className={cn(
          "mb-2 text-caption text-fg-muted transition-opacity duration-300 motion-reduce:transition-none",
          savedPhase === "fading" ? "opacity-0" : "opacity-100",
        )}
      >
        {t("artifacts.persistence.saved")}
      </div>
    );
  }
  return (
    <div
      role={status === "saving" ? "status" : "alert"}
      className={cn(
        "mb-2 text-caption",
        status === "saving" ? "text-fg-secondary" : "text-danger",
      )}
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
