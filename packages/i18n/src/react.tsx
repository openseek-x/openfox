import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import {
  DEFAULT_LOCALE,
  detectLocale,
  storeLocale,
  type Locale,
} from "./locales";
import { createTranslator, type Translate } from "./translate";

export interface I18nValue {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: Translate;
  /** 相对时间，如「3 分钟前」「in 2 days」 */
  formatRelative: (date: Date | string, now?: Date) => string;
  formatDate: (
    date: Date | string,
    options?: Intl.DateTimeFormatOptions,
  ) => string;
}

const UNITS: Array<[Intl.RelativeTimeFormatUnit, number]> = [
  ["year", 365 * 24 * 3600],
  ["month", 30 * 24 * 3600],
  ["week", 7 * 24 * 3600],
  ["day", 24 * 3600],
  ["hour", 3600],
  ["minute", 60],
  ["second", 1],
];

function buildValue(
  locale: Locale,
  setLocale: (locale: Locale) => void,
): I18nValue {
  const relative = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  return {
    locale,
    setLocale,
    t: createTranslator(locale),
    formatRelative: (date, now = new Date()) => {
      const diff = Math.round(
        (new Date(date).getTime() - now.getTime()) / 1000,
      );
      for (const [unit, seconds] of UNITS) {
        if (Math.abs(diff) >= seconds || unit === "second")
          return relative.format(Math.trunc(diff / seconds), unit);
      }
      return relative.format(0, "second");
    },
    formatDate: (date, options) =>
      new Intl.DateTimeFormat(
        locale,
        options ?? { dateStyle: "medium" },
      ).format(new Date(date)),
  };
}

// 没有 Provider 时（组件单测、独立使用 ui 组件）按默认语言渲染，不报错
const I18nContext = createContext<I18nValue>(
  buildValue(DEFAULT_LOCALE, () => undefined),
);

/** 提供当前语言；切换后写入本机并同步 <html lang>。 */
export function I18nProvider({
  children,
  initialLocale,
}: {
  children: ReactNode;
  initialLocale?: Locale;
}) {
  const [locale, setLocaleState] = useState<Locale>(
    () => initialLocale ?? detectLocale(),
  );
  useEffect(() => {
    if (typeof document !== "undefined") document.documentElement.lang = locale;
  }, [locale]);
  const setLocale = useCallback((next: Locale) => {
    storeLocale(next);
    setLocaleState(next);
  }, []);
  const value = useMemo(
    () => buildValue(locale, setLocale),
    [locale, setLocale],
  );
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nValue {
  return useContext(I18nContext);
}

export function useT(): Translate {
  return useContext(I18nContext).t;
}
