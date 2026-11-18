import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { bitbucketApi } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Select } from "@openfox/ui";
import { useApi } from "../context";
import { integrationErrorText } from "./queries";

export function bitbucketAuthorizeURL(raw: string): boolean {
  try {
    const url = new URL(raw);
    return (
      url.origin === "https://bitbucket.org" &&
      url.pathname === "/site/oauth2/authorize" &&
      !url.username &&
      !url.password &&
      !url.hash
    );
  } catch {
    return false;
  }
}

export function BitbucketComplete({
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
  const [consent, setConsent] = useState(false);
  const [senderConfirmed, setSenderConfirmed] = useState(false);
  const query = useQuery({
    queryKey: ["bitbucket-oauth", orgId, session],
    queryFn: () => bitbucketApi(api.http).session(orgId, session),
    enabled: Boolean(orgId && session),
    retry: false,
    refetchInterval: (q) =>
      q.state.data?.status === "PENDING" &&
      new Date(q.state.data.expires_at).getTime() > Date.now()
        ? 2000
        : false,
  });
  const data = query.data;
  const personal = data?.purpose === "personal";
  const reauthorize = data?.purpose === "reauthorize";
  const selected = reauthorize ? (data?.workspace_id ?? "") : workspace;
  const complete = useMutation({
    mutationFn: () =>
      bitbucketApi(api.http).complete(
        orgId,
        session,
        personal ? {} : { workspace_id: selected },
      ),
  });
  const done = complete.isSuccess || data?.status === "COMPLETED";
  useEffect(() => {
    setWorkspace("");
    setConsent(false);
    setSenderConfirmed(false);
  }, [orgId, session, data?.identity?.uuid, data?.previous_subject?.uuid]);
  useEffect(() => {
    if (!done || !window.opener) return;
    try {
      window.opener.postMessage(
        {
          type: "openfox:oauth",
          provider: "bitbucket",
          session,
          status: "ok",
          reason: "",
        },
        window.location.origin,
      );
      window.close();
    } catch {
      /* Completion remains recoverable through its stored session. */
    }
  }, [done, session]);
  const expired =
    data?.status === "EXPIRED" ||
    (data &&
      ["PENDING", "SELECTING"].includes(data.status) &&
      new Date(data.expires_at).getTime() <= Date.now());
  const failure = data?.error_code || reason;
  const failureKey = expired
    ? "expired"
    : failure === "permission_denied"
      ? "permission"
      : failure === "scope_denied"
        ? "scopeDenied"
        : failure === "configuration_changed"
          ? "appChanged"
          : failure === "connection_changed"
            ? "changed"
            : "failed";
  const workspaceValid =
    personal ||
    Boolean(selected && data?.workspaces.some((w) => w.uuid === selected));
  return (
    <main className="flex min-h-screen items-center justify-center bg-surface-app p-6">
      <div className="flex w-full max-w-[600px] flex-col gap-4 rounded-xl border border-line-default bg-surface-card p-6">
        <h1 className="text-title font-medium">
          {t(
            personal
              ? "integrations.bitbucket.identityTitle"
              : "integrations.bitbucket.completeTitle",
          )}
        </h1>
        <p className="text-body-sm text-fg-muted">
          {t(
            personal
              ? "integrations.bitbucket.identityDescription"
              : "integrations.bitbucket.organizationDescription",
          )}
        </p>
        {done ? (
          <Callout tone="info">{t("integrations.bitbucket.completed")}</Callout>
        ) : !expired && data?.status === "SELECTING" ? (
          <>
            {data.identity && (
              <p>
                {data.identity.display_name || data.identity.nickname} ·{" "}
                {data.identity.uuid}
              </p>
            )}
            {personal && (
              <p className="text-body-sm">
                {t("integrations.bitbucket.workspace")} · {data.workspace_id}
              </p>
            )}
            {!personal && (
              <>
                <label className="grid gap-2">
                  {t("integrations.bitbucket.workspace")}
                  <Select
                    aria-label={t("integrations.bitbucket.workspace")}
                    value={selected || null}
                    disabled={reauthorize}
                    options={data.workspaces.map((w) => ({
                      value: w.uuid,
                      label: `${w.name || w.slug} · ${w.slug}`,
                    }))}
                    onChange={setWorkspace}
                    placeholder={t("integrations.bitbucket.chooseWorkspace")}
                  />
                </label>
                {reauthorize && (
                  <p className="text-body-sm text-fg-muted">
                    {t("integrations.bitbucket.sameWorkspace")}
                  </p>
                )}
                {!data.workspaces.length && (
                  <Callout tone="warning">
                    {t("integrations.bitbucket.noWorkspace")}
                  </Callout>
                )}
                <p className="text-body-sm">
                  {t("integrations.bitbucket.scopeNotice")}
                </p>
              </>
            )}
            {data.subject_changed && (
              <Callout tone="warning">
                <p>{t("integrations.bitbucket.senderChanged")}</p>
                <p>
                  {t("integrations.bitbucket.previousSender", {
                    name:
                      data.previous_subject?.display_name ||
                      data.previous_subject?.nickname ||
                      "",
                    id: data.previous_subject?.uuid || "",
                  })}
                </p>
                <p>
                  {t("integrations.bitbucket.newSender", {
                    name:
                      data.identity?.display_name ||
                      data.identity?.nickname ||
                      "",
                    id: data.identity?.uuid || "",
                  })}
                </p>
                <label className="flex items-start gap-2 text-body-sm">
                  <input
                    type="checkbox"
                    checked={senderConfirmed}
                    onChange={(e) => setSenderConfirmed(e.target.checked)}
                  />
                  {t("integrations.bitbucket.senderConsent")}
                </label>
              </Callout>
            )}
            <label className="flex items-start gap-2 text-body-sm">
              <input
                type="checkbox"
                checked={consent}
                onChange={(e) => setConsent(e.target.checked)}
              />
              {t(
                personal
                  ? "integrations.bitbucket.identityConsent"
                  : "integrations.bitbucket.organizationConsent",
              )}
            </label>
            {complete.isError && (
              <Callout tone="warning">
                {integrationErrorText(t, complete.error)}
              </Callout>
            )}
            <Button
              onClick={() => complete.mutate()}
              loading={complete.isPending}
              disabled={
                !data.identity ||
                !workspaceValid ||
                !consent ||
                (data.subject_changed && !senderConfirmed)
              }
            >
              {t(
                personal
                  ? "integrations.bitbucket.confirmIdentity"
                  : "integrations.bitbucket.confirmWorkspace",
              )}
            </Button>
          </>
        ) : !query.isError &&
          !expired &&
          ((query.isPending && orgId && session) ||
            data?.status === "PENDING") ? (
          <p role="status">{t("accountBinding.waiting")}</p>
        ) : (
          <Callout tone="warning">
            {query.isError
              ? integrationErrorText(t, query.error)
              : t(`integrations.bitbucket.${failureKey}`)}
          </Callout>
        )}
        <Link
          className="underline"
          to={
            orgId
              ? `/o/${orgId}/settings/${personal ? "account-binding" : "integrations/bitbucket"}`
              : "/"
          }
        >
          {t("common.back")}
        </Link>
      </div>
    </main>
  );
}
