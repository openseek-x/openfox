import type { DocContainer, DocNode, DocNodeDetail } from "@openfox/api";

// 文档库纯函数：容器与位置、名称长度、拖拽落点、移动禁用、上传目录规划。

/** 文档集名称上限（字符数，按码点计） */
export const DOC_SET_NAME_MAX = 64;
/** 节点标题上限（服务端 maxLength） */
export const DOC_TITLE_MAX = 200;
/** 二级导航宽度：默认 300，可拖动范围 240–480 */
export const DOC_NAV_WIDTH = { default: 300, min: 240, max: 480 } as const;
/** 文件节点上传的单文件上限（与服务端一致，超过时不发请求） */
export const DOC_FILE_MAX_BYTES = 20 * 1024 * 1024;
/** 表格与正文自动保存的防抖时间 */
export const DOC_AUTOSAVE_MS = 1500;

export const PERSONAL_CONTAINER: DocContainer = { kind: "personal" };
export const setContainer = (setId: string): DocContainer => ({
  kind: "set",
  set_id: setId,
});

/** 容器的稳定字符串，用于查询键与分组。 */
export function containerKey(container: DocContainer): string {
  return container.kind === "set"
    ? `set:${container.set_id ?? ""}`
    : "personal";
}

/** 所在容器的位置：folder 之下的内容以该 folder 为父级，其他节点以其父节点为父级。 */
export interface DocLocation {
  container: DocContainer;
  parentId: string | null;
}

export const rootLocation = (): DocLocation => ({
  container: PERSONAL_CONTAINER,
  parentId: null,
});

export function locationOf(node: DocNode | DocNodeDetail): DocLocation {
  return node.kind === "folder"
    ? { container: node.container, parentId: node.id }
    : { container: node.container, parentId: node.parent_id };
}

/** 名称按字符数计（码点）：「0/64」计数与超长判断都用它。 */
export function nameLength(name: string): number {
  return Array.from(name).length;
}

/** 文档集名称是否合法：去掉首尾空白后非空，且不超过上限。 */
export function isDocSetNameValid(name: string): boolean {
  return name.trim() !== "" && nameLength(name) <= DOC_SET_NAME_MAX;
}

export function clampNavWidth(width: number): number {
  if (!Number.isFinite(width)) return DOC_NAV_WIDTH.default;
  return Math.min(
    DOC_NAV_WIDTH.max,
    Math.max(DOC_NAV_WIDTH.min, Math.round(width)),
  );
}

/**
 * 拖拽落点：上下各 30% 为前后插入线，中间为放入。
 * 只有能放入的目标（文件夹、文档集、我的文档）才有中间区域；文档的中间区域无效。
 */
export type DropZone = "before" | "after" | "inside";

export function dropZone(
  offsetY: number,
  height: number,
  acceptsInside: boolean,
): DropZone | null {
  if (height <= 0) return null;
  const ratio = offsetY / height;
  if (ratio < 0.3) return "before";
  if (ratio > 0.7) return "after";
  return acceptsInside ? "inside" : null;
}

/**
 * 移动目标是否禁用：自身，以及沿当前展开路径能追溯到被移动对象的子孙。
 * path 为候选节点到容器根之间的祖先 id（不含候选自身）。
 */
export function isMoveTargetBlocked(
  movingId: string,
  candidateId: string,
  path: readonly string[],
): boolean {
  return candidateId === movingId || path.includes(movingId);
}

/**
 * 同级前后插入时的 before_id：插在 target 之前就是 target 自身；
 * 插在 target 之后就是 target 之后的第一个非被拖拽节点，没有则为 null（放到最后）。
 */
export function beforeIdFor(
  siblings: readonly Pick<DocNode, "id">[],
  targetId: string,
  zone: "before" | "after",
  draggedId: string,
): string | null {
  if (zone === "before") return targetId;
  const index = siblings.findIndex((node) => node.id === targetId);
  if (index < 0) return null;
  const next = siblings.slice(index + 1).find((node) => node.id !== draggedId);
  return next?.id ?? null;
}

export interface UploadDirectory {
  /** 相对于本次选择的目录路径，例如 a/b */
  path: string;
  /** 目录名 */
  name: string;
  /** 父目录路径；本次选择的根目录的父为空串 */
  parent: string;
}

export interface UploadFilePlan {
  file: File;
  /** 所在目录路径（与 UploadDirectory.path 对应），根文件为空串 */
  dir: string;
}

/**
 * 把 webkitdirectory 选中的文件按相对路径规划为目录与文件：
 * 目录按层级排序（父在前），保证逐个创建时父目录已存在。
 */
export function planFolderUpload(files: readonly File[]): {
  directories: UploadDirectory[];
  files: UploadFilePlan[];
} {
  const dirs = new Map<string, UploadDirectory>();
  const plans: UploadFilePlan[] = [];
  for (const file of files) {
    const rel = (file as File & { webkitRelativePath?: string })
      .webkitRelativePath;
    const parts = (rel || file.name).split("/").filter(Boolean);
    const dirParts = parts.slice(0, -1);
    for (let i = 0; i < dirParts.length; i++) {
      const path = dirParts.slice(0, i + 1).join("/");
      if (!dirs.has(path))
        dirs.set(path, {
          path,
          name: dirParts[i]!,
          parent: dirParts.slice(0, i).join("/"),
        });
    }
    plans.push({ file, dir: dirParts.join("/") });
  }
  const directories = [...dirs.values()].sort(
    (a, b) => a.path.split("/").length - b.path.split("/").length,
  );
  return { directories, files: plans };
}

/** 文档链接：复制链接、添加到任务的描述里使用。 */
export function docLink(
  origin: string,
  orgId: string,
  spaceId: string,
  nodeId: string,
): string {
  return `${origin}/o/${encodeURIComponent(orgId)}/spaces/${encodeURIComponent(spaceId)}/docs/${encodeURIComponent(nodeId)}`;
}

/** 文档库路由：空间下的我的文档根（无 nodeId）或某个节点。 */
export function docsPath(
  orgId: string,
  spaceId: string,
  nodeId?: string,
): string {
  const base = `/o/${encodeURIComponent(orgId)}/spaces/${encodeURIComponent(spaceId)}/docs`;
  return nodeId ? `${base}/${encodeURIComponent(nodeId)}` : base;
}
