import { useMutation, useQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useT } from "@openfox/i18n";
import {
  Avatar,
  Button,
  Checkbox,
  Dialog,
  Icon,
  TextInput,
  cn,
  useToast,
} from "@openfox/ui";
import { useApi } from "../../context";
import { chatErrorMessage } from "../../conversations/queries";

export type OfferKind = "INVITE" | "TRANSFER";

export function ConversationOfferDialog({
  orgId,
  conversationId,
  kind,
  canAssign,
  assistant = false,
  close,
}: {
  orgId: string;
  conversationId: string;
  kind: OfferKind;
  canAssign: boolean;
  assistant?: boolean;
  close: () => void;
}) {
  const api = useApi(),
    t = useT(),
    toast = useToast();
  const invite = kind === "INVITE";
  const [q, search] = useState(""),
    [selected, select] = useState<{ id: string; name: string } | null>(null),
    [confirm, setConfirm] = useState(false),
    [note, setNote] = useState(""),
    [assign, setAssign] = useState(false);
  const peers = useQuery({
    queryKey: ["org", orgId, "offer-candidates", conversationId, kind, q],
    queryFn: () =>
      api.conversations.offerCandidates(orgId, conversationId, kind, q),
    enabled: !confirm,
  });
  const attempt = useRef<{ body: string; key: string } | null>(null);
  const send = useMutation({
    mutationFn: () => {
      const input = {
        kind,
        recipient_user_id: selected!.id,
        note: note.trim(),
        transfer_assignee: !invite && canAssign && assign,
      };
      const body = JSON.stringify(input);
      if (attempt.current?.body !== body)
        attempt.current = { body, key: crypto.randomUUID() };
      return api.conversations.offer(
        orgId,
        conversationId,
        input,
        attempt.current.key,
      );
    },
    onSuccess: () => {
      toast(
        t(invite ? "tasks.detail.inviteSent" : "tasks.detail.transferSent", {
          name: selected!.name,
        }),
      );
      close();
    },
    onError: (e) => toast(chatErrorMessage(t, e), { tone: "error" }),
  });
  return (
    <Dialog
      open
      onClose={send.isPending ? undefined : close}
      title={t(
        confirm
          ? invite
            ? "tasks.detail.confirmInvite"
            : "tasks.detail.confirmTransfer"
          : invite
            ? "tasks.detail.inviteTitle"
            : "tasks.detail.transfer",
      )}
      description={
        confirm
          ? t(
              invite
                ? "tasks.detail.offerInviteDesc"
                : "tasks.detail.offerTransferDesc",
              { name: selected!.name },
            )
          : t(invite ? "tasks.detail.inviteDesc" : "tasks.detail.transferDesc")
      }
      footer={
        <>
          <Button
            variant="outline"
            disabled={send.isPending}
            onClick={() => (confirm ? setConfirm(false) : close())}
          >
            {t(confirm ? "tasks.detail.prevStep" : "common.cancel")}
          </Button>
          <Button
            disabled={!selected}
            loading={send.isPending}
            onClick={() => (confirm ? send.mutate() : setConfirm(true))}
          >
            {t(
              confirm
                ? invite
                  ? "tasks.detail.inviteAction"
                  : "tasks.detail.transferAction"
                : "tasks.detail.nextStep",
            )}
          </Button>
        </>
      }
    >
      {assistant && !invite && (
        <p className="mb-3 text-body-sm text-fg-secondary">
          {t("chat.assistant.transferNotice")}
        </p>
      )}
      {!confirm ? (
        <div className="flex flex-col gap-2">
          <TextInput
            data-autofocus
            aria-label={t("tasks.detail.memberSearch")}
            placeholder={t("tasks.detail.memberSearch")}
            leading={<Icon name="search" />}
            value={q}
            maxLength={64}
            onChange={(e) => search(e.target.value)}
          />
          <div
            className="max-h-64 min-h-24 overflow-y-auto"
            role="group"
            aria-label={t("tasks.detail.memberSearch")}
          >
            {peers.isLoading ? (
              <p className="p-3 text-body-sm text-fg-muted">
                {t("common.loading")}
              </p>
            ) : peers.isError ? (
              <Button variant="ghost" onClick={() => void peers.refetch()}>
                {t("common.retry")}
              </Button>
            ) : !peers.data?.length ? (
              <p className="p-3 text-body-sm text-fg-muted">
                {t(
                  invite ? "tasks.detail.noInvite" : "tasks.detail.noTransfer",
                )}
              </p>
            ) : (
              peers.data.map((person) => (
                <button
                  key={person.id}
                  aria-label={person.name}
                  type="button"
                  disabled={!!person.unavailable_reason}
                  aria-pressed={selected?.id === person.id}
                  onClick={() => select(person)}
                  className={cn(
                    "flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-body hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-50",
                    selected?.id === person.id && "bg-surface-active",
                  )}
                >
                  <Avatar name={person.name} size={24} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate">{person.name}</span>
                    {person.unavailable_reason && (
                      <span className="block text-caption text-fg-muted">
                        {t("chat.assistant.transferNotReady")}
                      </span>
                    )}
                  </span>
                  {selected?.id === person.id && (
                    <Icon name="check" size={14} />
                  )}
                </button>
              ))
            )}
          </div>
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          <textarea
            autoFocus
            aria-label={t("tasks.detail.note")}
            placeholder={t("tasks.detail.note")}
            value={note}
            maxLength={1000}
            onChange={(e) => setNote(e.target.value)}
            className="min-h-20 w-full resize-y rounded-lg border border-line-default bg-surface-card p-2 text-body outline-none focus:border-accent"
          />
          {!invite && canAssign && (
            <label className="flex items-center gap-2 text-body-sm">
              <Checkbox
                checked={assign}
                onChange={(e) => setAssign(e.target.checked)}
              />
              {t("tasks.detail.alsoAssign")}
            </label>
          )}
        </div>
      )}
    </Dialog>
  );
}
