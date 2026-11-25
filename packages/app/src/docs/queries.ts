import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";

import {
  ApiError,
  isApiError,
  type DocContainer,
  type DocNode,
  type DocNodeDetail,
  type DocNodeMoveInput,
  type DocSet,
  type DocSetInput,
} from "@openfox/api";
import type { MessageKey, Translate } from "@openfox/i18n";

import { keys, useApi } from "../context";
import { DOC_AUTOSAVE_MS } from "./model";

// 阶段 5b · 文档库的查询与写操作。所有写操作成功后失效该空间的文档查询（同一前缀）。

/** 文档集列表：一次拉全（每个空间最多 200 个）。 */
export function useDocSets(orgId: string, spaceId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.docSets(orgId, spaceId),
    queryFn: () => api.docSets.list(orgId, spaceId),
    enabled: Boolean(spaceId),
  });
}

/** 某个父节点下的子节点（按需加载）；parentId 为空表示容器根。 */
export function useDocChildren(
  orgId: string,
  spaceId: string,
  container: DocContainer,
  parentId: string | null,
  enabled = true,
) {
  const api = useApi();
  const setId = container.kind === "set" ? (container.set_id ?? "") : "";
  return useQuery({
    queryKey: keys.docNodes(
      orgId,
      spaceId,
      container.kind,
      setId,
      parentId ?? "",
    ),
    queryFn: () =>
      api.docNodes.list(orgId, spaceId, {
        container: container.kind,
        set_id: setId || undefined,
        parent_id: parentId ?? undefined,
      }),
    enabled: enabled && Boolean(spaceId),
  });
}

export function useRecentPersonalFiles(
  orgId: string,
  spaceId: string,
  enabled: boolean,
) {
  const api = useApi();
  return useQuery({
    queryKey: [...keys.docs(orgId, spaceId), "recent-personal"],
    queryFn: () => api.docNodes.recentPersonal(orgId, spaceId),
    enabled: enabled && Boolean(spaceId),
  });
}

export function useDocNode(orgId: string, spaceId: string, nodeId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.docNode(orgId, spaceId, nodeId),
    queryFn: () => api.docNodes.get(orgId, spaceId, nodeId),
    enabled: Boolean(spaceId && nodeId),
    // 编辑器的正文由本地状态持有，重新拉取不应打断输入
    refetchOnWindowFocus: false,
  });
}

export function useDocSearch(orgId: string, spaceId: string, q: string) {
  const api = useApi();
  const query = q.trim();
  return useQuery({
    queryKey: keys.docSearch(orgId, spaceId, query),
    queryFn: () => api.docNodes.search(orgId, spaceId, query),
    enabled: Boolean(spaceId && query),
  });
}

/** 写操作后整体失效该空间的文档查询。 */
function invalidateDocs(
  queryClient: QueryClient,
  orgId: string,
  spaceId: string,
) {
  void queryClient.invalidateQueries({ queryKey: keys.docs(orgId, spaceId) });
}

export function useSaveDocSet(orgId: string, spaceId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      /** 编辑时带上 id 与 version */
      target?: Pick<DocSet, "id" | "version">;
      body: DocSetInput;
    }) =>
      input.target
        ? api.docSets.update(orgId, spaceId, input.target.id, {
            ...input.body,
            version: input.target.version,
          })
        : api.docSets.create(orgId, spaceId, input.body),
    onSettled: () => invalidateDocs(queryClient, orgId, spaceId),
  });
}

export function useDeleteDocSet(orgId: string, spaceId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (docSet: Pick<DocSet, "id" | "version">) =>
      api.docSets.remove(orgId, spaceId, docSet),
    onSettled: () => invalidateDocs(queryClient, orgId, spaceId),
  });
}

/** 重命名（只改标题）。正文保存走 useDebouncedSave。 */
export function useRenameDocNode(orgId: string, spaceId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      node: Pick<DocNode, "id" | "version">;
      title: string;
    }) =>
      api.docNodes.update(orgId, spaceId, input.node.id, {
        version: input.node.version,
        title: input.title,
      }),
    onSettled: () => invalidateDocs(queryClient, orgId, spaceId),
  });
}

export function useDeleteDocNode(orgId: string, spaceId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (node: Pick<DocNode, "id" | "version">) =>
      api.docNodes.remove(orgId, spaceId, node),
    onSettled: () => invalidateDocs(queryClient, orgId, spaceId),
  });
}

/** 移动（可跨空间）。源空间与目标空间的查询都要失效，所以按组织前缀整体失效。 */
export function useMoveDocNode(orgId: string, spaceId: string) {
  const api = useApi();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: {
      node: Pick<DocNode, "id" | "version">;
      target: Omit<DocNodeMoveInput, "version">;
    }) =>
      api.docNodes.move(orgId, spaceId, input.node.id, {
        ...input.target,
        version: input.node.version,
      }),
    onSettled: () =>
      void queryClient.invalidateQueries({ queryKey: ["org", orgId, "docs"] }),
  });
}

/** 文档错误文案：版本冲突、权限不足，其余使用调用方给的兜底文案。 */
export function docErrorMessage(
  t: Translate,
  error: unknown,
  fallback: MessageKey,
): string {
  if (isApiError(error, "VERSION_CONFLICT")) return t("docs.errors.conflict");
  if (isApiError(error) && error.status === 403)
    return t("docs.errors.forbidden");
  return t(fallback);
}

/**
 * 防抖自动保存：同一段时间内的补丁会合并，保存串行发送（上一次返回前不发下一次）。
 * 暂时失败有限退避重试；冲突保留本地补丁，等待用户处理。
 */
export function useDebouncedSave<T extends object>(
  save: (patch: Partial<T>) => Promise<void>,
  delay = DOC_AUTOSAVE_MS,
) {
  const [error, setError] = useState<unknown>(null);
  const saveRef = useRef(save);
  useEffect(() => {
    saveRef.current = save;
  }, [save]);
  const pending = useRef<Partial<T> | null>(null);
  const retries = useRef(0);
  const mounted = useRef(true);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const running = useRef<Promise<boolean> | null>(null);

  const run = useCallback((): Promise<boolean> => {
    if (running.current) return running.current;
    const operation = async () => {
      while (pending.current) {
        const patch = pending.current;
        pending.current = null;
        try {
          await saveRef.current(patch);
          retries.current = 0;
          if (mounted.current) setError(null);
        } catch (failure) {
          pending.current = Object.assign({}, patch, pending.current);
          if (mounted.current) setError(failure);
          if (
            mounted.current &&
            !isApiError(failure, "VERSION_CONFLICT") &&
            (!isApiError(failure) ||
              failure.status === 0 ||
              failure.status === 429 ||
              failure.status >= 500) &&
            retries.current < 3
          ) {
            clearTimeout(timer.current);
            timer.current = setTimeout(
              () => void run(),
              1000 * 2 ** retries.current++,
            );
          }
          return false;
        }
      }
      return true;
    };
    running.current = operation().finally(() => {
      running.current = null;
    });
    return running.current;
  }, []);

  const schedule = useCallback(
    (patch: Partial<T>) => {
      retries.current = 0;
      pending.current = { ...pending.current, ...patch };
      clearTimeout(timer.current);
      timer.current = setTimeout(() => void run(), delay);
    },
    [delay, run],
  );

  useEffect(() => {
    mounted.current = true;
    const unload = (event: BeforeUnloadEvent) => {
      if (!pending.current && !running.current) return;
      event.preventDefault();
      event.returnValue = "";
    };
    const hide = () => {
      clearTimeout(timer.current);
      if (pending.current) void run();
    };
    window.addEventListener("beforeunload", unload);
    window.addEventListener("pagehide", hide);
    return () => {
      mounted.current = false;
      window.removeEventListener("beforeunload", unload);
      window.removeEventListener("pagehide", hide);
      clearTimeout(timer.current);
      if (pending.current) void run();
    };
  }, [run]);

  const flush = useCallback(() => {
    clearTimeout(timer.current);
    return run();
  }, [run]);
  return { schedule, error, flush };
}

/** 把节点写回缓存（详情），保持 version 与内容一致。 */
export function putDocNode(
  queryClient: QueryClient,
  orgId: string,
  spaceId: string,
  node: DocNodeDetail,
) {
  queryClient.setQueryData(keys.docNode(orgId, spaceId, node.id), node);
}

/** 真实冲突携带新版供对照，只有用户确认合并后才采用其版本。 */
export class DocSaveConflict extends ApiError {
  constructor(readonly remote: DocNodeDetail) {
    super({ status: 409, code: "VERSION_CONFLICT", message: "文档已被修改" });
  }
}
export async function saveDocPatch(
  update: () => Promise<DocNodeDetail>,
  reload: () => Promise<DocNodeDetail>,
  expected: Pick<DocNodeDetail, "title" | "body">,
): Promise<DocNodeDetail> {
  try {
    return await update();
  } catch (error) {
    if (!isApiError(error, "VERSION_CONFLICT")) throw error;
    let fresh: DocNodeDetail;
    try {
      fresh = await reload();
    } catch {
      throw error;
    }
    if (
      (fresh.body ?? "") === (expected.body ?? "") &&
      fresh.title === expected.title
    )
      return fresh;
    throw new DocSaveConflict(fresh);
  }
}
