import { Link } from "react-router-dom";
import type { ConversationMessage } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon } from "@openfox/ui";
import { documentTabKey, useOpenArtifactList } from "./artifacts/params";

export function ReferenceCard({ message }: { message: ConversationMessage }) {
  const t = useT();
  const ref = message.reference;
  const document = ref?.type === "DOC";
  const openDocument = useOpenArtifactList(
    documentTabKey(ref?.space_id ?? "", ref?.doc_id ?? ""),
  );
  const available = Boolean(
    ref?.available && ref.space_id && (document ? ref.doc_id : ref.display_id),
  );
  const title = available
    ? ref!.title || t("docs.untitled")
    : t(
        document
          ? "chat.references.docUnavailable"
          : "chat.references.unavailable",
      );
  const href = document
    ? `/o/${message.organization_id}/spaces/${ref?.space_id}/docs/${ref?.doc_id}`
    : `/o/${message.organization_id}/spaces/${ref?.space_id}/tasks?task=${encodeURIComponent(ref?.display_id ?? "")}`;
  const content = (
    <>
      <Icon name={document ? "file-text" : "list"} size={18} />
      <span className="min-w-0">
        <span className="block text-caption text-fg-muted">
          {t(document ? "chat.references.docCard" : "chat.references.card")}
          {available && !document ? ` · ${ref?.display_id}` : ""}
        </span>
        <span className="block truncate">{title}</span>
      </span>
    </>
  );
  const style =
    "flex max-w-full items-center gap-3 rounded-lg border border-border bg-surface-card px-4 py-3";
  return available && document && ref?.doc_kind === "doc" ? (
    <button
      type="button"
      className={`${style} text-left`}
      onClick={openDocument}
    >
      {content}
    </button>
  ) : available ? (
    <Link className={style} to={href}>
      {content}
    </Link>
  ) : (
    <div className={style}>{content}</div>
  );
}
