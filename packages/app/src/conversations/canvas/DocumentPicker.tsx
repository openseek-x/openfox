import { useEffect, useState } from "react";
import { useT } from "@openfox/i18n";
import { Icon } from "@openfox/ui";
import { useDocSearch } from "../../docs/queries";
import { useConversation } from "../queries";
import { documentTabKey } from "../artifacts/params";
export function DocumentPicker({
  orgId,
  conversationId,
  onOpen,
}: {
  orgId: string;
  conversationId: string;
  onOpen: (id: string) => void;
}) {
  const t = useT(),
    chat = useConversation(orgId, conversationId);
  const [text, setText] = useState(""),
    [query, setQuery] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setQuery(text.trim()), 250);
    return () => clearTimeout(timer);
  }, [text]);
  const space = chat.data?.space_id ?? "";
  const results = useDocSearch(orgId, space, query);
  if (!space) return null;
  const docs = results.data?.filter((n) => n.kind === "doc").slice(0, 30) ?? [];
  return (
    <div className="w-full space-y-2">
      <div className="relative flex items-center">
        <Icon
          name="search"
          size={15}
          className="pointer-events-none absolute left-3 text-fg-muted"
        />
        <input
          aria-label={t("artifacts.canvas.search")}
          placeholder={t("artifacts.canvas.search")}
          value={text}
          onChange={(e) => setText(e.target.value)}
          maxLength={200}
          className="h-10 w-full rounded-xl border border-line-subtle bg-surface-card pl-9 pr-3 text-body-sm text-fg-default shadow-card outline-none transition-colors placeholder:text-fg-subtle focus:border-line-strong"
        />
      </div>
      {query && results.isError && (
        <p role="alert" className="text-caption text-danger">
          {t("docs.loadFailed")}
        </p>
      )}
      {query && docs.length > 0 && (
        <div className="overflow-hidden rounded-xl border border-line-subtle bg-surface-card shadow-card divide-y divide-line-subtle">
          {docs.map((node) => (
            <button
              key={node.id}
              type="button"
              className="flex w-full items-center gap-2 truncate px-3 py-2 text-left text-body-sm text-fg-secondary transition-colors hover:bg-surface-hover hover:text-fg-default"
              onClick={() => onOpen(documentTabKey(space, node.id))}
            >
              <Icon
                name="file-text"
                size={14}
                className="shrink-0 text-fg-muted"
              />
              <span className="truncate">{node.title}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
