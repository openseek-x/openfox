import { useEffect, useRef, useState } from "react";
import { useT } from "@openfox/i18n";
import { useToast } from "@openfox/ui";
import { useMe } from "../agents/queries";
import { useConversations } from "../conversations/queries";

export function NativeReviewSetup({
  orgId,
  spaceId,
  busy,
  onStart,
  onDismiss,
}: {
  orgId: string;
  spaceId: string;
  busy: boolean;
  onStart: (id: string) => void;
  onDismiss: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const notified = useRef(false);
  const me = useMe();
  const chats = useConversations(orgId);
  const [id, setID] = useState("");
  const choices = chats.list.filter(
    (c) =>
      c.space_id === spaceId &&
      c.owner_user_id === me.data?.id &&
      c.visibility === "PRIVATE" &&
      !c.task_id &&
      !c.crew_id &&
      c.lifecycle === "OPEN" &&
      c.effective?.runtime === "codex",
  );
  const { hasNextPage, isFetching, fetchNextPage } = chats;
  const loading = me.isPending || chats.isPending || isFetching;
  const failed = me.isError || chats.isError;
  useEffect(() => {
    if (choices.length || loading || notified.current) return;
    if (!failed && hasNextPage) {
      void fetchNextPage({ cancelRefetch: false });
      return;
    }
    notified.current = true;
    toast(t(failed ? "chat.list.loadFailed" : "artifacts.review.nativeNone"), {
      tone: failed ? "error" : "info",
    });
    onDismiss();
  }, [
    choices.length,
    loading,
    failed,
    hasNextPage,
    fetchNextPage,
    toast,
    t,
    onDismiss,
  ]);

  if (!choices.length) return null;
  return (
    <div className="flex flex-col gap-2 rounded border border-line-default p-3">
      <p className="text-body-sm">{t("artifacts.review.nativeHint")}</p>
      <label>
        {t("artifacts.review.nativeConversation")}
        <select
          disabled={busy}
          value={id}
          onChange={(e) => setID(e.target.value)}
          className="w-full rounded-md border border-line-default bg-surface-card p-2"
        >
          <option value="">—</option>
          {choices.map((c) => (
            <option key={c.id} value={c.id}>
              {c.title || c.agent.name}
            </option>
          ))}
        </select>
      </label>
      {chats.isError && (
        <button onClick={() => void chats.refetch()}>
          {t("artifacts.review.retry")}
        </button>
      )}
      {chats.hasNextPage && (
        <button
          disabled={chats.isFetchingNextPage}
          onClick={() => void chats.fetchNextPage()}
        >
          {t("artifacts.review.next")}
        </button>
      )}
      <button
        className="rounded border border-line-default px-2 py-1 disabled:opacity-50"
        disabled={busy || !choices.some((c) => c.id === id)}
        onClick={() => onStart(id)}
      >
        {t("artifacts.review.nativeStart")}
      </button>
    </div>
  );
}
