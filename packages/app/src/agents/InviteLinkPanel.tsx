import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import {
  isApiError,
  type InviteLink,
  type InviteLinkInput,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Field,
  Icon,
  Select,
  Switch,
  cn,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import { InviteSpacesField } from "./InviteSpacesField";

/** 邀请链接的完整地址：站点地址 + /invite/{token}。 */
export const inviteLinkUrl = (token: string) =>
  `${window.location.origin}/invite/${encodeURIComponent(token)}`;

/**
 * 邀请弹窗「通过链接」页签：开关、角色、加入空间改动即保存；
 * 复制时链接未启用会先静默启用；重置后旧链接立即失效。角色只能是成员或访客。
 */
export function InviteLinkPanel({ orgId }: { orgId: string }) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const link = useQuery({
    queryKey: keys.inviteLink(orgId),
    queryFn: () => api.inviteLink.get(orgId),
    retry: false,
  });
  const [resetting, setResetting] = useState(false);
  const setLink = (next: InviteLink) =>
    queryClient.setQueryData(keys.inviteLink(orgId), next);
  const save = useMutation({
    mutationFn: (patch: Partial<Omit<InviteLinkInput, "version">>) => {
      const current = link.data!;
      return api.inviteLink.put(orgId, {
        enabled: current.enabled,
        role: current.role,
        space_ids: current.space_ids,
        ...patch,
        version: current.version,
      });
    },
    onSuccess: setLink,
    onError: () => {
      toast(t("organization.inviteLink.updateFailed"), { tone: "error" });
      void link.refetch();
    },
  });
  const reset = useMutation({
    mutationFn: () => api.inviteLink.reset(orgId, link.data!.version),
    onSuccess: (next) => {
      setLink(next);
      setResetting(false);
      toast(t("organization.inviteLink.resetDone"));
    },
    onError: () =>
      toast(t("organization.inviteLink.resetFailed"), { tone: "error" }),
  });

  if (link.isLoading)
    return (
      <p className="py-8 text-center text-body-sm text-fg-muted">
        {t("organization.inviteLink.loading")}
      </p>
    );
  if (!link.data)
    return (
      <p className="py-8 text-center text-body-sm text-fg-muted">
        {isApiError(link.error, "SECRETS_UNAVAILABLE")
          ? t("organization.errors.secretsUnavailable")
          : t("organization.inviteLink.updateFailed")}
      </p>
    );

  const data = link.data;
  const busy = save.isPending || reset.isPending;
  const guest = data.role === "GUEST";
  const copy = async () => {
    try {
      let current = data;
      if (!current.enabled || !current.token)
        current = await save.mutateAsync({ enabled: true });
      await navigator.clipboard.writeText(inviteLinkUrl(current.token!));
      toast(t("organization.inviteLink.copied"));
    } catch {
      toast(t("organization.inviteLink.copyFailed"), { tone: "error" });
    }
  };
  const update = (patch: Partial<Omit<InviteLinkInput, "version">>) =>
    save.mutate(patch, {
      onSuccess: () => toast(t("organization.inviteLink.updated")),
    });

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-start gap-3">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="text-body font-medium">
            {t("organization.inviteLink.enable")}
          </span>
          <span className="text-caption text-fg-muted">
            {guest
              ? t("organization.inviteLink.enableGuest")
              : t("organization.inviteLink.enableMember")}
          </span>
        </div>
        <Switch
          checked={data.enabled}
          disabled={busy}
          label={t("organization.inviteLink.enable")}
          onCheckedChange={(enabled) => update({ enabled })}
        />
      </div>
      <div className="flex flex-col gap-1.5">
        <div className="flex items-center gap-2">
          <input
            readOnly
            aria-label={t("agents.invite.tabs.link")}
            value={data.token ? inviteLinkUrl(data.token) : ""}
            className={cn(
              "h-8 min-w-0 flex-1 rounded-md border border-line-default bg-surface-muted px-2.5 font-mono text-body-sm",
              !data.enabled && "opacity-50",
            )}
          />
          <Button
            variant="outline"
            leading={<Icon name="copy" />}
            disabled={busy}
            onClick={() => void copy()}
          >
            {t("organization.inviteLink.copy")}
          </Button>
          <Button
            variant="outline"
            leading={<Icon name="refresh-cw" />}
            disabled={busy || !data.token}
            onClick={() => setResetting(true)}
          >
            {t("organization.inviteLink.reset")}
          </Button>
        </div>
        <span className="text-caption text-fg-muted">
          {t("organization.inviteLink.share")}
        </span>
      </div>
      <Field
        label={t("agents.invite.role")}
        helper={t(`agents.invite.roles.${data.role}_DESC`)}
      >
        <Select
          value={data.role}
          disabled={busy}
          onChange={(role) => {
            if (role === "MEMBER" || role === "GUEST") update({ role });
          }}
          options={(["MEMBER", "ADMIN", "GUEST"] as const).map((role) => ({
            value: role,
            label: t(`agents.invite.roles.${role}`),
            description:
              role === "ADMIN"
                ? t("organization.inviteLink.adminDisabled")
                : t(`agents.invite.roles.${role}_DESC`),
            disabled: role === "ADMIN",
          }))}
        />
      </Field>
      <Field
        label={t("agents.invite.spaces")}
        helper={
          guest
            ? t("organization.inviteLink.spacesGuest")
            : t("organization.inviteLink.spacesMember")
        }
      >
        <InviteSpacesField
          orgId={orgId}
          value={data.space_ids}
          disabled={busy}
          onChange={(space_ids) => update({ space_ids })}
        />
      </Field>
      <ConfirmDialog
        open={resetting}
        title={t("organization.inviteLink.resetTitle")}
        description={t("organization.inviteLink.resetBody")}
        confirmLabel={
          reset.isPending
            ? t("organization.inviteLink.resetting")
            : t("organization.inviteLink.reset")
        }
        loading={reset.isPending}
        onCancel={() => setResetting(false)}
        onConfirm={() => reset.mutate()}
      />
    </div>
  );
}
