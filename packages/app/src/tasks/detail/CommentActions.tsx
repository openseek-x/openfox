import { plainPreview } from "../../conversations/model";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { isApiError, type Task, type TimelineComment } from "@openfox/api";
import { copyText } from "../parts";
import { taskPagePath } from "../navigation";
import { useOptionalCreateTaskDialog } from "../create/CreateTaskProvider";
import { useT } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  useToast,
} from "@openfox/ui";
import {
  attachmentMarkdown,
  filesOf,
  namedPastedFile,
} from "../../attachments/model";
import { keys, useApi } from "../../context";
import { useReadOnly } from "../../spaces/readonly";
import { taskErrorMessage } from "../queries";
import { useUploadTaskFiles } from "./AttachmentsSection";

interface Props {
  orgId: string;
  task: Task;
  comment: TimelineComment;
}

export function CommentMenu({
  orgId,
  task,
  comment,
  isResolution,
  onEdit,
}: Props & { isResolution: boolean; onEdit: () => void }) {
  const t = useT(),
    api = useApi(),
    toast = useToast(),
    cache = useQueryClient();
  const readOnly = useReadOnly();
  const attempt = useRef<{ resolved: boolean; key: string } | null>(null);
  const change = useMutation({
    mutationFn: (resolved: boolean) => {
      if (attempt.current?.resolved !== resolved)
        attempt.current = { resolved, key: crypto.randomUUID() };
      return api.comments.setResolution(
        orgId,
        task.space_id,
        task.id,
        comment.id,
        resolved,
        attempt.current.key,
      );
    },
    onSuccess: async () => {
      attempt.current = null;
      await cache.invalidateQueries({
        queryKey: keys.taskSub(orgId, task.id, "timeline"),
      });
    },
    onError: (error) => toast(taskErrorMessage(t, error), { tone: "error" }),
  });
  const createDialog = useOptionalCreateTaskDialog();
  const [deleting, setDeleting] = useState(false);
  const deleteKey = useRef<string | null>(null);
  const remove = useMutation({
    mutationFn: () =>
      api.comments.remove(
        orgId,
        task.space_id,
        task.id,
        comment.id,
        (deleteKey.current ??= crypto.randomUUID()),
      ),
    onSuccess: async () => {
      setDeleting(false);
      await cache.invalidateQueries({
        queryKey: keys.taskSub(orgId, task.id, "timeline"),
      });
    },
    onError: (err) => toast(taskErrorMessage(t, err), { tone: "error" }),
  });
  const copy = async (prompt: boolean) => {
    const value = prompt
      ? [
          t("tasks.detail.commentPromptGuide"),
          "Task: " +
            new URL(
              taskPagePath(orgId, task.display_id),
              window.location.origin,
            ).href,
          t("tasks.detail.commentPromptFocus", { id: comment.id }),
        ].join("\n\n")
      : comment.body;
    const ok = await copyText(value);
    toast(
      t(
        ok
          ? prompt
            ? "tasks.detail.copiedCommentPrompt"
            : "tasks.detail.copiedComment"
          : prompt
            ? "tasks.detail.copyCommentPromptFailed"
            : "tasks.detail.copyCommentFailed",
      ),
      ok ? undefined : { tone: "error" },
    );
  };
  const create = (sub: boolean) => {
    const body = comment.body.trim();
    createDialog?.openCreate({
      spaceId: task.space_id,
      title: Array.from(plainPreview(body)).slice(0, 200).join(""),
      description: body,
      projectId: task.project_id ?? undefined,
      iterationId: task.iteration_id ?? undefined,
      sourceComment: {
        space_id: task.space_id,
        task_id: task.id,
        id: comment.id,
      },
      ...(sub
        ? { parentTaskId: task.id, parentDisplayId: task.display_id }
        : { relateTo: { spaceId: task.space_id, taskId: task.id } }),
    });
  };
  const writable = !readOnly && Boolean(comment.edit_version);
  return (
    <>
      <Menu
        label={t("tasks.detail.commentMenu")}
        placement="bottom-end"
        trigger={({ ref, toggle, open }) => (
          <IconButton
            ref={ref}
            label={t("tasks.detail.commentMenu")}
            icon={<Icon name="ellipsis" />}
            size="28"
            aria-expanded={open}
            aria-haspopup="menu"
            disabled={change.isPending}
            onClick={toggle}
          />
        )}
      >
        {writable && comment.can_edit && (
          <>
            <MenuItem icon={<Icon name="openfox-pencil" />} onSelect={onEdit}>
              {t("tasks.detail.editComment")}
            </MenuItem>
            <MenuSeparator />
          </>
        )}
        {writable && (
          <>
            <MenuItem
              icon={
                <Icon
                  name={isResolution ? "rotate-ccw" : "openfox-circle-check"}
                />
              }
              onSelect={() => change.mutate(!isResolution)}
            >
              {t(
                isResolution
                  ? "tasks.detail.reopenComment"
                  : "tasks.detail.resolveComment",
              )}
            </MenuItem>
            <MenuSeparator />
          </>
        )}
        <MenuItem icon={<Icon name="copy" />} onSelect={() => void copy(false)}>
          {t("tasks.detail.copyComment")}
        </MenuItem>
        <MenuItem icon={<Icon name="copy" />} onSelect={() => void copy(true)}>
          {t("tasks.detail.copyCommentPrompt")}
        </MenuItem>
        {!readOnly && createDialog && (
          <>
            <MenuSeparator />
            <MenuItem
              icon={<Icon name="openfox-file-plus" />}
              onSelect={() => create(false)}
            >
              {t("tasks.detail.createFromComment")}
            </MenuItem>
            <MenuItem
              icon={<Icon name="openfox-file-input" />}
              onSelect={() => create(true)}
            >
              {t("tasks.detail.createSubFromComment")}
            </MenuItem>
          </>
        )}
        {writable && comment.can_delete && (
          <>
            <MenuSeparator />
            <MenuItem
              danger
              icon={<Icon name="trash-2" />}
              onSelect={() => setDeleting(true)}
            >
              {t("tasks.detail.deleteAction")}
            </MenuItem>
          </>
        )}
      </Menu>
      <ConfirmDialog
        open={deleting}
        onCancel={() => {
          if (!remove.isPending) setDeleting(false);
        }}
        onConfirm={() => {
          if (!remove.isPending) remove.mutate();
        }}
        loading={remove.isPending}
        danger
        title={t("tasks.detail.deleteComment")}
        description={t(
          comment.has_replies || comment.reply_count
            ? "tasks.detail.deleteCommentTree"
            : "tasks.detail.deleteCommentSingle",
        )}
        confirmLabel={t("tasks.detail.deleteAction")}
      />
    </>
  );
}

export function CommentEditor({
  orgId,
  task,
  comment,
  onClose,
}: Props & { onClose: () => void }) {
  const t = useT(),
    api = useApi(),
    toast = useToast(),
    cache = useQueryClient();
  const [original] = useState(() => ({
    body: comment.body,
    version: comment.edit_version!,
  }));
  const [body, setBody] = useState(original.body);
  const [discard, setDiscard] = useState(false);
  const input = useRef<HTMLTextAreaElement>(null),
    file = useRef<HTMLInputElement>(null);
  const readOnly = useReadOnly();
  const { upload, uploading } = useUploadTaskFiles(orgId, task);
  const attempt = useRef<{ body: string; key: string } | null>(null);
  const save = useMutation({
    mutationFn: (text: string) => {
      if (attempt.current?.body !== text)
        attempt.current = { body: text, key: crypto.randomUUID() };
      return api.comments.edit(
        orgId,
        task.space_id,
        task.id,
        comment.id,
        { body: text, version: original.version },
        attempt.current.key,
      );
    },
    onSuccess: async () => {
      await cache.invalidateQueries({
        queryKey: keys.taskSub(orgId, task.id, "timeline"),
      });
      onClose();
    },
    onError: (error) => {
      if (isApiError(error, "VERSION_CONFLICT"))
        void cache.invalidateQueries({
          queryKey: keys.taskSub(orgId, task.id, "timeline"),
        });
      toast(taskErrorMessage(t, error), { tone: "error" });
    },
  });
  useEffect(() => {
    const el = input.current;
    if (el) {
      el.focus();
      el.setSelectionRange(el.value.length, el.value.length);
    }
  }, []);
  const busy = save.isPending || uploading.length > 0;
  const cancel = () => {
    if (body === original.body) onClose();
    else setDiscard(true);
  };
  const submit = () => {
    if (!busy && !readOnly && body.trim()) save.mutate(body.trim());
  };
  const insertFiles = async (files: File[]) => {
    const start = input.current?.selectionStart ?? body.length,
      end = input.current?.selectionEnd ?? start;
    const uploaded = await upload(files);
    if (!uploaded.length) return;
    const links = uploaded
      .map((a) => attachmentMarkdown(a, api.attachments.url(orgId, a.id)))
      .join("\n");
    setBody((value) => value.slice(0, start) + links + value.slice(end));
    requestAnimationFrame(() => input.current?.focus());
  };
  return (
    <div
      className="mt-2 rounded-lg border border-line-default bg-surface-card p-2"
      onDragOver={(e) => {
        if (e.dataTransfer.types.includes("Files")) e.preventDefault();
      }}
      onDrop={(e) => {
        const files = filesOf(e.dataTransfer.files);
        if (!files.length) return;
        e.preventDefault();
        e.stopPropagation();
        if (!busy && !readOnly) void insertFiles(files);
      }}
    >
      <textarea
        ref={input}
        aria-label={t("tasks.detail.editCommentPlaceholder")}
        rows={2}
        placeholder={t("tasks.detail.editCommentPlaceholder")}
        maxLength={65536}
        value={body}
        disabled={busy || readOnly}
        onChange={(e) => setBody(e.target.value)}
        onPaste={(e) => {
          const images = filesOf(e.clipboardData.files).filter((file) =>
            file.type.startsWith("image/"),
          );
          if (!images.length) return;
          e.preventDefault();
          e.stopPropagation();
          void insertFiles(
            images.map((file) =>
              namedPastedFile(file, t("attachments.pastedImage")),
            ),
          );
        }}
        onKeyDown={(e) => {
          if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229)
            return;
          if (e.key === "Escape") {
            e.preventDefault();
            e.stopPropagation();
            cancel();
          }
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            e.stopPropagation();
            submit();
          }
        }}
        className="block max-h-[400px] min-h-10 w-full field-sizing-content resize-none bg-transparent px-1 text-body outline-none focus-visible:shadow-none"
      />
      <div className="mt-2 flex items-center gap-2">
        <IconButton
          label={t("attachments.add")}
          icon={<Icon name="paperclip" />}
          size="28"
          disabled={busy || readOnly}
          onClick={() => file.current?.click()}
        />
        <input
          type="file"
          multiple
          hidden
          ref={file}
          onChange={(e) => {
            const files = filesOf(e.target.files);
            e.target.value = "";
            void insertFiles(files);
          }}
        />
        <span className="flex-1" />
        <Button variant="ghost" size="28" disabled={busy} onClick={cancel}>
          {t("common.cancel")}
        </Button>
        <Button
          size="28"
          disabled={busy || readOnly || !body.trim()}
          onClick={submit}
        >
          {t("common.save")}
        </Button>
      </div>
      <ConfirmDialog
        open={discard}
        title={t("tasks.detail.discardEditTitle")}
        description={t("tasks.detail.discardEditDescription")}
        confirmLabel={t("tasks.detail.discardEdit")}
        danger
        onCancel={() => setDiscard(false)}
        onConfirm={onClose}
      />
    </div>
  );
}
