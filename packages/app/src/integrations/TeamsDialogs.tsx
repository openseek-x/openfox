import { useMemo, useState } from "react";
import { teamsApi, type TeamsBot, type TeamsConnectInput } from "@openfox/api";
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
import { useTeamsAction } from "./teams-model";
const guid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
function https(raw: string) {
  try {
    const u = new URL(raw);
    return (
      raw.trim() === raw &&
      u.protocol === "https:" &&
      !u.username &&
      !u.password &&
      !u.hash &&
      raw.length <= 2048
    );
  } catch {
    return false;
  }
}
export function TeamsConnectDialog({
  orgId,
  bots = [],
  bot,
  onClose,
  onSaved,
}: {
  orgId: string;
  bots?: TeamsBot[];
  bot?: TeamsBot;
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useT(),
    api = useApi(),
    action = useTeamsAction(),
    role = useOrgRole(orgId),
    directory = useDirectory(orgId),
    spaces = useSpaces(orgId),
    devices = useDevices(orgId),
    me = useMe();
  const [kind, setKind] = useState("AGENT"),
    [agent, setAgent] = useState<string | null>(null),
    [space, setSpace] = useState<string | null>(null),
    [runtime, setRuntime] = useState<string | null>(null),
    [extra, setExtra] = useState("");
  const [name, setName] = useState(""),
    [tenant, setTenant] = useState(""),
    [app, setApp] = useState(""),
    [secret, setSecret] = useState("");
  const [version, setVersion] = useState("1.0.0"),
    [developer, setDeveloper] = useState(""),
    [website, setWebsite] = useState(""),
    [privacy, setPrivacy] = useState(""),
    [terms, setTerms] = useState("");
  const admin = role === "OWNER" || role === "ADMIN";
  const agents = (directory.data?.entries ?? [])
    .filter(
      (e) =>
        e.type === "agent" &&
        !e.archived_at &&
        (admin || e.mine) &&
        !bots.some((b) => b.agent.id === e.id),
    )
    .map((e) => ({ value: e.id, label: e.name }));
  const choices = useMemo(
    () => runtimeChoices(devices.data ?? [], space ?? "", me.data?.id ?? ""),
    [devices.data, space, me.data?.id],
  );
  const selected = choices.find((c) => c.value === runtime);
  const valid =
    secret.length > 0 &&
    secret.length <= 4096 &&
    (!!bot ||
      (guid.test(tenant) &&
        guid.test(app) &&
        name.trim() === name &&
        [...name].length > 0 &&
        [...name].length <= 30 &&
        /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.test(version) &&
        version.length <= 64 &&
        developer.trim() === developer &&
        [...developer].length > 0 &&
        [...developer].length <= 32 &&
        [website, privacy, terms].every(https) &&
        (kind === "AGENT"
          ? agents.some((a) => a.value === agent)
          : space && selected && [...extra].length <= 4000)));
  const save = () =>
    action.run(async (signal) => {
      const key = secret;
      setSecret("");
      if (bot) {
        await teamsApi(api.http).credentials(
          orgId,
          bot.id,
          { version: bot.version, secret: key },
          signal,
        );
      } else {
        const input: TeamsConnectInput = {
          kind: kind as TeamsConnectInput["kind"],
          name,
          tenant_id: tenant,
          app_id: app,
          secret: key,
          extra_prompt: kind === "TASK_ASSISTANT" ? extra : "",
          manifest: {
            version,
            developer_name: developer,
            website_url: website,
            privacy_url: privacy,
            terms_url: terms,
          },
          ...(kind === "AGENT"
            ? { agent_id: agent! }
            : {
                space_id: space!,
                runtime: {
                  computer_id: selected!.deviceId,
                  grant_id: selected!.grantId,
                  runtime: selected!.provider,
                },
              }),
        };
        await teamsApi(api.http).create(orgId, input, signal);
      }
      if (!signal.aborted) {
        onSaved();
        onClose();
      }
    });
  return (
    <Dialog
      open
      title={t(bot ? "integrations.teams.rotate" : "integrations.teams.create")}
      onClose={onClose}
      width={560}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.close")}
          </Button>
          <Button
            disabled={!valid}
            loading={action.busy}
            onClick={() => void save()}
          >
            {t("integrations.teams.save")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {action.error && <p role="alert">{action.error}</p>}
        {!bot ? (
          <>
            <label>
              {t("integrations.teams.kind")}
              <Select
                aria-label={t("integrations.teams.kind")}
                value={kind}
                onChange={(v) => setKind(v ?? "AGENT")}
                options={[
                  { value: "AGENT", label: t("integrations.teams.agent") },
                  {
                    value: "TASK_ASSISTANT",
                    label: t("integrations.teams.assistant"),
                  },
                ]}
              />
            </label>
            {kind === "AGENT" ? (
              <label>
                {t("integrations.teams.agent")}
                <Select
                  aria-label={t("integrations.teams.agent")}
                  value={agent}
                  onChange={setAgent}
                  options={agents}
                />
              </label>
            ) : (
              <>
                <label>
                  {t("integrations.teams.space")}
                  <Select
                    aria-label={t("integrations.teams.space")}
                    value={space}
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
                  {t("integrations.teams.runtime")}
                  <Select
                    aria-label={t("integrations.teams.runtime")}
                    value={runtime}
                    onChange={setRuntime}
                    options={runtimeOptions(choices, t)}
                  />
                </label>
                <label>
                  {t("integrations.teams.extra")}
                  <Textarea
                    aria-label={t("integrations.teams.extra")}
                    value={extra}
                    onChange={(e) => setExtra(e.target.value)}
                  />
                </label>
              </>
            )}
            <label>
              {t("integrations.teams.name")}
              <TextInput
                aria-label={t("integrations.teams.name")}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <label>
              {t("integrations.teams.tenant")}
              <TextInput
                aria-label={t("integrations.teams.tenant")}
                value={tenant}
                onChange={(e) => setTenant(e.target.value)}
              />
            </label>
            <label>
              {t("integrations.teams.app")}
              <TextInput
                aria-label={t("integrations.teams.app")}
                value={app}
                onChange={(e) => setApp(e.target.value)}
              />
            </label>
          </>
        ) : (
          <p>
            {bot.name} · {bot.app_id}
          </p>
        )}
        <label>
          {t("integrations.teams.secret")}
          <TextInput
            aria-label={t("integrations.teams.secret")}
            type="password"
            autoComplete="off"
            value={secret}
            maxLength={4096}
            onChange={(e) => setSecret(e.target.value)}
          />
        </label>
        <p>{t("integrations.teams.credentialHelp")}</p>
        {!bot && (
          <>
            <Callout tone="info">{t("integrations.teams.realURLs")}</Callout>
            <label>
              {t("integrations.teams.manifestVersion")}
              <TextInput
                aria-label={t("integrations.teams.manifestVersion")}
                value={version}
                onChange={(e) => setVersion(e.target.value)}
              />
            </label>
            <label>
              {t("integrations.teams.developer")}
              <TextInput
                aria-label={t("integrations.teams.developer")}
                value={developer}
                onChange={(e) => setDeveloper(e.target.value)}
              />
            </label>
            <label>
              {t("integrations.teams.website")}
              <TextInput
                aria-label={t("integrations.teams.website")}
                value={website}
                onChange={(e) => setWebsite(e.target.value)}
              />
            </label>
            <label>
              {t("integrations.teams.privacy")}
              <TextInput
                aria-label={t("integrations.teams.privacy")}
                value={privacy}
                onChange={(e) => setPrivacy(e.target.value)}
              />
            </label>
            <label>
              {t("integrations.teams.terms")}
              <TextInput
                aria-label={t("integrations.teams.terms")}
                value={terms}
                onChange={(e) => setTerms(e.target.value)}
              />
            </label>
          </>
        )}
        <Callout tone="info">{t("integrations.teams.noEndToEnd")}</Callout>
      </div>
    </Dialog>
  );
}
