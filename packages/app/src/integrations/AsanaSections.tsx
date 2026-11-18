import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import type { IntegrationConnection, IntegrationDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Button, Callout } from "@openfox/ui";
import { keys, useApi } from "../context";
import {
  SettingsCard,
  SettingsSection,
  SettingRow,
} from "../spaces/settings/parts";
import { DisconnectDialog } from "./DisconnectDialog";
import { ProjectSyncList } from "./ProjectSyncDialog";
import { parseOAuthMessage, popupFeatures } from "./model";
import { integrationErrorText } from "./queries";
import { asanaApi, asanaAuthorizeURL } from "./asana-api";

export function AsanaSections({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const api = useApi(),
    t = useT(),
    cache = useQueryClient();
  const [removing, setRemoving] = useState<IntegrationConnection | null>(null);
  const [pending, setPending] = useState<{
    session: string;
    url: string;
    expires: string;
  } | null>(null);
  const popup = useRef<Window | null>(null);
  const refresh = () =>
    cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
  const start = useMutation({
    mutationFn: async (connection?: IntegrationConnection) => {
      const result = await asanaApi(api.http).start(
        orgId,
        connection
          ? {
              purpose: "reauthorize",
              connection_id: connection.id,
              version: connection.version,
            }
          : { purpose: "organization" },
      );
      if (!asanaAuthorizeURL(result.authorize_url))
        throw new Error("Invalid authorization URL");
      return result;
    },
    onSuccess: (result) => {
      setPending({
        session: result.session_id,
        url: result.authorize_url,
        expires: result.expires_at,
      });
      popup.current = window.open(
        result.authorize_url,
        "openfox-asana-oauth",
        popupFeatures(),
      );
    },
  });
  const session = useQuery({
    queryKey: ["asana-oauth", orgId, pending?.session],
    queryFn: () => asanaApi(api.http).session(orgId, pending!.session),
    enabled: !!pending,
    retry: false,
    refetchInterval: (q) =>
      pending &&
      new Date(pending.expires).getTime() > Date.now() &&
      (!q.state.data || ["PENDING", "SELECTING"].includes(q.state.data.status))
        ? 2000
        : false,
  });
  useEffect(() => {
    if (session.data?.status !== "COMPLETED") return;
    setPending(null);
    void cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
  }, [session.data?.status, cache, orgId]);
  useEffect(() => {
    if (!pending) return;
    const receive = (event: MessageEvent) => {
      const message = parseOAuthMessage(event, window.location.origin);
      if (message?.provider === "asana" && message.session === pending.session)
        void session.refetch();
    };
    window.addEventListener("message", receive);
    return () => window.removeEventListener("message", receive);
  }, [pending, session.refetch]);
  const connections = (
    detail.connections as (IntegrationConnection & {
      asana_delegating_user?: string;
      asana_delegating_user_id?: string;
    })[]
  ).filter((c) => c.status !== "DISCONNECTED");
  return (
    <>
      <Callout tone="info">{t("integrations.asana.sharedDescription")}</Callout>
      {!detail.provider.configured && (
        <Callout tone="warning">
          {t("integrations.asana.notConfigured")}
        </Callout>
      )}
      {start.isError && (
        <Callout tone="danger">{integrationErrorText(t, start.error)}</Callout>
      )}
      {pending && (
        <Callout tone="info">
          <p>{t("accountBinding.waiting")}</p>
          <a
            className="underline"
            href={`/integrations/oauth/complete?provider=asana&organization=${encodeURIComponent(orgId)}&session=${encodeURIComponent(pending.session)}`}
            target="_blank"
            rel="noopener noreferrer"
          >
            {t("integrations.asana.openConfirmation")}
          </a>
          {!popup.current && (
            <p>
              <a
                className="underline"
                href={pending.url}
                target="_blank"
                rel="noopener noreferrer"
              >
                {t("accountBinding.openAuthorize")}
              </a>
            </p>
          )}
          {(session.isError ||
            session.data?.status === "FAILED" ||
            session.data?.status === "EXPIRED") && (
            <p role="alert">{t("integrations.asana.failed")}</p>
          )}
          <Button
            size="28"
            variant="ghost"
            onClick={() => {
              setPending(null);
              void refresh();
            }}
          >
            {t("common.close")}
          </Button>
        </Callout>
      )}
      <SettingsSection title={t("integrations.asana.organizationTitle")}>
        <SettingsCard>
          {connections.map((connection) => (
            <SettingRow
              key={connection.id}
              title={connection.display_name}
              description={
                <>
                  {t("integrations.asana.workspace")} · {connection.external_id}
                  {connection.asana_delegating_user_id && (
                    <p>
                      {t("integrations.asana.delegatingUser", {
                        name: connection.asana_delegating_user ?? "",
                        id: connection.asana_delegating_user_id,
                      })}
                    </p>
                  )}
                </>
              }
              control={
                <div className="flex flex-wrap items-center gap-2">
                  <Badge
                    tone={
                      connection.status === "ACTIVE" ? "success" : "warning"
                    }
                  >
                    {connection.status === "ACTIVE"
                      ? t("accountBinding.connected")
                      : t("integrations.asana.needsAttention")}
                  </Badge>
                  {detail.can_manage && (
                    <>
                      <Button
                        size="28"
                        variant="outline"
                        disabled={start.isPending || !detail.provider.bindable}
                        onClick={() => start.mutate(connection)}
                      >
                        {t("integrations.asana.reauthorize")}
                      </Button>
                      <Button
                        size="28"
                        variant="outline"
                        onClick={() => setRemoving(connection)}
                      >
                        {t("integrations.disconnect")}
                      </Button>
                    </>
                  )}
                </div>
              }
            />
          ))}
          <SettingRow
            title={t("integrations.asana.connect")}
            control={
              <Button
                size="28"
                variant="outline"
                disabled={
                  !detail.can_manage ||
                  !detail.provider.bindable ||
                  start.isPending
                }
                onClick={() => start.mutate(undefined)}
              >
                {t("integrations.asana.connect")}
              </Button>
            }
          />
        </SettingsCard>
      </SettingsSection>
      <SettingsSection title={t("integrations.asana.identityTitle")}>
        <p className="text-body-sm text-fg-muted">
          {t("integrations.asana.identityDescription")}
        </p>
        <Link className="underline" to={`/o/${orgId}/settings/account-binding`}>
          {t("integrations.asana.manageIdentity")}
        </Link>
      </SettingsSection>
      <SettingsSection title={t("integrations.asana.syncTitle")}>
        <p className="text-body-sm text-fg-muted">
          {t("integrations.asana.importScope")}
        </p>
        <p className="text-body-sm text-fg-muted">
          {t("integrations.asana.syncScope")}
        </p>
        {detail.can_manage && (
          <Link
            className="underline"
            to={`/o/${orgId}/settings/import-export?source=asana`}
          >
            {t("imports.wizard.providerTitle", { brand: "Asana" })}
          </Link>
        )}
        {detail.can_manage &&
          connections.map((c) => (
            <div key={c.id} className="grid gap-2">
              <h3 className="text-body font-medium">{c.display_name}</h3>
              <ProjectSyncList org={orgId} connection={c.id} brand="Asana" />
            </div>
          ))}
      </SettingsSection>
      {removing && (
        <DisconnectDialog
          orgId={orgId}
          provider={detail.provider}
          connection={removing}
          body={t("integrations.asana.disconnectDescription", {
            workspace: removing.display_name,
          })}
          onClose={() => setRemoving(null)}
        />
      )}
    </>
  );
}
