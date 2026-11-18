import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { isApiError, zohoApi } from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import { Button, Callout, Select } from "@openfox/ui";
import { useApi } from "../context";
export function zohoErrorText(t: Translate, error: unknown) {
  const code = isApiError(error) ? error.code : "";
  const key =
    code.includes("RATE_LIMIT") || code === "ZOHO_RETRY_LATER"
      ? "rateLimited"
      : code === "ZOHO_GRANT_IN_USE"
        ? "grantInUse"
        : code === "ZOHO_DIRECTORY_LIMIT"
          ? "limit"
          : [
                "VERSION_CONFLICT",
                "ZOHO_APP_CHANGED",
                "ZOHO_ALREADY_CONNECTED",
              ].includes(code)
            ? "changed"
            : code.includes("PERMISSION") || code === "FORBIDDEN"
              ? "permission"
              : code.includes("MISMATCH")
                ? "mismatch"
                : "failed";
  return t(`integrations.zoho.${key}`);
}
export function ZohoComplete({
  orgId,
  session,
}: {
  orgId: string;
  session: string;
}) {
  const api = useApi(),
    t = useT();
  const [portal, setPortal] = useState("");
  const [consent, setConsent] = useState(false);
  const query = useQuery({
    queryKey: ["zoho-oauth", orgId, session],
    queryFn: ({ signal }) => zohoApi(api.http).session(orgId, session, signal),
    enabled: !!orgId && !!session,
    retry: false,
    gcTime: 0,
    refetchInterval: (q) =>
      q.state.data?.status === "PENDING" &&
      new Date(q.state.data.expires_at).getTime() > Date.now()
        ? 2000
        : false,
  });
  const personal = query.data?.purpose === "personal",
    reauthorize = query.data?.purpose === "reauthorize";
  const selected = reauthorize ? (query.data?.portal_id ?? "") : portal;
  const complete = useMutation({
    mutationFn: () =>
      zohoApi(api.http).complete(
        orgId,
        session,
        personal ? undefined : selected,
      ),
    gcTime: 0,
  });
  const done = complete.isSuccess || query.data?.status === "COMPLETED";
  useEffect(() => {
    if (!done || !window.opener) return;
    try {
      window.opener.postMessage(
        {
          type: "openfox:oauth",
          provider: "zoho_bugtracker",
          session,
          status: "ok",
          reason: "",
        },
        window.location.origin,
      );
      window.close();
    } catch {
      /* Persistent session can be checked after refresh. */
    }
  }, [done, session]);
  const expired =
    query.data?.status === "EXPIRED" ||
    (!!query.data &&
      ["PENDING", "SELECTING"].includes(query.data.status) &&
      new Date(query.data.expires_at).getTime() <= Date.now());
  const failure = query.data?.error_code;
  const failureKey = expired
    ? "expired"
    : failure === "account_mismatch"
      ? "mismatch"
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
              ? "integrations.zoho.identityTitle"
              : "integrations.zoho.completeTitle",
          )}
        </h1>
        <p>
          {t(
            personal
              ? "integrations.zoho.identityDescription"
              : "integrations.zoho.shared",
          )}
        </p>
        <Callout tone="warning">{t("integrations.zoho.lifecycle")}</Callout>
        {done ? (
          <Callout>{t("accountBinding.connectedToast")}</Callout>
        ) : !expired && query.data?.status === "SELECTING" ? (
          <>
            <p>
              {query.data.identity?.name} · {query.data.identity?.id}
              {personal && ` · ${query.data.identity?.email ?? ""}`}
            </p>
            {personal ? (
              <p>
                {t("integrations.zoho.portal")} · {query.data.portal_id}
              </p>
            ) : (
              <>
                <label className="grid gap-2">
                  {t("integrations.zoho.portal")}
                  <Select
                    aria-label={t("integrations.zoho.portal")}
                    value={selected || null}
                    disabled={reauthorize}
                    options={query.data.portals.map((p) => ({
                      value: p.id,
                      label: `${p.name} · ${p.id}`,
                    }))}
                    onChange={setPortal}
                    placeholder={t("integrations.zoho.choosePortal")}
                  />
                </label>
                {!query.data.portals.length && (
                  <p>{t("integrations.zoho.noPortals")}</p>
                )}
                <label className="flex items-start gap-2">
                  <input
                    type="checkbox"
                    checked={consent}
                    onChange={(e) => setConsent(e.target.checked)}
                  />
                  {t("integrations.zoho.shareConsent")}
                </label>
              </>
            )}
            {complete.isError && (
              <Callout tone="danger">
                {zohoErrorText(t, complete.error)}
              </Callout>
            )}
            <Button
              loading={complete.isPending}
              disabled={!personal && (!selected || !consent)}
              onClick={() => complete.mutate()}
            >
              {t(
                personal
                  ? "integrations.zoho.confirmIdentity"
                  : "integrations.zoho.confirmPortal",
              )}
            </Button>
          </>
        ) : !query.isError &&
          !expired &&
          ((query.isPending && orgId && session) ||
            query.data?.status === "PENDING") ? (
          <p role="status">{t("accountBinding.waiting")}</p>
        ) : (
          <Callout tone="warning">
            {t(`integrations.zoho.${failureKey}`)}
          </Callout>
        )}
        <Link
          className="underline"
          to={
            orgId
              ? `/o/${orgId}/settings/${personal ? "account-binding" : "integrations/zoho_bugtracker"}`
              : "/"
          }
        >
          {t("common.back")}
        </Link>
      </div>
    </main>
  );
}
