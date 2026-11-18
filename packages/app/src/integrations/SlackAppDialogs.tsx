import { useEffect, useMemo, useState } from "react";
import {
  newIdempotencyKey,
  type SlackApp,
  type SlackConfiguration,
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
import { useSlackWrite } from "./slack-hooks";
import { slackBodyFits, slackError, slackID } from "./slack-model";

export function SlackCreateDialog({
  orgId,
  config,
  purpose,
  onClose,
  refresh,
}: {
  orgId: string;
  config: SlackConfiguration;
  purpose: "agent" | "task_assistant";
  onClose: () => void;
  refresh: () => void;
}) {
  const t = useT(),
    api = useApi(),
    write = useSlackWrite(refresh),
    directory = useDirectory(orgId),
    spaces = useSpaces(orgId),
    devices = useDevices(orgId),
    me = useMe(),
    role = useOrgRole(orgId);
  const parents = config.apps.filter(
    (a) =>
      a.kind === "CENTRAL" &&
      a.state === "ACTIVE" &&
      config.identities.some((i) => i.parent_app_id === a.id && i.active),
  );
  const [parent, setParent] = useState<string | null>(parents[0]?.id ?? null),
    [target, setTarget] = useState<string | null>(null),
    [runtime, setRuntime] = useState<string | null>(null),
    [model, setModel] = useState(""),
    [name, setName] = useState(""),
    [extra, setExtra] = useState(""),
    [secret, setSecret] = useState(""),
    [consent, setConsent] = useState(false),
    [submitted, setSubmitted] = useState(false),
    [bodyTooLarge, setBodyTooLarge] = useState(false);
  useEffect(() => {
    const timer = setTimeout(() => {
      setSecret("");
      setConsent(false);
    }, 15 * 60_000);
    return () => clearTimeout(timer);
  }, []);
  const blocked = config.blocked_targets ?? [];
  const targets =
    purpose === "agent"
      ? (directory.data?.entries ?? [])
          .filter(
            (a) =>
              a.type === "agent" &&
              !a.archived_at &&
              (a.mine || role === "OWNER" || role === "ADMIN") &&
              !blocked.some((b) => b.kind === "AGENT" && b.agent_id === a.id),
          )
          .map((a) => ({ value: a.id, label: a.name }))
      : (spaces.data ?? [])
          .filter(
            (s) =>
              !blocked.some(
                (b) => b.kind === "TASK_ASSISTANT" && b.space_id === s.id,
              ),
          )
          .map((s) => ({ value: s.id, label: s.name }));
  const choices = useMemo(
    () =>
      runtimeChoices(
        devices.data ?? [],
        purpose === "task_assistant" ? (target ?? "") : "",
        me.data?.id ?? "",
      ),
    [devices.data, target, purpose, me.data?.id],
  );
  const choice = choices.find((c) => c.value === runtime),
    selectedParent = parents.find((a) => a.id === parent);
  const valid =
    !!selectedParent &&
    targets.some((a) => a.value === target) &&
    name.trim().length > 0 &&
    Array.from(name.trim()).length <= 35 &&
    Array.from(extra).length <= 4000 &&
    new TextEncoder().encode(secret).length >= 8 &&
    new TextEncoder().encode(secret).length <= 4096 &&
    consent &&
    (purpose === "agent" || (!!choice && choice.block === null));
  function close() {
    setSecret("");
    onClose();
  }
  function submit() {
    if (!valid || !selectedParent) return;
    const input = {
      parent_app_id: selectedParent.id,
      parent_version: selectedParent.version,
      purpose,
      name: name.trim(),
      agent_id: purpose === "agent" ? target! : "",
      space_id: purpose === "task_assistant" ? target! : "",
      runtime:
        purpose === "task_assistant" && choice
          ? {
              computer_id: choice.deviceId,
              grant_id: choice.grantId,
              runtime: choice.provider,
              model: model.trim() || undefined,
            }
          : undefined,
      extra_prompt: extra,
      idempotency_key: newIdempotencyKey(),
      configuration_refresh_token: secret,
    };
    setBodyTooLarge(!slackBodyFits(input));
    if (!slackBodyFits(input)) return;
    setSecret("");
    setSubmitted(true);
    void write.run(
      (signal) => api.slack.createApp(orgId, input, signal),
      close,
    );
  }
  return (
    <Dialog
      open
      title={t(
        purpose === "agent"
          ? "integrations.slack.createAgent"
          : "integrations.slack.createAssistant",
      )}
      onClose={close}
      width={620}
      footer={
        <>
          <Button variant="outline" onClick={close}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={!valid || write.busy || write.blocked}
            onClick={submit}
          >
            {t("integrations.slack.create")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Callout tone="info">{t("integrations.slack.targetLimit")}</Callout>
        <Field label={t("integrations.slack.workspace")}>
          <Select
            value={parent}
            onChange={setParent}
            options={parents.map((a) => ({
              value: a.id,
              label: `${a.name || a.team_id} · ${a.team_id}`,
            }))}
          />
        </Field>
        <Field
          label={t(
            purpose === "agent"
              ? "integrations.slack.agent"
              : "integrations.slack.space",
          )}
        >
          <Select
            value={target}
            onChange={(value) => {
              setTarget(value);
              setRuntime(null);
            }}
            options={targets}
            placeholder={t("integrations.slack.noTargets")}
          />
        </Field>
        {purpose === "task_assistant" && (
          <>
            <Field label={t("integrations.slack.runtime")}>
              <Select
                value={runtime}
                onChange={(value) => {
                  setRuntime(value);
                  setModel("");
                }}
                options={runtimeOptions(choices, t)}
                disabled={!target}
              />
            </Field>
            <Field
              label={t("integrations.slack.model")}
              helper={t("integrations.slack.modelHelp")}
            >
              <TextInput
                value={model}
                onChange={(e) => setModel(e.target.value)}
              />
            </Field>
            <Field label={t("integrations.slack.extraPrompt")}>
              <Textarea
                value={extra}
                onChange={(e) => setExtra(e.target.value)}
                rows={3}
              />
            </Field>
          </>
        )}
        <Field
          label={t("integrations.slack.name")}
          helper={t("integrations.slack.nameLimit")}
        >
          <TextInput value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Callout tone="warning">{t("integrations.slack.tokenWarning")}</Callout>
        <Field label={t("integrations.slack.refreshToken")}>
          <TextInput
            type="password"
            autoComplete="off"
            spellCheck={false}
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            maxLength={4096}
            disabled={write.busy || write.blocked}
          />
        </Field>
        <label className="flex items-start gap-2 text-body-sm">
          <Checkbox
            checked={consent}
            onChange={(e) => setConsent(e.target.checked)}
          />
          <span>{t("integrations.slack.tokenConsent")}</span>
        </label>
        {submitted && (
          <p className="text-caption text-fg-muted">
            {t("integrations.slack.tokenCleared")}
          </p>
        )}
        {bodyTooLarge && (
          <Callout tone="danger">
            {t("integrations.slack.bodyTooLarge")}
          </Callout>
        )}
        {!!write.error && (
          <Callout tone="danger">{slackError(t, write.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}
export function SlackCredentialsDialog({
  orgId,
  app,
  mode,
  refresh,
  onClose,
}: {
  orgId: string;
  app: SlackApp;
  mode: "recover" | "configure";
  refresh: () => void;
  onClose: () => void;
}) {
  const t = useT(),
    api = useApi(),
    write = useSlackWrite(refresh);
  const [id, setID] = useState(app.external_app_id),
    [client, setClient] = useState(""),
    [secret, setSecret] = useState(""),
    [signing, setSigning] = useState(""),
    [token, setToken] = useState(""),
    [consent, setConsent] = useState(false),
    [known, setKnown] = useState(false),
    [submitted, setSubmitted] = useState(false),
    [bodyTooLarge, setBodyTooLarge] = useState(false);
  const clear = () => {
    setToken("");
    setSecret("");
    setSigning("");
  };
  useEffect(() => {
    const timer = setTimeout(clear, 15 * 60_000);
    return () => clearTimeout(timer);
  }, []);
  function close() {
    clear();
    onClose();
  }
  const valid =
    consent &&
    new TextEncoder().encode(token).length >= 8 &&
    new TextEncoder().encode(token).length <= 4096 &&
    (mode === "configure" ||
      (known && slackID(id) && !!client && !!secret && !!signing));
  function submit() {
    if (!valid) return;
    const input = {
      version: app.version,
      external_app_id: id,
      client_id: client,
      client_secret: secret,
      signing_secret: signing,
      configuration_refresh_token: token,
    };
    setBodyTooLarge(
      !slackBodyFits(
        mode === "configure"
          ? { version: app.version, configuration_refresh_token: token }
          : input,
      ),
    );
    if (
      !slackBodyFits(
        mode === "configure"
          ? { version: app.version, configuration_refresh_token: token }
          : input,
      )
    )
      return;
    clear();
    setSubmitted(true);
    void write.run(
      (signal) =>
        mode === "configure"
          ? api.slack.configure(
              orgId,
              app.id,
              {
                version: app.version,
                configuration_refresh_token: input.configuration_refresh_token,
              },
              signal,
            )
          : api.slack.recover(orgId, app.id, input, signal),
      close,
    );
  }
  return (
    <Dialog
      open
      title={t(
        mode === "recover"
          ? "integrations.slack.recover"
          : "integrations.slack.refreshConfiguration",
      )}
      width={600}
      onClose={close}
      footer={
        <>
          <Button variant="outline" onClick={close}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={!valid || write.busy || write.blocked}
            onClick={submit}
          >
            {t("integrations.slack.submit")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {mode === "recover" && (
          <>
            <Callout tone="warning">
              {t("integrations.slack.recoverHelp")}
            </Callout>
            <Field label={t("integrations.slack.externalAppID")}>
              <TextInput
                value={id}
                onChange={(e) => setID(e.target.value)}
                disabled={!!app.external_app_id || write.busy || write.blocked}
              />
            </Field>
            <Field label={t("integrations.slack.clientID")}>
              <TextInput
                value={client}
                onChange={(e) => setClient(e.target.value)}
                autoComplete="off"
                disabled={write.busy || write.blocked}
              />
            </Field>
            <Field label={t("integrations.slack.clientSecret")}>
              <TextInput
                type="password"
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                autoComplete="off"
                disabled={write.busy || write.blocked}
                maxLength={4096}
              />
            </Field>
            <Field label={t("integrations.slack.signingSecret")}>
              <TextInput
                type="password"
                value={signing}
                onChange={(e) => setSigning(e.target.value)}
                autoComplete="off"
                disabled={write.busy || write.blocked}
                maxLength={4096}
              />
            </Field>
            <label className="flex items-start gap-2 text-body-sm">
              <Checkbox
                checked={known}
                onChange={(e) => setKnown(e.target.checked)}
              />
              <span>{t("integrations.slack.knownAppConsent")}</span>
            </label>
          </>
        )}
        <Callout tone="warning">{t("integrations.slack.tokenWarning")}</Callout>
        <Field label={t("integrations.slack.refreshToken")}>
          <TextInput
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            autoComplete="off"
            disabled={write.busy || write.blocked}
            maxLength={4096}
          />
        </Field>
        <label className="flex items-start gap-2 text-body-sm">
          <Checkbox
            checked={consent}
            onChange={(e) => setConsent(e.target.checked)}
          />
          <span>{t("integrations.slack.tokenConsent")}</span>
        </label>
        {submitted && (
          <p className="text-caption text-fg-muted">
            {t("integrations.slack.tokenCleared")}
          </p>
        )}
        {bodyTooLarge && (
          <Callout tone="danger">
            {t("integrations.slack.bodyTooLarge")}
          </Callout>
        )}
        {!!write.error && (
          <Callout tone="danger">{slackError(t, write.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}
export function SlackStopDialog({
  orgId,
  app,
  action,
  onClose,
  refresh,
}: {
  orgId: string;
  app: SlackApp;
  action: "cancel" | "disconnect";
  onClose: () => void;
  refresh: () => void;
}) {
  const t = useT(),
    api = useApi(),
    write = useSlackWrite(refresh),
    [confirmed, setConfirmed] = useState(false);
  const cleanup =
    action === "cancel" &&
    (!!app.external_app_id ||
      [
        "CREATING",
        "CREATE_UNKNOWN",
        "MANIFEST_UNKNOWN",
        "ORPHANED_APP",
      ].includes(app.state));
  return (
    <Dialog
      open
      title={t(
        action === "cancel"
          ? "integrations.slack.cancelOperation"
          : "integrations.slack.disconnect",
      )}
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="danger"
            disabled={write.busy || write.blocked || (cleanup && !confirmed)}
            onClick={() =>
              void write.run(
                (signal) =>
                  action === "cancel"
                    ? api.slack.cancel(
                        orgId,
                        app.id,
                        {
                          version: app.version,
                          confirm_external_cleanup: confirmed,
                        },
                        signal,
                      )
                    : api.slack.disconnect(
                        orgId,
                        app.id,
                        { version: app.version },
                        signal,
                      ),
                onClose,
              )
            }
          >
            {t(
              action === "cancel"
                ? "integrations.slack.cancelOperation"
                : "integrations.slack.disconnect",
            )}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <p>
          {t(
            action === "cancel"
              ? "integrations.slack.cancelHelp"
              : "integrations.slack.disconnectHelp",
          )}
        </p>
        {app.external_app_id && <p>{app.external_app_id}</p>}
        {cleanup && (
          <label className="flex items-start gap-2 text-body-sm">
            <Checkbox
              checked={confirmed}
              onChange={(e) => setConfirmed(e.target.checked)}
            />
            <span>{t("integrations.slack.cleanupConsent")}</span>
          </label>
        )}{" "}
        {!!write.error && (
          <Callout tone="danger">{slackError(t, write.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}
