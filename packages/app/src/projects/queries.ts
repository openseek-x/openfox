import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { isApiError, type ProjectStatus } from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../context";

const ERROR_KEYS = {
  VERSION_CONFLICT: "projects.errors.versionConflict",
  PRIMARY_SPACE: "projects.errors.primarySpace",
  SPACE_RETIRED: "projects.errors.retired",
  PROJECTS_DISABLED: "projects.errors.disabled",
  FORBIDDEN: "projects.errors.forbidden",
} as const;

export function projectErrorMessage(t: Translate, error: unknown): string {
  const key =
    isApiError(error) && Object.hasOwn(ERROR_KEYS, error.code)
      ? ERROR_KEYS[error.code as keyof typeof ERROR_KEYS]
      : "projects.errors.failed";
  return t(key);
}

/** 空间的专项列表；enabled 为 false（空间未启用专项）时不请求。 */
export function useProjects(
  orgId: string,
  spaceId: string,
  options: { status?: ProjectStatus; enabled?: boolean } = {},
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.projectList(orgId, spaceId, options.status),
    queryFn: () =>
      api.projects.list(orgId, {
        space_id: spaceId,
        status: options.status,
        limit: 100,
      }),
    enabled: Boolean(spaceId) && (options.enabled ?? true),
  });
}

export function useProject(orgId: string, projectId: string | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: keys.project(orgId, projectId ?? ""),
    queryFn: () => api.projects.get(orgId, projectId!),
    enabled: Boolean(projectId),
  });
}

/** 专项写操作的统一处理：成功后刷新专项与任务（任务带 project_id），失败按错误码提示。 */
export function useProjectMutation<TInput, TResult = unknown>(
  orgId: string,
  mutate: (input: TInput) => Promise<TResult>,
) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const t = useT();
  return useMutation({
    mutationFn: mutate,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: keys.projects(orgId) });
      void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
    },
    onError: (error) => toast(projectErrorMessage(t, error), { tone: "error" }),
  });
}
