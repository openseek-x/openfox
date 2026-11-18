import { useEffect, useState } from "react";
import { useT } from "@openfox/i18n";
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
  return (
    <div className="w-full max-w-sm space-y-2">
      <input
        aria-label={t("artifacts.canvas.search")}
        placeholder={t("artifacts.canvas.search")}
        value={text}
        onChange={(e) => setText(e.target.value)}
        maxLength={200}
        className="w-full rounded-lg border border-line-default bg-surface-default px-3 py-2 text-body-sm"
      />
      {query && results.isError && <p role="alert">{t("docs.loadFailed")}</p>}
      {query &&
        results.data
          ?.filter((n) => n.kind === "doc")
          .slice(0, 30)
          .map((node) => (
            <button
              key={node.id}
              className="block w-full truncate rounded px-3 py-2 text-left text-body-sm hover:bg-surface-hover"
              onClick={() => onOpen(documentTabKey(space, node.id))}
            >
              {node.title}
            </button>
          ))}
    </div>
  );
}
