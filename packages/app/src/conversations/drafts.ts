// 输入框草稿按会话隔离。
// 草稿放在模块级内存里，按会话 ID 作键；还没有 ID 的新会话用 newDraftKey 生成的键。
// 只在本次页面生命周期内有效，不写 localStorage；附件只保留 File 对象引用。

import type { AssistantAction } from "./assistantActions";

/** 已选的提及（正文里 @名字 对应的用户 / Agent） */
export type DraftMention = { kind: "user" | "agent"; id: string; name: string };

export interface ComposerDraft {
  assistantAction?: AssistantAction;
  body: string;
  picked: DraftMention[];
  files: File[];
}

/** 最多保留的草稿数；超出时丢弃最早写入的，避免长时间使用后附件对象一直占内存 */
export const MAX_DRAFTS = 100;

const drafts = new Map<string, ComposerDraft>();

/** 新会话（还没有 ID）的草稿键：按 Agent（小队按队长）区分，避免不同 Agent 的新会话互相串草稿 */
export function newDraftKey(agentId: string, crew: boolean): string {
  return `new:${crew ? "crew" : "agent"}:${agentId}`;
}

export function readDraft(key: string): ComposerDraft | undefined {
  return drafts.get(key);
}

/** 保存草稿；正文、提及、附件都为空时删除该键 */
export function saveDraft(key: string, draft: ComposerDraft): void {
  drafts.delete(key);
  if (
    !draft.assistantAction &&
    draft.body === "" &&
    draft.picked.length === 0 &&
    draft.files.length === 0
  )
    return;
  drafts.set(key, draft);
  while (drafts.size > MAX_DRAFTS) {
    const oldest = drafts.keys().next().value;
    if (oldest === undefined) break;
    drafts.delete(oldest);
  }
}

export function clearDraft(key: string): void {
  drafts.delete(key);
}

// 从输入框外写草稿（快捷指令「插入到输入框」）。
// 输入框只在挂载和换键时读草稿，所以外部写入要通知正在显示该键的输入框刷新；
// 还没挂载的输入框在挂载时读到新草稿，并按「聚焦请求」把焦点放进输入框。

/** 与输入框 maxLength 一致 */
export const MAX_DRAFT_BODY = 65536;

const listeners = new Map<string, Set<() => void>>();
const focusRequests = new Set<string>();

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

/** 测试用：清空全部草稿 */
export function resetDrafts(): void {
  drafts.clear();
  focusRequests.clear();
}
