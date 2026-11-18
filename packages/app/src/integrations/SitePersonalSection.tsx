import { useState, type ReactNode } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { IntegrationDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog, Select, TextInput } from "@openfox/ui";
import { keys, useApi } from "../context";
import { SettingsSection } from "../spaces/settings/parts";
import { integrationErrorText } from "./queries";

export function SitePersonalSection({
  orgId,
  detail,
  children,
}: {
  orgId: string;
  detail: IntegrationDetail;
  children: ReactNode;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient();
  const [open, setOpen] = useState(false),
    [parent, setParent] = useState("");
  const [account, setAccount] = useState(""),
    [secret, setSecret] = useState("");
  const [kind, setKind] = useState<"pat" | "api_token" | "password">("pat");
  const zen = detail.provider.id === "zentao";
  const connections = detail.connections.filter((c) => c.status === "ACTIVE");
  const own = detail.personal_connections.filter(
    (c) => c.account_kind === "sync_comments" && c.status !== "DISCONNECTED",
  );
  const existing = own.find((c) => c.parent_connection_id === parent);
  const vars = { brand: detail.provider.name, name: detail.provider.name };
  function close() {
    if (save.isPending) return;
    setOpen(false);
    setSecret("");
    save.reset();
  }
  const save = useMutation({
    mutationFn: () =>
      api.integrations.bindSitePersonal(orgId, {
        connection_id: parent,
        version: existing?.version ?? 0,
        ...(zen
          ? { zentao: { account, password: secret } }
          : {
              jira: {
                Kind: kind,
                Secret: secret,
                ...(kind === "api_token"
                  ? { Email: account }
                  : kind === "password"
                    ? { Username: account }
                    : {}),
              },
            }),
      }),
    onSuccess: async () => {
      setSecret("");
      setOpen(false);
      await Promise.all([
        cache.invalidateQueries({ queryKey: keys.integrations(orgId) }),
        cache.invalidateQueries({ queryKey: keys.accountBindings(orgId) }),
      ]);
    },
  });
  return (
    <SettingsSection
      title={t("integrations.sections.personal", vars)}
      description={t(
        zen
          ? "integrations.sitePersonal.zenDescription"
          : "integrations.sitePersonal.jiraDescription",
      )}
      action={
        <Button
          size="32"
          disabled={!connections.length}
          onClick={() => {
            setParent(connections[0]!.id);
            setAccount("");
            setSecret("");
            setOpen(true);
          }}
        >
          {t("integrations.bindMine")}
        </Button>
      }
    >
      {children}
      {open && (
        <Dialog
          open
          title={t("integrations.sitePersonal.title", vars)}
          onClose={save.isPending ? undefined : close}
          width={480}
          footer={
            <>
              <Button
                variant="outline"
                disabled={save.isPending}
                onClick={close}
              >
                {t("common.cancel")}
              </Button>
              <Button
                loading={save.isPending}
                disabled={
                  !parent || !secret || ((zen || kind !== "pat") && !account)
                }
                onClick={() => save.mutate()}
              >
                {t("integrations.bindMine")}
              </Button>
            </>
          }
        >
          <div className="flex flex-col gap-4">
            <p className="text-body text-fg-muted">
              {t(
                zen
                  ? "integrations.sitePersonal.zenNote"
                  : "integrations.sitePersonal.jiraNote",
              )}
            </p>
            {save.error && (
              <Callout tone="danger">
                {integrationErrorText(t, save.error)}
              </Callout>
            )}
            <label className="flex flex-col gap-1.5">
              {t("integrations.sitePersonal.instance")}
              <Select
                aria-label={t("integrations.sitePersonal.instance")}
                value={parent}
                onChange={setParent}
                disabled={save.isPending}
                options={connections.map((c) => ({
                  value: c.id,
                  label: c.instance_url || c.display_name,
                }))}
              />
            </label>
            {!zen && (
              <label className="flex flex-col gap-1.5">
                {t("integrations.sitePersonal.auth")}
                <Select
                  aria-label={t("integrations.sitePersonal.auth")}
                  value={kind}
                  onChange={(v) => {
                    setKind(v as typeof kind);
                    setSecret("");
                  }}
                  disabled={save.isPending}
                  options={[
                    { value: "pat", label: "Personal access token" },
                    { value: "api_token", label: "API token" },
                    {
                      value: "password",
                      label: t("integrations.sitePersonal.password"),
                    },
                  ]}
                />
              </label>
            )}
            {(zen || kind !== "pat") && (
              <label className="flex flex-col gap-1.5">
                {t("integrations.sitePersonal.account")}
                <TextInput
                  aria-label={t("integrations.sitePersonal.account")}
                  value={account}
                  onChange={(e) => setAccount(e.target.value)}
                  disabled={save.isPending}
                  maxLength={200}
                />
              </label>
            )}
            <label className="flex flex-col gap-1.5">
              {t(
                zen || kind === "password"
                  ? "integrations.sitePersonal.password"
                  : "integrations.sitePersonal.token",
              )}
              <TextInput
                type="password"
                autoComplete="off"
                aria-label={t(
                  zen || kind === "password"
                    ? "integrations.sitePersonal.password"
                    : "integrations.sitePersonal.token",
                )}
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                disabled={save.isPending}
                maxLength={2048}
              />
            </label>
          </div>
        </Dialog>
      )}
    </SettingsSection>
  );
}
