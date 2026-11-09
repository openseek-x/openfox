import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import type {
  ConversationSummary,
  ConversationWorkspaceSelection,
  Task,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Dialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  Switch,
  Tooltip,
  TextInput,
  useToast,
} from "@openfox/ui";
import { useMe } from "../../agents/queries";
import { useApi } from "../../context";
import { useAgentConfig } from "../../conversations/ChatPage";
import {
  useWorkspaceOptions,
  workspaceOptionsKey,
} from "../../conversations/WorkDirectoryPicker";
import { validWorkspaceRef } from "../../conversations/WorkDirectoryModes";
import { OverrideControls } from "../../conversations/OverrideControls";
import { useConversationOverrides } from "../../conversations/useOverrides";
import {
  chatErrorMessage,
  invalidateConversation,
  sameWorkspaceSelection,
  useConversationActions,
} from "../../conversations/queries";
import {
  useOpenArtifactList,
  ARTIFACT_PARAM,
} from "../../conversations/artifacts/params";
import { taskPagePath, useOpenTaskPage } from "../navigation";
import { copyText } from "../parts";
import { useAgentDirectory, useSpaceAgents } from "../queries";
import { useTaskConversations } from "./TaskConversations";
import {
  ConversationOfferDialog,
  type OfferKind,
} from "./ConversationOfferDialog";

interface Props {
  orgId: string;
  task: Task;
  conversation: ConversationSummary;
}
export function TaskConversationTools({
  orgId,
  task,
  conversation: c,
  readOnly,
}: Props & { readOnly: boolean }) {
  const t = useT(),
    me = useMe(),
    toast = useToast();
  const [mode, setMode] = useState<OfferKind | "new" | "rename" | null>(null);
  const [name, setName] = useState("");
  const [params] = useSearchParams();
  const openArtifacts = useOpenArtifactList(),
    actions = useConversationActions(orgId);
  const owner = !!me.data && me.data.id === c.owner_user_id;
  const open = !readOnly && !c.archived_at && c.lifecycle === "OPEN";
  const close = () => setMode(null);
  const copy = async () => {
    const ok = await copyText(
      new URL(
        taskPagePath(orgId, task.display_id, c.id),
        window.location.origin,
      ).href,
    );
    toast(
      t(ok ? "chat.header.linkCopied" : "chat.errors.generic"),
      ok ? undefined : { tone: "error" },
    );
  };
  const rename = () =>
    actions.rename.mutate({ id: c.id, title: name }, { onSuccess: close });
  return (
    <>
      <Menu
        label={t("tasks.detail.more")}
        placement="bottom-end"
        className="min-w-44"
        trigger={({ ref, toggle, open }) => (
          <IconButton
            ref={ref}
            label={t("tasks.detail.more")}
            aria-expanded={open}
            aria-haspopup="menu"
            icon={<Icon name="ellipsis" />}
            size="28"
            onClick={toggle}
          />
        )}
      >
        <MenuItem
          icon={<Icon name="openfox-new-chat" />}
          disabled={readOnly}
          onSelect={() => setMode("new")}
        >
          {t("tasks.detail.newConversation")}
        </MenuItem>
        <MenuSeparator />
        <MenuItem
          icon={<Icon name="user-plus" />}
          disabled={!open || !c.offer_participant}
          onSelect={() => setMode("INVITE")}
        >
          {t("tasks.detail.invite")}
        </MenuItem>
        <MenuItem
          icon={<Icon name="users" />}
          disabled={!open || !owner}
          onSelect={() => setMode("TRANSFER")}
        >
          {t("tasks.detail.transfer")}
        </MenuItem>
        <MenuSeparator />
        <MenuItem icon={<Icon name="link" />} onSelect={() => void copy()}>
          {t("chat.header.copyLink")}
        </MenuItem>
        <MenuItem
          icon={<Icon name="pencil" />}
          disabled={!owner || readOnly}
          onSelect={() => {
            setName(c.title ?? "");
            setMode("rename");
          }}
        >
          {t("chat.header.rename")}
        </MenuItem>
      </Menu>
      {!params.has(ARTIFACT_PARAM) && (
        <IconButton
          label={t("tasks.detail.artifactRail")}
          size="28"
          icon={<Icon name="panel-left" className="rotate-180" />}
          onClick={openArtifacts}
        />
      )}
      {mode === "rename" && (
        <Dialog
          open
          title={t("tasks.detail.renameTitle")}
          description={t("tasks.detail.renameHint")}
          onClose={close}
          footer={
            <>
              <Button variant="outline" onClick={close}>
                {t("common.cancel")}
              </Button>
              <Button
                disabled={!name.trim() || name.trim() === c.title}
                loading={actions.rename.isPending}
                onClick={rename}
              >
                {t("common.save")}
              </Button>
            </>
          }
        >
          <label className="flex flex-col gap-2 text-body-sm">
            {t("tasks.detail.chatName")}
            <TextInput
              data-autofocus
              maxLength={200}
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (
                  e.key === "Enter" &&
                  !e.nativeEvent.isComposing &&
                  name.trim() &&
                  !actions.rename.isPending
                )
                  rename();
              }}
            />
          </label>
        </Dialog>
      )}
      {(mode === "INVITE" || mode === "TRANSFER") && (
        <ConversationOfferDialog
          orgId={orgId}
          conversationId={c.id}
          kind={mode}
          assistant={c.agent.builtin_kind === "ASSISTANT"}
          canAssign={task.assignees.some(
            (x) => x.kind === "HUMAN" && x.id === me.data?.id,
          )}
          close={close}
        />
      )}
      {mode === "new" && (
        <NewTaskConversation
          orgId={orgId}
          task={task}
          conversation={c}
          close={close}
        />
      )}
    </>
  );
}

function NewTaskConversation({
  orgId,
  task,
  conversation: c,
  close,
}: Props & { close: () => void }) {
  const agents = useSpaceAgents(orgId, task.space_id),
    dir = useAgentDirectory(orgId),
    chats = useTaskConversations(orgId, task);
  const options = [
    ...(agents.data ?? [])
      .filter((a) => !a.archived_at && !a.builtin_kind)
      .map((a) => ({ id: a.id, name: a.name, crew: false })),
    ...(dir.data?.entries ?? [])
      .filter(
        (a) =>
          a.type === "crew" && !a.archived_at && a.space_id === task.space_id,
      )
      .map((a) => ({ id: a.id, name: a.name, crew: true })),
  ];
  const [actor, setActor] = useState(c.crew_id || c.agent_id),
    [body, setBody] = useState("");
  const selected = options.find((a) => a.id === actor);
  return (
    <NewTaskConversationForm
      key={actor}
      {...{
        orgId,
        task,
        source: c,
        close,
        actor,
        selected,
        body,
        setBody,
        options,
        chats: chats.list,
        setActor,
      }}
    />
  );
}
function NewTaskConversationForm({
  orgId,
  source,
  task,
  close,
  actor,
  selected,
  body,
  setBody,
  options,
  chats,
  setActor,
}: {
  orgId: string;
  source: ConversationSummary;
  task: Task;
  close: () => void;
  actor: string;
  selected: { id: string; name: string; crew: boolean } | undefined;
  body: string;
  setBody: (s: string) => void;
  options: { id: string; name: string; crew: boolean }[];
  chats: ConversationSummary[];
  setActor: (s: string) => void;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient(),
    toast = useToast(),
    openPage = useOpenTaskPage();
  const config = useAgentConfig(
    orgId,
    selected && !selected.crew ? task.space_id : undefined,
    selected && !selected.crew ? actor : "",
  );
  const overrides = useConversationOverrides({
    orgId,
    conversation: undefined,
    newDefaults: config.newDefaults,
    crew: !!selected?.crew,
    closed: false,
  });
  const work = useWorkspaceOptions(
    orgId,
    actor,
    !!selected && !selected.crew,
    source.id,
  );
  const [reuse, setReuse] = useState(false),
    [baseline, setBaseline] = useState(false);
  const [baseRef, setBaseRef] = useState("");
  const preparing =
    source.run?.state === "QUEUED" || source.run?.state === "PREPARING";
  const mayReuse =
    !preparing &&
    !!selected &&
    !selected.crew &&
    work.isSuccess &&
    work.data.can_reuse;
  const mayBranch =
    !preparing &&
    !!selected &&
    !selected.crew &&
    work.isSuccess &&
    work.data.can_set_baseline;
  const invalidBase = baseline && !validWorkspaceRef(baseRef);
  const selection: ConversationWorkspaceSelection | undefined = baseline
    ? {
        mode: "BASELINE",
        source_conversation_id: source.id,
        ...(baseRef.trim() ? { base_ref: baseRef.trim() } : {}),
      }
    : reuse
      ? { mode: "REUSE", source_conversation_id: source.id }
      : undefined;
  const unavailable =
    (baseline && !mayBranch) || (!baseline && reuse && !mayReuse);
  const attempt = useRef({
    id: crypto.randomUUID(),
    created: false,
    titled: false,
    body: "",
    message: "",
  });
  const [created, setCreated] = useState(false);
  const start = useMutation({
    mutationFn: async () => {
      if (!selected || !body.trim())
        throw new Error("missing recipient or message");
      const a = attempt.current;
      if (!a.created) {
        try {
          await api.conversations.create(orgId, {
            id: a.id,
            space_id: task.space_id,
            task_id: task.id,
            ...(selected.crew ? { crew_id: actor } : { agent_id: actor }),
            visibility: "SHARED",
            ...(selection ? { workspace_selection: selection } : {}),
          });
        } catch (err) {
          const existing = await api.conversations
            .get(orgId, a.id)
            .catch(() => null);
          if (
            !existing ||
            !sameWorkspaceSelection(selection, existing.workspace_selection)
          )
            throw err;
        }
        a.created = true;
        setCreated(true);
        void cache.invalidateQueries({
          queryKey: workspaceOptionsKey(orgId, actor),
        });
      }
      if (!a.titled) {
        await api.conversations.update(orgId, a.id, {
          title: Array.from(body.trim().split(/\s*\n/)[0] || body.trim())
            .slice(0, 80)
            .join(""),
        });
        a.titled = true;
      }
      await overrides.applyDraft(a.id);
      if (a.body !== body) {
        a.body = body;
        a.message = crypto.randomUUID();
      }
      await api.conversations.postMessage(orgId, a.id, {
        body: body.trim(),
        client_message_id: a.message,
        ...(selected.crew ? {} : { mentions: [{ agent_id: actor }] }),
      });
      return a.id;
    },
    onSuccess: (id) => {
      invalidateConversation(cache, orgId, id, true);
      close();
      openPage(task.display_id, id);
    },
    onError: (e) =>
      toast(
        attempt.current.created && attempt.current.message
          ? t("chat.workspace.sendFailed")
          : chatErrorMessage(t, e),
        { tone: "error" },
      ),
  });
  return (
    <Dialog
      open
      width={512}
      title={t("tasks.detail.newDialog")}
      description={t("tasks.detail.newHint")}
      onClose={start.isPending ? undefined : close}
      footer={
        <>
          <Button variant="outline" disabled={start.isPending} onClick={close}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={
              !selected ||
              !body.trim() ||
              (!created && (invalidBase || unavailable))
            }
            loading={start.isPending}
            onClick={() => start.mutate()}
          >
            {t("tasks.detail.startChat")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4 text-body-sm">
        <label className="flex flex-col gap-1">
          {t("tasks.detail.chooseActor")}
          <select
            data-autofocus
            className="h-9 rounded-md border border-line-default bg-surface-card px-2"
            value={actor}
            disabled={created || start.isPending}
            onChange={(e) => setActor(e.target.value)}
          >
            {!selected && <option value={actor}>{t("common.select")}</option>}
            {options.map((a) => {
              const n = chats.filter(
                (c) => (c.crew_id || c.agent_id) === a.id,
              ).length;
              return (
                <option key={a.id} value={a.id}>
                  {a.name}
                  {n
                    ? " · " + t("tasks.detail.alreadyChats", { count: n })
                    : ""}
                </option>
              );
            })}
          </select>
        </label>
        {overrides.controls && (
          <div className="flex flex-col gap-1">
            <span>{t("tasks.detail.runtimeModel")}</span>
            <div className="flex flex-wrap items-center gap-1">
              <OverrideControls {...overrides.controls} hideApproval />
            </div>
          </div>
        )}
        <div className="flex flex-col items-start gap-0.5 rounded-lg bg-surface-muted p-3">
          {(["reuse", "baseline"] as const).map((kind) => {
            const active = kind === "reuse" ? reuse : baseline;
            const available = kind === "reuse" ? mayReuse : mayBranch;
            if (
              kind === "baseline" &&
              mayReuse &&
              !work.data?.can_set_baseline &&
              !baseline
            )
              return null;
            const reason = preparing
              ? t("chat.workspace.preparing")
              : !selected || selected.crew
                ? t("chat.workspace.sourceMissing")
                : work.isPending
                  ? t("chat.workspace.loading")
                  : work.isError
                    ? t("chat.workspace.failed")
                    : t(
                        kind === "reuse"
                          ? "chat.workspace.sourceMissing"
                          : "chat.workspace.baseUnavailable",
                      );
            const label = t(
              kind === "reuse"
                ? "chat.workspace.reuse"
                : "chat.workspace.baseline",
            );
            const info = t(
              kind === "reuse"
                ? "chat.workspace.reuseHint"
                : "chat.workspace.baselineHint",
            );
            const toggle = (next: boolean) => {
              if (kind === "reuse") setReuse(next);
              else {
                setBaseline(next);
                if (next)
                  setBaseRef(source.workspace_selection?.base_ref ?? "");
              }
            };
            return (
              <label
                key={kind}
                title={available ? undefined : reason}
                className={`flex w-fit items-center gap-2.5 rounded-lg px-2 py-1 ${!available ? "opacity-60" : ""}`}
              >
                <Switch
                  label={label}
                  checked={active}
                  disabled={
                    created || start.isPending || (!available && !active)
                  }
                  onCheckedChange={toggle}
                />
                <span>{label}</span>
                <Tooltip content={info}>
                  <span
                    role="note"
                    tabIndex={0}
                    aria-label={info}
                    onClick={(e) => {
                      e.preventDefault();
                      e.stopPropagation();
                    }}
                  >
                    <Icon name="info" size={14} />
                  </span>
                </Tooltip>
              </label>
            );
          })}
          {baseline && (
            <div className="w-full pl-8">
              <TextInput
                aria-label={t("chat.workspace.base")}
                placeholder={t("chat.workspace.basePlaceholder")}
                value={baseRef}
                disabled={created || start.isPending}
                aria-invalid={invalidBase}
                onChange={(e) => setBaseRef(e.target.value)}
              />
              {invalidBase && (
                <p className="text-caption text-danger">
                  {t("chat.workspace.invalidRef")}
                </p>
              )}
            </div>
          )}
        </div>
        <label className="flex flex-col gap-1">
          {t("tasks.detail.firstMessage")}
          <textarea
            placeholder={t("tasks.detail.firstMessageHint")}
            className="min-h-24 resize-y rounded-md border border-line-default bg-surface-card p-2"
            value={body}
            disabled={start.isPending}
            onChange={(e) => setBody(e.target.value)}
          />
        </label>
      </div>
    </Dialog>
  );
}
