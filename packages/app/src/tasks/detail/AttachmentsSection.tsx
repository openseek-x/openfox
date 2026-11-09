import {
  useRef,
  useState,
  type DragEvent,
  type ClipboardEvent,
  type ReactNode,
} from "react";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  TASK_ATTACHMENT_LIMIT,
  type Attachment,
  type Task,
} from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  ConfirmDialog,
  Dialog,
  Icon,
  IconButton,
  Spinner,
  cn,
  useToast,
} from "@openfox/ui";

import {
  attachmentErrorMessage,
  filesOf,
  formatBytes,
  namedPastedFile,
  precheckFile,
} from "../../attachments/model";
import { keys, useApi } from "../../context";
import { ReadOnlyTip, useReadOnly } from "../../spaces/readonly";
import { formatRelativeTime } from "../primitives/dates";
import { Section } from "./Sections";

// 任务详情的「附件」区：列表（文件名、大小、上传人、时间）、上传（按钮 / 拖拽 / 粘贴图片）、
// 图片预览、下载、删除（上传人或空间管理员，由服务端 can_delete 决定）。

/** 任务附件列表的查询键，评论框插入附件链接时共用。 */
export const taskAttachmentsKey = (orgId: string, taskId: string) =>
  keys.taskSub(orgId, taskId, "attachments");

export function useTaskAttachments(orgId: string, task: Task) {
  const api = useApi();
  return useQuery({
    queryKey: taskAttachmentsKey(orgId, task.id),
    queryFn: () => api.attachments.listForTask(orgId, task.space_id, task.id),
  });
}

/** 依次上传文件；成功后刷新列表。返回成功的附件，失败的逐个提示。 */
export function useUploadTaskFiles(orgId: string, task: Task) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [uploading, setUploading] = useState<string[]>([]);
  const upload = async (files: File[]): Promise<Attachment[]> => {
    const done: Attachment[] = [];
    for (const file of files) {
      if (precheckFile(file)) {
        toast(t("attachments.errors.tooLarge", { name: file.name }), {
          tone: "error",
        });
        continue;
      }
      setUploading((prev) => [...prev, file.name]);
      try {
        done.push(
          await api.attachments.uploadToTask(
            orgId,
            task.space_id,
            task.id,
            file,
          ),
        );
      } catch (error) {
        toast(attachmentErrorMessage(t, error, file.name), { tone: "error" });
      } finally {
        setUploading((prev) => {
          const index = prev.indexOf(file.name);
          return index < 0 ? prev : prev.filter((_, i) => i !== index);
        });
      }
    }
    if (done.length > 0)
      await queryClient.invalidateQueries({
        queryKey: taskAttachmentsKey(orgId, task.id),
      });
    return done;
  };
  return { upload, uploading };
}

export function AttachmentsSection({
  orgId,
  task,
  renderActions,
}: {
  orgId: string;
  task: Task;
  renderActions: (uploadButton: ReactNode) => ReactNode;
}) {
  const t = useT();
  const { locale } = useI18n();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const readOnly = useReadOnly();
  const list = useTaskAttachments(orgId, task);
  const { upload, uploading } = useUploadTaskFiles(orgId, task);
  const input = useRef<HTMLInputElement | null>(null);
  const [dragging, setDragging] = useState(false);
  const [preview, setPreview] = useState<Attachment | null>(null);
  const [removing, setRemoving] = useState<Attachment | null>(null);
  const items = list.data ?? [];

  const remove = useMutation({
    mutationFn: (attachment: Attachment) =>
      api.attachments.remove(orgId, attachment.id),
    onSuccess: () => {
      setRemoving(null);
      toast(t("attachments.deleted"));
      void queryClient.invalidateQueries({
        queryKey: taskAttachmentsKey(orgId, task.id),
      });
    },
    onError: (error) => {
      setRemoving(null);
      toast(attachmentErrorMessage(t, error, removing?.name ?? ""), {
        tone: "error",
      });
    },
  });

  const onDrop = (event: DragEvent) => {
    event.preventDefault();
    setDragging(false);
    if (readOnly) return;
    void upload(filesOf(event.dataTransfer.files));
  };
  const onPaste = (event: ClipboardEvent) => {
    if (readOnly) return;
    const images = filesOf(event.clipboardData.files).filter((file) =>
      file.type.startsWith("image/"),
    );
    if (images.length === 0) return;
    event.preventDefault();
    void upload(
      images.map((file) => namedPastedFile(file, t("attachments.pastedImage"))),
    );
  };

  const count = items.length
    ? `${items.length}/${TASK_ATTACHMENT_LIMIT}`
    : undefined;
  return (
    <div
      tabIndex={-1}
      data-testid="attachment-drop"
      onDragOver={(event) => {
        if (readOnly) return;
        event.preventDefault();
        setDragging(true);
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={onDrop}
      onPaste={onPaste}
      className={cn(
        "flex flex-col gap-3 rounded-lg outline-none",
        dragging && "bg-surface-hover ring-1 ring-line-focus",
      )}
    >
      {renderActions(
        <ReadOnlyTip>
          <IconButton
            label={t("attachments.add")}
            icon={<Icon name="paperclip" />}
            size="28"
            disabled={readOnly}
            onClick={() => input.current?.click()}
          />
        </ReadOnlyTip>,
      )}
      <input
        ref={input}
        type="file"
        multiple
        hidden
        data-testid="attachment-input"
        onChange={(event) => {
          const files = filesOf(event.target.files);
          event.target.value = "";
          void upload(files);
        }}
      />
      {(items.length > 0 ||
        uploading.length > 0 ||
        list.isError ||
        dragging) && (
        <Section title={t("attachments.title")} count={count}>
          {dragging && (
            <p className="px-2 py-3 text-center text-body-sm text-fg-muted">
              {t("attachments.dropHint")}
            </p>
          )}
          {list.isError && (
            <p role="alert" className="px-1 py-2 text-body-sm text-fg-muted">
              {t("attachments.loadFailed")}
            </p>
          )}
          <ul className="flex flex-col">
            {items.map((attachment) => (
              <li
                key={attachment.id}
                className="flex min-h-9 items-center gap-2 rounded-lg px-1 py-1 text-body-sm hover:bg-surface-hover"
              >
                <Icon
                  name={attachment.previewable ? "image" : "file-text"}
                  size={14}
                  className="shrink-0 text-fg-muted"
                />
                {attachment.previewable ? (
                  <button
                    type="button"
                    aria-label={t("attachments.preview", {
                      name: attachment.name,
                    })}
                    onClick={() => setPreview(attachment)}
                    className="min-w-0 flex-1 truncate text-left text-fg-default hover:underline"
                  >
                    {attachment.name}
                  </button>
                ) : (
                  <a
                    href={api.attachments.url(orgId, attachment.id)}
                    className="min-w-0 flex-1 truncate text-fg-default hover:underline"
                  >
                    {attachment.name}
                  </a>
                )}
                <span className="shrink-0 text-fg-muted">
                  {formatBytes(attachment.size_bytes)}
                </span>
                <span
                  className="hidden shrink-0 text-fg-subtle sm:inline"
                  title={t("attachments.uploadedBy", {
                    name: attachment.uploader_name,
                    time: new Date(attachment.created_at).toLocaleString(
                      locale,
                    ),
                  })}
                >
                  {attachment.uploader_name}
                  {" · "}
                  <time dateTime={attachment.created_at}>
                    {formatRelativeTime(attachment.created_at, locale)}
                  </time>
                </span>
                <a
                  href={api.attachments.url(orgId, attachment.id)}
                  aria-label={t("attachments.download", {
                    name: attachment.name,
                  })}
                  className="flex size-6 shrink-0 items-center justify-center rounded-md text-fg-muted hover:bg-surface-muted"
                >
                  <Icon name="download" size={14} />
                </a>
                {attachment.can_delete && !readOnly && (
                  <IconButton
                    label={t("attachments.delete", { name: attachment.name })}
                    icon={<Icon name="trash-2" size={14} />}
                    size="28"
                    className="size-6"
                    onClick={() => setRemoving(attachment)}
                  />
                )}
              </li>
            ))}
            {uploading.map((name, index) => (
              <li
                key={`uploading-${index}`}
                className="flex min-h-9 items-center gap-2 px-1 py-1 text-body-sm text-fg-muted"
              >
                <Spinner size={14} />
                {t("attachments.uploading", { name })}
              </li>
            ))}
          </ul>
        </Section>
      )}
      <Dialog
        open={Boolean(preview)}
        onClose={() => setPreview(null)}
        title={preview?.name ?? ""}
        width={720}
      >
        {preview && (
          <img
            src={api.attachments.previewUrl(orgId, preview.id)}
            alt={preview.name}
            className="mx-auto max-h-[70vh] max-w-full object-contain"
          />
        )}
      </Dialog>
      <ConfirmDialog
        open={Boolean(removing)}
        title={t("attachments.deleteTitle")}
        description={t("attachments.deleteConfirm", {
          name: removing?.name ?? "",
        })}
        confirmLabel={t("attachments.confirmDelete")}
        danger
        loading={remove.isPending}
        onCancel={() => setRemoving(null)}
        onConfirm={() => removing && remove.mutate(removing)}
      />
    </div>
  );
}
