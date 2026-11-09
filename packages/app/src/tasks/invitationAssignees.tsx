import { useQuery } from "@tanstack/react-query";
import type { Invitation, SpaceDetail, TaskAssignee } from "@openfox/api";
import { Avatar, cn, initialOf, type AvatarProps } from "@openfox/ui";
import { keys, useApi } from "../context";
import { isOrgAdmin, useOrgRole } from "../spaces/queries";

/** Invitations share the human slot. Their IDs belong to the invitation, never to a member. */
export function isPersonAssignee(kind: TaskAssignee["kind"]) {
  return kind === "HUMAN" || kind === "INVITATION";
}

function eligibleInvitations(
  invitations: readonly Invitation[],
  spaces?: readonly SpaceDetail[],
) {
  const now = Date.now();
  return invitations.filter((invite) => {
    if (invite.status !== "PENDING" || !(Date.parse(invite.expires_at) > now))
      return false;
    return (
      !spaces ||
      spaces.some(
        (space) =>
          space.status !== "retired" &&
          (invite.space_ids?.includes(space.id) ||
            (space.visibility === "shared" && invite.role !== "GUEST")),
      )
    );
  });
}

export function useInvitationAssignees(
  orgId: string,
  open: boolean,
  spaceIds?: readonly string[],
) {
  const api = useApi();
  const admin = isOrgAdmin(useOrgRole(orgId));
  const invites = useQuery({
    queryKey: keys.invitations(orgId),
    queryFn: () => api.invitations.list(orgId),
    enabled: admin && open,
    retry: false,
  });
  const spaces = useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
    enabled: admin && open && !!spaceIds,
  });
  const selectedSpaces = spaceIds
    ? (spaces.data ?? []).filter((space) => spaceIds.includes(space.id))
    : undefined;
  // A disabled query still exposes its cached data after a role change.
  const candidates =
    admin && invites.isSuccess
      ? eligibleInvitations(invites.data, selectedSpaces)
      : [];
  return { candidates, lookup: admin && open ? invites : undefined };
}

/** A pending invite uses a dashed circle with the email initial inside. */
export function TaskAssigneeAvatar({
  kind,
  name,
  size = 20,
  ...props
}: Omit<AvatarProps, "kind"> & { kind: TaskAssignee["kind"] }) {
  if (kind !== "INVITATION")
    return (
      <Avatar
        kind={kind === "HUMAN" ? "user" : "agent"}
        name={name}
        size={size}
        {...props}
      />
    );
  return (
    <span
      role="img"
      aria-label={name}
      style={{ width: size, height: size, fontSize: size * 0.45 }}
      className={cn(
        "inline-flex shrink-0 items-center justify-center rounded-full border border-dashed border-fg-muted/45 font-medium text-fg-muted",
        props.className,
      )}
    >
      {initialOf(name)}
    </span>
  );
}
