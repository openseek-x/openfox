import type { Editor } from "@tiptap/core";
import { useEffect, useState } from "react";
import { useT } from "@openfox/i18n";
import { Icon, IconButton } from "@openfox/ui";
import { docSearchKey, navigateDocSearch, updateDocSearch } from "./search";

export function DocFindBar({
  editor,
  onClose,
}: {
  editor: Editor;
  onClose: () => void;
}) {
  const t = useT();
  const [query, setQuery] = useState("");
  const [matchCase, setMatchCase] = useState(false);
  const [result, setResult] = useState({ current: 0, total: 0 });
  useEffect(() => {
    const update = () => {
      const state = docSearchKey.getState(editor.state);
      setResult({
        current: state?.matches.length ? state.active + 1 : 0,
        total: state?.matches.length ?? 0,
      });
    };
    editor.on("transaction", update);
    update();
    return () => {
      editor.off("transaction", update);
    };
  }, [editor]);
  useEffect(() => {
    updateDocSearch(editor, query, matchCase);
  }, [editor, query, matchCase]);
  useEffect(
    () => () => {
      if (!editor.isDestroyed) updateDocSearch(editor, "", false);
    },
    [editor],
  );
  return (
    <div
      role="search"
      aria-label={t("docs.find.open")}
      className="of-input-frame sticky top-0 z-10 mb-3 flex flex-wrap items-center gap-2 rounded-lg border border-line-default bg-surface-card p-2 shadow-card focus-within:border-line-focus"
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.preventDefault();
          event.stopPropagation();
          onClose();
        }
        if (event.key === "Enter") {
          event.preventDefault();
          navigateDocSearch(editor, event.shiftKey ? -1 : 1);
        }
      }}
    >
      <input
        autoFocus
        type="search"
        aria-label={t("docs.find.query")}
        placeholder={t("docs.find.query")}
        maxLength={512}
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        className="min-w-0 flex-1 bg-transparent px-2 text-body outline-none"
      />
      <label className="flex items-center gap-1 text-caption">
        <input
          type="checkbox"
          checked={matchCase}
          onChange={(event) => setMatchCase(event.target.checked)}
        />
        {t("docs.find.matchCase")}
      </label>
      <output aria-live="polite" className="text-caption">
        {t("docs.find.count", result)}
      </output>
      <IconButton
        label={t("docs.find.previous")}
        icon={<Icon name="chevron-up" />}
        size="28"
        variant="ghost"
        disabled={!result.total}
        onClick={() => navigateDocSearch(editor, -1)}
      />
      <IconButton
        label={t("docs.find.next")}
        icon={<Icon name="chevron-down" />}
        size="28"
        variant="ghost"
        disabled={!result.total}
        onClick={() => navigateDocSearch(editor, 1)}
      />
      <IconButton
        label={t("docs.find.close")}
        icon={<Icon name="x" />}
        size="28"
        variant="ghost"
        onClick={onClose}
      />
    </div>
  );
}
