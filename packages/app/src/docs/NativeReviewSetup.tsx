import { useState } from "react";
import { useT } from "@openfox/i18n";
import { useMe } from "../agents/queries";
import { useConversations } from "../conversations/queries";

export function NativeReviewSetup({
  orgId,
  spaceId,
  busy,
  onStart,
}: {
  orgId: string;
  spaceId: string;
  busy: boolean;
  onStart: (id: string) => void;
}) {
  const t = useT();
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
  return (
    <div className="flex flex-col gap-2 rounded border border-line-default p-3">
      <p className="text-body-sm">{t("artifacts.review.nativeHint")}</p>
      <label>
        {t("artifacts.review.nativeConversation")}
        <select
          disabled={busy}
          value={id}
          onChange={(e) => setID(e.target.value)}
          className="w-full border border-line-default bg-surface-default p-2"
        >
          <option value="">—</option>
          {choices.map((c) => (
            <option key={c.id} value={c.id}>
              {c.title || c.agent.name}
            </option>
          ))}
        </select>
      </label>
      {!chats.isPending && choices.length === 0 && (
        <p>{t("artifacts.review.nativeNone")}</p>
      )}
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
