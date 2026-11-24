import {
  lazy,
  Suspense,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";

import { useT } from "@openfox/i18n";
import {
  Button,
  Textarea,
  cn,
  dismissOnBackdrop,
  useModalBehavior,
} from "@openfox/ui";

import {
  INSTRUCTION_NAVIGATION,
  useInstructionNavigation,
} from "./instructionNavigation";

const RichInstructions = lazy(() => import("./RichInstructions"));
export const INSTRUCTIONS_MAX = 100000;

export function InstructionsEditor({
  saved,
  label,
  editable,
  onSave,
  onDirty,
  history,
}: {
  saved: string;
  label: string;
  editable: boolean;
  onSave: (value: string) => Promise<string | undefined>;
  onDirty: (dirty: boolean) => void;
  history?: (
    draft: string,
    restore: (value: string) => Promise<boolean>,
    busy: boolean,
  ) => ReactNode;
}) {
  const t = useT();
  const routerManaged = useInstructionNavigation() !== null;
  const [draft, setDraft] = useState(saved);
  const [base, setBase] = useState(saved);
  const [saving, setSaving] = useState(false);
  const [rich, setRich] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [host] = useState(() => document.createElement("div"));
  const anchor = useRef<HTMLDivElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const selection = useRef<[number, number]>([0, 0]);
  const dirty = draft !== base;
  const dirtyCallback = useRef(onDirty);
  dirtyCallback.current = onDirty;
  // An optimistic cache update, conflict refresh, or failed save must not overwrite a draft.
  useEffect(() => {
    if (!dirty && !saving) {
      setDraft(saved);
      setBase(saved);
    }
  }, [saved, dirty, saving]);
  useLayoutEffect(() => {
    dirtyCallback.current(dirty);
    return () => dirtyCallback.current(false);
  }, [dirty]);
  useEffect(() => {
    if (!dirty) return;
    const unload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", unload);
    return () => window.removeEventListener("beforeunload", unload);
  }, [dirty]);
  useEffect(() => {
    if (!dirty || routerManaged) return;
    const ask = (event: Event) => {
      if (!window.confirm(t("agents.drawer.unsavedDesc")))
        event.preventDefault();
    };
    const link = (event: MouseEvent) => {
      if (
        event.defaultPrevented ||
        event.button !== 0 ||
        event.metaKey ||
        event.ctrlKey ||
        event.shiftKey ||
        event.altKey
      )
        return;
      const target =
        event.target instanceof Element
          ? event.target.closest<HTMLAnchorElement>("a[href]")
          : null;
      if (
        !target ||
        target.hasAttribute("download") ||
        (target.target && target.target !== "_self")
      )
        return;
      const href = target.getAttribute("href");
      if (!href || href.startsWith("#")) return;
      ask(event);
      if (event.defaultPrevented) event.stopImmediatePropagation();
    };
    window.addEventListener(INSTRUCTION_NAVIGATION, ask);
    document.addEventListener("click", link, true);
    return () => {
      window.removeEventListener(INSTRUCTION_NAVIGATION, ask);
      document.removeEventListener("click", link, true);
    };
  }, [dirty, t, routerManaged]);
  // A stable portal target keeps the same editor/textarea DOM and selection when expanded.
  useLayoutEffect(() => {
    (expanded ? document.body : anchor.current)?.appendChild(host);
    return () => host.remove();
  }, [host, expanded]);
  const changeExpanded = (next: boolean) => {
    if (textarea.current && !rich)
      selection.current = [
        textarea.current.selectionStart,
        textarea.current.selectionEnd,
      ];
    setExpanded(next);
  };
  useModalBehavior(expanded, panel, () => changeExpanded(false));
  const previousExpanded = useRef(expanded);
  useEffect(() => {
    if (previousExpanded.current === expanded) return;
    previousExpanded.current = expanded;
    if (!textarea.current || rich) return;
    textarea.current.focus();
    textarea.current.setSelectionRange(...selection.current);
  }, [expanded, rich]);
  const save = async (value: string) => {
    if (!editable || saving || value.length > INSTRUCTIONS_MAX) return false;
    setSaving(true);
    try {
      const result = await onSave(value);
      if (result === undefined) return false;
      setDraft(result);
      setBase(result);
      return true;
    } finally {
      setSaving(false);
    }
  };
  return (
    <div ref={anchor}>
      {createPortal(
        <div
          className={expanded ? "fixed inset-0 z-50 p-6" : undefined}
          onPointerDown={
            expanded
              ? dismissOnBackdrop(() => changeExpanded(false))
              : undefined
          }
        >
          <div
            ref={panel}
            role={expanded ? "dialog" : undefined}
            aria-modal={expanded || undefined}
            aria-label={expanded ? label : undefined}
            onKeyDownCapture={(event) => {
              if (expanded && event.key === "Escape") {
                event.preventDefault();
                event.stopPropagation();
                changeExpanded(false);
              }
            }}
            className={cn(
              "flex min-w-0 flex-col gap-3",
              expanded &&
                "h-full overflow-auto rounded-xl border border-line-default bg-surface-card p-6 shadow-dialog",
            )}
          >
            <div className="flex flex-wrap items-center gap-2">
              <Button
                size="28"
                variant="outline"
                aria-pressed={!rich}
                onClick={() => setRich(false)}
              >
                {t("agents.sections.instructions.source")}
              </Button>
              <Button
                size="28"
                variant="outline"
                aria-pressed={rich}
                onClick={() => setRich(true)}
              >
                {t("agents.sections.instructions.rich")}
              </Button>
              <span className="flex-1 text-caption text-fg-muted">
                {t("agents.sections.instructions.chars", {
                  count: draft.length,
                })}
              </span>
              <Button
                size="28"
                variant="outline"
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => changeExpanded(!expanded)}
              >
                {t(
                  expanded
                    ? "agents.sections.instructions.collapse"
                    : "agents.sections.instructions.expand",
                )}
              </Button>
              <Button
                size="28"
                disabled={
                  !editable || !dirty || draft.length > INSTRUCTIONS_MAX
                }
                loading={saving}
                onClick={() => void save(draft)}
              >
                {t("common.save")}
              </Button>
            </div>
            {draft.length > INSTRUCTIONS_MAX && (
              <p role="alert" className="text-danger">
                {t("agents.sections.instructions.tooLong")}
              </p>
            )}
            <div hidden={rich}>
              <Textarea
                ref={textarea}
                rows={expanded ? 22 : 12}
                aria-label={label}
                value={draft}
                maxLength={INSTRUCTIONS_MAX}
                readOnly={!editable || saving}
                onChange={(event) => setDraft(event.target.value)}
                className="bg-surface-canvas font-mono"
              />
            </div>
            {rich && (
              <Suspense fallback={<p>{t("common.loading")}</p>}>
                <RichInstructions
                  value={draft}
                  onChange={setDraft}
                  editable={editable && !saving}
                  expanded={expanded}
                  label={label}
                />
              </Suspense>
            )}
            {history?.(
              draft,
              async (value) => {
                if (
                  dirty &&
                  !window.confirm(
                    t("agents.sections.instructions.replaceDraft"),
                  )
                )
                  return false;
                return save(value);
              },
              saving,
            )}
          </div>
        </div>,
        host,
      )}
    </div>
  );
}
