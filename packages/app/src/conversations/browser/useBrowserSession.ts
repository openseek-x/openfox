import { takeFreshRebind } from "../comments/resolutionState";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  desktopBridge,
  type DesktopBrowserCommand,
  type DesktopBrowserState,
} from "../../desktop";
import { takeBrowserLocation } from "./BrowserReferences";
import { armBrowserDraft, clearBrowserDraft } from "./draftControl";
import { workspaceStore } from "../workspaceStorage";
import { saveBrowserTabs, restoreBrowserTabs } from "./restoreTabs";

export function useBrowserSession(
  orgId: string,
  conversationId: string,
  enabled: boolean,
) {
  const [state, setState] = useState<DesktopBrowserState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [armed, setArmed] = useState<string[] | null>(null);
  const wasActive = useRef(false);
  const lease = useRef<string | null>(null);
  const store = workspaceStore();
  const disarm = useCallback(() => {
    clearBrowserDraft(orgId, conversationId);
    setArmed(null);
  }, [orgId, conversationId]);
  const command = useCallback(
    async (input: Omit<DesktopBrowserCommand, "lease">) => {
      const current = lease.current;
      if (!current) return;
      if (
        !["layout", "allow-control", "reveal-download"].includes(input.action)
      )
        disarm();
      try {
        const next = await desktopBridge()?.browserCommand?.({
          ...input,
          lease: current,
        });
        if (lease.current === current) {
          if (next) setState(next);
          setError(null);
          return next;
        }
      } catch (error) {
        if (lease.current === current)
          setError(
            String(error).includes("LIMIT_EXCEEDED") ? "limit" : "failed",
          );
      }
      return undefined;
    },
    [disarm],
  );
  useEffect(() => {
    if (!enabled) return;
    const bridge = desktopBridge();
    if (!bridge?.browserMount || !bridge.browserCommand) {
      setError("unavailable");
      return;
    }
    let cancelled = false,
      restoring = true;
    setState(null);
    setError(null);
    const unsubscribe = bridge.onBrowserState?.((next) => {
      if (restoring || cancelled) return;
      if (next.lease !== lease.current) return;
      if (wasActive.current && !next.control?.active) disarm();
      if (next.control?.active) setArmed(null);
      wasActive.current = Boolean(next.control?.active);
      if (next.reset) {
        lease.current = null;
        setState(null);
        setError("reset");
      } else {
        setState(next);
        saveBrowserTabs(store, conversationId, next);
      }
    });
    void bridge.browserMount({ orgId, conversationId }).then(
      async (next) => {
        if (cancelled) {
          void bridge
            .browserCommand?.({ lease: next.lease, action: "hide" })
            .catch(() => {});
          return;
        }
        lease.current = next.lease;
        try {
          const restored = await restoreBrowserTabs(
            store,
            conversationId,
            next,
            bridge.browserCommand!,
            () => cancelled,
          );
          if (!cancelled) {
            setState(restored);
            saveBrowserTabs(store, conversationId, restored);
          }
        } catch {
          if (!cancelled) {
            setState(next);
            setError("failed");
          }
        } finally {
          restoring = false;
        }
      },
      () => {
        if (!cancelled) setError("unavailable");
      },
    );
    return () => {
      cancelled = true;
      unsubscribe?.();
      const current = lease.current;
      lease.current = null;
      clearBrowserDraft(orgId, conversationId, current ?? undefined);
      setArmed(null);
      wasActive.current = false;
      if (current)
        void bridge
          .browserCommand?.({ lease: current, action: "hide" })
          .catch(() => {});
    };
  }, [orgId, conversationId, attempt, enabled, disarm]);
  useEffect(() => {
    if (!state?.lease) return;
    const locate = () => {
      const live = takeBrowserLocation(conversationId);
      if (live) void command({ action: "annotation-locate", live });
    };
    const rebind = () => {
      if (takeFreshRebind(conversationId))
        void command({ action: "annotate", enabled: true, mode: "element" });
    };
    // Restoring saved intent does not activate selection without a fresh user click.
    locate();
    rebind();
    window.addEventListener("openfox:browser-rebind", rebind);
    window.addEventListener("openfox:browser-locate", locate);
    return () => {
      window.removeEventListener("openfox:browser-locate", locate);
      window.removeEventListener("openfox:browser-rebind", rebind);
    };
  }, [state?.lease, conversationId, command]);
  return {
    orgId,
    conversationId,
    state,
    error,
    command,
    armed,
    disarm,
    arm(origins: string[], replace = false) {
      if (!lease.current) return;
      armBrowserDraft(orgId, conversationId, lease.current, origins, replace);
      setArmed(origins);
    },
    retry: () => setAttempt((value) => value + 1),
  };
}
export type BrowserSession = ReturnType<typeof useBrowserSession>;
