import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";

import { useT } from "@openfox/i18n";
import { Button, Dialog, TextInput, useToast } from "@openfox/ui";

import { keys, useApi } from "../context";
import { conversationTitle } from "./model";
import { chatErrorMessage, useConversations } from "./queries";

export function ForwardReferenceDialog({
  orgId,
  source,
  onClose,
}: {
  orgId: string;
  source: { type: "TASK"; task_id: string } | { type: "DOC"; doc_id: string };
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const cache = useQueryClient();
  const toast = useToast();
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState("");
  const conversations = useConversations(orgId, {
    q: search.trim() || undefined,
  });
  const sourceKey =
    source.type === "TASK" ? `TASK:${source.task_id}` : `DOC:${source.doc_id}`;
  const attempt = useRef<{
    target: string;
    source: string;
    key: string;
  } | null>(null);
  const send = useMutation({
    mutationFn: () => {
      if (
        attempt.current?.target !== selected ||
        attempt.current.source !== sourceKey
      )
        attempt.current = {
          target: selected,
          source: sourceKey,
          key: crypto.randomUUID(),
        };
      return api.conversations.postReference(orgId, selected, {
        ...source,
        client_message_id: attempt.current.key,
      });
    },
    onSuccess: (message) => {
      void cache.invalidateQueries({
        queryKey: keys.conversationMessages(orgId, message.conversation_id),
      });
      void cache.invalidateQueries({ queryKey: keys.conversationLists(orgId) });
      toast(
        t(
          source.type === "DOC"
            ? "chat.references.docSent"
            : "chat.references.sent",
        ),
      );
      onClose();
    },
  });
  const close = () => {
    if (!send.isPending) onClose();
  };
  return (
    <Dialog
      open
      onClose={close}
      title={t("chat.references.forward")}
      description={t(
        source.type === "DOC"
          ? "chat.references.docDescription"
          : "chat.references.description",
      )}
      width={520}
      footer={
        <>
          <Button variant="secondary" disabled={send.isPending} onClick={close}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={!selected || send.isPending}
            loading={send.isPending}
            onClick={() => send.mutate()}
          >
            {t("chat.references.send")}
          </Button>
        </>
      }
    >
      <TextInput
        aria-label={t("chat.references.search")}
        placeholder={t("chat.references.search")}
        value={search}
        maxLength={200}
        disabled={send.isPending}
        onChange={(event) => {
          setSearch(event.target.value);
          setSelected("");
          send.reset();
        }}
      />
      {conversations.isError ? (
        <p role="alert">{chatErrorMessage(t, conversations.error)}</p>
      ) : (
        <div
          role="radiogroup"
          aria-label={t("chat.references.targets")}
          className="mt-3 max-h-80 space-y-1 overflow-y-auto"
        >
          {conversations.list.map((conversation) => {
            const disabled =
              !conversation.offer_participant ||
              conversation.lifecycle !== "OPEN";
            return (
              <label
                key={conversation.id}
                className="flex items-center gap-3 rounded-md border border-border px-3 py-2"
              >
                <input
                  type="radio"
                  name="reference-target"
                  value={conversation.id}
                  checked={selected === conversation.id}
                  disabled={disabled || send.isPending}
                  onChange={() => {
                    setSelected(conversation.id);
                    send.reset();
                  }}
                />
                <span className="min-w-0 flex-1 truncate">
                  {conversationTitle(conversation) ?? t("chat.untitled")}
                </span>
                {disabled && (
                  <span className="text-caption text-fg-muted">
                    {t("chat.references.readOnly")}
                  </span>
                )}
              </label>
            );
          })}
          {!conversations.isPending && conversations.list.length === 0 && (
            <p>{t("chat.references.empty")}</p>
          )}
        </div>
      )}
      {conversations.hasNextPage && (
        <Button
          variant="ghost"
          loading={conversations.isFetchingNextPage}
          onClick={() => void conversations.fetchNextPage()}
        >
          {t("chat.references.more")}
        </Button>
      )}
      {send.isError && (
        <p role="alert" className="mt-3 text-danger">
          {chatErrorMessage(t, send.error)}
        </p>
      )}
    </Dialog>
  );
}
