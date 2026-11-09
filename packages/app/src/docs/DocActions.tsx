import { useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  type ChangeEvent,
  type ReactNode,
} from "react";
import { useNavigate } from "react-router-dom";

import {
  isApiError,
  type DocNode,
  type DocNodeDetail,
  type DocSet,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { ConfirmDialog, useToast } from "@openfox/ui";

import { useApi } from "../context";
import { useCreateTaskDialog } from "../tasks/create/CreateTaskProvider";
import { DocSetDialog } from "./DocSetDialog";
import { MoveDocDialog } from "./MoveDocDialog";
import { RenameDocDialog } from "./RenameDocDialog";
import { SearchDocsDialog } from "./SearchDocsDialog";
import { docErrorMessage, useDeleteDocNode, useDeleteDocSet } from "./queries";
import { docLink, docsPath, planFolderUpload, type DocLocation } from "./model";
import { UPLOAD_CONCURRENCY, runLimited } from "./upload";
import { DOC_FILE_MAX_BYTES } from "./model";

// 文档库动作中心：新建、上传、删除、复制链接、添加到任务，以及它们共用的弹窗。
// 导航树、表格、编辑页与菜单都通过 useDocActions 调用，避免各处重复实现。

/** 一批上传的结果计数 */
interface UploadOutcome {
  failed: number;
  tooLarge: number;
}

type DialogState =
  | { kind: "set"; set?: DocSet }
  | { kind: "setDelete"; set: DocSet }
  | { kind: "rename"; node: DocNode }
  | { kind: "delete"; node: DocNode }
  | { kind: "move"; node: DocNode }
  | { kind: "search" }
  | null;

export interface DocActions {
  /** 导航树里已展开的节点 id（文件夹）与文档集（set:<id>） */
  expanded: ReadonlySet<string>;
  toggleExpanded(key: string): void;
  /** 新建文档 / 表格 / 文件夹，成功后文档与表格直接打开 */
  createNode(location: DocLocation, kind: "doc" | "sheet" | "folder"): void;
  /** 打开系统选择框：文件或文件夹（保留目录结构） */
  requestUpload(location: DocLocation, mode: "file" | "folder"): void;
  /** 上传进行中：菜单项禁用，文字变为「正在上传」 */
  uploading: boolean;
  newSet(): void;
  editSet(set: DocSet): void;
  deleteSet(set: DocSet): void;
  rename(node: DocNode): void;
  remove(node: DocNode): void;
  move(node: DocNode): void;
  search(): void;
  copyLink(node: DocNode): void;
  addToTask(node: DocNode): void;
  openNode(nodeId: string): void;
}

const DocActionsContext = createContext<DocActions | null>(null);

export function useDocActions(): DocActions {
  const value = useContext(DocActionsContext);
  if (!value) throw new Error("useDocActions 必须在 DocActionsProvider 内使用");
  return value;
}

/** 上传与新建写入的容器参数（与 API 的 container / set_id / parent_id 对应）。 */
function targetOf(location: DocLocation) {
  return {
    container: location.container.kind,
    set_id:
      location.container.kind === "set" ? location.container.set_id : undefined,
    parent_id: location.parentId ?? undefined,
  };
}

/** 当前打开的节点（用于删除后判断是否需要回到父级）。 */
function isInside(current: DocNodeDetail | undefined, nodeId: string): boolean {
  if (!current) return false;
  return (
    current.id === nodeId ||
    current.breadcrumb.some((crumb) => crumb.id === nodeId)
  );
}

export function DocActionsProvider({
  orgId,
  spaceId,
  current,
  children,
}: {
  orgId: string;
  spaceId: string;
  /** 当前打开的节点；没有时为我的文档根 */
  current: DocNodeDetail | undefined;
  children: ReactNode;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { openCreate } = useCreateTaskDialog();
  const deleteNode = useDeleteDocNode(orgId, spaceId);
  const deleteSet = useDeleteDocSet(orgId, spaceId);
  const [dialog, setDialog] = useState<DialogState>(null);
  const [uploading, setUploading] = useState(false);
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(
    () => new Set(),
  );
  const expandKeys = (keys: readonly string[]) =>
    setExpanded((prev) => {
      if (keys.every((key) => prev.has(key))) return prev;
      return new Set([...prev, ...keys]);
    });
  // 打开某个节点时，展开它所在的文档集与所有祖先，让树里能看到当前位置
  useEffect(() => {
    if (!current) return;
    expandKeys([
      ...(current.container.kind === "set" && current.container.set_id
        ? [`set:${current.container.set_id}`]
        : []),
      ...current.breadcrumb.map((crumb) => crumb.id),
      ...(current.kind === "folder" ? [current.id] : []),
    ]);
    // 只在切换打开的节点时展开，不随 current 的其他字段变化重复执行
  }, [current?.id]);
  // 选择文件后要上传到哪里：点击菜单项时记下位置，change 事件里使用
  const target = useRef<DocLocation | null>(null);
  const fileInput = useRef<HTMLInputElement | null>(null);
  const folderInput = useRef<HTMLInputElement | null>(null);

  const invalidate = () =>
    void queryClient.invalidateQueries({ queryKey: ["org", orgId, "docs"] });

  const createNode: DocActions["createNode"] = async (location, kind) => {
    try {
      const node = await api.docNodes.create(orgId, spaceId, {
        container: location.container,
        ...(location.parentId ? { parent_id: location.parentId } : {}),
        kind,
        title: kind === "folder" ? t("docs.create.folderName") : "",
      });
      invalidate();
      if (kind !== "folder") navigate(docsPath(orgId, spaceId, node.id));
    } catch (error) {
      toast(docErrorMessage(t, error, "docs.create.failed"), {
        tone: "error",
      });
    }
  };

  /** 上传完成后统一提示与刷新：超大文件与其他失败各提示一次。 */
  const finishUpload = (outcome: UploadOutcome) => {
    setUploading(false);
    invalidate();
    if (outcome.tooLarge > 0)
      toast(t("docs.create.fileTooLarge"), { tone: "error" });
    if (outcome.failed > 0)
      toast(t("docs.create.uploadFailed"), { tone: "error" });
  };

  /** 发送一个文件；服务端 413 计入超大，其余错误计入失败。 */
  const sendFile = (
    outcome: UploadOutcome,
    location: DocLocation,
    file: File,
    parentId?: string,
  ) =>
    api.docNodes
      .upload(orgId, spaceId, {
        ...targetOf(location),
        ...(parentId ? { parent_id: parentId } : {}),
        file,
      })
      .then(
        () => undefined,
        (error: unknown) => {
          if (isApiError(error) && error.status === 413) outcome.tooLarge++;
          else outcome.failed++;
        },
      );

  const uploadFiles = async (location: DocLocation, files: File[]) => {
    setUploading(true);
    const outcome: UploadOutcome = { failed: 0, tooLarge: 0 };
    await runLimited(files, UPLOAD_CONCURRENCY, (file) =>
      sendFile(outcome, location, file),
    );
    finishUpload(outcome);
  };

  const uploadFolder = async (location: DocLocation, files: File[]) => {
    setUploading(true);
    const outcome: UploadOutcome = { failed: 0, tooLarge: 0 };
    try {
      const plan = planFolderUpload(files);
      // 目录路径 → 节点 id；根目录的父级就是当前位置
      const folderIds = new Map<string, string | null>([
        ["", location.parentId],
      ]);
      // 目录按层级排序，父目录先创建；任一目录失败则整体中止，避免文件落到错误位置
      for (const dir of plan.directories) {
        const parentId = folderIds.get(dir.parent) ?? null;
        const node = await api.docNodes.create(orgId, spaceId, {
          container: location.container,
          ...(parentId ? { parent_id: parentId } : {}),
          kind: "folder",
          title: dir.name,
        });
        folderIds.set(dir.path, node.id);
      }
      await runLimited(plan.files, UPLOAD_CONCURRENCY, ({ file, dir }) =>
        sendFile(outcome, location, file, folderIds.get(dir) ?? undefined),
      );
    } catch {
      outcome.failed++;
    }
    finishUpload(outcome);
  };

  const onFilesPicked = (
    event: ChangeEvent<HTMLInputElement>,
    folder: boolean,
  ) => {
    const picked = Array.from(event.target.files ?? []);
    event.target.value = "";
    const location = target.current;
    if (!location || picked.length === 0) return;
    // 超过 20 MB 的文件在本地拦截，不发请求。目录只由保留下来的文件推导，
    // 因此子树里全部超限的文件夹不会被创建
    const files = picked.filter((file) => file.size <= DOC_FILE_MAX_BYTES);
    const skipped = picked.length - files.length;
    if (skipped > 0)
      toast(t("docs.create.skippedTooLarge", { count: skipped }), {
        tone: "error",
      });
    if (files.length === 0) return;
    if (folder) void uploadFolder(location, files);
    else void uploadFiles(location, files);
  };

  const copyLink = async (node: DocNode) => {
    const link = docLink(window.location.origin, orgId, spaceId, node.id);
    try {
      await navigator.clipboard.writeText(link);
      toast(t("docs.row.linkCopied"));
    } catch {
      toast(t("docs.row.copyFailed"), { tone: "error" });
    }
  };

  const value: DocActions = {
    expanded,
    toggleExpanded(key) {
      setExpanded((prev) => {
        const next = new Set(prev);
        if (next.has(key)) next.delete(key);
        else next.add(key);
        return next;
      });
    },
    createNode,
    requestUpload(location, mode) {
      target.current = location;
      (mode === "folder" ? folderInput : fileInput).current?.click();
    },
    uploading,
    newSet: () => setDialog({ kind: "set" }),
    editSet: (set) => setDialog({ kind: "set", set }),
    deleteSet: (set) => setDialog({ kind: "setDelete", set }),
    rename: (node) => setDialog({ kind: "rename", node }),
    remove: (node) => setDialog({ kind: "delete", node }),
    move: (node) => setDialog({ kind: "move", node }),
    search: () => setDialog({ kind: "search" }),
    copyLink: (node) => void copyLink(node),
    addToTask: (node) =>
      openCreate({
        spaceId,
        description: docLink(window.location.origin, orgId, spaceId, node.id),
      }),
    openNode: (nodeId) => navigate(docsPath(orgId, spaceId, nodeId)),
  };

  const confirmDeleteNode = async (node: DocNode) => {
    try {
      await deleteNode.mutateAsync(node);
      setDialog(null);
      // 删除的是当前打开的节点（或其祖先）时，回到它的父级
      if (isInside(current, node.id))
        navigate(docsPath(orgId, spaceId, node.parent_id ?? undefined));
    } catch (error) {
      toast(docErrorMessage(t, error, "docs.row.deleteFailed"), {
        tone: "error",
      });
    }
  };

  const confirmDeleteSet = async (set: DocSet) => {
    try {
      await deleteSet.mutateAsync(set);
      setDialog(null);
      if (current?.container.set_id === set.id)
        navigate(docsPath(orgId, spaceId));
    } catch (error) {
      toast(docErrorMessage(t, error, "docs.set.deleteFailed"), {
        tone: "error",
      });
    }
  };

  return (
    <DocActionsContext.Provider value={value}>
      {children}
      {/* 上传用的隐藏选择框；文件夹上传开启目录选择 */}
      <input
        ref={fileInput}
        type="file"
        multiple
        className="sr-only"
        tabIndex={-1}
        aria-hidden
        onChange={(event) => onFilesPicked(event, false)}
      />
      <input
        ref={(node) => {
          folderInput.current = node;
          node?.setAttribute("webkitdirectory", "");
        }}
        type="file"
        multiple
        className="sr-only"
        tabIndex={-1}
        aria-hidden
        onChange={(event) => onFilesPicked(event, true)}
      />
      <DocSetDialog
        orgId={orgId}
        spaceId={spaceId}
        set={dialog?.kind === "set" ? dialog.set : undefined}
        open={dialog?.kind === "set"}
        onClose={() => setDialog(null)}
        onCreated={(created) => expandKeys([`set:${created.id}`])}
      />
      <ConfirmDialog
        open={dialog?.kind === "setDelete"}
        danger
        loading={deleteSet.isPending}
        title={t("docs.set.deleteTitle")}
        description={
          dialog?.kind === "setDelete"
            ? t("docs.set.deleteDesc", {
                name: dialog.set.name,
              })
            : ""
        }
        confirmLabel={t("docs.set.delete")}
        cancelLabel={t("docs.set.cancel")}
        onCancel={() => setDialog(null)}
        onConfirm={() => {
          if (dialog?.kind === "setDelete") void confirmDeleteSet(dialog.set);
        }}
      />
      <ConfirmDialog
        open={dialog?.kind === "delete"}
        danger
        loading={deleteNode.isPending}
        title={t("docs.row.deleteTitle")}
        description={
          dialog?.kind === "delete"
            ? t("docs.row.deleteDesc", {
                name: dialog.node.title || t("docs.untitled"),
              })
            : ""
        }
        confirmLabel={t("docs.row.delete")}
        cancelLabel={t("docs.set.cancel")}
        onCancel={() => setDialog(null)}
        onConfirm={() => {
          if (dialog?.kind === "delete") void confirmDeleteNode(dialog.node);
        }}
      />
      {dialog?.kind === "rename" && (
        <RenameDocDialog
          orgId={orgId}
          spaceId={spaceId}
          node={dialog.node}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "move" && (
        <MoveDocDialog
          orgId={orgId}
          spaceId={spaceId}
          node={dialog.node}
          onClose={() => setDialog(null)}
        />
      )}
      <SearchDocsDialog
        orgId={orgId}
        spaceId={spaceId}
        open={dialog?.kind === "search"}
        onClose={() => setDialog(null)}
        onOpen={(nodeId) => {
          setDialog(null);
          navigate(docsPath(orgId, spaceId, nodeId));
        }}
      />
    </DocActionsContext.Provider>
  );
}
