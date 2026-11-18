import { NavLink } from "react-router-dom";

import { cn } from "@openfox/ui";

import { ViewIcon } from "./ViewIcon";
import { pinnedViews, useViews } from "./queries";

/** 侧栏空间子项「视图」下面的置顶视图（按本人排序）。 */
export function PinnedViews({
  orgId,
  spaceId,
}: {
  orgId: string;
  spaceId: string;
}) {
  const views = pinnedViews(useViews(orgId, spaceId).data);
  return (
    <>
      {views.map((view) => (
        <NavLink
          key={view.id}
          to={`/o/${orgId}/spaces/${spaceId}/views/${view.id}`}
          className={({ isActive }) =>
            cn(
              "flex h-[var(--density-subnav-row,30px)] items-center gap-2 rounded-lg pr-1.5 pl-6 text-body hover:bg-surface-hover",
              isActive ? "bg-surface-active font-medium" : "text-fg-default",
            )
          }
        >
          <ViewIcon view={view} size={14} />
          <span className="min-w-0 flex-1 truncate">{view.name}</span>
        </NavLink>
      ))}
    </>
  );
}
