import type { ReactNode } from "react";

import { cn } from "../cn";

export type AvatarSize = 16 | 20 | 24 | 28 | 32 | 40 | 64;

export interface AvatarProps {
  /** 显示名，用于首字母和无障碍名称 */
  name: string;
  /** 头像图片地址；加载失败或缺省时显示首字母 */
  src?: string | null;
  /** user：圆形；agent：圆角方形。 */
  kind?: "user" | "agent";
  size?: AvatarSize;
  /** 首字母背景色，未提供时按名字生成稳定颜色 */
  color?: string;
  /** Agent 头像里的图形（图标或表情），优先于首字母 */
  glyph?: ReactNode;
  /** 离线时降低透明度（成员页离线 Agent） */
  offline?: boolean;
  className?: string;
}

const palette = [
  "#f59e0a",
  "#2171cc",
  "#139948",
  "#8b5cf6",
  "#e11d48",
  "#0891b2",
  "#ea580c",
  "#4f46e5",
];

/** 按名字取稳定颜色，同一个人每次显示同一种颜色。 */
export function avatarColor(name: string): string {
  let hash = 0;
  for (const ch of name) hash = (hash * 31 + ch.charCodeAt(0)) | 0;
  return palette[Math.abs(hash) % palette.length]!;
}

/** 取首字母：中文取第一个字，英文取首字母大写。 */
export function initialOf(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) return "?";
  return Array.from(trimmed)[0]!.toUpperCase();
}

const agentRadius: Partial<Record<AvatarSize, string>> = {
  16: "rounded-[4px]",
  20: "rounded-[5px]",
  24: "rounded-sm",
  28: "rounded-[7px]",
  32: "rounded-md",
  40: "rounded-lg",
  64: "rounded-xl",
};

/** 根据用户或 Agent 类型显示头像。 */
export function Avatar({
  name,
  src,
  kind = "user",
  size = 32,
  color,
  glyph,
  offline = false,
  className,
}: AvatarProps) {
  const fontSize = size <= 20 ? 10 : size <= 24 ? 12 : size >= 64 ? 24 : 14;
  return (
    <span
      role="img"
      aria-label={name}
      style={{
        width: size,
        height: size,
        fontSize,
        background: src
          ? undefined
          : kind === "agent" && glyph
            ? undefined
            : (color ?? avatarColor(name)),
      }}
      className={cn(
        "inline-flex shrink-0 items-center justify-center overflow-hidden font-medium text-fg-inverse select-none",
        kind === "user" ? "rounded-full" : agentRadius[size],
        kind === "agent" &&
          Boolean(glyph) &&
          !src &&
          "bg-accent-soft text-accent",
        offline && "opacity-50 grayscale",
        className,
      )}
    >
      {src ? (
        <img
          src={src}
          alt=""
          className="size-full object-cover"
          draggable={false}
        />
      ) : (
        (glyph ?? initialOf(name))
      )}
    </span>
  );
}
