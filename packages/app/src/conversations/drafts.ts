// 阶段 10a · 输入框草稿按会话隔离（验收台账 R12）。
// WorkspacePersistence 先按账号/组织恢复 IndexedDB，再挂载输入框；附件用结构化克隆保存。

import type {
  BrowserContext,
  BrowserComment,
  FileCommentContext,
  FileComment,
} from "@openfox/api";
import { useSyncExternalStore } from "react";
import type { AssistantAction } from "./assistantActions";
import {
  scopedMap,
  workspaceStore,
  resetWorkspaceMemory,
} from "./workspaceStorage";

/** 已选的提及（正文里 @名字 对应的用户 / Agent） */
export type DraftMention = { kind: "user" | "agent"; id: string; name: string };

export type DraftReply = {
  id: string;
  authorKind: string;
  body: string;
  truncated: boolean;
};

export type DraftFileContext = FileCommentContext & { name: string };

export type DraftBrowserContext = Omit<BrowserContext, "comments"> & {
  comments: (BrowserComment & { screenshot?: File })[];
};

export interface ComposerDraft {
  bodyVersion?: string;
  filesVersion?: string;
  browserContexts?: DraftBrowserContext[];
  fileContexts?: DraftFileContext[];
  reply?: DraftReply;
  assistantAction?: AssistantAction;
  body: string;
  picked: DraftMention[];
  files: File[];
}

/** 最多保留的草稿数；超出时丢弃最早写入的，避免长时间使用后附件对象一直占内存 */
export const MAX_DRAFTS = 100;

const drafts = scopedMap<ComposerDraft>("draft:");

/** 新会话（还没有 ID）的草稿键：按 Agent（小队按队长）区分，避免不同 Agent 的新会话互相串草稿 */
export function newDraftKey(agentId: string, crew: boolean): string {
  return `new:${crew ? "crew" : "agent"}:${agentId}`;
}

export function readDraft(key: string): ComposerDraft | undefined {
  return drafts.get(key);
}

/** 保存草稿；正文、提及、附件都为空时删除该键 */
export function saveDraft(key: string, draft: ComposerDraft): void {
  const previous = drafts.get(key);
  if (
    previous &&
    previous.body === draft.body &&
    previous.files === draft.files &&
    previous.picked === draft.picked &&
    previous.reply === draft.reply &&
    previous.assistantAction === draft.assistantAction &&
    previous.fileContexts === draft.fileContexts &&
    previous.browserContexts === draft.browserContexts
  )
    return;
  draft = {
    ...draft,
    bodyVersion:
      previous?.body === draft.body
        ? previous.bodyVersion
        : crypto.randomUUID(),
    filesVersion:
      previous &&
      previous.files.length === draft.files.length &&
      previous.files.every((file, i) => file === draft.files[i])
        ? previous.filesVersion
        : crypto.randomUUID(),
  };
  if (
    !draft.assistantAction &&
    !draft.reply &&
    draft.body === "" &&
    draft.picked.length === 0 &&
    draft.files.length === 0 &&
    !draft.browserContexts?.length &&
    !draft.fileContexts?.length
  ) {
    drafts.delete(key);
    return;
  }
  drafts.set(key, draft);
  while (!workspaceStore().durable && drafts.size > MAX_DRAFTS) {
    const oldest = drafts.keys().next().value;
    if (oldest === undefined) break;
    drafts.delete(oldest);
  }
}

export function clearDraft(key: string): void {
  drafts.delete(key);
}

// 阶段 10b · 从输入框外写草稿（快捷指令「插入到输入框」）。
// 输入框只在挂载和换键时读草稿，所以外部写入要通知正在显示该键的输入框刷新；
// 还没挂载的输入框在挂载时读到新草稿，并按「聚焦请求」把焦点放进输入框。

/** 与输入框 maxLength 一致 */
export const MAX_DRAFT_BODY = 65536;

const listeners = new Map<string, Set<() => void>>();
const focusRequests = new Set<string>();

/** Quoting changes only the reference, preserving text, mentions and files. */
export function quoteDraft(key: string, reply: DraftReply): void {
  const current = drafts.get(key) ?? { body: "", picked: [], files: [] };
  saveDraft(key, { ...current, reply });
  focusRequests.add(key);
  for (const listener of [...(listeners.get(key) ?? [])]) listener();
}

/** 订阅某个键的外部写入；返回取消订阅函数 */
export function subscribeDraft(key: string, listener: () => void): () => void {
  let set = listeners.get(key);
  if (!set) {
    set = new Set();
    listeners.set(key, set);
  }
  set.add(listener);
  return () => {
    set.delete(listener);
    if (set.size === 0) listeners.delete(key);
  };
}
export function notifyDraft(key: string) {
  for (const listener of [...(listeners.get(key) ?? [])]) listener();
}

/** 取走该键的聚焦请求（只生效一次） */
export function takeDraftFocus(key: string): boolean {
  return focusRequests.delete(key);
}

/**
 * 把一段文字追加到草稿末尾：已有正文时空一行再追加，不覆盖；正文和附件保持不变。
 * mention 不为空且正文里还没有「@名字」时，在追加的文字前加上 @名字，并记入已选提及（发送时生效）。
 * 写入后请求聚焦，并通知正在显示该键的输入框。
 */
export function appendToDraft(
  key: string,
  text: string,
  mention?: DraftMention,
): void {
  const current = drafts.get(key) ?? { body: "", picked: [], files: [] };
  const lead =
    mention && !current.body.includes(`@${mention.name}`)
      ? `@${mention.name} `
      : "";
  const head = current.body.replace(/\s+$/, "");
  const body = (head === "" ? lead + text : `${head}\n\n${lead}${text}`).slice(
    0,
    MAX_DRAFT_BODY,
  );
  const picked =
    mention &&
    !current.picked.some(
      (item) => item.kind === mention.kind && item.id === mention.id,
    )
      ? [...current.picked, mention]
      : current.picked;
  saveDraft(key, { ...current, body, picked });
  focusRequests.add(key);
  for (const listener of [...(listeners.get(key) ?? [])]) listener();
}

/** 切换账号时清空全部草稿并清除已挂载输入框。 */
export async function resetDrafts(erase = true): Promise<void> {
  const old = workspaceStore();
  resetWorkspaceMemory();
  focusRequests.clear();
  for (const set of [...listeners.values()])
    for (const listener of [...set]) listener();
  if (erase) await old.erase();
  else await old.close();
}

const emptyFileContexts: DraftFileContext[] = [];
const emptyBrowserContexts: DraftBrowserContext[] = [];
export function useDraftFileContexts(key: string): DraftFileContext[] {
  return useSyncExternalStore(
    (listener) => subscribeDraft(key, listener),
    () => readDraft(key)?.fileContexts ?? emptyFileContexts,
  );
}
export function useDraftBrowserContexts(key: string): DraftBrowserContext[] {
  return useSyncExternalStore(
    (listener) => subscribeDraft(key, listener),
    () => readDraft(key)?.browserContexts ?? emptyBrowserContexts,
  );
}
function changeFileContexts(
  key: string,
  fileContexts: DraftFileContext[],
  focus = false,
) {
  const current = readDraft(key) ?? { body: "", picked: [], files: [] };
  saveDraft(key, { ...current, fileContexts });
  if (focus) focusRequests.add(key);
  for (const listener of [...(listeners.get(key) ?? [])]) listener();
}
export function addFileComment(
  key: string,
  file: Omit<DraftFileContext, "id" | "comments">,
  comment: FileComment,
): boolean {
  const contexts = readDraft(key)?.fileContexts ?? [];
  const existing = contexts.find(
    (c) => c.artifact_id === file.artifact_id && c.node_id === file.node_id,
  );
  if (
    contexts.reduce((n, c) => n + c.comments.length, 0) >= 20 ||
    (!existing && contexts.length >= 10) ||
    (existing && existing.sha256 !== file.sha256)
  )
    return false;
  const next = existing
    ? contexts.map((c) =>
        c === existing ? { ...c, comments: [...c.comments, comment] } : c,
      )
    : [...contexts, { ...file, id: crypto.randomUUID(), comments: [comment] }];
  if (new TextEncoder().encode(JSON.stringify(next)).length > 256 * 1024)
    return false;
  changeFileContexts(key, next, true);
  return true;
}
export function removeFileComment(
  key: string,
  contextID: string,
  commentID?: string,
) {
  const next = (readDraft(key)?.fileContexts ?? []).flatMap((c) => {
    if (c.id !== contextID) return [c];
    const comments = commentID
      ? c.comments.filter((a) => a.thread_id !== commentID)
      : [];
    return comments.length ? [{ ...c, comments }] : [];
  });
  changeFileContexts(key, next);
}
/** 就地更新待发送文件批注正文（保持锚点不变并校验 8 KiB/256 KiB 体积上限）。 */
export function updateFileComment(
  key: string,
  contextID: string,
  commentID: string,
  body: string,
): boolean {
  const trimmed = body.trim();
  if (!trimmed || new TextEncoder().encode(trimmed).length > 8192) return false;
  const contexts = readDraft(key)?.fileContexts ?? [];
  let updated = false;
  const next = contexts.map((c) => {
    if (c.id !== contextID) return c;
    return {
      ...c,
      comments: c.comments.map((a) => {
        if (a.thread_id !== commentID) return a;
        updated = true;
        return { ...a, body: trimmed };
      }),
    };
  });
  if (!updated) return false;
  if (new TextEncoder().encode(JSON.stringify(next)).length > 256 * 1024)
    return false;
  changeFileContexts(key, next);
  return true;
}
// The body remains readable in chat; full anchors travel as structured context.
export function fileFeedbackBody(
  body: string,
  contexts: DraftFileContext[],
  defaultPrompt: string,
): string {
  if (!contexts.length) return body;
  return [
    body.trim() || defaultPrompt,
    ...contexts.map(
      (c) =>
        `${c.name}\n${c.comments.map((a, i) => `${i + 1}. ${a.body}\n   ${JSON.stringify(a.anchor.quote.slice(0, 160))}`).join("\n")}`,
    ),
  ].join("\n\n");
}

export function addBrowserComment(
  key: string,
  source: Omit<BrowserContext, "id" | "comments">,
  comment: DraftBrowserContext["comments"][number],
): boolean {
  const current = readDraft(key) ?? { body: "", picked: [], files: [] };
  const contexts = current.browserContexts ?? [];
  const existing = contexts.find(
    (c) =>
      c.source_url === source.source_url &&
      c.comments[0]?.anchor.live.tab_id === comment.anchor.live.tab_id &&
      c.comments[0]?.anchor.live.document_generation ===
        comment.anchor.live.document_generation,
  );
  if (
    contexts.reduce((n, c) => n + c.comments.length, 0) >= 20 ||
    (!existing && contexts.length >= 10)
  )
    return false;
  const browserContexts = existing
    ? contexts.map((c) =>
        c === existing ? { ...c, comments: [...c.comments, comment] } : c,
      )
    : [
        ...contexts,
        { ...source, id: crypto.randomUUID(), comments: [comment] },
      ];
  if (
    new TextEncoder().encode(JSON.stringify(browserContexts)).length >
      256 * 1024 ||
    current.files.length +
      browserContexts.flatMap((c) => c.comments.filter((a) => a.screenshot))
        .length >
      10
  )
    return false;
  saveDraft(key, { ...current, browserContexts });
  focusRequests.add(key);
  for (const listener of [...(listeners.get(key) ?? [])]) listener();
  return true;
}
export function removeBrowserComment(key: string, id: string) {
  const current = readDraft(key);
  if (!current) return;
  const browserContexts = (current.browserContexts ?? []).flatMap((c) => {
    const comments = c.comments.filter((a) => a.id !== id);
    return comments.length ? [{ ...c, comments }] : [];
  });
  saveDraft(key, { ...current, browserContexts });
  for (const listener of [...(listeners.get(key) ?? [])]) listener();
}
/** 就地更新待发送网页批注正文（保持元素/区域锚点与截图不变）。 */
export function updateBrowserComment(
  key: string,
  id: string,
  body: string,
): boolean {
  const trimmed = body.trim();
  if (!trimmed || new TextEncoder().encode(trimmed).length > 8192) return false;
  const current = readDraft(key);
  if (!current?.browserContexts?.length) return false;
  let updated = false;
  const browserContexts = current.browserContexts.map((c) => ({
    ...c,
    comments: c.comments.map((a) => {
      if (a.id !== id) return a;
      updated = true;
      return { ...a, body: trimmed };
    }),
  }));
  if (!updated) return false;
  if (
    new TextEncoder().encode(JSON.stringify(browserContexts)).length >
    256 * 1024
  )
    return false;
  saveDraft(key, { ...current, browserContexts });
  for (const listener of [...(listeners.get(key) ?? [])]) listener();
  return true;
}

const annotationBodies = scopedMap<string>("annotation:");
export const readAnnotationBody = (id: string) =>
  annotationBodies.get(id) ?? "";
export function saveAnnotationBody(id: string, body: string) {
  if (body) annotationBodies.set(id, body);
  else annotationBodies.delete(id);
  if (!workspaceStore().durable && annotationBodies.size > 100)
    annotationBodies.delete(annotationBodies.keys().next().value!);
}
