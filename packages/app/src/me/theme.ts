import type { CSSProperties } from "react";
import { useEffect, useState } from "react";

import { localKeys, readLocal, writeLocal } from "../storage";

// 主题与外边框色。主题选择缓存在本机，偏好加载前按缓存决定首屏配色；
// 解析后的深浅写到 <html data-theme>，tokens.css 据此切换颜色变量。

export type ThemeChoice =
  "system" | "light" | "dark" | "magic-blue" | "classic-dark";
export type ResolvedTheme = "light" | "dark";

const DARK_QUERY = "(prefers-color-scheme: dark)";

/** 外边框色表：下标 0 对应色 1（偏好里的 outline_color 从 1 开始，0 = 不设置）。 */
export const OUTLINE_COLORS: ReadonlyArray<{ light: string; dark: string }> = [
  { light: "#ede9fe", dark: "#2e1065" },
  { light: "#fce7f3", dark: "#500724" },
  { light: "#ffe4e6", dark: "#4c0519" },
  { light: "#ffedd5", dark: "#431407" },
  { light: "#fef3c7", dark: "#451a03" },
  { light: "#ecfccb", dark: "#1a2e05" },
  { light: "#ccfbf1", dark: "#042f2e" },
  { light: "#e0f2fe", dark: "#082f49" },
  { light: "#e0e7ff", dark: "#1e1b4b" },
  { light: "#f3e8ff", dark: "#3b0764" },
  { light: "#fae8ff", dark: "#4a044e" },
  { light: "#fce7e7", dark: "#450a0a" },
];

export function isThemeChoice(value: unknown): value is ThemeChoice {
  return (
    value === "system" ||
    value === "light" ||
    value === "dark" ||
    value === "magic-blue" ||
    value === "classic-dark"
  );
}

export function readCachedTheme(): ThemeChoice {
  const value = readLocal(localKeys.theme);
  return isThemeChoice(value) ? value : "system";
}

export function writeCachedTheme(choice: ThemeChoice): void {
  writeLocal(localKeys.theme, choice);
}

function systemPrefersDark(): boolean {
  return globalThis.matchMedia?.(DARK_QUERY)?.matches ?? false;
}

export function resolveTheme(
  choice: ThemeChoice,
  systemDark: boolean,
): ResolvedTheme {
  if (choice === "system") return systemDark ? "dark" : "light";
  return choice === "light" ? "light" : "dark";
}

/** 系统深浅色；跟随系统时由 matchMedia 变化驱动重新渲染。 */
export function useSystemDark(): boolean {
  const [dark, setDark] = useState(systemPrefersDark);
  useEffect(() => {
    const media = globalThis.matchMedia?.(DARK_QUERY);
    if (!media) return;
    const onChange = () => setDark(media.matches);
    media.addEventListener("change", onChange);
    return () => media.removeEventListener("change", onChange);
  }, []);
  return dark;
}

export function useResolvedTheme(choice: ThemeChoice): ResolvedTheme {
  return resolveTheme(choice, useSystemDark());
}

/** 写到 <html data-theme>，tokens.css 的深色变量据此生效。 */
export function applyTheme(theme: ResolvedTheme, choice: ThemeChoice): void {
  document.documentElement.dataset.theme = theme;
  document.documentElement.dataset.palette = choice;
}

/** 应用入口在偏好加载前调用：按本机缓存设置首屏主题（不监听系统变化，那由已登录后的偏好同步负责）。 */
export function applyCachedTheme(): void {
  const choice = readCachedTheme();
  applyTheme(resolveTheme(choice, systemPrefersDark()), choice);
}

/** 外壳底色覆盖（--color-surface-app）；0 或无效值时不覆盖。 */
export function outlineStyle(
  outline: number,
  theme: ResolvedTheme,
): CSSProperties {
  const color = OUTLINE_COLORS[outline - 1];
  if (!color) return {};
  return {
    "--color-surface-app": theme === "dark" ? color.dark : color.light,
  } as CSSProperties;
}
