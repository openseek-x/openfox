import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  newIdempotencyKey,
  isApiError,
  type ConversationSummary,
  type ConversationWorkspaceSelection,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Dialog } from "@openfox/ui";
import { useApi } from "../context";
import { useSpaceAgents } from "../tasks/queries";
import {
  useWorkspaceOptions,
  WorkDirectoryPicker,
  type WorkDirectoryDraft,
} from "./WorkDirectoryPicker";
import { BaseBranch, validWorkspaceRef } from "./WorkDirectoryModes";
import { invalidateConversation, chatErrorMessage } from "./queries";
import { useChatNavigate } from "./chatNavigation";
import { conversationPath } from "./ChatPanel";

type Props = {
  orgId: string;
  conversation: ConversationSummary;
  messageId: string;
  attempts: Map<string, string>;
  onClose: () => void;
};
export function ForkConversationDialog({
  orgId,
  conversation,
  messageId,
  attempts,
  onClose,
}: Props) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient(),
    navigate = useChatNavigate();
  const agents = useSpaceAgents(orgId, conversation.space_id);
  const [agentId, setAgentId] = useState("");
  const [mode, setMode] = useState("");
  const [directory, setDirectory] = useState<WorkDirectoryDraft>();
  const [base, setBase] = useState("");
  const work = useWorkspaceOptions(orgId, agentId, !!agentId, conversation.id);
  const selection: ConversationWorkspaceSelection | null =
    mode === "REUSE" || mode === "BASELINE"
      ? {
          mode,
          source_conversation_id: conversation.id,
          ...(mode === "BASELINE" && base.trim()
            ? { base_ref: base.trim() }
            : {}),
        }
      : mode === "CHOOSE"
        ? (directory?.selection ?? null)
        : null;
  const unavailable =
    !agentId ||
    !mode ||
    (mode === "CHOOSE" && !directory) ||
    (mode === "REUSE" && !work.data?.can_reuse) ||
    (mode === "BASELINE" &&
      (!work.data?.can_set_baseline || !validWorkspaceRef(base)));
  const create = useMutation({
    mutationFn: () => {
      const input = {
        source_message_id: messageId,
        agent_id: agentId,
        workspace_selection: selection,
      };
      const fingerprint = JSON.stringify([conversation.id, input]);
      let key = attempts.get(fingerprint);
      if (!key) {
        key = newIdempotencyKey();
        attempts.set(fingerprint, key);
      }
      return api.conversations.fork(orgId, conversation.id, input, key);
    },
    onSuccess: (created) => {
      invalidateConversation(cache, orgId, created.id, true);
      onClose();
      navigate(conversationPath(orgId, created.id));
    },
  });
  const candidates = (agents.data ?? []).filter(
    (a) =>
      !a.archived_at &&
      (conversation.task_id
        ? !a.builtin_kind
        : a.allow_direct_chat || !!a.builtin_kind),
  );
  return (
    <Dialog
      open
      title={t("chat.fork.title")}
      onClose={create.isPending ? undefined : onClose}
      width={560}
      footer={
        <>
          <Button
            variant="outline"
            disabled={create.isPending}
            onClick={onClose}
          >
            {t("common.cancel")}
          </Button>
          <Button
            disabled={unavailable}
            loading={create.isPending}
            onClick={() => create.mutate()}
          >
            {t("chat.fork.create")}
          </Button>
        </>
      }
    >
      <div className="space-y-4 text-body-sm">
        <p>{t("chat.fork.independent")}</p>
        <p>{t("chat.fork.limits")}</p>
        <p className="text-fg-muted">{t("chat.fork.notice")}</p>
        <label className="flex flex-col gap-2">
          {t("chat.fork.agent")}
          <select
            aria-label={t("chat.fork.agent")}
            disabled={create.isPending}
            value={agentId}
            className="rounded border border-line-default bg-surface-card p-2"
            onChange={(e) => {
              setAgentId(e.target.value);
              setMode("");
              setDirectory(undefined);
              create.reset();
            }}
          >
            <option value="">{t("chat.fork.chooseAgent")}</option>
            {candidates.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
        </label>
        {agents.isError && <p role="alert">{t("chat.fork.loadFailed")}</p>}
        <label className="flex flex-col gap-2">
          {t("chat.fork.directory")}
          <select
            aria-label={t("chat.fork.directory")}
            disabled={!agentId || create.isPending}
            value={mode}
            className="rounded border border-line-default bg-surface-card p-2"
            onChange={(e) => {
              setMode(e.target.value);
              create.reset();
            }}
          >
            <option value="">{t("chat.fork.chooseDirectory")}</option>
            <option value="DEFAULT">{t("chat.fork.defaultDirectory")}</option>
            <option value="CHOOSE">{t("chat.fork.selectDirectory")}</option>
            {conversation.task_id && work.data?.can_reuse && (
              <option value="REUSE">{t("chat.fork.reuse")}</option>
            )}
            {conversation.task_id && work.data?.can_set_baseline && (
              <option value="BASELINE">{t("chat.fork.baseline")}</option>
            )}
          </select>
        </label>
        {mode === "DEFAULT" && (
          <p className="text-fg-muted">{t("chat.fork.defaultHint")}</p>
        )}
        {mode === "CHOOSE" && (
          <WorkDirectoryPicker
            orgId={orgId}
            agentId={agentId}
            locked={false}
            busy={create.isPending}
            value={directory}
            onChange={setDirectory}
          />
        )}
        {mode === "BASELINE" && <BaseBranch value={base} onChange={setBase} />}
        <p className="text-fg-muted">{t("chat.fork.directoryHint")}</p>
        {create.isError && (
          <p role="alert" className="text-danger">
            {isApiError(create.error) &&
            create.error.code === "FORK_CONTEXT_TOO_LARGE"
              ? t("chat.fork.tooLarge")
              : chatErrorMessage(t, create.error)}
          </p>
        )}
      </div>
    </Dialog>
  );
}
