import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  useBlocker,
  useLocation,
  type BlockerFunction,
  type Location,
} from "react-router-dom";

import { useT } from "@openfox/i18n";
import { Button, ConfirmDialog } from "@openfox/ui";

import {
  InstructionNavigationContext,
  useInstructionNavigation,
} from "./instructionNavigation";

// Panel width/anchor changes keep the same settings component and its draft.
function draftLocation(
  location: Pick<Location, "pathname" | "search">,
): string {
  const params = new URLSearchParams(location.search);
  params.delete("agentPanel");
  params.delete("agentSection");
  if (params.get("agentTab") === "settings") params.delete("agentTab");
  params.sort();
  return location.pathname + "?" + params.toString();
}

// The installed DOM types do not yet declare the full Navigation API.
type NativeNavigation = EventTarget & {
  currentEntry: { key: string; url: string | null } | null;
  traverseTo: (
    key: string,
    options: { info: object },
  ) => {
    committed: Promise<unknown>;
    finished: Promise<unknown>;
  };
};
type NativeNavigateEvent = Event & {
  navigationType: string;
  destination: { key: string; url: string; sameDocument: boolean };
  info: unknown;
};
type Source = { location: Location; key: string };
type Decision = "cancel" | "proceed";
type Pending = {
  target: { key: string; url: string };
  operation?: {
    token: object;
    decision: Decision;
    finished: boolean;
    timer: ReturnType<typeof setTimeout>;
  };
  failed?: boolean;
};

function nativeNavigation(): NativeNavigation | undefined {
  const candidate = (window as Window & { navigation?: NativeNavigation })
    .navigation;
  return candidate?.currentEntry?.key &&
    typeof candidate.traverseTo === "function"
    ? candidate
    : undefined;
}
function routeURL(location: Location): string {
  return new URL(
    location.pathname + location.search + location.hash,
    window.location.href,
  ).href;
}

/** Mount once inside a Data Router; AppRoutes consumes its held location. */
export function InstructionNavigationProvider({
  children,
}: {
  children: ReactNode;
}) {
  const location = useLocation();
  const actual = useRef(location);
  actual.current = location;
  const [native] = useState(nativeNavigation);
  const dirty = useRef(new Set<symbol>());
  const source = useRef<Source | null>(null);
  const pending = useRef<Pending | null>(null);
  const [revision, refresh] = useState(0);
  const [routeEpoch, setRouteEpoch] = useState(0);
  const confirmed = useRef(false);
  const redraw = useCallback(() => refresh((value) => value + 1), []);
  const stopOperation = useCallback(() => {
    if (pending.current?.operation)
      clearTimeout(pending.current.operation.timer);
    if (pending.current) pending.current.operation = undefined;
  }, []);
  const release = useCallback(() => {
    stopOperation();
    const hadDraft = dirty.current.size > 0 || source.current !== null;
    dirty.current.clear();
    source.current = null;
    pending.current = null;
    // Explicit discard must also reset same-component query-only destinations.
    // It must never run for an approved card collapse that does not navigate.
    if (hadDraft) setRouteEpoch((value) => value + 1);
    redraw();
  }, [redraw, stopOperation]);
  const setDirty = useCallback(
    (id: symbol, value: boolean) => {
      if (dirty.current.has(id) === value) return;
      if (value) {
        dirty.current.add(id);
        if (!source.current && native?.currentEntry?.key)
          source.current = {
            location: actual.current,
            key: native.currentEntry.key,
          };
      } else dirty.current.delete(id);
      redraw();
    },
    [native, redraw],
  );
  const runConfirmed = useCallback((action: () => void) => {
    confirmed.current = true;
    try {
      action();
    } finally {
      confirmed.current = false;
    }
  }, []);
  const blocker = useBlocker(
    useCallback<BlockerFunction>(
      ({ currentLocation, nextLocation, historyAction }) => {
        if (!dirty.current.size && !pending.current) return false;
        if (
          draftLocation(source.current?.location ?? currentLocation) ===
            draftLocation(nextLocation) &&
          !pending.current
        )
          return false;
        if (confirmed.current) {
          release();
          return false;
        }
        // Do not race React Router's relative POP compensation against native keys.
        return !(native && historyAction === "POP");
      },
      [native, release],
    ),
  );
  const blockerRef = useRef(blocker);
  blockerRef.current = blocker;
  const resetBlocker = useCallback(() => {
    if (blockerRef.current.state === "blocked") blockerRef.current.reset();
  }, []);
  const releaseForSecurity = useCallback(() => {
    resetBlocker();
    if (!source.current && !dirty.current.size && !pending.current) return;
    release();
  }, [release, resetBlocker]);
  const failOperation = useCallback(() => {
    stopOperation();
    if (pending.current) pending.current.failed = true;
    redraw();
  }, [redraw, stopOperation]);
  const settle = useCallback(() => {
    const p = pending.current;
    const s = source.current;
    const operation = p?.operation;
    if (!s || !p || !operation?.finished || !native) return;
    const endpoint =
      operation.decision === "cancel"
        ? { key: s.key, url: routeURL(s.location) }
        : p.target;
    // Matching URL alone is insufficient when two history entries share it.
    if (
      native.currentEntry?.key !== endpoint.key ||
      routeURL(actual.current) !== endpoint.url ||
      actual.current.key !== (window.history.state?.key ?? "default")
    )
      return;
    stopOperation();
    pending.current = null;
    if (operation.decision === "proceed") release();
    else {
      if (!dirty.current.size) source.current = null;
      redraw();
    }
  }, [native, redraw, release, stopOperation]);
  const decide = useCallback(
    (decision: Decision) => {
      const p = pending.current;
      const s = source.current;
      if (!p || !s || !native || p.operation) return;
      resetBlocker();
      const token = {};
      p.failed = false;
      p.operation = {
        token,
        decision,
        finished: false,
        timer: setTimeout(failOperation, 10_000),
      };
      redraw();
      try {
        const result = native.traverseTo(
          decision === "cancel" ? s.key : p.target.key,
          { info: token },
        );
        // Both promises can reject; only the still-current operation may settle.
        void result.committed.catch(() => {});
        void result.finished.then(
          () => {
            if (pending.current?.operation?.token !== token) return;
            pending.current.operation.finished = true;
            settle();
          },
          () => {
            if (pending.current?.operation?.token === token) failOperation();
          },
        );
      } catch {
        failOperation();
      }
    },
    [failOperation, native, redraw, resetBlocker, settle],
  );

  useLayoutEffect(() => {
    if (!native) return;
    const onNavigate = (event: Event) => {
      const e = event as NativeNavigateEvent;
      const s = source.current;
      if (
        !s ||
        (!dirty.current.size && !pending.current) ||
        !e.destination.sameDocument
      )
        return;
      if (
        pending.current?.operation &&
        pending.current.operation.token === e.info
      )
        return;
      if (pending.current) {
        if (pending.current.operation) failOperation();
        return; // Preserve the first target across repeated Back/Forward.
      }
      if (
        e.navigationType !== "traverse" ||
        draftLocation(new URL(e.destination.url)) === draftLocation(s.location)
      )
        return;
      pending.current = {
        target: { key: e.destination.key, url: e.destination.url },
      };
      redraw();
    };
    native.addEventListener("navigate", onNavigate);
    return () => native.removeEventListener("navigate", onNavigate);
  }, [native, redraw, failOperation]);
  useLayoutEffect(() => {
    settle();
    if (pending.current) {
      // A save that was already in flight must not send the user to an
      // unconfirmed target. A failed recovery waits for an explicit retry.
      if (
        !dirty.current.size &&
        !pending.current.operation &&
        !pending.current.failed
      )
        decide("cancel");
      return;
    }
    if (!dirty.current.size) {
      if (source.current) {
        source.current = null;
        redraw();
      }
      resetBlocker();
    } else if (
      source.current &&
      native?.currentEntry?.key &&
      draftLocation(location) === draftLocation(source.current.location)
    ) {
      // Benign panel/hash navigation still updates the editor and its return key.
      if (source.current.location !== location) {
        source.current = { location, key: native.currentEntry.key };
        redraw();
      }
    }
  }, [location, revision, native, settle, decide, redraw, resetBlocker]);
  useEffect(() => {
    const unload = (event: BeforeUnloadEvent) => {
      if (!dirty.current.size) return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", unload);
    return () => window.removeEventListener("beforeunload", unload);
  }, []);
  useEffect(() => stopOperation, [stopOperation]);

  return (
    <InstructionNavigationContext.Provider
      value={{
        setDirty,
        runConfirmed,
        releaseForSecurity,
        heldLocation: source.current?.location,
        routeEpoch,
        unsupported: !native && dirty.current.size > 0,
        prompt: !!pending.current || blocker.state === "blocked",
        recovering: !!pending.current?.operation,
        recoveryFailed: !!pending.current?.failed,
        cancel: () => (pending.current ? decide("cancel") : resetBlocker()),
        proceed: () => {
          if (pending.current) decide("proceed");
          else if (blocker.state === "blocked") {
            release();
            blocker.proceed();
          }
        },
        discardAtCurrentLocation: () => {
          resetBlocker();
          release();
        },
      }}
    >
      {children}
    </InstructionNavigationContext.Provider>
  );
}

/** Render inside App's locale provider; the router provider lives at the host. */
export function InstructionNavigationPrompt() {
  const navigation = useInstructionNavigation();
  const t = useT();
  return (
    <>
      {navigation?.unsupported && (
        <div
          role="status"
          className="fixed bottom-4 left-1/2 z-50 max-w-lg -translate-x-1/2 rounded-lg bg-surface-card p-3 text-sm shadow-lg"
        >
          {t("agents.drawer.saveBeforeHistory")}
        </div>
      )}
      <ConfirmDialog
        open={navigation?.prompt ?? false}
        title={t("agents.drawer.unsaved")}
        description={t(
          navigation?.recoveryFailed
            ? "agents.drawer.historyRecoveryFailed"
            : "agents.drawer.unsavedDesc",
        )}
        confirmLabel={t("agents.drawer.discard")}
        cancelLabel={t("agents.drawer.keepEditing")}
        loading={navigation?.recovering}
        danger
        onCancel={() => navigation?.cancel()}
        onConfirm={() => navigation?.proceed()}
      >
        {navigation?.recoveryFailed && (
          <Button
            variant="danger"
            onClick={() => navigation.discardAtCurrentLocation()}
          >
            {t("agents.drawer.discardAtCurrentLocation")}
          </Button>
        )}
      </ConfirmDialog>
    </>
  );
}
