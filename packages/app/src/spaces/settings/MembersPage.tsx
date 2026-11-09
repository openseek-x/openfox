import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";

import {
  isApiError,
  type OrganizationMember,
  type SpaceMemberDetail,
  type SpaceRole,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
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
  Select,
  Spinner,
  TextInput,
  cn,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { isOrgAdmin, isSpaceAdminRole, spaceErrorMessage } from "../queries";
import type { SpacePageProps } from "./GeneralPage";
import { RetiredBanner } from "./parts";

/** 界面上的三种空间角色；OWNER / EDITOR 都显示为「管理员」。 */
export type UiRole = "admin" | "member" | "viewer";
export const uiRole = (role: SpaceRole): UiRole =>
  isSpaceAdminRole(role) ? "admin" : role === "VIEWER" ? "viewer" : "member";
const apiRole: Record<UiRole, SpaceRole> = {
  admin: "OWNER",
  member: "MEMBER",
  viewer: "VIEWER",
};
const roleLabel: Record<UiRole, MessageKey> = {
  admin: "spaces.members.roles.admin",
  member: "spaces.members.roles.member",
  viewer: "spaces.members.roles.viewer",
};
const roleDescription: Record<UiRole, MessageKey> = {
  admin: "spaces.members.roles.adminDescription",
  member: "spaces.members.roles.memberDescription",
  viewer: "spaces.members.roles.viewerDescription",
};

export const memberName = (member: { name?: string; email: string }) =>
  member.name?.trim() || member.email;

/** 按名称或邮箱搜索（不区分大小写）+ 按角色筛选，结果按名称升序。 */
export function filterMembers(
  members: readonly SpaceMemberDetail[],
  query: string,
  role: UiRole | "all",
): SpaceMemberDetail[] {
  const q = query.trim().toLowerCase();
  return members
    .filter((member) => member.status === "ACTIVE")
    .filter(
      (member) =>
        !q ||
        memberName(member).toLowerCase().includes(q) ||
        member.email.toLowerCase().includes(q),
    )
    .filter((member) => role === "all" || uiRole(member.role) === role)
    .sort((a, b) => memberName(a).localeCompare(memberName(b)));
}

function useOrgMembers(orgId: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: keys.orgMemberRoles(orgId),
    queryFn: async () => {
      const all: OrganizationMember[] = [];
      let cursor: string | undefined;
      // 最多 5 页（500 人），与任务模块的成员列表一致
      for (let i = 0; i < 5; i++) {
        const page = await api.members.list(orgId, cursor);
        all.push(...page.members.filter((m) => m.status === "ACTIVE"));
        if (!page.next_cursor) break;
        cursor = page.next_cursor;
      }
      return all;
    },
    enabled,
    staleTime: 5 * 60_000,
  });
}

/** 空间成员。 */
export function SpaceMembersPage({
  orgId,
  space,
  canManage,
  retired,
}: SpacePageProps) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  const manage = canManage && !retired;
  const [query, setQuery] = useState("");
  const [role, setRole] = useState<UiRole | "all">("all");
  const [removing, setRemoving] = useState<SpaceMemberDetail | null>(null);
  const membersKey = keys.spaceMemberDetails(orgId, space.id);
  const members = useQuery({
    queryKey: membersKey,
    queryFn: () => api.spaceMembership.list(orgId, space.id),
  });
  const orgMembers = useOrgMembers(orgId, true);
  const orgRoles = useMemo(
    () => new Map((orgMembers.data ?? []).map((m) => [m.user_id, m.role])),
    [orgMembers.data],
  );
  const orgRoleOf = (member: SpaceMemberDetail) =>
    member.org_role ?? orgRoles.get(member.user_id);
  const active = (members.data ?? []).filter((m) => m.status === "ACTIVE");
  const rows = filterMembers(members.data ?? [], query, role);
  const refresh = () => {
    void queryClient.invalidateQueries({
      queryKey: keys.spaceMembers(orgId, space.id),
    });
    void queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
  };

  const changeRole = useMutation({
    mutationFn: (vars: { member: SpaceMemberDetail; role: UiRole }) =>
      api.spaceMembership.put(
        orgId,
        space.id,
        vars.member.user_id,
        apiRole[vars.role],
      ),
    onSuccess: () => toast(t("spaces.members.roleUpdated")),
    onError: (error) =>
      toast(spaceErrorMessage(t, error, "spaces.members.roleUpdateFailed"), {
        tone: "error",
      }),
    onSettled: refresh,
  });
  const remove = useMutation({
    mutationFn: (member: SpaceMemberDetail) =>
      api.spaceMembership.remove(orgId, space.id, member.user_id),
    onSuccess: (_result, member) => {
      setRemoving(null);
      if (member.user_id === me.data?.id) {
        toast(t("spaces.members.left", { space: space.name }));
        navigate(`/o/${orgId}/settings`, { replace: true });
      } else toast(t("spaces.members.removed", { name: memberName(member) }));
    },
    onError: (error, member) => {
      setRemoving(null);
      if (isApiError(error, "ONLY_SPACE"))
        toast(t("spaces.members.onlySpace"), { tone: "error" });
      else
        toast(
          spaceErrorMessage(
            t,
            error,
            member.user_id === me.data?.id
              ? "spaces.members.leaveFailed"
              : "spaces.members.removeFailed",
          ),
          { tone: "error" },
        );
    },
    onSettled: refresh,
  });
  const leaving = removing?.user_id === me.data?.id;

  return (
    <div className="mx-auto flex w-full max-w-[768px] flex-col gap-5 px-6 pt-6 pb-12">
      {retired && <RetiredBanner />}
      <h1 className="text-title font-semibold">{t("spaces.members.title")}</h1>
      <div className="flex items-center gap-2">
        <TextInput
          aria-label={t("spaces.members.search")}
          placeholder={t("spaces.members.search")}
          leading={<Icon name="search" size={14} />}
          className="h-9 w-[288px]"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <Select<UiRole | "all">
          aria-label={t("spaces.members.roleFilter")}
          value={role}
          onChange={setRole}
          className="h-9 min-w-[120px]"
          options={[
            { value: "all", label: t("spaces.members.allRoles") },
            { value: "admin", label: t(roleLabel.admin) },
            { value: "member", label: t(roleLabel.member) },
            { value: "viewer", label: t(roleLabel.viewer) },
          ]}
        />
        <div className="flex-1" />
        {manage && (
          <AddMemberButton
            orgId={orgId}
            spaceId={space.id}
            existing={new Set(active.map((m) => m.user_id))}
            onAdded={refresh}
          />
        )}
      </div>
      {members.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : members.isError ? (
        <p role="alert" className="text-body text-fg-muted">
          {t("spaces.members.loadFailed")}
        </p>
      ) : active.length === 0 ? (
        <p className="text-body text-fg-muted">{t("spaces.members.empty")}</p>
      ) : (
        <div
          role="table"
          aria-label={t("spaces.members.title")}
          className="flex flex-col"
        >
          <div
            role="row"
            className="flex h-9 items-center gap-3 px-4 text-caption font-medium text-fg-muted"
          >
            <span role="columnheader" className="flex-1 pl-10">
              {t("spaces.members.columnName")}
            </span>
            <span role="columnheader" className="w-[200px]">
              {t("spaces.members.columnEmail")}
            </span>
            <span role="columnheader" className="w-[120px]">
              {t("spaces.members.columnRole")}
            </span>
            <span className="w-7" />
          </div>
          {rows.length === 0 ? (
            <p className="px-4 py-8 text-center text-body text-fg-muted">
              {t("spaces.members.noMatch")}
            </p>
          ) : (
            rows.map((member) => (
              <MemberRow
                key={member.user_id}
                member={member}
                self={member.user_id === me.data?.id}
                orgRole={orgRoleOf(member)}
                manage={manage}
                onRole={(next) => {
                  if (next !== uiRole(member.role))
                    changeRole.mutate({ member, role: next });
                }}
                onRemove={() => setRemoving(member)}
              />
            ))
          )}
        </div>
      )}
      <ConfirmDialog
        open={Boolean(removing)}
        danger
        title={
          leaving
            ? t("spaces.members.confirmLeaveTitle", { space: space.name })
            : t("spaces.members.confirmRemoveTitle", {
                name: removing ? memberName(removing) : "",
              })
        }
        description={
          leaving
            ? t("spaces.members.confirmLeaveDescription")
            : t("spaces.members.confirmRemoveDescription", {
                name: removing ? memberName(removing) : "",
                space: space.name,
              })
        }
        confirmLabel={
          leaving ? t("spaces.members.leave") : t("spaces.members.remove")
        }
        loading={remove.isPending}
        onCancel={() => setRemoving(null)}
        onConfirm={() => removing && remove.mutate(removing)}
      />
    </div>
  );
}

function MemberRow({
  member,
  self,
  orgRole,
  manage,
  onRole,
  onRemove,
}: {
  member: SpaceMemberDetail;
  self: boolean;
  orgRole: string | undefined;
  manage: boolean;
  onRole: (role: UiRole) => void;
  onRemove: () => void;
}) {
  const t = useT();
  const role = uiRole(member.role);
  const implicitAdmin = isOrgAdmin(orgRole);
  const guest = orgRole === "GUEST";
  const editable = manage && !self && !implicitAdmin;
  const hasMenu = self || manage;
  const onlySpace = member.only_space === true;
  const removeLabel = self
    ? t("spaces.members.leave")
    : t("spaces.members.remove");
  return (
    <div
      role="row"
      className="group flex items-center gap-3 rounded-lg border-t border-line-soft/50 p-4 first-of-type:border-t-0 hover:bg-surface-hover"
    >
      <span role="cell" className="flex min-w-0 flex-1 items-center gap-3">
        <Avatar name={memberName(member)} size={28} />
        <span className="truncate text-body font-medium">
          {memberName(member)}
          {self && (
            <span className="ml-1 text-fg-muted">
              {t("spaces.members.you")}
            </span>
          )}
        </span>
      </span>
      <span
        role="cell"
        className="w-[200px] truncate text-body-sm text-fg-muted"
      >
        {member.email}
      </span>
      <span role="cell" className="w-[120px]">
        {implicitAdmin ? (
          <span title={t("spaces.members.implicitAdmin")}>
            <Badge tone="neutral">{t(roleLabel.admin)}</Badge>
          </span>
        ) : editable ? (
          <Select<UiRole>
            aria-label={t("spaces.members.roleFor", {
              name: memberName(member),
            })}
            value={role}
            onChange={onRole}
            className="h-7 text-body-sm"
            options={(["admin", "member", "viewer"] as const).map((value) => ({
              value,
              label: t(roleLabel[value]),
              description: t(roleDescription[value]),
              disabled: value === "admin" && guest,
            }))}
          />
        ) : (
          <Badge tone="neutral">{t(roleLabel[role])}</Badge>
        )}
      </span>
      <span className="flex w-7 justify-end">
        {hasMenu && (
          <Menu
            label={t("spaces.members.actions", { name: memberName(member) })}
            placement="bottom-end"
            trigger={({ ref, toggle, open }) => (
              <IconButton
                ref={ref}
                label={t("spaces.members.actions", {
                  name: memberName(member),
                })}
                icon={<Icon name="ellipsis" />}
                size="28"
                aria-expanded={open}
                onClick={toggle}
                className={cn(
                  "opacity-0 group-hover:opacity-100 focus-visible:opacity-100",
                  open && "opacity-100",
                )}
              />
            )}
          >
            <MenuItem
              danger
              icon={<Icon name={self ? "log-out" : "user-x"} />}
              disabled={onlySpace}
              disabledReason={`${removeLabel}. ${t("spaces.members.onlySpace")}`}
              onSelect={onRemove}
            >
              {removeLabel}
            </MenuItem>
          </Menu>
        )}
      </span>
    </div>
  );
}

/** 「添加成员」弹层：搜索组织成员，逐个加入。 */
function AddMemberButton({
  orgId,
  spaceId,
  existing,
  onAdded,
}: {
  orgId: string;
  spaceId: string;
  existing: Set<string>;
  onAdded: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const anchor = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const orgMembers = useOrgMembers(orgId, open);
  const add = useMutation({
    mutationFn: (member: OrganizationMember) =>
      api.spaceMembership.put(orgId, spaceId, member.user_id, "MEMBER"),
    onSuccess: () => {
      toast(t("spaces.members.added", { count: 1 }));
      onAdded();
    },
    onError: () => toast(t("spaces.members.addFailed"), { tone: "error" }),
  });
  const q = query.trim().toLowerCase();
  const list = (orgMembers.data ?? [])
    .filter((m) => !q || m.email.toLowerCase().includes(q))
    .sort((a, b) => a.email.localeCompare(b.email));
  return (
    <>
      <Button
        ref={anchor}
        size="32"
        leading={<Icon name="user-plus" />}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        {t("spaces.members.add")}
      </Button>
      <Popover
        open={open}
        onOpenChange={(next) => {
          setOpen(next);
          if (!next) setQuery("");
        }}
        anchorRef={anchor}
        placement="bottom-end"
        offset={6}
        role="dialog"
        aria-label={t("spaces.members.add")}
        className="flex w-[320px] flex-col"
      >
        <input
          autoFocus
          aria-label={t("spaces.members.pickerSearch")}
          placeholder={t("spaces.members.pickerSearch")}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          className="h-10 border-b border-line-default bg-transparent px-3 text-body outline-none"
        />
        <div className="max-h-[280px] overflow-y-auto p-1">
          {orgMembers.isLoading ? (
            <div className="flex justify-center p-3">
              <Spinner />
            </div>
          ) : list.length === 0 ? (
            <p className="px-3 py-4 text-center text-body-sm text-fg-muted">
              {q
                ? t("spaces.members.pickerNoMatch")
                : t("spaces.members.pickerEmpty")}
            </p>
          ) : (
            list.map((member) => {
              const joined = existing.has(member.user_id);
              return (
                <div
                  key={member.user_id}
                  className="flex h-11 items-center gap-2 px-2"
                >
                  <Avatar name={member.email} size={24} />
                  <span className="min-w-0 flex-1 truncate text-body">
                    {member.email}
                  </span>
                  {joined ? (
                    <Button
                      size="28"
                      variant="outline"
                      disabled
                      aria-label={t("spaces.members.addedAria", {
                        name: member.email,
                      })}
                    >
                      {t("spaces.members.joinedBadge")}
                    </Button>
                  ) : (
                    <Button
                      size="28"
                      disabled={add.isPending}
                      aria-label={t("spaces.members.addAria", {
                        name: member.email,
                      })}
                      onClick={() => add.mutate(member)}
                    >
                      {t("spaces.members.join")}
                    </Button>
                  )}
                </div>
              );
            })
          )}
        </div>
        <button
          type="button"
          onClick={() => navigate(`/o/${orgId}/settings/members`)}
          className="flex h-10 items-center gap-2 border-t border-line-default px-3 text-body-sm text-fg-secondary hover:bg-surface-hover"
        >
          <Icon name="plus" size={14} />
          {t("spaces.members.invite")}
        </button>
      </Popover>
    </>
  );
}
