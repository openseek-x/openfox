import { useEffect, useMemo, useState } from "react";
import type {
  DiscordBot,
  DiscordConnectInput,
  DiscordPreparation,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Checkbox,
  Dialog,
  Field,
  Select,
  TextInput,
  Textarea,
} from "@openfox/ui";
import { useApi } from "../context";
import {
  useDevices,
  useDirectory,
  useMe,
  useOrgRole,
  useSpaces,
} from "../agents/queries";
import { runtimeChoices } from "../agents/model";
import { runtimeOptions } from "../agents/CreateAgentDialog";
import { discordError, useDiscordWrite } from "./discord-hooks";
export type DiscordTarget = {
  kind: "AGENT" | "TASK_ASSISTANT";
  agentId?: string;
  spaceId?: string;
};
export function DiscordConnectDialog({
  orgId,
  target,
  current,
  onClose,
  refresh,
}: {
  orgId: string;
  target: DiscordTarget;
  current?: DiscordBot;
  onClose: () => void;
  refresh: () => void;
}) {
  const t = useT(),
    api = useApi(),
    write = useDiscordWrite(refresh),
    directory = useDirectory(orgId),
    spaces = useSpaces(orgId),
    devices = useDevices(orgId),
    me = useMe(),
    role = useOrgRole(orgId);
  const [selected, setSelected] = useState<string | null>(
      current?.space?.id ??
        current?.agent.id ??
        target.spaceId ??
        target.agentId ??
        null,
    ),
    [runtime, setRuntime] = useState<string | null>(null),
    [model, setModel] = useState(""),
    [extra, setExtra] = useState(""),
    [token, setToken] = useState(""),
    [prepared, setPrepared] = useState<DiscordPreparation>(),
    [confirmed, setConfirmed] = useState(false),
    [operation] = useState(() => crypto.randomUUID());
  useEffect(() => {
    const timer = setTimeout(() => setToken(""), 10 * 60_000);
    return () => clearTimeout(timer);
  }, []);
  const choices = useMemo(
    () =>
      runtimeChoices(
        devices.data ?? [],
        target.kind === "TASK_ASSISTANT" ? (selected ?? "") : "",
        me.data?.id ?? "",
      ),
    [devices.data, target.kind, selected, me.data?.id],
  );
  const choice = choices.find((c) => c.value === runtime),
    fixed = !!current || !!target.agentId || !!target.spaceId;
  const options =
    target.kind === "AGENT"
      ? (directory.data?.entries ?? [])
          .filter(
            (a) =>
              a.type === "agent" &&
              !a.archived_at &&
              (a.mine || role === "OWNER" || role === "ADMIN"),
          )
          .map((a) => ({ value: a.id, label: a.name }))
      : (spaces.data ?? []).map((s) => ({ value: s.id, label: s.name }));
  const valid =
    !!selected &&
    token.length > 0 &&
    token.length <= 4096 &&
    !/\s/.test(token) &&
    Array.from(extra).length <= 4000 &&
    (target.kind === "AGENT" ||
      !!current?.runtime ||
      (!!choice && choice.block === null));
  function close() {
    setToken("");
    onClose();
  }
  function prepare() {
    if (!valid || !selected) return;
    const input: DiscordConnectInput = {
      operation_key: operation,
      kind: target.kind,
      version: current?.version ?? 0,
      token,
      extra_prompt: current?.extra_prompt ?? extra,
      bot_id: current?.id,
      agent_id: target.kind === "AGENT" ? selected : undefined,
      space_id: target.kind === "TASK_ASSISTANT" ? selected : undefined,
      runtime:
        target.kind === "TASK_ASSISTANT"
          ? current?.runtime
            ? {
                computer_id: current.runtime.computer_id,
                grant_id: current.runtime.grant_id,
                runtime: current.runtime.runtime,
                model: current.runtime.model,
              }
            : choice
              ? {
                  computer_id: choice.deviceId,
                  grant_id: choice.grantId,
                  runtime: choice.provider,
                  model: model.trim() || "default",
                }
              : undefined
          : undefined,
    };
    setToken("");
    void write.run(
      (signal) => api.discord.prepare(orgId, input, signal),
      setPrepared,
    );
  }
  return (
    <Dialog
      open
      title={t(
        current?.state === "DISCONNECTED"
          ? "integrations.discord.reconnect"
          : current
            ? "integrations.discord.rotate"
            : "integrations.discord.connect",
      )}
      onClose={close}
      width={600}
      footer={
        <>
          <Button variant="outline" onClick={close}>
            {t("common.cancel")}
          </Button>
          {prepared ? (
            <Button
              disabled={!confirmed || write.busy || write.blocked}
              onClick={() =>
                void write.run(
                  (signal) =>
                    api.discord.confirm(
                      orgId,
                      prepared.id,
                      {
                        version: prepared.version,
                        remote_bot_id: prepared.remote_bot_id,
                        application_id: prepared.application_id,
                      },
                      signal,
                    ),
                  close,
                )
              }
            >
              {t("integrations.discord.confirm")}
            </Button>
          ) : (
            <Button
              disabled={!valid || write.busy || write.blocked}
              onClick={prepare}
            >
              {t("integrations.discord.prepare")}
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {prepared ? (
          <>
            <Callout tone="info">
              {t("integrations.discord.confirmHelp")}
            </Callout>
            <p>
              {t("integrations.discord.verified")}: {prepared.username}
            </p>
            <p>
              {t("integrations.discord.botID")}: {prepared.remote_bot_id}
            </p>
            <p>
              {t("integrations.discord.application")}: {prepared.application_id}
            </p>
            <p>
              {t("integrations.discord.expires", {
                time: new Date(prepared.expires_at).toLocaleString(),
              })}
            </p>
            <p>{t("integrations.discord.tokenCleared")}</p>
            <label className="flex gap-2">
              <Checkbox
                checked={confirmed}
                onChange={(e) => setConfirmed(e.target.checked)}
              />
              {t("integrations.discord.confirmMe")}
            </label>
          </>
        ) : (
          <>
            <Field
              label={t(
                target.kind === "AGENT"
                  ? "integrations.discord.agent"
                  : "integrations.discord.space",
              )}
            >
              <Select
                value={selected}
                onChange={(v) => {
                  setSelected(v);
                  setRuntime(null);
                }}
                options={options}
                disabled={fixed}
                placeholder={t("integrations.discord.noTargets")}
              />
            </Field>
            {target.kind === "TASK_ASSISTANT" && !current && (
              <>
                <Field label={t("integrations.discord.runtime")}>
                  <Select
                    value={runtime}
                    onChange={setRuntime}
                    options={runtimeOptions(choices, t)}
                    disabled={!selected}
                  />
                </Field>
                <Field label={t("integrations.discord.model")}>
                  <TextInput
                    value={model}
                    onChange={(e) => setModel(e.target.value)}
                  />
                </Field>
                <Field label={t("integrations.discord.extra")}>
                  <Textarea
                    rows={3}
                    value={extra}
                    onChange={(e) => setExtra(e.target.value)}
                  />
                </Field>
              </>
            )}
            <Field
              label={t("integrations.discord.token")}
              helper={t("integrations.discord.tokenHelp")}
            >
              <TextInput
                type="password"
                autoComplete="off"
                value={token}
                onChange={(e) => setToken(e.target.value)}
              />
            </Field>
          </>
        )}
        {!!write.error && (
          <Callout tone="danger">{discordError(t, write.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}
