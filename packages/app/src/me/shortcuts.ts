import type { CommandShortcuts, UserPreferences } from "@openfox/api";

// 键盘快捷键：发送键判断、应用命令的默认按键、按键格式化与录制。
// 保存格式为 mod[+shift][+alt]+键，mod 表示 macOS 的 ⌘ / 其他平台的 Ctrl。

export type SendShortcut = UserPreferences["send_shortcut"];
export type CommandId = keyof CommandShortcuts;

export const COMMAND_IDS: readonly CommandId[] = [
  "command_menu",
  "move_to_space",
  "find_in_view",
];

export const DEFAULT_SHORTCUTS: Record<CommandId, string> = {
  command_menu: "mod+k",
  move_to_space: "mod+shift+m",
  find_in_view: "mod+f",
};

/** 「这次按键是否发送」所需的最小字段（React 与原生 KeyboardEvent 都满足）。 */
export interface SendKeyEvent {
  key: string;
  shiftKey: boolean;
  nativeEvent: { isComposing: boolean };
}

/** 输入法组合中（isComposing）按下的 Enter 永远不发送，它用来确认候选词。 */
export function isSendKey(event: SendKeyEvent, mode: SendShortcut): boolean {
  if (event.key !== "Enter" || event.nativeEvent.isComposing) return false;
  return mode === "enter" ? !event.shiftKey : event.shiftKey;
}

export function isMacPlatform(): boolean {
  return /Mac|iPhone|iPad/.test(globalThis.navigator?.platform ?? "");
}

/** 命令当前生效的按键：有自定义用自定义，否则用默认。 */
export function effectiveShortcut(
  custom: CommandShortcuts,
  id: CommandId,
): string {
  return custom[id] ?? DEFAULT_SHORTCUTS[id];
}

/**
 * 写入一个命令的按键，返回整个 command_shortcuts（接口整体替换）。
 * 与默认相同或传 null（重置）时去掉该键，保证只存自定义过的命令。
 */
export function withShortcut(
  custom: CommandShortcuts,
  id: CommandId,
  value: string | null,
): CommandShortcuts {
  const next: CommandShortcuts = { ...custom };
  if (value === null || value === DEFAULT_SHORTCUTS[id]) delete next[id];
  else next[id] = value;
  return next;
}

/** 把保存格式拆成键帽文字：macOS 用 ⌘ ⇧ ⌥，其他平台用 Ctrl Shift Alt。 */
export function shortcutKeycaps(value: string, mac: boolean): string[] {
  const parts = value.split("+");
  const key = parts[parts.length - 1]!.toUpperCase();
  const mods = parts.slice(0, -1).map((part) => {
    if (part === "mod") return mac ? "⌘" : "Ctrl";
    if (part === "shift") return mac ? "⇧" : "Shift";
    return mac ? "⌥" : "Alt";
  });
  return [...mods, key];
}

const MODIFIER_KEYS = new Set(["Meta", "Control", "Shift", "Alt"]);

/** 录制时只认字母和数字键（按 code 取，不受 Option 等修饰键改变字符的影响）。 */
function keyOf(code: string): string | null {
  const letter = /^Key([A-Z])$/.exec(code);
  if (letter) return letter[1]!.toLowerCase();
  const digit = /^Digit([0-9])$/.exec(code);
  if (digit) return digit[1]!;
  return null;
}

export type RecordResult =
  | { ok: true; value: string }
  | { ok: false; reason: "needModifier" | "unsupported" };

/** 录制一次按键：先要求 ⌘ 或 Ctrl，再检查主键是否支持。单独的修饰键返回 null，继续等待。 */
export function recordShortcut(
  event: Pick<
    KeyboardEvent,
    "key" | "code" | "metaKey" | "ctrlKey" | "shiftKey" | "altKey"
  >,
): RecordResult | null {
  if (MODIFIER_KEYS.has(event.key)) return null;
  if (!(event.metaKey || event.ctrlKey))
    return { ok: false, reason: "needModifier" };
  const key = keyOf(event.code);
  if (!key) return { ok: false, reason: "unsupported" };
  const mods = [
    "mod",
    ...(event.shiftKey ? ["shift"] : []),
    ...(event.altKey ? ["alt"] : []),
  ];
  return { ok: true, value: [...mods, key].join("+") };
}
