import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { useT } from "@openfox/i18n";
import { Button, Callout, Select } from "@openfox/ui";
import { useApi } from "../context";
import { azureApi } from "./azure-api";
import { integrationErrorText } from "./queries";

export function AzureComplete({
  orgId,
  session,
  reason,
}: {
  orgId: string;
  session: string;
  reason: string;
}) {
  const api = useApi(),
    t = useT();
  const [workspace, setWorkspace] = useState("");
  const [shared, setShared] = useState(false);
  const query = useQuery({
    queryKey: ["azure-oauth", orgId, session],
    queryFn: () => azureApi(api.http).session(orgId, session),
    enabled: Boolean(orgId && session),
    retry: false,
    refetchInterval: (q) =>
      q.state.data?.status === "PENDING" &&
      new Date(q.state.data.expires_at).getTime() > Date.now()
        ? 2000
        : false,
  });
  const personal = query.data?.purpose === "personal";
  const reauthorize = query.data?.purpose === "reauthorize";
  const selectedWorkspace = reauthorize
    ? (query.data?.account_id ?? "")
    : workspace;
  const complete = useMutation({
    mutationFn: () =>
      azureApi(api.http).complete(
        orgId,
        session,
        personal ? undefined : selectedWorkspace,
      ),
  });
  const done = complete.isSuccess || query.data?.status === "COMPLETED";
  useEffect(() => {
    if (!done || !window.opener) return;
    try {
      window.opener.postMessage(
        {
          type: "openfox:oauth",
          provider: "azure_devops",
          session,
          status: "ok",
          reason: "",
        },
        window.location.origin,
      );
      window.close();
    } catch {
      /* The completed session can still be read after refresh. */
    }
  }, [done, session]);
  const expired =
    query.data?.status === "EXPIRED" ||
    (["PENDING", "SELECTING"].includes(query.data?.status ?? "") &&
      new Date(query.data?.expires_at ?? "").getTime() <= Date.now());
  const failure = query.data?.error_code || reason;
  const message = expired
    ? "expired"
    : failure === "account_mismatch"
      ? "identityMismatch"
      : failure === "permission_denied"
        ? "permission"
        : failure === "rate_limited"
          ? "rateLimited"
          : failure === "connection_changed"
            ? "changed"
            : "failed";
  return (
    <main className="flex min-h-screen items-center justify-center bg-surface-app p-6">
      <div className="flex w-full max-w-[560px] flex-col gap-4 rounded-xl border border-line-default bg-surface-card p-6">
        <h1 className="text-title font-medium">
          {t(
            personal
              ? "integrations.azure.identityTitle"
              : "integrations.azure.completeTitle",
          )}
        </h1>
        <p className="text-body-sm text-fg-muted">
          {t(
            personal
              ? "integrations.azure.identityDescription"
              : "integrations.azure.sharedDescription",
          )}
        </p>
        {done ? (
          <Callout tone="info">{t("accountBinding.connectedToast")}</Callout>
        ) : query.data?.status === "SELECTING" && !expired ? (
          <>
            <p>
              {query.data.identity?.display_name}· {query.data.identity?.id}
            </p>
            <p className="text-caption text-fg-muted">
              {t("integrations.azure.tenant")} · {query.data.tenant_id}
            </p>
            <p className="break-all text-caption">
              {t("integrations.azure.workspace")} ·{" "}
              {personal ? query.data.account_id : selectedWorkspace}
            </p>
            {!personal && (
              <>
                <label className="grid gap-2">
                  {t("integrations.azure.workspace")}
                  <Select
                    aria-label={t("integrations.azure.workspace")}
                    value={selectedWorkspace || null}
                    disabled={reauthorize}
                    options={(query.data.accounts ?? []).map((w) => ({
                      value: w.id,
                      label: w.name || w.id,
                    }))}
                    onChange={setWorkspace}
                    placeholder={t("integrations.azure.chooseWorkspace")}
                  />
                </label>
                {!!query.data.affected_accounts?.length && (
                  <Callout tone="warning">
                    <p>{t("integrations.azure.affected")}</p>
                    <ul className="list-disc pl-5">
                      {query.data.affected_accounts.map((name) => (
                        <li key={name.id}>
                          {name.name} · {name.id}
                        </li>
                      ))}
                    </ul>
                    <p>{t("integrations.azure.reverifyNotice")}</p>
                  </Callout>
                )}
                <label className="flex items-start gap-2 text-body-sm">
                  <input
                    type="checkbox"
                    checked={shared}
                    onChange={(e) => setShared(e.target.checked)}
                  />
                  {t("integrations.azure.shareConsent")}
                </label>
              </>
            )}
            {complete.isError && (
              <Callout tone="warning">
                {integrationErrorText(t, complete.error)}
              </Callout>
            )}
            <Button
              onClick={() => complete.mutate()}
              loading={complete.isPending}
              disabled={!personal && (!selectedWorkspace || !shared)}
            >
              {t(
                personal
                  ? "integrations.azure.confirmIdentity"
                  : "integrations.azure.confirmWorkspace",
              )}
            </Button>
          </>
        ) : !query.isError &&
          !expired &&
          ((query.isPending && orgId && session) ||
            query.data?.status === "PENDING") ? (
          <p role="status">{t("accountBinding.waiting")}</p>
        ) : (
          <Callout tone="warning">{t(`integrations.azure.${message}`)}</Callout>
        )}
        <Link
          className="underline"
          to={
            orgId
              ? `/o/${orgId}/settings/${personal ? "account-binding" : "integrations/azure_devops"}`
              : "/"
          }
        >
          {t("common.back")}
        </Link>
      </div>
    </main>
  );
}
