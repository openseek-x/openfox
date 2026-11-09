import { useRef, useState } from "react";

import { isApiError, type View, type ViewIconKind } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Icon,
  Popover,
  Tabs,
  cn,
  iconData,
  useToast,
  type IconName,
} from "@openfox/ui";

import { useApi } from "../context";
import { EmojiGrid } from "../projects/EmojiGrid";
import { SPACE_ICON_CHOICES } from "../spaces/SpaceIcon";
import { viewErrorMessage } from "./queries";

/** 视图图标：内置图标 / 表情 / 上传图片；没有图标时显示默认图标。 */
export function ViewIcon({
  view,
  size = 16,
}: {
  view: Pick<View, "icon_kind" | "icon" | "icon_url" | "name">;
  size?: number;
}) {
  if (view.icon_kind === "emoji" && view.icon)
    return (
      <span aria-hidden style={{ fontSize: size }} className="leading-none">
        {view.icon}
      </span>
    );
  if (view.icon_kind === "upload" && view.icon_url)
    return (
      <img
        src={view.icon_url}
        alt=""
        width={size}
        height={size}
        className="rounded-sm object-cover"
        style={{ width: size, height: size }}
      />
    );
  const name =
    view.icon_kind === "icon" && view.icon && Object.hasOwn(iconData, view.icon)
      ? (view.icon as IconName)
      : "layout-grid";
  return <Icon name={name} size={size} className="text-fg-secondary" />;
}

export interface ViewIconValue {
  kind: ViewIconKind | null;
  icon: string | null;
  /** 上传图标的预览地址 */
  url: string | null;
}

type PickerTab = ViewIconKind;

/** 图标选择提供图标、表情和上传页签。存储未配置（503）时上传页签禁用并提示。 */
export function ViewIconPicker({
  orgId,
  value,
  onChange,
}: {
  orgId: string;
  value: ViewIconValue;
  onChange: (value: ViewIconValue) => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<PickerTab>(value.kind ?? "icon");
  const [uploading, setUploading] = useState(false);
  const [storageDown, setStorageDown] = useState(false);
  const anchor = useRef<HTMLButtonElement | null>(null);
  const preview = {
    icon_kind: value.kind,
    icon: value.icon,
    icon_url: value.url,
    name: "",
  };

  const upload = async (file: File) => {
    setUploading(true);
    try {
      const icon = await api.icons.upload(orgId, file);
      onChange({ kind: "upload", icon: icon.id, url: icon.url });
      setOpen(false);
    } catch (error) {
      if (isApiError(error, "STORAGE_UNAVAILABLE")) setStorageDown(true);
      toast(viewErrorMessage(t, error), { tone: "error" });
    } finally {
      setUploading(false);
    }
  };

  return (
    <>
      <button
        ref={anchor}
        type="button"
        aria-label={t("views.icon.choose")}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
        className="flex size-9 shrink-0 items-center justify-center rounded-lg border border-line-default hover:bg-surface-hover"
      >
        <ViewIcon view={preview} size={18} />
      </button>
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        placement="bottom-start"
        role="dialog"
        aria-label={t("views.icon.choose")}
        className="flex w-[320px] flex-col gap-3 p-3"
      >
        <Tabs
          label={t("views.icon.choose")}
          value={tab}
          onChange={(id) => setTab(id as PickerTab)}
          tabs={[
            { id: "icon", label: t("views.icon.tabIcon") },
            { id: "emoji", label: t("views.icon.tabEmoji") },
            {
              id: "upload",
              label: t("views.icon.tabUpload"),
              disabled: storageDown,
            },
          ]}
        />
        {tab === "icon" && (
          <div className="grid grid-cols-8 gap-1">
            {SPACE_ICON_CHOICES.map((name) => (
              <button
                key={name}
                type="button"
                aria-label={name}
                aria-pressed={value.kind === "icon" && value.icon === name}
                onClick={() => {
                  onChange({ kind: "icon", icon: name, url: null });
                  setOpen(false);
                }}
                className={cn(
                  "flex size-8 items-center justify-center rounded-lg hover:bg-surface-hover",
                  value.kind === "icon" &&
                    value.icon === name &&
                    "bg-surface-active",
                )}
              >
                <Icon name={name} size={18} className="text-fg-secondary" />
              </button>
            ))}
          </div>
        )}
        {tab === "emoji" && (
          <EmojiGrid
            value={value.kind === "emoji" ? value.icon : null}
            onChange={(emoji) => {
              onChange(
                emoji
                  ? { kind: "emoji", icon: emoji, url: null }
                  : { kind: null, icon: null, url: null },
              );
              setOpen(false);
            }}
          />
        )}
        {tab === "upload" &&
          (storageDown ? (
            <p
              role="status"
              className="py-4 text-center text-body-sm text-fg-muted"
            >
              {t("views.icon.uploadUnavailable")}
            </p>
          ) : (
            <label className="flex cursor-pointer flex-col items-center gap-2 rounded-xl border border-dashed border-line-default py-6 text-body-sm text-fg-muted hover:bg-surface-hover">
              <Icon name="upload" size={20} />
              {uploading
                ? t("views.icon.uploading")
                : t("views.icon.uploadHint")}
              <input
                type="file"
                accept="image/png,image/jpeg,image/webp,image/gif"
                aria-label={t("views.icon.tabUpload")}
                className="sr-only"
                disabled={uploading}
                onChange={(event) => {
                  const file = event.target.files?.[0];
                  event.target.value = "";
                  if (file) void upload(file);
                }}
              />
            </label>
          ))}
      </Popover>
    </>
  );
}
