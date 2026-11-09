import { useQuery } from "@tanstack/react-query";

import type { RunArtifact } from "@openfox/api";

import { keys, useApi } from "../../context";
import {
  ARTIFACT_IMAGE_MAX_BYTES,
  ARTIFACT_TEXT_MAX_BYTES,
  artifactPreviewKind,
} from "./model";

// 会话产物查询。列表挂在会话详情键下（Run 结束、会话变化时一并刷新），
// 实时由 run.artifact 组织事件精确失效（见 realtime/useOrgEvents.ts）。

export function useConversationArtifacts(
  orgId: string,
  conversationId: string | null | undefined,
) {
  const api = useApi();
  return useQuery({
    queryKey: keys.conversationArtifacts(orgId, conversationId ?? ""),
    queryFn: ({ signal }) =>
      api.runs.conversationArtifacts(orgId, conversationId!, signal),
    enabled: Boolean(orgId && conversationId),
  });
}

/** 预览内容：图片返回 Blob，文本返回解码后的字符串。产物不可变，取一次即可。 */
export function useArtifactContent(
  orgId: string,
  artifact: RunArtifact | undefined,
  enabled: boolean,
) {
  const api = useApi();
  const kind = artifact ? artifactPreviewKind(artifact) : "binary";
  return useQuery({
    queryKey: [
      ...keys.runSub(orgId, artifact?.run_id ?? "", "artifacts"),
      "content",
      artifact?.id ?? "",
    ],
    queryFn: async ({ signal }) => {
      const image = kind === "image";
      const blob = await api.runs.artifactBlob(
        orgId,
        artifact!.run_id,
        artifact!.id,
        image ? ARTIFACT_IMAGE_MAX_BYTES : ARTIFACT_TEXT_MAX_BYTES,
        signal,
      );
      if (image) return { blob, text: "" };
      return { blob: null, text: await blob.text() };
    },
    enabled: Boolean(orgId && artifact) && enabled && kind !== "binary",
    staleTime: Infinity,
    retry: false,
  });
}
