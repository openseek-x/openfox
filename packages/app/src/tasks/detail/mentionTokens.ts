// 评论提及令牌：输入框里显示 @名字，提交时序列化为
// @[名字](openfox://mention/<kind>/<id>)；与后端 internal/conversations/mentiontext 保持一致。

export type MentionKind = "user" | "agent" | "crew" | "all";

export interface ComposerMention {
  kind: MentionKind;
  /** all 没有 ID */
  id?: string;
  /** 输入框里 @ 后面的文字（用户为邮箱，其余为名字） */
  token: string;
  /** 令牌里的显示名 */
  label: string;
}

export type MentionPayload =
  | { user_id: string }
  | { agent_id: string }
  | { crew_id: string }
  | { all: true };

const TOKEN =
  /@\[([^[\]\r\n]*)\]\(openfox:\/\/mention\/(?:(user|agent|crew)\/([0-9a-fA-F-]{36})|(all))\)/g;

/** 名字里的 [ ] 与换行会破坏令牌，统一去掉 */
export function safeMentionLabel(name: string): string {
  return name.replace(/[[\]\r\n]+/g, " ").trim() || "?";
}

function tokenOf(mention: ComposerMention): string {
  const label = safeMentionLabel(mention.label);
  return mention.kind === "all"
    ? `@[${label}](openfox://mention/all)`
    : `@[${label}](openfox://mention/${mention.kind}/${mention.id})`;
}

export function payloadOf(mention: ComposerMention): MentionPayload {
  switch (mention.kind) {
    case "user":
      return { user_id: mention.id! };
    case "agent":
      return { agent_id: mention.id! };
    case "crew":
      return { crew_id: mention.id! };
    default:
      return { all: true };
  }
}

/**
 * 把输入框文本里仍保留的 @名字 替换成令牌，返回正文与对应的 mentions。
 * 先替换更长的名字，避免前缀互相吞掉；已有令牌里的文字不会被再次替换。
 */
export function serializeMentions(
  text: string,
  mentions: ComposerMention[],
): { body: string; mentions: MentionPayload[] } {
  const kept: ComposerMention[] = [];
  const ordered = [...mentions].sort((a, b) => b.token.length - a.token.length);
  // 先把已有令牌挖出来保护，再对剩余文本替换
  const parts: string[] = [];
  const protectedTokens: string[] = [];
  let last = 0;
  for (const match of text.matchAll(TOKEN)) {
    parts.push(text.slice(last, match.index));
    protectedTokens.push(match[0]);
    last = match.index + match[0].length;
  }
  parts.push(text.slice(last));
  const replaced = parts.map((part) => {
    let current = part;
    for (const mention of ordered) {
      const needle = `@${mention.token}`;
      if (!current.includes(needle)) continue;
      if (!kept.includes(mention)) kept.push(mention);
      current = current.split(needle).join(tokenOf(mention));
    }
    return current;
  });
  let body = "";
  replaced.forEach((part, index) => {
    body += part + (protectedTokens[index] ?? "");
  });
  return { body, mentions: kept.map(payloadOf) };
}

/** 编辑评论时把令牌还原成 @名字，并返回可用于再次序列化的提及表 */
export function deserializeMentions(body: string): {
  text: string;
  mentions: ComposerMention[];
} {
  const mentions: ComposerMention[] = [];
  const text = body.replace(
    TOKEN,
    (_all, label: string, kind?: string, id?: string, all?: string) => {
      const mention: ComposerMention = all
        ? { kind: "all", token: label, label }
        : {
            kind: kind as MentionKind,
            id: id!.toLowerCase(),
            token: label,
            label,
          };
      if (
        !mentions.some(
          (m) =>
            m.kind === mention.kind && m.id === mention.id && m.token === label,
        )
      )
        mentions.push(mention);
      return `@${label}`;
    },
  );
  return { text, mentions };
}

/** 正文里 openfox://mention/ 令牌的解析，供 Markdown 渲染按 ID 高亮 */
export function parseMentionHref(
  href: string,
): { kind: MentionKind; id?: string } | null {
  const match =
    /^openfox:\/\/mention\/(?:(user|agent|crew)\/([0-9a-fA-F-]{36})|(all))$/.exec(
      href,
    );
  if (!match) return null;
  return match[3]
    ? { kind: "all" }
    : { kind: match[1] as MentionKind, id: match[2]!.toLowerCase() };
}
