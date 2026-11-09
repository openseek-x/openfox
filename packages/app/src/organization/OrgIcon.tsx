import { useRef, useState } from "react";

import { isApiError, type ViewIconKind } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Avatar,
  ColorSwatches,
  Icon,
  LABEL_COLORS,
  Popover,
  Spinner,
  Tabs,
  cn,
  useToast,
  type AvatarSize,
} from "@openfox/ui";

import { useApi } from "../context";
import { EmojiGrid } from "../projects/EmojiGrid";
import {
  formatSpaceIcon,
  parseSpaceIcon,
  SPACE_ICON_CHOICES,
} from "../spaces/SpaceIcon";

// 组织图标：图标（图标名:#背景色）/ 表情 / 上传图片；没有图标时显示首字母头像。

export interface OrgIconValue {
  icon_kind?: ViewIconKind | null;
  icon?: string | null;
  icon_url?: string | null;
}

export function OrgIcon({
  org,
  size = 20,
}: {
  org: OrgIconValue & { name: string };
  size?: AvatarSize;
}) {
  if (org.icon_kind === "upload" && org.icon_url)
    return (
      <Avatar kind="agent" name={org.name} src={org.icon_url} size={size} />
    );
  if (org.icon_kind === "emoji" && org.icon)
    return (
      <Avatar
        kind="agent"
        name={org.name}
        size={size}
        glyph={
          <span style={{ fontSize: Math.round(size * 0.65) }}>{org.icon}</span>
        }
      />
    );
  if (org.icon_kind === "icon" && org.icon) {
    const parsed = parseSpaceIcon(org.icon);
    return (
      <Avatar
        kind="agent"
        name={org.name}
        size={size}
        color={parsed.color ?? undefined}
        glyph={
          <Icon
            name={parsed.name}
            size={Math.round(size * 0.6)}
            className={parsed.color ? "text-fg-inverse" : undefined}
          />
        }
      />
    );
  }
  return <Avatar kind="agent" name={org.name} size={size} />;
}

type Tab = "icon" | "emoji" | "upload" | "default";

/** null 表示清除图标（回到首字母头像）。 */
export type OrgIconChange = { kind: ViewIconKind; value: string } | null;

export function OrgIconPicker({
  orgId,
  org,
  disabled,
  onChange,
}: {
  orgId: string;
  org: OrgIconValue & { name: string };
  disabled: boolean;
  onChange: (icon: OrgIconChange) => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<Tab>("icon");
  const [uploading, setUploading] = useState(false);
  const [storageDown, setStorageDown] = useState(false);
  const current = org.icon_kind === "icon" ? parseSpaceIcon(org.icon) : null;
  const [color, setColor] = useState<string | null>(current?.color ?? null);
  const anchor = useRef<HTMLButtonElement | null>(null);
  const pick = (icon: OrgIconChange) => {
    setOpen(false);
    onChange(icon);
  };

  const upload = async (file: File) => {
    setUploading(true);
    try {
      const icon = await api.icons.upload(orgId, file);
      pick({ kind: "upload", value: icon.id });
    } catch (error) {
      if (isApiError(error, "STORAGE_UNAVAILABLE")) setStorageDown(true);
      toast(
        isApiError(error, "STORAGE_UNAVAILABLE")
          ? t("organization.icon.storageUnavailable")
          : t("organization.icon.uploadFailed"),
        { tone: "error" },
      );
    } finally {
      setUploading(false);
    }
  };

  return (
    <>
      <button
        ref={anchor}
        type="button"
        disabled={disabled || uploading}
        aria-label={t("organization.icon.choose")}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className="group relative flex size-7 shrink-0 items-center justify-center rounded-lg disabled:cursor-not-allowed"
      >
        {uploading ? <Spinner size={16} /> : <OrgIcon org={org} size={28} />}
        {!disabled && !uploading && (
          <span className="absolute inset-0 hidden items-center justify-center rounded-lg bg-black/40 text-fg-inverse group-hover:flex">
            <Icon name="camera" size={14} />
          </span>
        )}
      </button>
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        placement="bottom-start"
        role="dialog"
        aria-label={t("organization.icon.choose")}
        className="flex h-[360px] w-[320px] flex-col gap-3 p-3"
      >
        <Tabs
          label={t("organization.icon.choose")}
          value={tab}
          onChange={(id) => {
            // 「默认」页签：点击即清除图标
            if (id === "default") pick(null);
            else setTab(id as Tab);
          }}
          tabs={[
            { id: "icon", label: t("organization.icon.tabIcon") },
            { id: "emoji", label: t("organization.icon.tabEmoji") },
            { id: "upload", label: t("organization.icon.tabUpload") },
            { id: "default", label: t("organization.icon.tabDefault") },
          ]}
        />
        {tab === "icon" && (
          <div className="flex flex-col gap-3 overflow-y-auto">
            <ColorSwatches
              colors={LABEL_COLORS}
              value={color}
              onChange={setColor}
              label={t("organization.icon.color")}
            />
            <div className="grid grid-cols-8 gap-1">
              {SPACE_ICON_CHOICES.map((name) => (
                <button
                  key={name}
                  type="button"
                  aria-label={name}
                  aria-pressed={current?.name === name}
                  onClick={() =>
                    pick({ kind: "icon", value: formatSpaceIcon(name, color) })
                  }
                  className={cn(
                    "flex size-8 items-center justify-center rounded-lg hover:bg-surface-hover",
                    current?.name === name && "bg-surface-active",
                  )}
                >
                  <Icon name={name} size={16} className="text-fg-secondary" />
                </button>
              ))}
            </div>
          </div>
        )}
        {tab === "emoji" && (
          <EmojiGrid
            value={org.icon_kind === "emoji" ? (org.icon ?? null) : null}
            onChange={(emoji) =>
              pick(emoji ? { kind: "emoji", value: emoji } : null)
            }
          />
        )}
        {tab === "upload" && (
          <div className="flex flex-col gap-2">
            <label
              className={cn(
                "flex h-8 w-full cursor-pointer items-center justify-center gap-2 rounded-[10px] border border-line-default text-body hover:bg-surface-hover",
                (uploading || storageDown) && "pointer-events-none opacity-50",
              )}
            >
              {uploading ? (
                <Spinner size={14} />
              ) : (
                <Icon name="upload" size={14} />
              )}
              {t("organization.icon.upload")}
              <input
                type="file"
                accept="image/png,image/jpeg,image/webp,image/gif"
                className="sr-only"
                disabled={uploading || storageDown}
                onChange={(event) => {
                  const file = event.target.files?.[0];
                  event.target.value = "";
                  if (file) void upload(file);
                }}
              />
            </label>
            <p className="text-caption text-fg-muted">
              {t("organization.icon.uploadHelp")}
            </p>
          </div>
        )}
      </Popover>
    </>
  );
}
