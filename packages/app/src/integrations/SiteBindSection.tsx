import { useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  isApiError,
  type IntegrationConnection,
  type IntegrationDetail,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog, Field, Select, TextInput } from "@openfox/ui";
import { keys, useApi } from "../context";
import { SettingsSection } from "../spaces/settings/parts";
import { integrationErrorText } from "./queries";

export function SiteBindSection({
  orgId,
  detail,
  title,
  description,
  children,
}: {
  orgId: string;
  detail: IntegrationDetail;
  title: string;
  description: string;
  children: (edit: (connection: IntegrationConnection) => void) => ReactNode;
}) {
  const t = useT();
  const [edit, setEdit] = useState<IntegrationConnection | "new" | null>(null);
  return (
    <SettingsSection
      title={title}
      description={description}
      action={
        detail.can_manage && (
          <Button
            disabled={!detail.provider.bindable}
            onClick={() => setEdit("new")}
          >
            {t("integrations.bind")}
          </Button>
        )
      }
    >
      {!detail.provider.configured && (
        <Callout tone="warning">
          {t("integrations.notConfigured", { name: detail.provider.name })}
        </Callout>
      )}
      <p className="text-body-sm text-fg-muted">
        {t("integrations.site.network")}
      </p>
      {children(setEdit)}
      {edit && (
        <SiteBindDialog
          key={typeof edit === "string" ? edit : edit.id}
          orgId={orgId}
          provider={detail.provider.id === "jira" ? "jira" : "zentao"}
          connection={edit === "new" ? undefined : edit}
          close={() => setEdit(null)}
        />
      )}
    </SettingsSection>
  );
}

function SiteBindDialog({
  orgId,
  provider,
  connection,
  close,
}: {
  orgId: string;
  provider: "jira" | "zentao";
  connection?: IntegrationConnection;
  close: () => void;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient();
  const [site, setSite] = useState(connection?.instance_url ?? "");
  const [kind, setKind] = useState<"api_token" | "pat" | "password">(
    provider === "zentao"
      ? "password"
      : connection?.site_auth === "pat" || connection?.site_auth === "password"
        ? connection.site_auth
        : "api_token",
  );
  const [account, setAccount] = useState(
    provider === "zentao" ? (connection?.site_identity ?? "") : "",
  );
  const [secret, setSecret] = useState("");
  const [basicUser, setBasicUser] = useState("");
  const [basicPassword, setBasicPassword] = useState("");
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  async function save() {
    setBusy(true);
    setError("");
    try {
      await api.integrations.bindSite(orgId, provider, {
        site_url: site.trim(),
        ...(connection
          ? { connection_id: connection.id, version: connection.version }
          : {}),
        ...(provider === "jira"
          ? {
              deployment: kind === "api_token" ? "cloud" : "server",
              jira: {
                Kind: kind,
                Secret: secret,
                ...(kind === "api_token"
                  ? { Email: account.trim() }
                  : kind === "password"
                    ? { Username: account.trim() }
                    : {}),
              },
            }
          : {
              zen: {
                account: account.trim(),
                password: secret,
                ...(basicUser || basicPassword
                  ? { basic_user: basicUser, basic_password: basicPassword }
                  : {}),
              },
            }),
      });
      await cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
      close();
    } catch (e) {
      if (isApiError(e, "SOURCE_AUTH_FAILED"))
        setError(t("integrations.site.authFailed"));
      else if (isApiError(e, "SOURCE_ADDRESS_BLOCKED"))
        setError(t("integrations.site.network"));
      else if (isApiError(e, "JIRA_CONNECTION_FAILED"))
        setError(t("integrations.site.verifyFailed"));
      else if (isApiError(e, "INVALID_REQUEST"))
        setError(t("integrations.site.invalid"));
      else setError(integrationErrorText(t, e));
      if (isApiError(e, "VERSION_CONFLICT"))
        await cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
    } finally {
      setBusy(false);
    }
  }
  const valid =
    site.startsWith("https://") &&
    secret &&
    ((provider === "jira" && kind === "pat") || account.trim()) &&
    !basicUser === !basicPassword;
  return (
    <Dialog
      open
      title={
        connection
          ? t("integrations.site.reauthorize")
          : t("integrations.site.connect", {
              brand: provider === "jira" ? "Jira" : "ZenTao",
            })
      }
      onClose={busy ? undefined : close}
      footer={
        <>
          <Button variant="outline" onClick={close} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button onClick={() => void save()} loading={busy} disabled={!valid}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <fieldset disabled={busy} className="grid gap-3">
        <Field label={t("imports.api.site")}>
          <TextInput
            aria-label={t("imports.api.site")}
            value={site}
            onChange={(e) => setSite(e.target.value)}
            readOnly={!!connection}
            autoComplete="off"
          />
        </Field>
        {provider === "jira" && (
          <Field label={t("imports.api.authKind")}>
            <Select
              aria-label={t("imports.api.authKind")}
              value={kind}
              onChange={setKind}
              options={[
                {
                  value: "api_token",
                  label:
                    t("imports.api.cloud") + " · " + t("imports.api.apiToken"),
                },
                {
                  value: "pat",
                  label: t("imports.api.server") + " · " + t("imports.api.pat"),
                },
                {
                  value: "password",
                  label:
                    t("imports.api.server") + " · " + t("imports.api.password"),
                },
              ]}
            />
          </Field>
        )}
        {(provider === "zentao" || kind !== "pat") && (
          <Field label={t("integrations.sitePersonal.account")}>
            <TextInput
              aria-label={t("integrations.sitePersonal.account")}
              value={account}
              onChange={(e) => setAccount(e.target.value)}
              autoComplete="off"
            />
          </Field>
        )}
        <Field
          label={
            kind === "password"
              ? t("integrations.sitePersonal.password")
              : t("integrations.sitePersonal.token")
          }
        >
          <TextInput
            aria-label={
              kind === "password"
                ? t("integrations.sitePersonal.password")
                : t("integrations.sitePersonal.token")
            }
            type="password"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            autoComplete="new-password"
          />
        </Field>
        {provider === "zentao" && (
          <>
            <p className="text-body-sm text-fg-muted">
              {t("integrations.site.gateway")}
            </p>
            <Field label={t("imports.wizard.zenBasicUser")}>
              <TextInput
                aria-label={t("imports.wizard.zenBasicUser")}
                value={basicUser}
                onChange={(e) => setBasicUser(e.target.value)}
                autoComplete="off"
              />
            </Field>
            <Field label={t("imports.wizard.zenBasicPassword")}>
              <TextInput
                aria-label={t("imports.wizard.zenBasicPassword")}
                type="password"
                value={basicPassword}
                onChange={(e) => setBasicPassword(e.target.value)}
                autoComplete="new-password"
              />
            </Field>
          </>
        )}
        <Callout tone="info">
          {t("integrations.site.network")}
          {provider === "jira" && " " + t("integrations.site.cloudToken")}
        </Callout>
        {connection && (
          <p className="text-body-sm text-fg-muted">
            {t("integrations.site.sameIdentity")}
          </p>
        )}
        {error && <Callout tone="danger">{error}</Callout>}
      </fieldset>
    </Dialog>
  );
}
