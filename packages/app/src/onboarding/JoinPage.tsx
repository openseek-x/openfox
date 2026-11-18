import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";

import { isApiError } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Icon } from "@openfox/ui";

import { AuthLayout } from "../auth/AuthLayout";
import { keys, useApi } from "../context";
import { AccountBar } from "./AccountBar";

/** 通过邀请链接加入组织（07）。链接格式：/join?invite=<邀请 ID>&token=<令牌>。 */
export function JoinPage() {
  const t = useT();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [params] = useSearchParams();
  const inviteId = params.get("invite") ?? "";
  const token = params.get("token") ?? "";
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(
    inviteId && token ? null : t("join.incomplete"),
  );

  const accept = async () => {
    setBusy(true);
    setError(null);
    try {
      const result = await api.invites.accept(inviteId, token);
      await queryClient.invalidateQueries({ queryKey: keys.organizations });
      navigate(`/o/${result.organization_id}`, { replace: true });
    } catch (err) {
      if (
        isApiError(err) &&
        (err.status === 404 || err.status === 409 || err.status === 422)
      )
        setError(t("join.invalid"));
      else setError(t("join.failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthLayout aside={<AccountBar email={me.data?.email ?? ""} />}>
      <div className="flex w-[420px] flex-col items-center gap-6 text-center">
        <span className="flex size-14 items-center justify-center rounded-xl bg-accent-soft text-accent">
          <Icon name="users" size={28} />
        </span>
        <div className="flex flex-col gap-2">
          <h1 className="text-heading font-medium">{t("join.title")}</h1>
          <p className="text-body text-fg-muted">{t("join.desc")}</p>
        </div>
        {error && <Callout tone="danger">{error}</Callout>}
        <div className="flex w-full gap-2">
          <Button
            variant="outline"
            size="lg"
            className="flex-1"
            onClick={() => navigate("/", { replace: true })}
          >
            {t("join.decline")}
          </Button>
          <Button
            size="lg"
            className="flex-1"
            loading={busy}
            disabled={!inviteId || !token}
            onClick={() => void accept()}
          >
            {t("join.accept")}
          </Button>
        </div>
        {me.data && (
          <p className="text-caption text-fg-muted">
            {t("join.signedInAs", { email: me.data.email })}
          </p>
        )}
      </div>
    </AuthLayout>
  );
}
