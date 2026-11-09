import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useRef, useState } from "react";
import { Navigate, useParams, useSearchParams } from "react-router-dom";

import type {
  Invitation,
  MemberSpace,
  OrganizationMember,
  OrganizationRole,
} from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Avatar,
  Badge,
  Button,
  ConfirmDialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Popover,
  Tabs,
  cn,
  useToast,
} from "@openfox/ui";

import { InviteDialog } from "../agents/InviteDialog";
import { keys, useApi } from "../context";
import { SettingsColumn } from "../spaces/settings/parts";
import { SpaceIcon } from "../spaces/SpaceIcon";
import { ExternalAccountsTab } from "./ExternalAccountsTab";
import { ManageSpacesDialog } from "./ManageSpacesDialog";
import { organizationErrorMessage, useOrganizationSettings } from "./queries";

// 设置 · 成员。外部账号页签见 ExternalAccountsTab。

const MEMBER_PAGES = 5;
const ROLE_ORDER: OrganizationRole[] = ["OWNER", "ADMIN", "MEMBER", "GUEST"];

export type MemberRow = OrganizationMember & { spaces: MemberSpace[] };

/** 组织里所有 ACTIVE 成员（含所在空间），最多 500 人。 */
export function useMemberDetails(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.memberDetails(orgId),
    queryFn: async () => {
      const rows: MemberRow[] = [];
      let cursor: string | undefined;
      for (let i = 0; i < MEMBER_PAGES; i++) {
        const page = await api.members.list(orgId, cursor);
        for (const member of page.members)
          if (member.status === "ACTIVE")
            rows.push({ ...member, spaces: member.spaces ?? [] });
        if (!page.next_cursor) break;
        cursor = page.next_cursor;
      }
      return rows;
    },
  });
}

export function MembersSettingsPage() {
  const { orgId = "" } = useParams();
  const settings = useOrganizationSettings(orgId);
  if (!settings.data) return null;
  // 只有所有者 / 管理员可访问，其他角色去个人资料
  if (!settings.data.can_manage) return <Navigate to="../profile" replace />;
  return (
    <MembersBody
      orgId={orgId}
      orgName={settings.data.name}
      myRole={settings.data.my_role}
    />
  );
}

function MembersBody({
  orgId,
  orgName,
  myRole,
}: {
  orgId: string;
  orgName: string;
  myRole: OrganizationRole;
}) {
  const t = useT();
  const api = useApi();
  const [params, setParams] = useSearchParams();
  const tab = params.get("tab") === "external" ? "external" : "members";
  const [inviting, setInviting] = useState(false);
  // 外部账号页签里未保存的关联数；离开页签前要确认
  const [dirty, setDirty] = useState(0);
  const [leaveTo, setLeaveTo] = useState<string | null>(null);
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  const members = useMemberDetails(orgId);
  const rows = useMemo(() => {
    const myId = me.data?.id;
    // 所有者在前，其次是自己，其余按加入时间
    const rank = (row: MemberRow) =>
      row.role === "OWNER" ? 0 : row.user_id === myId ? 1 : 2;
    return [...(members.data ?? [])].sort(
      (a, b) => rank(a) - rank(b) || a.created_at.localeCompare(b.created_at),
    );
  }, [members.data, me.data?.id]);

  return (
    <SettingsColumn wide>
      <header className="flex items-start gap-4">
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <h1 className="text-[20px] leading-7 font-semibold text-fg-default">
            {tab === "external"
              ? t("organization.members.tabExternal")
              : t("organization.members.title", { count: rows.length })}
          </h1>
          <p className="text-body text-fg-muted">
            {tab === "external"
              ? t("organization.members.externalDescription")
              : t("organization.members.description")}
          </p>
        </div>
        {tab === "members" && (
          <Button
            leading={<Icon name="user-plus" />}
            onClick={() => setInviting(true)}
          >
            {t("organization.members.invite")}
          </Button>
        )}
      </header>
      <Tabs
        label={t("organization.members.tabs")}
        value={tab}
        onChange={(id) => {
          if (id !== "external" && dirty > 0) return setLeaveTo(id);
          setParams(id === "external" ? { tab: "external" } : {}, {
            replace: true,
          });
        }}
        tabs={[
          {
            id: "members",
            label: t("organization.members.tabMembers"),
            count: rows.length,
          },
          {
            id: "external",
            label: t("organization.members.tabExternal"),
          },
        ]}
      />
      {tab === "external" ? (
        <ExternalAccountsTab orgId={orgId} onDirtyChange={setDirty} />
      ) : (
        <>
          <MemberTable
            orgId={orgId}
            orgName={orgName}
            rows={rows}
            myId={me.data?.id}
            myRole={myRole}
          />
          <PendingInvitations orgId={orgId} />
        </>
      )}
      {inviting && (
        <InviteDialog orgId={orgId} onClose={() => setInviting(false)} />
      )}
      <ConfirmDialog
        open={leaveTo !== null}
        onCancel={() => setLeaveTo(null)}
        onConfirm={() => {
          setLeaveTo(null);
          setDirty(0);
          setParams({}, { replace: true });
        }}
        title={t("organization.members.ext.leaveTitle")}
        description={t("organization.members.ext.leaveBody", { count: dirty })}
        confirmLabel={t("organization.members.ext.discard")}
        cancelLabel={t("organization.members.ext.keepEditing")}
        danger
      />
    </SettingsColumn>
  );
}

const GRID =
  "grid grid-cols-[minmax(0,1fr)_112px_80px_96px_32px] items-center gap-3";

function MemberTable({
  orgId,
  orgName,
  rows,
  myId,
  myRole,
}: {
  orgId: string;
  orgName: string;
  rows: MemberRow[];
  myId: string | undefined;
  myRole: OrganizationRole;
}) {
  const t = useT();
  const { formatDate } = useI18n();
  return (
    <div role="table" aria-label={t("organization.members.tabMembers")}>
      <div
        role="row"
        className={cn(
          GRID,
          "h-10 rounded-lg bg-surface-muted/50 px-2 text-caption text-fg-muted",
        )}
      >
        <span role="columnheader">{t("organization.members.name")}</span>
        <span role="columnheader">{t("organization.members.role")}</span>
        <span role="columnheader">{t("organization.members.spaces")}</span>
        <span role="columnheader">{t("organization.members.joined")}</span>
        <span role="columnheader" className="sr-only">
          {t("organization.members.rowActions")}
        </span>
      </div>
      {rows.length === 0 && (
        <p className="py-10 text-center text-body-sm text-fg-muted">
          {t("organization.members.empty")}
        </p>
      )}
      {rows.map((row) => (
        <div
          key={row.user_id}
          role="row"
          className={cn(GRID, "h-14 border-b border-line-soft px-2")}
        >
          <span role="cell" className="flex min-w-0 items-center gap-2.5">
            <Avatar
              name={row.full_name || row.email}
              src={row.avatar_url}
              size={28}
            />
            {/* 有全名时第一行显示全名，第二行显示邮箱 */}
            <span className="flex min-w-0 flex-col">
              <span className="truncate text-body font-medium text-fg-default">
                {row.full_name || row.email}
              </span>
              {row.full_name && (
                <span className="truncate text-caption text-fg-muted">
                  {row.email}
                </span>
              )}
            </span>
          </span>
          <span role="cell">
            <Badge variant="outline">
              {t(`agents.invite.roles.${row.role}`)}
            </Badge>
          </span>
          <span role="cell">
            <SpacesPopover spaces={row.spaces} />
          </span>
          <span role="cell" className="text-body-sm text-fg-muted">
            {formatDate(row.created_at, { month: "long", day: "numeric" })}
          </span>
          <span role="cell">
            <RowActions
              orgId={orgId}
              orgName={orgName}
              row={row}
              self={row.user_id === myId}
              myRole={myRole}
            />
          </span>
        </div>
      ))}
    </div>
  );
}

function SpacesPopover({ spaces }: { spaces: MemberSpace[] }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLButtonElement | null>(null);
  return (
    <>
      <button
        ref={anchor}
        type="button"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className="text-[13px] text-fg-muted hover:text-fg-default"
      >
        {t("organization.members.spaceCount", { count: spaces.length })}
      </button>
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        placement="top-start"
        role="dialog"
        aria-label={t("organization.members.spaces")}
        className="w-56 p-1.5"
      >
        {spaces.length === 0 ? (
          <p className="px-2 py-1.5 text-body-sm text-fg-muted">
            {t("organization.members.noActiveSpaces")}
          </p>
        ) : (
          <ul>
            {spaces.map((space) => (
              <li
                key={space.id}
                className="flex h-8 items-center gap-2 px-2 text-body-sm"
              >
                <SpaceIcon icon={space.icon} size={16} />
                <span className="min-w-0 flex-1 truncate">{space.name}</span>
                {space.visibility === "private" && (
                  <Icon
                    name="lock"
                    size={12}
                    className="text-fg-muted"
                    aria-label={t("organization.members.private")}
                  />
                )}
              </li>
            ))}
          </ul>
        )}
      </Popover>
    </>
  );
}

/**
 * 行操作：可改角色 = 可移除 = 不是自己且（当前用户是所有者，或目标是成员 / 访客）——管理员不能管理管理员与所有者，
 * 与服务端规则一致；不满足时隐藏对应项，自己那一行只剩「管理空间…」。管理员只能在成员与访客之间切换。
 */
function RowActions({
  orgId,
  orgName,
  row,
  self,
  myRole,
}: {
  orgId: string;
  orgName: string;
  row: MemberRow;
  self: boolean;
  myRole: OrganizationRole;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const anchor = useRef<HTMLElement | null>(null);
  const [panel, setPanel] = useState<"role" | "spaces" | "remove" | null>(null);
  const canChange =
    !self &&
    (myRole === "OWNER" || row.role === "MEMBER" || row.role === "GUEST");
  const roles = ROLE_ORDER.filter(
    (role) => myRole === "OWNER" || role === "MEMBER" || role === "GUEST",
  );
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: keys.members(orgId) });
    void queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
  };
  const change = useMutation({
    mutationFn: (input: { role?: OrganizationRole; status?: "REVOKED" }) =>
      api.memberships.change(orgId, row.user_id, input),
    onSuccess: (_member, input) => {
      toast(
        input.status
          ? t("organization.members.removed")
          : t("organization.members.roleUpdated"),
      );
      setPanel(null);
      refresh();
    },
    onError: (error, input) =>
      toast(
        organizationErrorMessage(
          t,
          error,
          input.status
            ? "organization.members.removeFailed"
            : "organization.members.updateFailed",
        ),
        { tone: "error" },
      ),
  });
  return (
    <>
      <Menu
        label={t("organization.members.rowActions")}
        placement="bottom-end"
        className="min-w-44"
        trigger={({ ref, open, toggle }) => (
          <IconButton
            ref={(node) => {
              ref(node);
              anchor.current = node;
            }}
            label={t("organization.members.rowActions")}
            icon={<Icon name="ellipsis" />}
            size="28"
            active={open}
            disabled={change.isPending}
            aria-haspopup="menu"
            aria-expanded={open}
            onClick={toggle}
          />
        )}
      >
        {canChange && (
          <MenuItem
            trailing={<Icon name="chevron-right" />}
            onSelect={() => setPanel("role")}
          >
            {t("organization.members.changeRole")}
          </MenuItem>
        )}
        <MenuItem onSelect={() => setPanel("spaces")}>
          {t("organization.members.manageSpaces")}
        </MenuItem>
        {canChange && (
          <MenuItem danger onSelect={() => setPanel("remove")}>
            {t("organization.members.remove")}
          </MenuItem>
        )}
      </Menu>
      <Popover
        open={panel === "role"}
        onOpenChange={(open) => setPanel(open ? "role" : null)}
        anchorRef={anchor}
        placement="bottom-end"
        role="menu"
        aria-label={t("organization.members.changeRole")}
        className="w-72 p-1.5"
      >
        {roles.map((role) => (
          <MenuItem
            key={role}
            selected={role === row.role}
            description={t(`agents.invite.roles.${role}_DESC`)}
            onSelect={() => {
              if (role === row.role) setPanel(null);
              else change.mutate({ role });
            }}
          >
            {t(`agents.invite.roles.${role}`)}
          </MenuItem>
        ))}
      </Popover>
      {panel === "spaces" && (
        <ManageSpacesDialog
          orgId={orgId}
          member={row}
          onClose={() => setPanel(null)}
        />
      )}
      <ConfirmDialog
        open={panel === "remove"}
        danger
        title={t("organization.members.removeTitle", { name: row.email })}
        description={t("organization.members.removeBody", {
          name: row.email,
          org: orgName,
        })}
        confirmLabel={t("common.confirm")}
        loading={change.isPending}
        onCancel={() => setPanel(null)}
        onConfirm={() => change.mutate({ status: "REVOKED" })}
      />
    </>
  );
}

function PendingInvitations({ orgId }: { orgId: string }) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const invitations = useQuery({
    queryKey: keys.invitations(orgId),
    queryFn: () => api.invitations.list(orgId),
  });
  const [revoking, setRevoking] = useState<Invitation | null>(null);
  const refresh = () =>
    void queryClient.invalidateQueries({ queryKey: keys.invitations(orgId) });
  const resend = useMutation({
    mutationFn: (id: string) => api.invitations.resend(orgId, id),
    onSuccess: (invitation) => {
      toast(
        invitation.delivery_status === "FAILED"
          ? t("organization.members.resendFailed")
          : t("organization.members.resent"),
        { tone: invitation.delivery_status === "FAILED" ? "error" : undefined },
      );
      refresh();
    },
    onError: (error) =>
      toast(
        organizationErrorMessage(t, error, "organization.members.resendFailed"),
        { tone: "error" },
      ),
  });
  const revoke = useMutation({
    mutationFn: (id: string) => api.invitations.revoke(orgId, id),
    onSuccess: () => {
      toast(t("organization.members.revoked"));
      setRevoking(null);
      refresh();
    },
    onError: () =>
      toast(t("organization.members.revokeFailed"), { tone: "error" }),
  });
  const pending = (invitations.data ?? []).filter(
    (item) => item.status === "PENDING",
  );
  // 只在有邀请时出现
  if (pending.length === 0) return null;
  const now = Date.now();
  return (
    <section
      aria-label={t("organization.members.pendingTitle", {
        count: pending.length,
      })}
      className="flex flex-col gap-3 pt-4"
    >
      <header className="flex flex-col gap-0.5">
        <h2 className="text-title font-medium text-fg-default">
          {t("organization.members.pendingTitle", { count: pending.length })}
        </h2>
        <p className="text-body-sm text-fg-muted">
          {t("organization.members.pendingDescription")}
        </p>
      </header>
      <div
        role="table"
        aria-label={t("organization.members.pendingDescription")}
      >
        <div className="flex h-10 items-center gap-3 rounded-lg bg-surface-muted/50 px-2 text-caption text-fg-muted">
          <span className="flex-1 pl-9">{t("organization.members.email")}</span>
          <span className="w-28">{t("organization.members.role")}</span>
          <span className="w-[108px]" />
        </div>
        {pending.map((invitation) => {
          const cooling =
            invitation.last_sent_at !== null &&
            invitation.last_sent_at !== undefined &&
            now - new Date(invitation.last_sent_at).getTime() < 60_000;
          const failed = invitation.delivery_status === "FAILED";
          return (
            <div
              key={invitation.id}
              role="row"
              className="flex h-14 items-center gap-3 border-b border-line-soft px-2"
            >
              <span
                role="cell"
                className="flex min-w-0 flex-1 items-center gap-2.5"
              >
                <span className="flex size-7 shrink-0 items-center justify-center rounded-full bg-surface-muted text-fg-muted">
                  <Icon name="mail" size={14} />
                </span>
                <span className="flex min-w-0 flex-col">
                  <span className="truncate text-body font-medium">
                    {invitation.email}
                  </span>
                  <span
                    className={cn(
                      "flex items-center gap-1 text-caption",
                      failed ? "text-danger" : "text-fg-muted",
                    )}
                  >
                    <Icon name={failed ? "circle-alert" : "clock"} size={12} />
                    {failed
                      ? t("organization.members.deliveryFailed")
                      : t("organization.members.pending")}
                  </span>
                </span>
              </span>
              <span role="cell" className="w-28">
                <Badge variant="outline">
                  {t(`agents.invite.roles.${invitation.role}`)}
                </Badge>
              </span>
              <span role="cell" className="flex w-[108px] items-center gap-1">
                <Button
                  variant="ghost"
                  size="28"
                  disabled={cooling || resend.isPending}
                  onClick={() => resend.mutate(invitation.id)}
                >
                  {t("organization.members.resend")}
                </Button>
                <IconButton
                  label={t("organization.members.revoke")}
                  icon={<Icon name="x" />}
                  size="28"
                  onClick={() => setRevoking(invitation)}
                />
              </span>
            </div>
          );
        })}
      </div>
      <ConfirmDialog
        open={revoking !== null}
        danger
        title={t("organization.members.revoke")}
        description={t("organization.members.revokeBody", {
          email: revoking?.email ?? "",
        })}
        confirmLabel={t("common.confirm")}
        loading={revoke.isPending}
        onCancel={() => setRevoking(null)}
        onConfirm={() => revoking && revoke.mutate(revoking.id)}
      />
    </section>
  );
}
