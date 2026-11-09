/** 支持的界面语言。新增语言：在这里加一项，并在 messages/ 下补一份完整词典。 */
export const locales = [
  { value: "zh-CN", label: "简体中文" },
  { value: "en-US", label: "English" },
] as const;

export type Locale = (typeof locales)[number]["value"];

export const DEFAULT_LOCALE: Locale = "zh-CN";
export const LOCALE_STORAGE_KEY = "openfox:locale";

export function isLocale(value: unknown): value is Locale {
  return locales.some((item) => item.value === value);
}

/** 把浏览器语言（如 zh-Hans-CN、en-GB）映射到支持的语言；无法匹配时返回 null。 */
export function matchLocale(tag: string): Locale | null {
  const lower = tag.toLowerCase();
  if (lower.startsWith("zh")) return "zh-CN";
  if (lower.startsWith("en")) return "en-US";
  return null;
}

/** 初始语言：本机保存的选择 → 浏览器语言 → 默认简体中文。 */
export function detectLocale(): Locale {
  try {
    const stored = globalThis.localStorage?.getItem(LOCALE_STORAGE_KEY);
    if (isLocale(stored)) return stored;
  } catch {
    // 隐私模式下 localStorage 不可用，继续按浏览器语言判断
  }
  const tags =
    globalThis.navigator?.languages ??
    (globalThis.navigator?.language ? [globalThis.navigator.language] : []);
  for (const tag of tags) {
    const matched = matchLocale(tag);
    if (matched) return matched;
  }
  return DEFAULT_LOCALE;
}

export function storeLocale(locale: Locale): void {
  try {
    globalThis.localStorage?.setItem(LOCALE_STORAGE_KEY, locale);
  } catch {
    // 只影响下次打开时的默认语言
  }
}
