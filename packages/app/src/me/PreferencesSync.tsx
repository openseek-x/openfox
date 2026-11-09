import { useEffect, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { useI18n } from "@openfox/i18n";

import { PREFERENCES_MUTATION_KEY, useMyPreferences } from "./queries";
import { keys } from "../context";
import {
  applyTheme,
  readCachedTheme,
  useResolvedTheme,
  writeCachedTheme,
} from "./theme";

// 登录后把账号偏好应用到本机：主题（并缓存）、语言与密度。挂在登录守卫内，只在已登录时运行。

export function PreferencesSync() {
  const { locale, setLocale } = useI18n();
  const prefs = useMyPreferences();
  const queryClient = useQueryClient();
  const density = prefs.data?.density === "compact" ? "compact" : "comfortable";
  const choice = prefs.data?.theme ?? readCachedTheme();
  const resolved = useResolvedTheme(choice);
  const localeSynced = useRef(false);

  useEffect(() => applyTheme(resolved, choice), [resolved, choice]);

  useEffect(() => {
    document.documentElement.dataset.density = density;
    return () => {
      delete document.documentElement.dataset.density;
    };
  }, [density]);

  useEffect(() => {
    const refresh = () => {
      if (
        document.visibilityState === "visible" &&
        queryClient.isMutating({ mutationKey: PREFERENCES_MUTATION_KEY }) === 0
      )
        void queryClient.invalidateQueries({ queryKey: keys.preferences });
    };
    window.addEventListener("focus", refresh);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      window.removeEventListener("focus", refresh);
      document.removeEventListener("visibilitychange", refresh);
    };
  }, [queryClient]);

  useEffect(() => {
    if (prefs.data) writeCachedTheme(prefs.data.theme);
  }, [prefs.data]);

  // 账号语言与本机不同时切换一次；之后语言以用户在本机的选择为准，避免覆盖刚切换的语言
  useEffect(() => {
    if (localeSynced.current || !prefs.data) return;
    localeSynced.current = true;
    const accountLocale = prefs.data.locale;
    if (accountLocale && accountLocale !== locale) setLocale(accountLocale);
  }, [prefs.data, locale, setLocale]);

  return null;
}
