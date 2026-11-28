import { useEffectEvent, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import {
  newIdempotencyKey,
  type SlackApp,
  type SlackOAuthSession,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog } from "@openfox/ui";
import { useApi } from "../context";
import { slackAuthorizeURL, slackError, slackReason } from "./slack-model";
import { useSlackWrite } from "./slack-hooks";

export function SlackOAuthButton({
  orgId,
  app,
  purpose,
  refresh,
  disabled,
  label,
}: {
  orgId: string;
  app?: SlackApp;
  purpose: "organization" | "personal" | "app";
  refresh: () => void;
  disabled?: boolean;
  label: string;
}) {
  const api = useApi(),
    t = useT(),
    write = useSlackWrite(refresh);
  const [session, setSession] = useState<SlackOAuthSession>();
  return (
    <>
      <Button
        variant="outline"
        disabled={disabled || write.busy || write.blocked}
        onClick={() => {
          const input = {
            purpose,
            app_id: app?.id ?? "",
            version: app?.version ?? 0,
            idempotency_key: newIdempotencyKey(),
          };
          void write.run(
            (signal) =>
              app?.installed && purpose !== "personal"
                ? api.slack.reauthorize(orgId, app.id, input, signal)
                : api.slack.startOAuth(orgId, input, signal),
            setSession,
          );
        }}
      >
        {label}
      </Button>
      {!!write.error && (
        <Callout tone="danger">{slackError(t, write.error)}</Callout>
      )}
      {session && (
        <Dialog
          open
          title={t("integrations.slack.session")}
          onClose={() => setSession(undefined)}
          footer={
            <Button variant="outline" onClick={() => setSession(undefined)}>
              {t("common.close")}
            </Button>
          }
        >
          <SlackSession
            orgId={orgId}
            sessionId={session.id}
            initial={session}
            onDone={refresh}
          />
        </Dialog>
      )}
    </>
  );
}
export function SlackSession({
  orgId,
  sessionId,
  initial,
  onDone,
}: {
  orgId: string;
  sessionId: string;
  initial?: SlackOAuthSession;
  onDone?: () => void;
}) {
  const api = useApi(),
    t = useT();
  const query = useQuery({
    queryKey: ["slack-oauth", orgId, sessionId],
    queryFn: ({ signal }) => api.slack.session(orgId, sessionId, signal),
    initialData: initial,
    enabled: !!(orgId && sessionId),
    gcTime: 0,
    retry: false,
    refetchInterval: (q) =>
      q.state.data &&
      ["PENDING", "EXCHANGING"].includes(q.state.data.status) &&
      Date.parse(q.state.data.expires_at) > Date.now()
        ? 2000
        : false,
  });
  const data = query.data,
    done = data?.status === "DONE",
    expired =
      !!data &&
      ["PENDING", "EXCHANGING"].includes(data.status) &&
      Date.parse(data.expires_at) <= Date.now();
  const notifyDone = useEffectEvent(() => onDone?.());
  useEffect(() => {
    if (done) notifyDone();
  }, [done]); // Revalidate through GET; callback query strings never establish success.
  if (query.isError || !orgId || !sessionId)
    return (
      <Callout tone="danger">{t("integrations.slack.loadFailed")}</Callout>
    );
  if (!data) return <p>{t("integrations.slack.waiting")}</p>;
  return (
    <div className="flex flex-col gap-3">
      <Callout
        tone={
          done
            ? "info"
            : ["FAILED", "UNKNOWN", "CANCELLED"].includes(data.status) ||
                expired
              ? "warning"
              : "info"
        }
      >
        {t(
          done
            ? "integrations.slack.oauthDone"
            : expired
              ? "integrations.slack.oauthExpired"
              : data.status === "UNKNOWN"
                ? "integrations.slack.oauthUnknown"
                : ["FAILED", "CANCELLED"].includes(data.status)
                  ? "integrations.slack.oauthFailed"
                  : "integrations.slack.waiting",
        )}
      </Callout>
      {!!data.status_reason && (
        <p className="text-body-sm text-fg-muted">
          {slackReason(t, data.status_reason)}
        </p>
      )}
      {data.status === "PENDING" &&
        !expired &&
        (slackAuthorizeURL(data.authorize_url, data.purpose) ? (
          <a
            href={data.authorize_url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-body font-medium underline"
          >
            {t("integrations.slack.openAuthorize")}
          </a>
        ) : (
          <Callout tone="danger">
            {t("integrations.slack.oauthInvalid")}
          </Callout>
        ))}
      <Button
        variant="outline"
        disabled={query.isFetching}
        onClick={() => void query.refetch()}
      >
        {t("integrations.slack.reload")}
      </Button>
    </div>
  );
}
export function SlackComplete({
  orgId,
  session,
}: {
  orgId: string;
  session: string;
}) {
  const t = useT();
  return (
    <main className="flex min-h-screen items-center justify-center bg-surface-app p-6">
      <div className="flex w-full max-w-[560px] flex-col gap-4 rounded-xl border border-line-default bg-surface-card p-6">
        <h1 className="text-title font-medium">
          {t("integrations.slack.session")}
        </h1>
        <SlackSession orgId={orgId} sessionId={session} />
        <Link
          className="underline"
          to={
            orgId
              ? `/o/${encodeURIComponent(orgId)}/settings/integrations/slack`
              : "/"
          }
        >
          {t("integrations.slack.return")}
        </Link>
      </div>
    </main>
  );
}
