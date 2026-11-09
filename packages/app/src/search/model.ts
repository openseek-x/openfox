import type { SearchItem, SearchType } from "@openfox/api";

export const PALETTE_CATEGORIES = [
  "task",
  "conversation",
  "project",
  "view",
  "agent",
  "person",
  "space",
  "command",
] as const;
export type PaletteCategory = (typeof PALETTE_CATEGORIES)[number];
export type PaletteFilter = PaletteCategory | "all";

export const SEARCH_TYPES: readonly SearchType[] = [
  "task",
  "conversation",
  "message",
  "project",
  "view",
  "agent",
  "person",
  "space",
];
export const SEARCH_DEBOUNCE_MS = 300;
export const SEARCH_LIMIT_MIXED = 8;
export const SEARCH_LIMIT_SINGLE = 50;
export const SEARCH_MAX_LENGTH = 100;

/**
 * 结果的跳转地址（带 /o/:orgId 前缀）。任务打开所在空间任务页并带 ?task=（详情抽屉，和 taskUrl 同规则）。
 * 缺少所需字段（如没有 space_id 的专项）时返回 null，调用方不应跳转。
 * Agent 打开 Agent 目录上的设置抽屉；调用方跳转前应按 space_id 调 rememberSpace，抽屉就不必再查目录。
 */
export function resultPath(
  orgId: string,
  type: Exclude<SearchType, "conversation" | "message">,
  item: Pick<SearchItem, "id" | "space_id" | "display_id">,
): string | null {
  const base = `/o/${encodeURIComponent(orgId)}`;
  const space = item.space_id ? encodeURIComponent(item.space_id) : null;
  const id = encodeURIComponent(item.id);
  switch (type) {
    case "task":
      return space && item.display_id
        ? `${base}/spaces/${space}/tasks?task=${encodeURIComponent(item.display_id.toUpperCase())}`
        : null;
    case "project":
      return space ? `${base}/spaces/${space}/projects/${id}` : null;
    case "view":
      return space ? `${base}/spaces/${space}/views/${id}` : null;
    case "doc":
      return space ? `${base}/spaces/${space}/docs/${id}` : null;
    case "space":
      return `${base}/spaces/${id}/tasks`;
    case "agent":
      // 9a · 打开 Agent 目录并带设置抽屉（?agentSettings=，抽屉按 space_id 请求详情）；
      // 没有 space_id 时抽屉无法定位，退回成员资料页
      return space
        ? `${base}/agents?agentSettings=${id}`
        : `${base}/members?contact=agent:${id}`;
    case "person":
      return `${base}/members?contact=user:${id}`;
  }
}

interface ParsedShortcut {
  mod: boolean;
  shift: boolean;
  alt: boolean;
  key: string;
}

/** 解析保存格式 mod[+shift][+alt]+键（见 me/shortcuts.ts）。 */
export function parseShortcut(value: string): ParsedShortcut | null {
  const parts = value.toLowerCase().split("+");
  const key = parts.pop();
  if (!key) return null;
  return {
    mod: parts.includes("mod"),
    shift: parts.includes("shift"),
    alt: parts.includes("alt"),
    key,
  };
}

/** 事件的主键：录制时按 code 取字母与数字，这里同样按 code 判断，不受 Option 等修饰键改变字符的影响。 */
function eventKey(event: Pick<KeyboardEvent, "key" | "code">): string {
  const letter = /^Key([A-Z])$/.exec(event.code);
  if (letter) return letter[1]!.toLowerCase();
  const digit = /^Digit([0-9])$/.exec(event.code);
  if (digit) return digit[1]!;
  return event.key.toLowerCase();
}

/** 这次按键是否就是该快捷键；mod 在 macOS 上是 ⌘，其他平台是 Ctrl。 */
export function matchesShortcut(
  event: Pick<
    KeyboardEvent,
    "key" | "code" | "metaKey" | "ctrlKey" | "shiftKey" | "altKey"
  >,
  value: string,
  mac: boolean,
): boolean {
  const spec = parseShortcut(value);
  if (!spec) return false;
  const modPressed = mac ? event.metaKey : event.ctrlKey;
  const otherModPressed = mac ? event.ctrlKey : event.metaKey;
  return (
    modPressed === spec.mod &&
    !otherModPressed &&
    event.shiftKey === spec.shift &&
    event.altKey === spec.alt &&
    eventKey(event) === spec.key
  );
}
