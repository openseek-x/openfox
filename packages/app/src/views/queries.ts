import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { isApiError, type View } from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../context";

const ERROR_KEYS = {
  VERSION_CONFLICT: "views.errors.versionConflict",
  SPACE_RETIRED: "views.errors.retired",
  VIEWS_DISABLED: "views.errors.disabled",
  INVALID_VIEW_FILTERS: "views.errors.invalidFilters",
  STORAGE_UNAVAILABLE: "views.errors.storageUnavailable",
  INVALID_ICON: "views.errors.invalidIcon",
  ICON_TOO_LARGE: "views.errors.iconTooLarge",
  FORBIDDEN: "views.errors.forbidden",
} as const;

export function viewErrorMessage(t: Translate, error: unknown): string {
  const key =
    isApiError(error) && Object.hasOwn(ERROR_KEYS, error.code)
      ? ERROR_KEYS[error.code as keyof typeof ERROR_KEYS]
      : "views.errors.failed";
  return t(key);
}

/** 本人可见的视图（含本人的置顶 / 排序 / 展示方式）。空间未启用视图时不请求。 */
export function useViews(orgId: string, spaceId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: keys.viewList(orgId, spaceId),
    queryFn: () => api.views.list(orgId, spaceId),
    enabled: Boolean(spaceId) && enabled,
  });
}

export function useView(orgId: string, viewId: string | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: keys.view(orgId, viewId ?? ""),
    queryFn: () => api.views.get(orgId, viewId!),
    enabled: Boolean(viewId),
  });
}

/** 视图写操作的统一处理：成功后刷新视图，失败按错误码提示。 */
export function useViewMutation<TInput, TResult = unknown>(
  orgId: string,
  mutate: (input: TInput) => Promise<TResult>,
) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const t = useT();
  return useMutation({
    mutationFn: mutate,
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: keys.views(orgId) }),
    onError: (error) => toast(viewErrorMessage(t, error), { tone: "error" }),
  });
}

/** 置顶视图：按本人 position 排序（列表接口已排好序，这里只过滤）。 */
export const pinnedViews = (views: readonly View[] | undefined) =>
  (views ?? []).filter((view) => view.pinned);
