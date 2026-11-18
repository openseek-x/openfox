import { useEffect } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { useT } from "@openfox/i18n";
import { Button, Callout } from "@openfox/ui";
import { useApi } from "../context";

export function TeambitionComplete({
  orgId,
  session,
  reason,
}: {
  orgId: string;
  session: string;
  reason: string;
}) {
  const api = useApi();
  const t = useT();
  const query = useQuery({
    queryKey: ["teambition-personal", orgId, session],
    queryFn: () => api.accountBindings.teambitionSession(orgId, session),
    enabled: Boolean(orgId && session),
    refetchInterval: (q) => (q.state.data?.status === "PENDING" ? 2000 : false),
  });
  const complete = useMutation({
    mutationFn: () => api.accountBindings.completeTeambition(orgId, session),
  });
  const done = complete.isSuccess || query.data?.status === "COMPLETED";
  useEffect(() => {
    if (!done || !window.opener) return;
    try {
      window.opener.postMessage(
        {
          type: "openfox:oauth",
          provider: "teambition",
          session,
          status: "ok",
          reason: "",
        },
        window.location.origin,
      );
      window.close();
    } catch {
      /* The completed identity remains available after refresh. */
    }
  }, [done, session]);
  const failure = query.data?.error_code || reason;
  const errorKey =
    failure === "account_mismatch"
      ? "identity"
      : failure === "permission_denied"
        ? "permission"
        : failure === "rate_limited"
          ? "rate"
          : failure === "connection_changed"
            ? "changed"
            : "failed";
  return (
    <main className="flex min-h-screen items-center justify-center bg-surface-app p-6">
      <div className="flex w-full max-w-[460px] flex-col gap-4 rounded-xl border border-line-default bg-surface-card p-6">
        <h1 className="text-title font-medium">
          {t("accountBinding.teambition.title")}
        </h1>
        <p>{t("accountBinding.teambition.description")}</p>
        {done ? (
          <Callout tone="info">{t("accountBinding.connectedToast")}</Callout>
        ) : query.data?.status === "SELECTING" ? (
          <>
            <p>
              {query.data.identity?.name} · {query.data.identity?.email}
            </p>
            {complete.isError && (
              <Callout tone="warning">
                {t("accountBinding.teambition.failed")}
              </Callout>
            )}
            <Button
              onClick={() => complete.mutate()}
              loading={complete.isPending}
            >
              {t("accountBinding.teambition.confirm")}
            </Button>
          </>
        ) : (query.isPending && orgId && session) ||
          query.data?.status === "PENDING" ? (
          <p>{t("accountBinding.waiting")}</p>
        ) : (
          <Callout tone="warning">
            {t(`accountBinding.teambition.${errorKey}`)}
          </Callout>
        )}
        <Link
          className="underline"
          to={orgId ? `/o/${orgId}/settings/account-binding` : "/"}
        >
          {t("accountBinding.teambition.back")}
        </Link>
      </div>
    </main>
  );
}
