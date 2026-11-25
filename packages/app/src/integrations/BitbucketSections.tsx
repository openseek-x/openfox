import { BitbucketRepositoriesDialog } from "./BitbucketRepositoriesDialog";
import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import {
  bitbucketApi,
  type IntegrationConnection,
  type IntegrationDetail,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Button, Callout } from "@openfox/ui";
import { keys, useApi } from "../context";
import {
  SettingsCard,
  SettingsSection,
  SettingRow,
} from "../spaces/settings/parts";
import { DisconnectDialog } from "./DisconnectDialog";
import { bitbucketAuthorizeURL } from "./BitbucketComplete";
import { parseOAuthMessage, popupFeatures } from "./model";
import { integrationErrorText } from "./queries";

export function BitbucketSections({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const api = useApi(),
    t = useT(),
    cache = useQueryClient();
  const [repositories, setRepositories] =
    useState<IntegrationConnection | null>(null);
  const [removing, setRemoving] = useState<IntegrationConnection | null>(null);
  const [pending, setPending] = useState<{
    session: string;
    url: string;
    expires: string;
  } | null>(null);
  const popup = useRef<Window | null>(null);
  const start = useMutation({
    mutationFn: async (connection?: IntegrationConnection) => {
      const result = await bitbucketApi(api.http).start(
        orgId,
        connection
          ? {
              purpose: "reauthorize",
              connection_id: connection.id,
              version: connection.version,
            }
          : { purpose: "organization" },
      );
      if (!bitbucketAuthorizeURL(result.authorize_url))
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
        "openfox-bitbucket-oauth",
        popupFeatures(),
      );
    },
  });
  const session = useQuery({
    queryKey: ["bitbucket-oauth", orgId, pending?.session],
    queryFn: () => bitbucketApi(api.http).session(orgId, pending!.session),
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
    void cache.invalidateQueries({ queryKey: keys.accountBindings(orgId) });
  }, [session.data?.status, cache, orgId]);
  const refetchSession = session.refetch;
  useEffect(() => {
    if (!pending) return;
    const receive = (event: MessageEvent) => {
      const message = parseOAuthMessage(event, window.location.origin);
      if (
        message?.provider === "bitbucket" &&
        message.session === pending.session
      )
        void refetchSession();
    };
    window.addEventListener("message", receive);
    return () => window.removeEventListener("message", receive);
  }, [pending, refetchSession]);
  const connections = detail.connections.filter(
    (c) => c.status !== "DISCONNECTED",
  );
  return (
    <>
      <Callout tone="info">
        {t("integrations.bitbucket.organizationDescription")}
      </Callout>
      <p className="text-body-sm text-fg-muted">
        {t("integrations.bitbucket.scopeNotice")}
      </p>
      {!detail.provider.configured && (
        <Callout tone="warning">
          {t("integrations.bitbucket.notConfigured")}
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
            href={`/integrations/oauth/complete?provider=bitbucket&organization=${encodeURIComponent(orgId)}&session=${encodeURIComponent(pending.session)}`}
            target="_blank"
            rel="noopener noreferrer"
          >
            {t("integrations.bitbucket.openConfirmation")}
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
            <p role="alert">{t("integrations.bitbucket.failed")}</p>
          )}
          <Button size="28" variant="ghost" onClick={() => setPending(null)}>
            {t("common.close")}
          </Button>
        </Callout>
      )}
      <SettingsSection title={t("integrations.bitbucket.organizationTitle")}>
        <SettingsCard>
          {connections.map((connection) => (
            <SettingRow
              key={connection.id}
              title={connection.display_name}
              description={
                <>
                  {t("integrations.bitbucket.workspace")} ·{" "}
                  {connection.external_id}
                  {connection.bitbucket_delegating_user_id && (
                    <p>
                      {t("integrations.bitbucket.delegatingUser", {
                        name: connection.bitbucket_delegating_user || "",
                        id: connection.bitbucket_delegating_user_id,
                      })}
                    </p>
                  )}
                  {connection.status_reason && (
                    <p>{t("integrations.bitbucket.needsAttention")}</p>
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
                    {t(
                      connection.status === "ACTIVE"
                        ? "accountBinding.connected"
                        : "integrations.bitbucket.needsAttention",
                    )}
                  </Badge>
                  {detail.can_manage && (
                    <>
                      <Button
                        size="28"
                        variant="outline"
                        onClick={() => setRepositories(connection)}
                      >
                        {t("integrations.bitbucket.repositoriesTitle")}
                      </Button>
                      <Button
                        size="28"
                        variant="outline"
                        disabled={
                          start.isPending ||
                          !!pending ||
                          !detail.provider.bindable
                        }
                        onClick={() => start.mutate(connection)}
                      >
                        {t("integrations.bitbucket.reauthorize")}
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
          {!connections.length && (
            <SettingRow
              title={t("integrations.bitbucket.connect")}
              control={
                <Button
                  size="28"
                  variant="outline"
                  disabled={
                    !detail.can_manage ||
                    !detail.provider.bindable ||
                    start.isPending ||
                    !!pending
                  }
                  onClick={() => start.mutate(undefined)}
                >
                  {t("integrations.bitbucket.connect")}
                </Button>
              }
            />
          )}
        </SettingsCard>
      </SettingsSection>
      <SettingsSection title={t("integrations.bitbucket.identityTitle")}>
        <p className="text-body-sm text-fg-muted">
          {t("integrations.bitbucket.identityDescription")}
        </p>
        <Link className="underline" to={`/o/${orgId}/settings/account-binding`}>
          {t("integrations.bitbucket.manageIdentity")}
        </Link>
      </SettingsSection>
      {repositories && (
        <BitbucketRepositoriesDialog
          org={orgId}
          connection={
            detail.connections.find((c) => c.id === repositories.id) ??
            repositories
          }
          close={() => setRepositories(null)}
        />
      )}
      {removing && (
        <DisconnectDialog
          orgId={orgId}
          provider={detail.provider}
          connection={removing}
          body={t("integrations.bitbucket.disconnectDescription")}
          onClose={() => setRemoving(null)}
        />
      )}
    </>
  );
}
