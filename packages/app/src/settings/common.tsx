import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryKey,
} from "@tanstack/react-query";
import { useCallback, useRef, type ReactNode } from "react";

import { isApiError, newIdempotencyKey } from "@openfox/api";
import { useT, type MessageKey, type Translate } from "@openfox/i18n";
import { Skeleton, useToast } from "@openfox/ui";

import { keys, useApi } from "../context";

/** 当前用户在组织里的角色（来自 organizations 查询；未加载时为 undefined）。 */
export function useOrgRole(orgId: string) {
  const api = useApi();
  const organizations = useQuery({
    queryKey: keys.organizations,
    queryFn: api.me.organizations,
  });
  return organizations.data?.find((item) => item.id === orgId)?.role;
}

/** 当前用户能否管理全局配置：OWNER / ADMIN 可写。 */
export function useCanManage(orgId: string): boolean {
  const role = useOrgRole(orgId);
  return role === "OWNER" || role === "ADMIN";
}

/** 已知错误码对应的界面文案；其余回退到服务端消息。 */
const errorMessages: Record<string, MessageKey> = {
  NAME_TAKEN: "settings.errors.nameTaken",
  LIMIT_REACHED: "settings.errors.limitReached",
  VERSION_CONFLICT: "settings.errors.versionConflict",
  IDEMPOTENCY_CONFLICT: "settings.errors.idempotencyConflict",
  STATUS_DEFAULT_CATEGORY: "settings.statuses.defaultCategoryOnly",
  STATUS_IS_WORKFLOW_DEFAULT: "settings.errors.statusWorkflowDefault",
  STATUS_REPLACEMENT_REQUIRED: "settings.errors.statusReplacementRequired",
  STATUS_REPLACEMENT_NOT_IN_WORKFLOW:
    "settings.errors.statusReplacementNotInWorkflow",
  STATUS_MOVE_TOO_LARGE: "settings.errors.statusMoveTooLarge",
  RELATION_TYPE_BUILTIN: "settings.relationTypes.builtinLocked",
  CUSTOM_FIELD_IN_USE: "settings.customFields.inUse",
};

export function describeError(error: unknown, t: Translate): string {
  if (!isApiError(error)) return t("settings.errors.generic");
  const key = errorMessages[error.code];
  if (key) return t(key);
  if (error.uncertain) return t("settings.errors.uncertain");
  if (error.status === 403) return t("settings.errors.forbidden");
  return error.message || t("settings.errors.generic");
}

/**
 * 创建接口的幂等键：同一份输入重试时复用，输入变了换新键（否则服务端返回 IDEMPOTENCY_CONFLICT）。
 * 弹窗按需挂载，每次打开都是新的组件实例，因此每次打开会生成新键。
 */
export function useIdempotencyKey(): (input: unknown) => string {
  const last = useRef<{ key: string; input: string } | null>(null);
  return useCallback((input: unknown) => {
    const serialized = JSON.stringify(input);
    if (!last.current || last.current.input !== serialized)
      last.current = { key: newIdempotencyKey(), input: serialized };
    return last.current.key;
  }, []);
}

interface ConfigMutationOptions<TVars, TResult, TData> {
  /** 乐观更新与失败回滚的目标查询 */
  queryKey: QueryKey;
  mutationFn: (vars: TVars) => Promise<TResult>;
  /** 返回乐观更新后的缓存数据 */
  optimistic?: (data: TData, vars: TVars) => TData;
  onSuccess?: (result: TResult, vars: TVars) => void;
  /** 结束后额外失效的查询 */
  invalidate?: QueryKey[];
  /** 为 false 时不弹出错误提示（调用方自己展示） */
  toastErrors?: boolean;
  /** 自定义错误处理；返回 true 表示已处理，不再弹默认提示 */
  onError?: (error: unknown, vars: TVars) => boolean | void;
}

/**
 * 全局配置写操作：乐观更新，失败时回滚并提示；
 * 409 VERSION_CONFLICT 时提示「内容已被他人修改，已刷新」并重新拉取。
 */
export function useConfigMutation<TVars, TResult = unknown, TData = unknown>({
  queryKey,
  mutationFn,
  optimistic,
  onSuccess,
  invalidate = [],
  toastErrors = true,
  onError,
}: ConfigMutationOptions<TVars, TResult, TData>) {
  const queryClient = useQueryClient();
  const toast = useToast();
  const t = useT();
  return useMutation({
    mutationFn,
    onMutate: async (vars: TVars) => {
      if (!optimistic) return { previous: undefined as TData | undefined };
      await queryClient.cancelQueries({ queryKey });
      const previous = queryClient.getQueryData<TData>(queryKey);
      if (previous !== undefined)
        queryClient.setQueryData<TData>(queryKey, optimistic(previous, vars));
      return { previous };
    },
    onError: (error, vars, context) => {
      if (context?.previous !== undefined)
        queryClient.setQueryData(queryKey, context.previous);
      if (isApiError(error, "VERSION_CONFLICT")) {
        toast(t("settings.errors.versionConflict"), { tone: "error" });
        return;
      }
      if (onError?.(error, vars)) return;
      if (toastErrors) toast(describeError(error, t), { tone: "error" });
    },
    onSuccess: (result, vars) => onSuccess?.(result, vars),
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey });
      for (const key of invalidate)
        void queryClient.invalidateQueries({ queryKey: key });
    },
  });
}

/** 设置子页的页头：标题 + 副标题 + 右侧主操作。 */
export function ConfigPageHeader({
  title,
  subtitle,
  action,
}: {
  title: string;
  subtitle: string;
  action?: ReactNode;
}) {
  return (
    <header className="flex items-start gap-4">
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <h1 className="text-subheading font-medium text-fg-default">{title}</h1>
        <p className="text-body text-fg-muted">{subtitle}</p>
      </div>
      {action}
    </header>
  );
}

/** 列表加载占位。 */
export function ListSkeleton({
  rows = 4,
  label,
}: {
  rows?: number;
  label: string;
}) {
  return (
    <div aria-busy="true" aria-label={label} className="flex flex-col gap-2">
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-10 w-full" />
      ))}
    </div>
  );
}

/** 列表空态 / 加载失败。 */
export function EmptyState({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-xl border border-dashed border-line-default px-6 py-10 text-center">
      <p className="text-body font-medium text-fg-default">{title}</p>
      {description && (
        <p className="max-w-sm text-body-sm text-fg-muted">{description}</p>
      )}
      {action}
    </div>
  );
}
