import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { DirectChatMessage } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, useToast } from "@openfox/ui";
import { keys, useApi } from "../context";
import { useOpenTaskPage } from "../tasks/navigation";
import { useNavigate } from "react-router-dom";
import { conversationPath } from "./ChatPanel";
import { chatErrorMessage, invalidateConversation } from "./queries";

export function useOfferDecision(orgId: string) {
  const api = useApi(),
    t = useT(),
    toast = useToast(),
    cache = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      accept,
    }: {
      id: string;
      accept: boolean;
      conversation: string;
    }) => api.conversations.decideOffer(orgId, id, accept),
    onSettled: (_data, error, { conversation }) => {
      for (const queryKey of [
        keys.conversationOffers(orgId),
        keys.inboxStats(orgId),
        keys.inbox(orgId),
        keys.tasks(orgId),
        ["org", orgId, "direct-chats"],
      ])
        void cache.invalidateQueries({ queryKey });
      invalidateConversation(cache, orgId, conversation);
      if (error) toast(chatErrorMessage(t, error), { tone: "error" });
    },
  });
}
export function OfferCard({
  orgId,
  offer: o,
  own,
}: {
  orgId: string;
  offer: NonNullable<DirectChatMessage["offer"]>;
  own: boolean;
}) {
  const t = useT(),
    navigate = useNavigate(),
    decide = useOfferDecision(orgId),
    open = useOpenTaskPage(),
    invite = o.kind === "INVITE",
    pending = o.status === "PENDING";
  const label = pending
    ? own
      ? invite
        ? "tasks.detail.cardSent"
        : "tasks.detail.cardOffered"
      : invite
        ? "tasks.detail.cardInvite"
        : "tasks.detail.cardTransfer"
    : o.status === "ACCEPTED"
      ? invite
        ? "tasks.detail.cardJoined"
        : "tasks.detail.cardTransferred"
      : o.status === "DECLINED"
        ? "tasks.detail.cardDeclined"
        : "tasks.detail.cardStale";
  const submit = (accept: boolean) =>
    decide.mutate({ id: o.id, accept, conversation: o.conversation_id });
  return (
    <div className="max-w-full min-w-56 rounded-lg border border-line-default bg-surface-card p-3">
      <div className="flex items-start gap-2 text-body font-medium">
        <Icon
          name={invite ? "user-plus" : "users"}
          size={16}
          className="mt-0.5 shrink-0 text-fg-muted"
        />
        <span className="min-w-0 break-words">
          {t(label, { title: o.title })}
        </span>
      </div>
      {o.note && (
        <p className="mt-2 whitespace-pre-wrap break-words text-body-sm text-fg-secondary">
          {o.note}
        </p>
      )}
      {pending && o.assistant && !invite && (
        <p className="mt-2 text-body-sm text-fg-secondary">
          {t("chat.assistant.transferNotice")}
        </p>
      )}
      {o.transfer_assignee && (
        <p className="mt-2 text-caption text-fg-muted">
          {t("tasks.detail.assigneeIncluded")}
        </p>
      )}
      {pending && !own ? (
        <div className="mt-3 flex gap-2">
          <Button
            size="28"
            disabled={decide.isPending}
            onClick={() => submit(true)}
          >
            {t(invite ? "tasks.detail.cardAgree" : "tasks.detail.offerAccept")}
          </Button>
          <Button
            size="28"
            variant="outline"
            disabled={decide.isPending}
            onClick={() => submit(false)}
          >
            {t("tasks.detail.offerDecline")}
          </Button>
        </div>
      ) : (
        o.can_open && (
          <Button
            variant="outline"
            size="28"
            className="mt-3"
            onClick={() =>
              o.task_display_id
                ? open(o.task_display_id, o.conversation_id)
                : navigate(
                    o.assistant && !own
                      ? `/o/${orgId}/assistant?session=${encodeURIComponent(o.conversation_id)}`
                      : conversationPath(orgId, o.conversation_id),
                  )
            }
          >
            {t("tasks.detail.cardOpen")}
          </Button>
        )
      )}
    </div>
  );
}
