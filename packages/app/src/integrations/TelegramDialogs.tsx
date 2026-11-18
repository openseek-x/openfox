import { useEffect, useMemo, useRef, useState } from "react";
import {
  telegramApi,
  type TelegramBot,
  type TelegramPreparation,
  type TelegramConnectInput,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Dialog,
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
import { runtimeOptions } from "../agents/CreateAgentDialog";
import { runtimeChoices } from "../agents/model";
import { telegramError } from "./telegram-model";

// Mutation inputs can contain secrets. Keep them outside React Query's cache.
export function useTelegramAction() {
  const t = useT(),
    controller = useRef<AbortController | null>(null);
  const mounted = useRef(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      controller.current?.abort();
    };
  }, []);
  const run = async (action: (signal: AbortSignal) => Promise<void>) => {
    if (controller.current) return;
    const current = new AbortController();
    controller.current = current;
    setBusy(true);
    setError("");
    try {
      await action(current.signal);
    } catch (e) {
      if (mounted.current && !current.signal.aborted)
        setError(telegramError(t, e));
    } finally {
      if (controller.current === current) controller.current = null;
      if (mounted.current) setBusy(false);
    }
  };
  return { busy, error, run };
}

export function TelegramConnectDialog({
  orgId,
  bots,
  bot,
  recover = false,
  onClose,
  onSaved,
}: {
  orgId: string;
  bots: TelegramBot[];
  bot?: TelegramBot;
  recover?: boolean;
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useT(),
    api = useApi(),
    action = useTelegramAction();
  const role = useOrgRole(orgId),
    directory = useDirectory(orgId),
    spaces = useSpaces(orgId),
    devices = useDevices(orgId),
    me = useMe();
  const [kind, setKind] = useState(bot?.kind ?? "AGENT"),
    [agentId, setAgent] = useState<string | null>(bot?.agent.id ?? null),
    [spaceId, setSpace] = useState<string | null>(bot?.space?.id ?? null);
  const [runtime, setRuntime] = useState<string | null>(null),
    [extra, setExtra] = useState(bot?.extra_prompt ?? ""),
    [token, setToken] = useState("");
  const [proof, setProof] = useState<TelegramPreparation | null>(null),
    [consent, setConsent] = useState(false),
    [recoveryConsent, setRecoveryConsent] = useState(false);
  const operation = useRef(crypto.randomUUID());
  const choices = useMemo(
    () => runtimeChoices(devices.data ?? [], spaceId ?? "", me.data?.id ?? ""),
    [devices.data, spaceId, me.data?.id],
  );
  const admin = role === "OWNER" || role === "ADMIN";
  const agentOptions = (directory.data?.entries ?? [])
    .filter(
      (e) =>
        e.type === "agent" &&
        !e.archived_at &&
        (admin || e.mine) &&
        !bots.some((b) => b.agent.id === e.id),
    )
    .map((e) => ({ value: e.id, label: e.name }));
  const selectedRuntime = choices.find((c) => c.value === runtime);
  const valid =
    token.length >= 3 &&
    token.length <= 512 &&
    token.trim() === token &&
    (!recover || recoveryConsent) &&
    (bot ||
      (kind === "AGENT"
        ? agentOptions.some((a) => a.value === agentId)
        : spaceId && selectedRuntime && [...extra].length <= 4000));
  const prepare = () =>
    action.run(async (signal) => {
      const input: TelegramConnectInput = {
        operation_key: operation.current,
        kind,
        version: bot?.version ?? 0,
        token,
        extra_prompt: kind === "TASK_ASSISTANT" ? extra : "",
        recover_webhook: recover,
        ...(bot ? { bot_id: bot.id } : {}),
        ...(kind === "AGENT"
          ? { agent_id: agentId! }
          : {
              space_id: spaceId!,
              runtime: bot?.runtime
                ? {
                    computer_id: bot.runtime.computer_id,
                    grant_id: bot.runtime.grant_id,
                    runtime: bot.runtime.runtime,
                    model: bot.runtime.model,
                  }
                : {
                    computer_id: selectedRuntime!.deviceId,
                    grant_id: selectedRuntime!.grantId,
                    runtime: selectedRuntime!.provider,
                  },
            }),
      };
      // Clear the visible token before awaiting; only the current request retains it.
      setToken("");
      const next = await telegramApi(api.http).prepare(orgId, input, signal);
      if (!signal.aborted) {
        setProof(next);
        setConsent(false);
      }
    });
  return (
    <Dialog
      open
      onClose={onClose}
      title={t(
        proof
          ? "integrations.telegram.preparation"
          : recover
            ? "integrations.telegram.recover"
            : bot
              ? "integrations.telegram.rotate"
              : "integrations.telegram.create",
      )}
      width={560}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.close")}
          </Button>
          {proof ? (
            <Button
              loading={action.busy}
              disabled={
                !consent ||
                proof.status !== "PENDING_CONFIRM" ||
                Date.parse(proof.expires_at) <= Date.now()
              }
              onClick={() =>
                void action.run(async (signal) => {
                  await telegramApi(api.http).confirm(
                    orgId,
                    proof.id,
                    {
                      version: proof.version,
                      remote_bot_id: proof.remote_bot_id,
                    },
                    signal,
                  );
                  if (!signal.aborted) {
                    onSaved();
                    onClose();
                  }
                })
              }
            >
              {t("integrations.telegram.confirmConnection")}
            </Button>
          ) : (
            <Button
              loading={action.busy}
              disabled={!valid}
              onClick={() => void prepare()}
            >
              {t("integrations.telegram.prepare")}
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Callout tone="info">{t("integrations.telegram.permanent")}</Callout>
        {action.error && <p role="alert">{action.error}</p>}
        {proof ? (
          <>
            <p>
              {t("integrations.telegram.remoteID")}:{" "}
              <code>{proof.remote_bot_id}</code>
            </p>
            <p>
              {t("integrations.telegram.username")}: @{proof.username} ·{" "}
              {proof.bot_name}
            </p>
            <p>
              {t("integrations.telegram.kind")}:{" "}
              {t(
                proof.kind === "AGENT"
                  ? "integrations.telegram.agent"
                  : "integrations.telegram.assistant",
              )}{" "}
              ·{" "}
              {bot?.space?.name ??
                bot?.agent.name ??
                (proof.kind === "AGENT"
                  ? agentOptions.find((a) => a.value === proof.agent_id)?.label
                  : spaces.data?.find((s) => s.id === proof.space_id)?.name)}
            </p>
            <p>
              {t("integrations.telegram.expires", {
                time: new Date(proof.expires_at).toLocaleString(),
              })}
            </p>
            <Callout tone="info">
              {t("integrations.telegram.preparedOnly")}
            </Callout>
            <label className="flex items-start gap-2">
              <input
                type="checkbox"
                checked={consent}
                onChange={(e) => setConsent(e.target.checked)}
              />
              {t("integrations.telegram.confirmBot")}
            </label>
          </>
        ) : (
          <>
            {!bot ? (
              <>
                <label>
                  {t("integrations.telegram.kind")}
                  <Select
                    aria-label={t("integrations.telegram.kind")}
                    value={kind}
                    onChange={(v) => {
                      setKind(v ?? "AGENT");
                      operation.current = crypto.randomUUID();
                    }}
                    options={[
                      {
                        value: "AGENT",
                        label: t("integrations.telegram.agent"),
                      },
                      {
                        value: "TASK_ASSISTANT",
                        label: t("integrations.telegram.assistant"),
                      },
                    ]}
                  />
                </label>
                {kind === "AGENT" ? (
                  <label>
                    {t("integrations.telegram.agent")}
                    <Select
                      aria-label={t("integrations.telegram.agent")}
                      value={agentId}
                      onChange={setAgent}
                      options={agentOptions}
                    />
                  </label>
                ) : (
                  <>
                    <label>
                      {t("integrations.telegram.space")}
                      <Select
                        aria-label={t("integrations.telegram.space")}
                        value={spaceId}
                        onChange={(v) => {
                          setSpace(v);
                          setRuntime(null);
                        }}
                        options={(spaces.data ?? []).map((s) => ({
                          value: s.id,
                          label: s.name,
                        }))}
                      />
                    </label>
                    <label>
                      {t("integrations.telegram.runtime")}
                      <Select
                        aria-label={t("integrations.telegram.runtime")}
                        value={runtime}
                        onChange={setRuntime}
                        options={runtimeOptions(choices, t)}
                      />
                    </label>
                    <label>
                      {t("integrations.telegram.extraPrompt")}
                      <Textarea
                        aria-label={t("integrations.telegram.extraPrompt")}
                        value={extra}
                        onChange={(e) => setExtra(e.target.value)}
                        rows={3}
                      />
                    </label>
                  </>
                )}
              </>
            ) : (
              <p>
                {bot.space?.name ?? bot.agent.name} · @{bot.username}
              </p>
            )}
            <label>
              {t("integrations.telegram.token")}
              <TextInput
                aria-label={t("integrations.telegram.token")}
                type="password"
                autoComplete="off"
                value={token}
                maxLength={512}
                onChange={(e) => {
                  setToken(e.target.value);
                  operation.current = crypto.randomUUID();
                }}
              />
            </label>
            <p className="text-body-sm text-fg-muted">
              {t("integrations.telegram.tokenHelp")}
            </p>
            {recover && (
              <label className="flex items-start gap-2">
                <input
                  type="checkbox"
                  checked={recoveryConsent}
                  onChange={(e) => setRecoveryConsent(e.target.checked)}
                />
                {t("integrations.telegram.recoverConsent")}
              </label>
            )}
          </>
        )}
      </div>
    </Dialog>
  );
}

export function TelegramDisconnectDialog({
  orgId,
  bot,
  onClose,
  onSaved,
}: {
  orgId: string;
  bot: TelegramBot;
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useT(),
    api = useApi(),
    action = useTelegramAction(),
    [mode, setMode] = useState<"local" | "remote_then_local">("local");
  return (
    <Dialog
      open
      onClose={onClose}
      title={t("integrations.telegram.disconnect")}
      description={t("integrations.telegram.disconnectHelp")}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={action.busy}
            onClick={() =>
              void action.run(async (signal) => {
                await telegramApi(api.http).disconnect(
                  orgId,
                  bot.id,
                  { version: bot.version, mode },
                  signal,
                );
                if (!signal.aborted) {
                  onSaved();
                  onClose();
                }
              })
            }
          >
            {t("integrations.telegram.disconnect")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        {action.error && <p role="alert">{action.error}</p>}
        <label>
          <input
            type="radio"
            name="disconnect"
            checked={mode === "local"}
            onChange={() => setMode("local")}
          />{" "}
          {t("integrations.telegram.local")}
        </label>
        <label>
          <input
            type="radio"
            name="disconnect"
            checked={mode === "remote_then_local"}
            onChange={() => setMode("remote_then_local")}
            disabled={
              bot.control_status === "SENDING" ||
              bot.control_status === "UNKNOWN"
            }
          />{" "}
          {t("integrations.telegram.remote")}
        </label>
        <p>{t("integrations.telegram.remoteHelp")}</p>
      </div>
    </Dialog>
  );
}
