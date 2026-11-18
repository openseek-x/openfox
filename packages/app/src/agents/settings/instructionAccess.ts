import type { Crew, SpaceAgent } from "@openfox/api";

import { canManageSpace } from "../../spaces/queries";
import { useMe, useOrgRole, useSpaces } from "../queries";

/** UI mirrors the existing manage-space + private-owner checks; the API remains authoritative. */
export function useInstructionAccess(orgId: string, entity: Crew | SpaceAgent) {
  const me = useMe();
  const role = useOrgRole(orgId);
  const spaces = useSpaces(orgId);
  const space = spaces.data?.find((item) => item.id === entity.space_id);
  return (
    canManageSpace(space, role) &&
    !entity.archived_at &&
    !("builtin_kind" in entity && entity.builtin_kind) &&
    (entity.visibility !== "PRIVATE" || entity.owner_user_id === me.data?.id)
  );
}
