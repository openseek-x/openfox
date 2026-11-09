import type { Locale } from "./locales";
import { enUS } from "./messages/en-US";
import { zhCN } from "./messages/zh-CN";

type DeepString<T> = {
  [K in keyof T]: T[K] extends string ? string : DeepString<T[K]>;
};

/** 词典结构以简体中文为准，叶子统一为 string，供其他语言实现。 */
export type Messages = DeepString<typeof zhCN>;

type Leaves<T, P extends string = ""> = {
  [K in keyof T & string]: T[K] extends string
    ? `${P}${K}`
    : Leaves<T[K], `${P}${K}.`>;
}[keyof T & string];

/** 所有合法的文案键，例如 "auth.login"。 */
export type MessageKey = Leaves<Messages>;
export type MessageParams = Record<string, string | number>;
export type Translate = (key: MessageKey, params?: MessageParams) => string;

export const dictionaries: Record<Locale, Messages> = {
  "zh-CN": zhCN,
  "en-US": enUS,
};

function lookup(messages: Messages, key: string): string | undefined {
  let node: unknown = messages;
  for (const part of key.split(".")) {
    if (typeof node !== "object" || node === null) return undefined;
    node = (node as Record<string, unknown>)[part];
  }
  return typeof node === "string" ? node : undefined;
}

/** 替换 {name} 占位符；缺少参数时保留原占位符，便于发现遗漏。 */
export function interpolate(template: string, params?: MessageParams): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (match, name: string) =>
    name in params ? String(params[name]) : match,
  );
}

/** 创建翻译函数：当前语言缺键时回退到简体中文，仍缺时返回键本身。 */
export function createTranslator(locale: Locale): Translate {
  const messages = dictionaries[locale];
  return (key, params) =>
    interpolate(lookup(messages, key) ?? lookup(zhCN, key) ?? key, params);
}
