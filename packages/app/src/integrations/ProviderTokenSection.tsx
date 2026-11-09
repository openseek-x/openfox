import { useCopy } from "../tasks/parts";
import { ProjectSyncDialog } from "./ProjectSyncDialog";
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import type { IntegrationDetail, IntegrationConnection } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Dialog,
  Field,
  Icon,
  IconButton,
  TextInput,
  useToast,
} from "@openfox/ui";
import { keys, useApi } from "../context";
import {
  SettingsSection,
  SettingsCard,
  SettingRow,
} from "../spaces/settings/parts";
import { integrationErrorText } from "./queries";
import { DisconnectDialog } from "./DisconnectDialog";
import { PingCodePersonalSection } from "./PingCodePersonalSection";

export function ProviderTokenSection({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const api = useApi(),
    t = useT(),
    toast = useToast(),
    cache = useQueryClient();
  const { provider, connections, can_manage } = detail;
  const [syncConnection, setSyncConnection] = useState("");
  const hasProjectSync = [
    "pingcode",
    "tapd",
    "shortcut",
    "teambition",
  ].includes(provider.id);
  const [hook, setHook] = useState<IntegrationConnection | null>(null);
  const [open, setOpen] = useState(false),
    [busy, setBusy] = useState(false);
  const [disconnect, setDisconnect] = useState<IntegrationConnection | null>(
    null,
  );
  const [method, setMethod] = useState<"basic" | "token">("basic");
  const [secret, setSecret] = useState(""),
    [account, setAccount] = useState(""),
    [company, setCompany] = useState("");
  const basic = provider.id === "tapd" && method === "basic";
  const app = provider.id === "teambition";
  const needsCompany = app || basic || provider.id === "pingcode";
  const label = t("shell.bindProvider", { brand: provider.name });
  function close() {
    setOpen(false);
    setSecret("");
    setCompany("");
    setAccount("");
    setMethod("basic");
  }
  async function save() {
    setBusy(true);
    try {
      await api.integrations.bindInstanceToken(orgId, provider.id, {
        method: app ? "app" : basic ? "basic" : "token",
        secret,
        ...(basic || app ? { account } : {}),
        ...(needsCompany ? { company_id: company } : {}),
      });
      await cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
      close();
    } catch (error) {
      toast(integrationErrorText(t, error), { tone: "error" });
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <SettingsSection title={t("shell.credentials")}>
        <SettingsCard>
          {connections.map((c) => (
            <SettingRow
              key={c.id}
              title={c.display_name}
              description={c.external_id}
              control={
                can_manage && (
                  <div className="flex items-center gap-2">
                    {hasProjectSync && c.status === "ACTIVE" && (
                      <Button
                        size="28"
                        variant="outline"
                        onClick={() => setSyncConnection(c.id)}
                      >
                        {t("imports.options.syncProjects")}
                      </Button>
                    )}
                    {provider.id === "shortcut" && c.status === "ACTIVE" && (
                      <Button
                        size="28"
                        variant="outline"
                        onClick={() => setHook(c)}
                      >
                        {t("imports.hook.view")}
                      </Button>
                    )}
                    <Button
                      variant="outline"
                      size="28"
                      onClick={() => setDisconnect(c)}
                    >
                      {t("shell.disconnectProvider")}
                    </Button>
                  </div>
                )
              }
            />
          ))}
          <SettingRow
            title={label}
            control={
              <Button
                variant="outline"
                size="28"
                disabled={!can_manage || !provider.bindable}
                onClick={() => setOpen(true)}
              >
                {label}
              </Button>
            }
          />
        </SettingsCard>
      </SettingsSection>
      {provider.id === "pingcode" && (
        <PingCodePersonalSection orgId={orgId} detail={detail} />
      )}
      {["pingcode", "tapd", "shortcut", "teambition"].includes(provider.id) &&
        can_manage && (
          <Link
            className="text-body text-accent"
            to={`/o/${orgId}/settings/import-export?source=${provider.id}`}
          >
            {t("imports.wizard.providerTitle", { brand: provider.name })}
          </Link>
        )}
      {syncConnection && (
        <ProjectSyncDialog
          org={orgId}
          connection={syncConnection}
          brand={provider.name}
          close={() => setSyncConnection("")}
        />
      )}
      {hook && (
        <ShortcutHookDialog
          org={orgId}
          id={hook.id}
          close={() => setHook(null)}
        />
      )}
      {open && (
        <Dialog
          open
          title={label}
          onClose={busy ? undefined : close}
          footer={
            <>
              <Button variant="outline" onClick={close} disabled={busy}>
                {t("common.cancel")}
              </Button>
              <Button
                loading={busy}
                disabled={
                  !secret ||
                  ((basic || app) && !account) ||
                  (needsCompany && !company)
                }
                onClick={() => void save()}
              >
                {label}
              </Button>
            </>
          }
        >
          <div className="flex flex-col gap-4">
            {provider.id === "tapd" && (
              <div className="flex gap-2">
                {(["basic", "token"] as const).map((v) => (
                  <Button
                    key={v}
                    variant={method === v ? "secondary" : "ghost"}
                    aria-pressed={method === v}
                    onClick={() => {
                      setMethod(v);
                      setSecret("");
                    }}
                  >
                    {t(
                      v === "basic" ? "shell.apiAccount" : "shell.accessToken",
                    )}
                  </Button>
                ))}
              </div>
            )}
            {(basic || app) && (
              <Field label={app ? "App ID" : t("shell.apiAccount")}>
                <TextInput
                  aria-label={app ? "App ID" : t("shell.apiAccount")}
                  value={account}
                  onChange={(e) => setAccount(e.target.value)}
                />
              </Field>
            )}
            <Field
              label={
                app
                  ? "App Secret"
                  : t(basic ? "shell.apiSecret" : "shell.accessToken")
              }
            >
              <TextInput
                autoComplete="off"
                type="password"
                aria-label={
                  app
                    ? "App Secret"
                    : t(basic ? "shell.apiSecret" : "shell.accessToken")
                }
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
              />
            </Field>
            {needsCompany && (
              <Field label={t("shell.companyId")}>
                <TextInput
                  aria-label={t("shell.companyId")}
                  value={company}
                  onChange={(e) => setCompany(e.target.value)}
                />
              </Field>
            )}
          </div>
        </Dialog>
      )}
      {disconnect && (
        <DisconnectDialog
          orgId={orgId}
          provider={provider}
          connection={disconnect}
          onClose={() => setDisconnect(null)}
        />
      )}
    </>
  );
}

function ShortcutHookDialog({
  org,
  id,
  close,
}: {
  org: string;
  id: string;
  close: () => void;
}) {
  const copy = useCopy();
  const t = useT(),
    api = useApi(),
    toast = useToast();
  const [secret, setSecret] = useState(""),
    [busy, setBusy] = useState(false);
  const q = useQuery({
    queryKey: [...keys.integrations(org), id, "shortcut-hook"],
    queryFn: () => api.integrations.shortcutHook(org, id),
  });
  const save = async (value: string) => {
    if (!q.data) return;
    setBusy(true);
    try {
      await api.integrations.saveShortcutHook(org, id, q.data.version, value);
      await q.refetch();
      close();
    } catch (e) {
      toast(integrationErrorText(t, e), { tone: "error" });
      await q.refetch();
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      title={t("imports.hook.view")}
      onClose={busy ? undefined : close}
      footer={
        <>
          <Button variant="outline" disabled={busy} onClick={close}>
            {t("common.cancel")}
          </Button>
          {q.data?.enabled && (
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => void save("")}
            >
              {t("imports.hook.disable")}
            </Button>
          )}
          <Button
            disabled={!q.data?.url || secret.length < 16 || secret.length > 256}
            loading={busy}
            onClick={() => void save(secret)}
          >
            {t("common.save")}
          </Button>
        </>
      }
    >
      {q.isError ? (
        <p role="alert">{integrationErrorText(t, q.error)}</p>
      ) : (
        q.data &&
        (q.data.url ? (
          <div className="grid gap-3">
            <p>{t("imports.hook.help")}</p>
            <Field label={t("imports.hook.url")}>
              <div className="flex items-center gap-2">
                <TextInput
                  aria-label={t("imports.hook.url")}
                  readOnly
                  value={q.data.url}
                  onFocus={(e) => e.target.select()}
                />
                <IconButton
                  size="28"
                  icon={<Icon name="copy" />}
                  label={
                    t("integrations.github.webhook.copy") +
                    " " +
                    t("imports.hook.url")
                  }
                  onClick={() =>
                    void copy(
                      q.data.url,
                      t("integrations.github.webhook.copied"),
                    )
                  }
                />
              </div>
            </Field>
            <Field label={t("imports.hook.secret")}>
              <TextInput
                autoComplete="new-password"
                aria-label={t("imports.hook.secret")}
                type="password"
                maxLength={256}
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
              />
            </Field>
            {q.data.last_delivery && (
              <p>
                {t("imports.hook.last", {
                  time: new Date(q.data.last_delivery).toLocaleString(),
                })}
              </p>
            )}
          </div>
        ) : (
          <p>{t("imports.hook.unavailable")}</p>
        ))
      )}
    </Dialog>
  );
}
