import { useRef, useState } from "react";
import { useParams } from "react-router-dom";

import { useT } from "@openfox/i18n";
import {
  ColorSwatches,
  Icon,
  LABEL_COLORS,
  Popover,
  Tabs,
  cn,
  iconData,
  type IconName,
} from "@openfox/ui";
import { IconUploadInput } from "../icons/IconUploadInput";
import { uploadedIconURL } from "../icons/uploaded";
import { EmojiGrid } from "../projects/EmojiGrid";

// 空间图标：spaces.icon 存「图标名」或「图标名:#RRGGBB」（不超过 64 字符），空值显示默认 orbit。

export const DEFAULT_SPACE_ICON: IconName = "orbit";

/** 图标页签可选的图标（取图标库中适合做空间标识的一组）。 */
export const SPACE_ICON_CHOICES: IconName[] = [
  "orbit",
  "code",
  "box",
  "megaphone",
  "pen-tool",
  "clipboard",
  "cog",
  "wrench",
  "users",
  "headphones",
  "book-open",
  "dollar",
  "trending-up",
  "bar-chart",
  "shapes",
  "rocket",
  "target",
  "lightbulb",
  "flask-conical",
  "folder",
  "layers",
  "globe",
  "shield",
  "star",
  "zap",
  "heading-1",
  "kanban",
  "briefcase",
  "building",
  "server",
  "terminal",
  "bug",
];

export interface ParsedSpaceIcon {
  name: IconName;
  color: string | null;
  emoji?: string;
  uploadId?: string;
}

export function parseSpaceIcon(
  icon: string | null | undefined,
): ParsedSpaceIcon {
  if (!icon) return { name: DEFAULT_SPACE_ICON, color: null };
  if (icon.startsWith("emoji:") && icon.length > 6 && [...icon].length <= 64)
    return { name: DEFAULT_SPACE_ICON, color: null, emoji: icon.slice(6) };
  if (icon.startsWith("upload:"))
    return { name: DEFAULT_SPACE_ICON, color: null, uploadId: icon.slice(7) };
  const [name = "", color] = icon.split(":");
  return {
    name: Object.hasOwn(iconData, name)
      ? (name as IconName)
      : DEFAULT_SPACE_ICON,
    color: color && /^#[0-9a-fA-F]{6}$/.test(color) ? color : null,
  };
}

export function formatSpaceIcon(name: IconName, color: string | null): string {
  return color ? `${name}:${color}` : name;
}

export function SpaceIcon({
  icon,
  orgId,
  size = 16,
  className,
}: {
  icon: string | null | undefined;
  orgId?: string;
  size?: number;
  className?: string;
}) {
  const params = useParams();
  const parsed = parseSpaceIcon(icon);
  const url = parsed.uploadId
    ? uploadedIconURL(orgId ?? params.orgId ?? "", parsed.uploadId)
    : null;
  if (url)
    return (
      <img
        src={url}
        alt=""
        width={size}
        height={size}
        className={cn("shrink-0 rounded-sm object-cover", className)}
        style={{ width: size, height: size }}
      />
    );
  if (parsed.emoji)
    return (
      <span
        aria-hidden
        className={cn("shrink-0 leading-none", className)}
        style={{ fontSize: size }}
      >
        {parsed.emoji}
      </span>
    );
  return (
    <Icon
      name={parsed.name}
      size={size}
      className={cn(!parsed.color && "text-fg-secondary", className)}
      style={parsed.color ? { color: parsed.color } : undefined}
    />
  );
}

type PickerTab = "icon" | "emoji" | "upload";

/**
 * 图标选择器：28×28 按钮 + 320 宽弹层。
 */
export function SpaceIconPicker({
  orgId,
  icon,
  onChange,
  disabled = false,
  size = 20,
}: {
  orgId: string;
  icon: string | null | undefined;
  onChange: (icon: string) => void;
  disabled?: boolean;
  size?: number;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<PickerTab>("icon");
  const [uploading, setUploading] = useState(false);
  const parsed = parseSpaceIcon(icon);
  const [color, setColor] = useState<string | null>(parsed.color);
  const anchor = useRef<HTMLButtonElement | null>(null);
  return (
    <>
      <button
        ref={anchor}
        type="button"
        disabled={disabled || uploading}
        aria-label={t("spaces.icon.choose")}
        title={t("spaces.icon.choose")}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => {
          setColor(parsed.color);
          setTab(parsed.uploadId ? "upload" : parsed.emoji ? "emoji" : "icon");
          setOpen((value) => !value);
        }}
        className="flex size-7 shrink-0 items-center justify-center rounded-lg hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-50"
      >
        <SpaceIcon orgId={orgId} icon={icon} size={size} />
      </button>
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        placement="bottom-start"
        role="dialog"
        aria-label={t("spaces.icon.choose")}
        className="flex w-[320px] flex-col gap-3 p-3"
      >
        <Tabs
          label={t("spaces.icon.choose")}
          value={tab}
          onChange={(id) => {
            if (!disabled && !uploading) setTab(id as PickerTab);
          }}
          tabs={[
            { id: "icon", label: t("spaces.icon.tabIcon") },
            { id: "emoji", label: t("spaces.icon.tabEmoji") },
            { id: "upload", label: t("spaces.icon.tabUpload") },
          ]}
        />
        {tab === "icon" && (
          <>
            <ColorSwatches
              colors={LABEL_COLORS}
              value={color}
              onChange={(next) => {
                if (!disabled) setColor(next);
              }}
              label={t("spaces.icon.color")}
            />
            <div className="grid grid-cols-8 gap-1">
              {SPACE_ICON_CHOICES.map((name) => (
                <button
                  key={name}
                  type="button"
                  aria-label={name}
                  aria-pressed={
                    !parsed.emoji && !parsed.uploadId && name === parsed.name
                  }
                  disabled={disabled}
                  onClick={() => {
                    setOpen(false);
                    onChange(formatSpaceIcon(name, color));
                  }}
                  className={cn(
                    "flex size-8 items-center justify-center rounded-lg hover:bg-surface-hover",
                    !parsed.emoji &&
                      !parsed.uploadId &&
                      name === parsed.name &&
                      "bg-surface-active",
                  )}
                >
                  <Icon
                    name={name}
                    size={18}
                    style={color ? { color } : undefined}
                    className={color ? undefined : "text-fg-secondary"}
                  />
                </button>
              ))}
            </div>
          </>
        )}
        {tab === "emoji" && (
          <EmojiGrid
            value={parsed.emoji ?? null}
            onChange={(emoji) => {
              if (disabled) return;
              onChange(emoji ? `emoji:${emoji}` : DEFAULT_SPACE_ICON);
              setOpen(false);
            }}
          />
        )}
        {tab === "upload" && (
          <IconUploadInput
            key={orgId}
            orgId={orgId}
            label={t("spaces.icon.tabUpload")}
            disabled={disabled}
            onBusyChange={setUploading}
            onUploaded={(uploaded) => {
              onChange(`upload:${uploaded.id}`);
              setOpen(false);
            }}
          />
        )}
      </Popover>
    </>
  );
}
