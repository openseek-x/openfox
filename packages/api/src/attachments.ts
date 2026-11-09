import type { HttpClient } from "./http";
import type { components } from "./schema.gen";

// 阶段 7c · 任务附件与对话附件。公开类型取自生成契约。
type S = components["schemas"];

export type Attachment = S["Attachment"];

/** 单文件上限（字节），与文档库一致。 */
export const ATTACHMENT_MAX_BYTES = 20 * 1024 * 1024;
/** 每个任务最多的附件数。 */
export const TASK_ATTACHMENT_LIMIT = 100;
/** 一条消息最多携带的附件数。 */
export const MESSAGE_ATTACHMENT_LIMIT = 10;

const org = (orgId: string) => `/v1/organizations/${encodeURIComponent(orgId)}`;

export function createAttachmentsApi(http: HttpClient) {
  const upload = (path: string, file: File) => {
    const form = new FormData();
    form.set("file", file);
    return http.request<Attachment>(path, { method: "POST", body: form });
  };
  return {
    attachments: {
      listForTask: async (orgId: string, spaceId: string, taskId: string) =>
        (
          await http.request<{ attachments: Attachment[] }>(
            `${org(orgId)}/spaces/${encodeURIComponent(spaceId)}/tasks/${encodeURIComponent(taskId)}/attachments`,
          )
        ).attachments,
      uploadToTask: (
        orgId: string,
        spaceId: string,
        taskId: string,
        file: File,
      ) =>
        upload(
          `${org(orgId)}/spaces/${encodeURIComponent(spaceId)}/tasks/${encodeURIComponent(taskId)}/attachments`,
          file,
        ),
      /** 先上传，再在 postMessage 的 attachment_ids 里引用。 */
      uploadToConversation: (
        orgId: string,
        conversationId: string,
        file: File,
      ) =>
        upload(
          `${org(orgId)}/conversations/${encodeURIComponent(conversationId)}/attachments`,
          file,
        ),
      remove: (orgId: string, attachmentId: string) =>
        http.request<void>(
          `${org(orgId)}/attachments/${encodeURIComponent(attachmentId)}`,
          { method: "DELETE" },
        ),
      /** 下载地址（attachment）；浏览器直接访问。 */
      url: (orgId: string, attachmentId: string) =>
        http.url(
          `${org(orgId)}/attachments/${encodeURIComponent(attachmentId)}`,
        ),
      /** 图片预览地址（inline），只对 previewable 的附件有效。 */
      previewUrl: (orgId: string, attachmentId: string) =>
        http.url(
          `${org(orgId)}/attachments/${encodeURIComponent(attachmentId)}?disposition=inline`,
        ),
    },
  };
}
