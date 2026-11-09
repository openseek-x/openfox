import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";

import { isApiError, type ExportRequest } from "@openfox/api";
import type { Translate } from "@openfox/i18n";

import { keys, useApi } from "../context";
import { exportPollInterval } from "./export-model";
import { PAGE_SIZE, isFetching, isTerminal, pollInterval } from "./model";

// 导入的查询与写操作。

/** 导入记录：默认 5 条，cursor 分页；有执行中的导入时 4 秒刷新，否则 30 秒。 */
export function useImportList(orgId: string) {
  const api = useApi();
  return useInfiniteQuery({
    queryKey: keys.importList(orgId),
    queryFn: ({ pageParam }) =>
      api.imports.list(orgId, {
        limit: PAGE_SIZE,
        cursor: pageParam || undefined,
      }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
    refetchInterval: (query) =>
      pollInterval(query.state.data?.pages[0]?.has_active),
  });
}

/** 单个导入；进行中每 2 秒轮询，结束后停止。 */
export function useImportJob(
  orgId: string,
  importId: string | undefined,
  poll: boolean,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.import(orgId, importId ?? ""),
    queryFn: () => api.imports.get(orgId, importId!),
    enabled: Boolean(importId),
    refetchInterval: (query) => {
      const data = query.state.data;
      if (!poll || !data) return false;
      // API 草稿只在拉取阶段轮询；拉完或被丢弃后停止
      if (data.status === "DRAFT") return isFetching(data) ? 2000 : false;
      return isTerminal(data.status) || data.status === "DISCARDED"
        ? false
        : 2000;
    },
  });
}

export function useRollbackPreview(
  orgId: string,
  importId: string,
  enabled: boolean,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.importRollback(orgId, importId),
    queryFn: () => api.imports.rollbackPreview(orgId, importId),
    enabled,
    gcTime: 0,
  });
}

export function useRollbackImport(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { importId: string; version: number }) =>
      api.imports.rollback(orgId, input.importId, input.version),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: keys.imports(orgId) }),
  });
}

export function importErrorText(t: Translate, error: unknown): string {
  if (isApiError(error)) {
    switch (error.code) {
      case "VERSION_CONFLICT":
        return t("imports.errors.versionConflict");
      case "IMPORT_STATE":
        return t("imports.errors.state");
      case "IMPORT_ACTIVE":
        return t("imports.errors.active");
      case "IMPORT_ROLLBACK_UNAVAILABLE":
        return t("imports.errors.rollbackUnavailable");
      case "RUN_ACTIVE":
        return t("imports.errors.runActive");
      case "INVALID_IMPORT_CONFIG":
        return t("imports.errors.invalidConfig");
      case "IMPORT_CONFIG_INCOMPLETE":
        return t("imports.errors.incomplete");
      case "OVERWRITE_CONFIRMATION_REQUIRED":
        return t("imports.errors.overwriteConfirm");
      case "IMPORT_FILE_TOO_LARGE":
        return t("imports.errors.tooLarge");
      case "SECRETS_UNAVAILABLE":
        return t("imports.errors.secretsUnavailable");
      case "IDENTIFIER_TAKEN":
        return t("imports.errors.identifierTaken");
      case "INVALID_IDENTIFIER":
        return t("imports.errors.invalidIdentifier");
      case "JIRA_CONNECTION_FAILED":
        return t("imports.errors.connectionFailed");
      case "INVALID_IMPORT_FILE":
        return error.message || t("imports.errors.invalidFile");
    }
    if (error.status === 413) return t("imports.errors.tooLarge");
    if (error.status === 403) return t("imports.errors.forbidden");
  }
  return t("imports.errors.generic");
}

// 导出任务数据。

export function useExportSpaces(orgId: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: keys.exportSpaces(orgId),
    queryFn: () => api.exportTasks.spaces(orgId),
    enabled,
    gcTime: 0,
  });
}

/** 导出记录；有生成中的导出时 2 秒刷新。 */
export function useExportList(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.exportList(orgId),
    queryFn: () => api.exportTasks.list(orgId),
    refetchInterval: (query) => exportPollInterval(query.state.data?.exports),
  });
}

export function useStartExport(orgId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: { request: ExportRequest; idempotencyKey: string }) =>
      api.exportTasks.start(orgId, input.request, input.idempotencyKey),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: keys.exportList(orgId) }),
  });
}
