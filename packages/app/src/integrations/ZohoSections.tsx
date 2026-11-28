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
import { parseOAuthMessage, popupFeatures } from "./model";
import { zohoErrorText } from "./ZohoComplete";
import { zohoApi, zohoAuthorizeURL } from "@openfox/api";

export function ZohoSections({
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
    gcTime: 0,
    mutationFn: async (connection?: IntegrationConnection) => {
      const result = await zohoApi(api.http).start(
        orgId,
        connection
          ? {
              purpose: "reauthorize",
              connection_id: connection.id,
              version: connection.version,
            }
          : { purpose: "organization" },
      );
      if (!zohoAuthorizeURL(result.authorize_url))
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
        "openfox-zoho-oauth",
        popupFeatures(),
      );
    },
  });
  const session = useQuery({
    queryKey: ["zoho-oauth", orgId, pending?.session],
    queryFn: () => zohoApi(api.http).session(orgId, pending!.session),
    enabled: !!pending,
    retry: false,
    gcTime: 0,
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
  const refetchSession = session.refetch;
  useEffect(() => {
    if (!pending) return;
    const receive = (event: MessageEvent) => {
      const message = parseOAuthMessage(event, window.location.origin);
      if (
        message?.provider === "zoho_bugtracker" &&
        message.session === pending.session
      )
        void refetchSession();
    };
    window.addEventListener("message", receive);
    return () => window.removeEventListener("message", receive);
  }, [pending, refetchSession]);
  const connections = detail.connections.filter(
    (c) => c.scope === "ORGANIZATION" && c.status !== "DISCONNECTED",
  );
  return (
    <>
      <Callout tone="warning">{t("integrations.zoho.lifecycle")}</Callout>
      <Callout tone="info">{t("integrations.zoho.shared")}</Callout>
      {!detail.provider.configured && (
        <Callout tone="warning">{t("integrations.zoho.notConfigured")}</Callout>
      )}
      {start.isError && (
        <Callout tone="danger">{zohoErrorText(t, start.error)}</Callout>
      )}
      {pending && (
        <Callout tone="info">
          <p>{t("accountBinding.waiting")}</p>
          <a
            className="underline"
            href={`/integrations/oauth/complete?provider=zoho_bugtracker&organization=${encodeURIComponent(orgId)}&session=${encodeURIComponent(pending.session)}`}
            target="_blank"
            rel="noopener noreferrer"
          >
            {t("integrations.zoho.openConfirmation")}
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
            <p role="alert">{t("integrations.zoho.failed")}</p>
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
      <SettingsSection title={t("integrations.zoho.organizationTitle")}>
        <SettingsCard>
          {connections.map((connection) => (
            <SettingRow
              key={connection.id}
              title={connection.display_name}
              description={
                <>
                  {t("integrations.zoho.portal")} · {connection.external_id}
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
                      : t("integrations.zoho.needsAttention")}
                  </Badge>
                  {detail.can_manage && (
                    <>
                      <Button
                        size="28"
                        variant="outline"
                        disabled={
                          start.isPending ||
                          !detail.provider.bindable ||
                          !detail.provider.configured
                        }
                        onClick={() => start.mutate(connection)}
                      >
                        {t("integrations.zoho.reauthorize")}
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
            title={t("integrations.zoho.connect")}
            control={
              <Button
                size="28"
                variant="outline"
                disabled={
                  !detail.can_manage ||
                  !detail.provider.bindable ||
                  !detail.provider.configured ||
                  start.isPending
                }
                onClick={() => start.mutate(undefined)}
              >
                {t("integrations.zoho.connect")}
              </Button>
            }
          />
        </SettingsCard>
      </SettingsSection>
      <SettingsSection title={t("integrations.zoho.identityTitle")}>
        <p className="text-body-sm text-fg-muted">
          {t("integrations.zoho.identityDescription")}
        </p>
        <Link className="underline" to={`/o/${orgId}/settings/account-binding`}>
          {t("integrations.zoho.manageIdentity")}
        </Link>
      </SettingsSection>
      <SettingsSection title={t("integrations.zoho.import")}>
        <p className="text-body-sm text-fg-muted">
          {t("integrations.zoho.scope")}
        </p>
        {detail.can_manage && (
          <Link
            className="underline"
            to={`/o/${orgId}/settings/import-export?source=zoho_bugtracker`}
          >
            {t("integrations.zoho.import")}
          </Link>
        )}
      </SettingsSection>
      {removing && (
        <DisconnectDialog
          orgId={orgId}
          provider={detail.provider}
          connection={removing}
          body={t("integrations.zoho.disconnect", {
            workspace: removing.display_name,
          })}
          onClose={() => setRemoving(null)}
        />
      )}
    </>
  );
}
