import { isCurrentTab } from "./params";
import { useQuery } from "@tanstack/react-query";

import type { ArtifactReviewFile, OfficeView } from "@openfox/api";

import { keys, useApi } from "../../context";
import { artifactFetchLimit, artifactPreviewKind } from "./model";

// 阶段 10d · 会话产物查询。列表挂在会话详情键下（Run 结束、会话变化时一并刷新），
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

/** 预览内容：图片、PDF、视频返回 Blob，文本返回解码后的字符串。产物不可变，取一次即可。 */
export function useArtifactContent(
  orgId: string,
  artifact: ArtifactReviewFile | undefined,
  enabled: boolean,
) {
  const api = useApi();
  const kind = artifact ? artifactPreviewKind(artifact) : "binary";
  return useQuery({
    queryKey: [
      ...keys.runSub(orgId, artifact?.run_id ?? "", "artifacts"),
      "content",
      artifact?.id ?? "",
      artifact?.sha256 ?? "",
    ],
    queryFn: async ({
      signal,
    }): Promise<{ blob: Blob | null; text: string; office?: OfficeView }> => {
      const image = kind === "image" || kind === "pdf" || kind === "video";
      if (!artifact?.run_id) throw new Error("Artifact run is missing");
      if (kind === "office") {
        const office = await api.artifactReviews.office(
          orgId,
          artifact.run_id,
          artifact.id,
          signal,
        );
        if (office.sha256 !== artifact.sha256)
          throw new Error("Office content mismatch");
        const blob =
          office.kind !== "xlsx"
            ? await api.runs.artifactBlob(
                orgId,
                artifact.run_id,
                artifact.id,
                10 * 1024 * 1024,
                signal,
              )
            : await (
                office.spreadsheet
                  ? api.artifactReviews.officeWorkbook
                  : api.artifactReviews.officePreview
              )(orgId, artifact.run_id, artifact.id, signal);
        return { blob, text: "", office };
      }
      const blob = await api.runs.artifactBlob(
        orgId,
        artifact!.run_id,
        artifact!.id,
        artifactFetchLimit(kind),
        signal,
      );
      if (image) return { blob, text: "" };
      return {
        blob: null,
        text: new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(
          await blob.arrayBuffer(),
        ),
      };
    },
    enabled: Boolean(orgId && artifact) && enabled && kind !== "binary",
    staleTime: Infinity,
    gcTime: 0, // Release decoded text/Blobs when the last preview unmounts.
    retry: false,
  });
}

export const resolvedArtifactKey = (
  orgId: string,
  conversationId: string,
  tab: string,
) =>
  [
    ...keys.conversationArtifacts(orgId, conversationId),
    "resolve",
    tab,
  ] as const;
export function useResolvedArtifact(
  orgId: string,
  conversationId: string,
  tab: string,
  enabled: boolean,
) {
  const api = useApi();
  return useQuery({
    queryKey: resolvedArtifactKey(orgId, conversationId, tab),
    queryFn: ({ signal }) =>
      api.runs.resolveArtifactPreview(
        orgId,
        conversationId,
        isCurrentTab(tab) ? { content_id: tab.slice(8) } : { artifact_id: tab },
        signal,
      ),
    enabled,
    retry: false,
  });
}
