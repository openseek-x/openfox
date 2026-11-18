import type { HttpClient } from "./http";
import type { components, operations } from "./schema.gen";

// 阶段 5b · 文档库：文档集、文件夹 / 文档 / 表格 / 文件、附件、搜索。所有公开类型取自生成契约。
type S = components["schemas"];
type JsonBody<K extends keyof operations> =
  NonNullable<operations[K]["requestBody"]> extends {
    content: { "application/json": infer B };
  }
    ? B
    : never;

export type DocRevision = S["DocRevision"];
export type DocSet = S["DocSet"];
export type DocSetInput = S["DocSetInput"];
export type DocContainer = S["DocContainer"];
export type DocNode = S["DocNode"];
export type DocNodeDetail = S["DocNodeDetail"];
export type DocNodeKind = S["DocNodeKind"];
export type DocAttachment = S["DocAttachment"];
export type DocNodeCreateInput = JsonBody<"createDocNode">;
export type DocNodeUpdateInput = JsonBody<"updateDocNode">;
export type DocNodeMoveInput = JsonBody<"moveDocNode">;

const spacePath = (orgId: string, spaceId: string) =>
  `/v1/organizations/${encodeURIComponent(orgId)}/spaces/${encodeURIComponent(spaceId)}`;
const docsPath = (orgId: string, spaceId: string) =>
  `${spacePath(orgId, spaceId)}/docs`;
const nodePath = (orgId: string, spaceId: string, nodeId: string) =>
  `${docsPath(orgId, spaceId)}/${encodeURIComponent(nodeId)}`;

/** 列出子节点的查询条件：container 必填，set_id 仅文档集需要，parent_id 缺省为容器根。 */
export type DocNodeListQuery = {
  container: DocContainer["kind"];
  set_id?: string;
  parent_id?: string;
};

export function createDocsApi(http: HttpClient) {
  return {
    docSets: {
      list: async (orgId: string, spaceId: string) =>
        (
          await http.request<{ doc_sets: DocSet[] }>(
            `${spacePath(orgId, spaceId)}/doc-sets`,
          )
        ).doc_sets,
      create: (orgId: string, spaceId: string, input: DocSetInput) =>
        http.request<DocSet>(`${spacePath(orgId, spaceId)}/doc-sets`, {
          method: "POST",
          body: input,
        }),
      update: (
        orgId: string,
        spaceId: string,
        setId: string,
        input: Pick<DocSet, "version"> & DocSetInput,
      ) =>
        http.request<DocSet>(
          `${spacePath(orgId, spaceId)}/doc-sets/${encodeURIComponent(setId)}`,
          { method: "PATCH", body: input },
        ),
      remove: (
        orgId: string,
        spaceId: string,
        docSet: Pick<DocSet, "id" | "version">,
      ) =>
        http.request<void>(
          `${spacePath(orgId, spaceId)}/doc-sets/${encodeURIComponent(docSet.id)}`,
          { method: "DELETE", query: { version: docSet.version } },
        ),
    },
    docNodes: {
      revisions: (
        orgId: string,
        spaceId: string,
        nodeId: string,
        before?: number,
      ) =>
        http.request<{ revisions: DocRevision[]; next_before: number }>(
          `${nodePath(orgId, spaceId, nodeId)}/revisions`,
          { query: { before } },
        ),
      revision: (
        orgId: string,
        spaceId: string,
        nodeId: string,
        revision: number,
      ) =>
        http.request<DocRevision>(
          `${nodePath(orgId, spaceId, nodeId)}/revisions/${revision}`,
        ),
      restore: (
        orgId: string,
        spaceId: string,
        nodeId: string,
        revision: number,
        version: number,
      ) =>
        http.request<DocNodeDetail>(
          `${nodePath(orgId, spaceId, nodeId)}/revisions/${revision}/restore`,
          { method: "POST", body: { version } },
        ),
      list: async (orgId: string, spaceId: string, query: DocNodeListQuery) =>
        (
          await http.request<{ nodes: DocNode[] }>(docsPath(orgId, spaceId), {
            query,
          })
        ).nodes,
      create: (orgId: string, spaceId: string, input: DocNodeCreateInput) =>
        http.request<DocNodeDetail>(docsPath(orgId, spaceId), {
          method: "POST",
          body: input,
        }),
      /** 上传一个文件；.md 变为文档，.csv 变为表格，其余为文件。 */
      upload: (
        orgId: string,
        spaceId: string,
        input: DocNodeListQuery & { file: File },
      ) => {
        const form = new FormData();
        form.set("container", input.container);
        if (input.set_id) form.set("set_id", input.set_id);
        if (input.parent_id) form.set("parent_id", input.parent_id);
        form.set("file", input.file);
        return http.request<DocNode>(`${docsPath(orgId, spaceId)}/upload`, {
          method: "POST",
          body: form,
        });
      },
      search: async (orgId: string, spaceId: string, q: string) =>
        (
          await http.request<{ nodes: DocNode[] }>(
            `${docsPath(orgId, spaceId)}/search`,
            { query: { q } },
          )
        ).nodes,
      get: (orgId: string, spaceId: string, nodeId: string) =>
        http.request<DocNodeDetail>(nodePath(orgId, spaceId, nodeId)),
      update: (
        orgId: string,
        spaceId: string,
        nodeId: string,
        input: DocNodeUpdateInput,
      ) =>
        http.request<DocNodeDetail>(nodePath(orgId, spaceId, nodeId), {
          method: "PATCH",
          body: input,
        }),
      remove: (
        orgId: string,
        spaceId: string,
        node: Pick<DocNode, "id" | "version">,
      ) =>
        http.request<void>(nodePath(orgId, spaceId, node.id), {
          method: "DELETE",
          query: { version: node.version },
        }),
      move: (
        orgId: string,
        spaceId: string,
        nodeId: string,
        input: DocNodeMoveInput,
      ) =>
        http.request<DocNode>(`${nodePath(orgId, spaceId, nodeId)}/move`, {
          method: "POST",
          body: input,
        }),
      /** 下载地址，用于 <a href> 与图片预览；浏览器直接访问，不经过 JSON 解析。 */
      downloadUrl: (orgId: string, spaceId: string, nodeId: string) =>
        http.url(`${nodePath(orgId, spaceId, nodeId)}/download`),
      uploadAttachment: (
        orgId: string,
        spaceId: string,
        nodeId: string,
        file: File,
      ) => {
        const form = new FormData();
        form.set("file", file);
        return http.request<DocAttachment>(
          `${nodePath(orgId, spaceId, nodeId)}/attachments`,
          { method: "POST", body: form },
        );
      },
    },
  };
}
