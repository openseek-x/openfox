import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  bitbucketApi,
  type BitbucketJobDiagnostic,
  type IntegrationConnection,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout } from "@openfox/ui";
import { useApi } from "../context";
import { integrationErrorText } from "./queries";

export function bitbucketJobURL(
  job: Pick<BitbucketJobDiagnostic, "repository" | "url">,
): string {
  const parts = job.repository.split("/");
  if (
    parts.length !== 2 ||
    job.repository.length > 255 ||
    !parts.every((p) => /^[A-Za-z0-9_.-]+$/.test(p) && p !== "." && p !== "..")
  )
    return "";
  const base = `https://bitbucket.org/${job.repository}`;
  if (job.url === base) return base;
  const suffix = job.url.slice(base.length);
  return job.url.startsWith(base) &&
    /^\/pull-requests\/[1-9][0-9]*$/.test(suffix)
    ? job.url
    : "";
}
export function BitbucketJobs({
  org,
  connection,
}: {
  org: string;
  connection: IntegrationConnection;
}) {
  const api = useApi(),
    t = useT(),
    client = bitbucketApi(api.http);
  const [resend, setResend] = useState<BitbucketJobDiagnostic | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const active = connection.status === "ACTIVE";
  const query = useQuery({
    queryKey: ["bitbucket-jobs", org, connection.id],
    queryFn: () => client.jobs(org, connection.id),
    retry: false,
    refetchInterval: 5000,
  });
  const retry = useMutation({
    mutationFn: ({
      job,
      confirm,
    }: {
      job: BitbucketJobDiagnostic;
      confirm: boolean;
    }) =>
      client.retryJob(org, connection.id, {
        kind: job.kind,
        id: job.id,
        version: job.version,
        confirm_unsent: confirm,
      }),
    onSuccess: async () => {
      setResend(null);
      setConfirmed(false);
      await query.refetch();
    },
  });
  useEffect(() => {
    setResend(null);
    setConfirmed(false);
  }, [connection.version]);
  useEffect(() => {
    if (!resend || !query.data) return;
    const current = query.data.jobs.find(
      (j) => j.kind === resend.kind && j.id === resend.id,
    );
    if (
      !current ||
      current.version !== resend.version ||
      current.state !== resend.state ||
      !current.can_retry
    ) {
      setResend(null);
      setConfirmed(false);
    }
  }, [query.data, resend]);
  return (
    <section className="grid gap-3 border-t border-line-default pt-4">
      <h3 className="text-body font-medium">
        {t("integrations.bitbucket.jobsTitle")}
      </h3>
      <p className="text-body-sm text-fg-muted">
        {t("integrations.bitbucket.jobsDescription")}
      </p>
      {query.isError && (
        <Callout tone="warning">{integrationErrorText(t, query.error)}</Callout>
      )}
      {retry.isError && (
        <Callout tone="warning">{integrationErrorText(t, retry.error)}</Callout>
      )}
      {query.data?.jobs.map((job) => (
        <div
          key={`${job.kind}-${job.id}`}
          className="grid gap-2 rounded border border-line-default p-3"
        >
          <p>
            {t(
              job.kind === "BACKLINK"
                ? "integrations.bitbucket.backlinkJob"
                : "integrations.bitbucket.eventJob",
            )}{" "}
            · {job.repository} · {job.state}
          </p>
          {job.error_code && <p className="text-caption">{job.error_code}</p>}
          <p className="text-caption">
            {t("integrations.bitbucket.jobAttempts", { count: job.attempts })}
          </p>
          {job.state === "UNKNOWN" && (
            <Callout tone="warning">
              {t("integrations.bitbucket.unknownDescription")}
            </Callout>
          )}
          {bitbucketJobURL(job) && (
            <a
              className="underline"
              href={bitbucketJobURL(job)}
              target="_blank"
              rel="noopener noreferrer"
            >
              {t("integrations.bitbucket.inspectSource")}
            </a>
          )}
          {job.can_retry && (
            <Button
              size="28"
              variant="outline"
              disabled={!active || retry.isPending}
              onClick={() => {
                if (
                  job.requires_unsent_confirmation ||
                  job.state === "UNKNOWN"
                ) {
                  setResend(job);
                  setConfirmed(false);
                } else retry.mutate({ job, confirm: false });
              }}
            >
              {t(
                job.requires_unsent_confirmation || job.state === "UNKNOWN"
                  ? "integrations.bitbucket.reviewResend"
                  : "common.retry",
              )}
            </Button>
          )}
        </div>
      ))}
      {resend && (
        <Callout tone="warning">
          <p>{resend.repository}</p>
          <p>{t("integrations.bitbucket.resendDescription")}</p>
          <label className="flex gap-2">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={(e) => setConfirmed(e.target.checked)}
            />
            {t("integrations.bitbucket.unsentConsent")}
          </label>
          <div className="flex gap-2">
            <Button
              disabled={!confirmed || !active}
              loading={retry.isPending}
              onClick={() => retry.mutate({ job: resend, confirm: true })}
            >
              {t("integrations.bitbucket.confirmResend")}
            </Button>
            <Button
              variant="ghost"
              onClick={() => {
                setResend(null);
                setConfirmed(false);
              }}
            >
              {t("common.cancel")}
            </Button>
          </div>
        </Callout>
      )}
    </section>
  );
}
