import { createContext, useContext, type ReactElement } from "react";

import type { SpaceDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Tooltip, cn } from "@openfox/ui";

import { useOrgRole, useSpaceDetail, useSpaceList } from "./queries";

// Task controls share the server membership rules; server authorization remains authoritative.

/** 当前任务视图 / 详情是否只读，以及禁用原因。 */
export const ReadOnlyContext = createContext<boolean | string>(false);

export const useReadOnly = () => Boolean(useContext(ReadOnlyContext));

export function useReadOnlyReason() {
  const value = useContext(ReadOnlyContext);
  const t = useT();
  return typeof value === "string"
    ? value
    : value
      ? t("spaces.errors.retired")
      : undefined;
}

export function canWriteTasksIn(
  space: Pick<SpaceDetail, "visibility" | "my_role"> | undefined,
  orgRole: string | undefined,
): boolean {
  if (!space) return false;
  if (space.my_role) return space.my_role !== "VIEWER";
  return space.visibility === "shared" && orgRole !== "GUEST";
}

/** Reuse current membership for task controls, including personal task lists. */
export function useTaskReadOnlyReason(
  orgId: string,
  spaceId: string | undefined,
) {
  const inherited = useReadOnlyReason();
  const t = useT();
  const list = useSpaceList(orgId);
  const listed = list.data?.find((space) => space.id === spaceId);
  const detail = useSpaceDetail(
    orgId,
    spaceId && list.data && !listed ? spaceId : undefined,
  );
  const orgRole = useOrgRole(orgId);
  const space = listed ?? detail.data;
  if (inherited) return inherited;
  if (space?.status === "retired") return t("spaces.errors.retired");
  if (space && orgRole !== undefined && !canWriteTasksIn(space, orgRole))
    return t("spaces.task.readOnlyBanner");
  return undefined;
}

/**
 * 空间是否已停用。默认空间列表只含活跃空间：在列表里即活跃；不在列表里时取详情，
 * 详情为 retired 才算停用（404 等情况不当作停用，避免误伤看不到的空间）。
 */
export function useSpaceRetired(
  orgId: string,
  spaceId: string | undefined,
): boolean {
  const list = useSpaceList(orgId);
  const listed = Boolean(list.data?.some((space) => space.id === spaceId));
  const detail = useSpaceDetail(
    orgId,
    spaceId && list.data && !listed ? spaceId : undefined,
  );
  return !listed && detail.data?.status === "retired";
}

/**
 * 只读时把被禁用的控件包在可聚焦的 span 里，悬停 / 聚焦显示只读原因。
 * 子元素自身需要按 readOnly 设置 disabled；这里只负责提示（禁用控件收不到鼠标事件，所以由外层 span 接收）。
 */
export function ReadOnlyTip({
  children,
  block = false,
}: {
  children: ReactElement;
  /** 撑满整行（属性行、评论框） */
  block?: boolean;
}) {
  const reason = useReadOnlyReason();
  if (!reason) return children;
  return (
    <Tooltip content={reason}>
      <span
        tabIndex={0}
        data-readonly-tip=""
        aria-label={reason}
        className={cn(
          block ? "flex w-full" : "inline-flex",
          // 让鼠标事件落到外层 span 上
          "[&_*]:pointer-events-none",
        )}
      >
        {children}
      </span>
    </Tooltip>
  );
}

/** 某个任务的写操作是否禁用：上层给了只读上下文，或任务所在空间停用 / 无写权限。 */
export function useTaskReadOnly(orgId: string, spaceId: string | undefined) {
  return Boolean(useTaskReadOnlyReason(orgId, spaceId));
}
