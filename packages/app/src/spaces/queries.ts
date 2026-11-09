import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { useEffect, useMemo, useRef } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import {
  isApiError,
  type SpaceDetail,
  type UpdateSpaceInput,
} from "@openfox/api";
import {
  useT,
  type MessageKey,
  type MessageParams,
  type Translate,
} from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../context";

// 空间列表、详情与写操作的公共查询。

/** 当前用户可读的 ACTIVE 空间（服务端已按个人顺序排好）。与侧栏、任务页共用同一个查询键。 */
export function useSpaceList(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
  });
}

/** 单个空间详情（含已停用；已删除或不可读时 404）。 */
export function useSpaceDetail(orgId: string, spaceId: string | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: keys.space(orgId, spaceId ?? ""),
    queryFn: () => api.spaces.get(orgId, spaceId!),
    enabled: Boolean(spaceId),
  });
}

/** 组织角色（OWNER / ADMIN / MEMBER / GUEST），来自 organizations 查询。 */
export function useOrgRole(orgId: string): string | undefined {
  const api = useApi();
  const organizations = useQuery({
    queryKey: keys.organizations,
    queryFn: api.me.organizations,
  });
  return organizations.data?.find((item) => item.id === orgId)?.role;
}

export const isOrgAdmin = (role: string | undefined) =>
  role === "OWNER" || role === "ADMIN";

/** 空间管理员：OWNER（EDITOR 在权限策略里同为管理员）。 */
export const isSpaceAdminRole = (role: string | null | undefined) =>
  role === "OWNER" || role === "EDITOR";

/** 空间设置的管理权限：服务端给出 can_manage 时以它为准，否则按「空间管理员或组织 OWNER / ADMIN」推断。 */
export function canManageSpace(
  space: Pick<SpaceDetail, "my_role" | "can_manage"> | undefined,
  orgRole: string | undefined,
): boolean {
  if (!space) return false;
  if (typeof space.can_manage === "boolean") return space.can_manage;
  return isOrgAdmin(orgRole) || isSpaceAdminRole(space.my_role);
}

export const isRetired = (space: Pick<SpaceDetail, "status"> | undefined) =>
  space?.status === "retired";

/** 专项 / 视图的写入口：未停用，且是空间管理员（服务端的任务写权限判断才是最终依据）。 */
export const canWriteSpace = (
  space: Pick<SpaceDetail, "status" | "my_role" | "can_manage"> | undefined,
) =>
  Boolean(space) &&
  !isRetired(space) &&
  (space!.can_manage || isSpaceAdminRole(space!.my_role));

/** 侧栏「我的空间」：已加入的 ACTIVE 空间。 */
export function joinedSpaces(spaces: readonly SpaceDetail[] | undefined) {
  return (spaces ?? []).filter((space) => space.my_role);
}

/** 可加入的空间：组织内公开、自己还不是成员。 */
export function joinableSpaces(spaces: readonly SpaceDetail[] | undefined) {
  return (spaces ?? []).filter(
    (space) => !space.my_role && space.visibility === "shared",
  );
}

/** 写入空间缓存（列表与详情），用于写操作成功后的即时更新。 */
export function putSpace(
  queryClient: QueryClient,
  orgId: string,
  space: SpaceDetail,
) {
  queryClient.setQueryData<SpaceDetail>(keys.space(orgId, space.id), space);
  queryClient.setQueryData<SpaceDetail[]>(keys.spaces(orgId), (list) =>
    list?.map((item) => (item.id === space.id ? { ...item, ...space } : item)),
  );
}

const spaceErrors: Record<string, MessageKey> = {
  VERSION_CONFLICT: "spaces.errors.versionConflict",
  IDENTIFIER_TAKEN: "spaces.errors.identifierTaken",
  SPACE_RETIRED: "spaces.errors.retired",
  LAST_SPACE: "spaces.errors.lastSpace",
  LAST_OWNER: "spaces.errors.lastOwner",
  ONLY_SPACE: "spaces.errors.onlySpace",
  CONFIRM_MISMATCH: "spaces.errors.confirmMismatch",
  NO_CANCELED_STATUS: "spaces.errors.noCanceledStatus",
  TYPE_IN_USE: "spaces.errors.typeInUse",
  LIMIT_REACHED: "spaces.errors.limitReached",
  REPO_EXISTS: "spaces.errors.repoExists",
  INVALID_IDENTIFIER: "spaces.validation.identifierFormat",
  INVALID_TARGET_SPACE: "spaces.errors.invalidTarget",
  DEFAULT_TYPE_REQUIRED: "spaces.errors.defaultTypeRequired",
  SPACE_NOT_RETIRED: "spaces.errors.notRetired",
  ESTIMATION_DISABLED: "spaces.errors.estimationDisabled",
  INVALID_ESTIMATE: "spaces.errors.invalidEstimate",
};

/** 空间相关错误的界面文案；fallback 为该操作的通用失败文案。 */
export function spaceErrorMessage(
  t: Translate,
  error: unknown,
  fallback: MessageKey = "spaces.errors.saveFailed",
  params?: MessageParams,
): string {
  if (!isApiError(error)) return t(fallback, params);
  const key = spaceErrors[error.code];
  if (key) return t(key);
  if (error.status === 403) return t("spaces.errors.forbidden");
  return t(fallback, params);
}

/**
 * PATCH 空间资料：成功后写回缓存并提示；版本冲突时刷新。
 * 同一空间的修改串行发送，每次取缓存里最新的 version。
 */
export function useUpdateSpace(orgId: string, spaceId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const t = useT();
  return useMutation({
    scope: { id: `space:${spaceId}` },
    mutationFn: (input: Omit<UpdateSpaceInput, "version">) => {
      const current = queryClient.getQueryData<SpaceDetail>(
        keys.space(orgId, spaceId),
      );
      return api.spaces.update(orgId, spaceId, {
        ...input,
        version: current?.version ?? 1,
      });
    },
    onSuccess: (space, input) => {
      putSpace(queryClient, orgId, space);
      toast(
        input.name !== undefined
          ? t("spaces.toast.renamed", { name: space.name })
          : t("spaces.toast.updated"),
      );
    },
    onError: (error) => {
      toast(spaceErrorMessage(t, error), { tone: "error" });
      void queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
    },
  });
}

/** 按 id 建索引，便于侧栏与筛选器查找空间。 */
export function useSpaceIndex(spaces: readonly SpaceDetail[] | undefined) {
  return useMemo(
    () => new Map((spaces ?? []).map((space) => [space.id, space])),
    [spaces],
  );
}

/**
 * 正在查看的空间被删除时（§1.3）：跳到收件箱并提示「空间已删除」。
 * 空间从列表里消失可能是停用也可能是删除，以详情接口是否 404 为准；已停用的空间留在原页面（只读横幅）。
 */
export function useDeletedSpaceRedirect(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const t = useT();
  const navigate = useNavigate();
  const location = useLocation();
  const list = useSpaceList(orgId);
  const spaceId = /^\/o\/[^/]+\/(?:settings\/)?spaces\/([^/]+)/.exec(
    location.pathname,
  )?.[1];
  const inList = Boolean(list.data?.some((space) => space.id === spaceId));
  const seen = useRef<string | null>(null);
  useEffect(() => {
    if (!spaceId) {
      seen.current = null;
      return;
    }
    if (inList) {
      seen.current = spaceId;
      return;
    }
    if (seen.current !== spaceId || !list.data) return;
    seen.current = null;
    queryClient
      .fetchQuery({
        queryKey: keys.space(orgId, spaceId),
        queryFn: () => api.spaces.get(orgId, spaceId),
        staleTime: 0,
      })
      .catch((error: unknown) => {
        if (!isApiError(error) || error.status !== 404) return;
        toast(t("spaces.task.deleted"));
        navigate(`/o/${orgId}/inbox`, { replace: true });
      });
  }, [api, inList, list.data, navigate, orgId, queryClient, spaceId, t, toast]);
}
