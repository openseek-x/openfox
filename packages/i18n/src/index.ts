export {
  DEFAULT_LOCALE,
  LOCALE_STORAGE_KEY,
  detectLocale,
  isLocale,
  locales,
  matchLocale,
  storeLocale,
  type Locale,
} from "./locales";
export {
  createTranslator,
  dictionaries,
  interpolate,
  type MessageKey,
  type MessageParams,
  type Messages,
  type Translate,
} from "./translate";
export { I18nProvider, useI18n, useT, type I18nValue } from "./react";
