import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "react-router-dom";

import { useT } from "@openfox/i18n";
import { Badge, Button, Callout } from "@openfox/ui";

import { AuthLayout } from "../auth/AuthLayout";
import { keys, useApi } from "../context";
import { OrgIcon } from "../organization/OrgIcon";
import { clearNavMemory } from "../shell/navMemory";
import { AccountBar } from "./AccountBar";

/** 凭邀请链接加入组织。链接格式：/invite/<令牌>，需要登录。 */
export function InviteLinkPage() {
  const t = useT();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { token = "" } = useParams();
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  const preview = useQuery({
    queryKey: ["invite-link-preview", token],
    queryFn: () => api.inviteLink.preview(token),
    retry: false,
  });
  const join = useMutation({
    mutationFn: () => api.inviteLink.join(token),
    onSuccess: async (result) => {
      await queryClient.invalidateQueries({ queryKey: keys.organizations });
      navigate(`/o/${result.organization_id}`, { replace: true });
    },
  });
  const switchAccount = async () => {
    await api.auth.logout().catch(() => undefined);
    queryClient.clear();
    clearNavMemory();
    navigate(`/login?next=${encodeURIComponent(`/invite/${token}`)}`, {
      replace: true,
    });
  };
  const data = preview.data;
  const role =
    data?.role === "GUEST"
      ? t("organization.join.roleGuest")
      : t("organization.join.roleMember");

  return (
    <AuthLayout aside={<AccountBar email={me.data?.email ?? ""} />}>
      <div className="flex w-[420px] flex-col items-center gap-6 text-center">
        {preview.isLoading ? (
          <div className="h-40" aria-busy="true" />
        ) : !data ? (
          <>
            <Callout tone="danger">{t("organization.join.invalid")}</Callout>
            <Button
              variant="outline"
              onClick={() => navigate("/", { replace: true })}
            >
              {t("common.back")}
            </Button>
          </>
        ) : (
          <>
            <OrgIcon org={data} size={64} />
            <div className="flex flex-col gap-2">
              <h1 className="text-heading font-medium">
                {t("organization.join.title", { name: data.name })}
              </h1>
              {data.inviter_email && (
                <p className="text-body text-fg-muted">
                  {t("organization.join.invitedBy", {
                    inviter: data.inviter_email,
                    name: data.name,
                  })}
                </p>
              )}
              <p className="text-body-sm text-fg-muted">
                {t("organization.join.stats", {
                  members: data.member_count,
                  agents: data.agent_count,
                })}
              </p>
            </div>
            {data.already_member ? (
              <>
                <p className="text-body text-fg-secondary">
                  {t("organization.join.alreadyMember", { name: data.name })}
                </p>
                <Button
                  size="lg"
                  className="w-full"
                  onClick={() =>
                    navigate(`/o/${data.organization_id}`, { replace: true })
                  }
                >
                  {t("organization.join.open")}
                </Button>
              </>
            ) : (
              <>
                <div className="flex flex-col items-center gap-1.5">
                  <Badge variant="outline">{role}</Badge>
                  <p className="text-body-sm text-fg-muted">
                    {t("organization.join.asRole", { role })}
                  </p>
                </div>
                {join.isError && (
                  <Callout tone="danger">
                    {t("organization.join.failed")}
                  </Callout>
                )}
                <div className="flex w-full gap-2">
                  <Button
                    variant="outline"
                    size="lg"
                    className="flex-1"
                    disabled={join.isPending}
                    onClick={() => navigate("/", { replace: true })}
                  >
                    {t("organization.join.decline")}
                  </Button>
                  <Button
                    size="lg"
                    className="flex-1"
                    loading={join.isPending}
                    onClick={() => join.mutate()}
                  >
                    {t("organization.join.join")}
                  </Button>
                </div>
              </>
            )}
          </>
        )}
        {me.data && (
          <p className="text-caption text-fg-muted">
            {t("organization.join.signedInAs", { email: me.data.email })}{" "}
            <button
              type="button"
              className="text-fg-default underline-offset-2 hover:underline"
              onClick={() => void switchAccount()}
            >
              {t("organization.join.switchAccount")}
            </button>
          </p>
        )}
      </div>
    </AuthLayout>
  );
}
