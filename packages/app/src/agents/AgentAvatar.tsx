import { useEffect, useRef, useState } from "react";

import type { AgentAvatarValue } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Icon,
  Popover,
  Tooltip,
  avatarColor,
  cn,
  initialOf,
} from "@openfox/ui";

import {
  AVATAR_COLORS,
  AVATAR_MOODS,
  readAvatar,
  type AvatarSpec,
} from "./model";
import { IconUploadInput } from "../icons/IconUploadInput";

const glyphSize: Record<number, number> = {
  16: 10,
  20: 12,
  24: 14,
  28: 16,
  32: 18,
  40: 22,
  64: 32,
};

/**
 * Agent 头像：圆形色块 + 机器人图标或表情；
 * 私有 Agent 右下角加锁，离线时变灰。没有头像设置时按名字取颜色。
 */
export function AgentAvatar({
  name,
  avatar,
  size = 40,
  locked = false,
  offline = false,
  className,
}: {
  name: string;
  avatar?: AgentAvatarValue;
  size?: 16 | 20 | 24 | 28 | 32 | 40 | 64;
  locked?: boolean;
  offline?: boolean;
  className?: string;
}) {
  const t = useT();
  const spec = readAvatar(avatar);
  const color = spec?.color ?? avatarColor(name);
  const node = (
    <span
      role="img"
      aria-label={name}
      style={{ width: size, height: size, background: color }}
      className={cn(
        "relative inline-flex shrink-0 items-center justify-center rounded-full font-medium text-white select-none",
        offline && "opacity-50 grayscale",
        className,
      )}
    >
      {spec?.kind === "upload" ? (
        <img
          src={spec.value}
          alt=""
          width={size}
          height={size}
          className="size-full rounded-full object-cover"
        />
      ) : spec?.kind === "mood" ? (
        <span style={{ fontSize: Math.max(10, size * 0.32) }}>
          {spec.value}
        </span>
      ) : (
        <Icon name="openfox-robot-head" size={glyphSize[size] ?? 18} />
      )}
      {locked && (
        <span
          aria-label={t("agents.privateLock")}
          className="absolute -right-0.5 -bottom-0.5 flex size-[15px] items-center justify-center rounded-full border-2 border-surface-card bg-primary text-fg-inverse"
        >
          <Icon name="lock" size={8} />
        </span>
      )}
    </span>
  );
  return offline ? (
    <Tooltip content={t("agents.offline")}>{node}</Tooltip>
  ) : (
    node
  );
}

/** 小队头像：三个成员头像叠成品字形；没有成员时显示用户组图标。 */
export function CrewAvatar({
  name,
  members,
  size = 40,
}: {
  name: string;
  members: ReadonlyArray<{ name: string; avatar?: AgentAvatarValue }>;
  size?: 28 | 32 | 40 | 64;
}) {
  if (members.length === 0)
    return (
      <span
        role="img"
        aria-label={name}
        style={{ width: size, height: size }}
        className="inline-flex shrink-0 items-center justify-center rounded-lg bg-surface-muted text-fg-secondary"
      >
        <Icon name="users" size={size / 2} />
      </span>
    );
  const small = size >= 64 ? 32 : size >= 40 ? 20 : 16;
  const [a, b, c] = members;
  return (
    <span
      role="img"
      aria-label={name}
      style={{ width: size, height: size }}
      className="relative inline-flex shrink-0"
    >
      {a && (
        <span className="absolute top-0 left-1/2 -translate-x-1/2">
          <AgentAvatar name={a.name} avatar={a.avatar} size={small} />
        </span>
      )}
      {b && (
        <span className="absolute bottom-0 left-0">
          <AgentAvatar name={b.name} avatar={b.avatar} size={small} />
        </span>
      )}
      {c && (
        <span className="absolute right-0 bottom-0">
          <AgentAvatar name={c.name} avatar={c.avatar} size={small} />
        </span>
      )}
    </span>
  );
}

/** 真人头像：首字母圆形（与 ui Avatar 一致的取色）。 */
export function PersonAvatar({
  name,
  size = 28,
}: {
  name: string;
  size?: number;
}) {
  return (
    <span
      role="img"
      aria-label={name}
      style={{
        width: size,
        height: size,
        background: avatarColor(name),
        fontSize: size <= 24 ? 11 : 13,
      }}
      className="inline-flex shrink-0 items-center justify-center rounded-full font-medium text-fg-inverse"
    >
      {initialOf(name)}
    </span>
  );
}

/** 头像选择面板：颜色、默认图标、Moods 和上传图片。 */
export function AvatarPicker({
  orgId,
  name,
  value,
  onChange,
  size = 64,
  disabled = false,
  onBusyChange,
}: {
  orgId: string;
  name: string;
  value: AvatarSpec;
  onChange: (next: AvatarSpec) => void;
  size?: 64;
  disabled?: boolean;
  onBusyChange?: (busy: boolean) => void;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const [uploading, setUploading] = useState(false);
  const anchor = useRef<HTMLButtonElement | null>(null);
  useEffect(() => {
    onBusyChange?.(uploading);
  }, [onBusyChange, uploading]);
  const unavailable = disabled || uploading;
  return (
    <>
      <button
        ref={anchor}
        type="button"
        disabled={unavailable}
        aria-label={t("agents.avatar.label")}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="rounded-full"
      >
        <AgentAvatar name={name || "Agent"} avatar={{ ...value }} size={size} />
      </button>
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        aria-label={t("agents.avatar.label")}
        className="flex w-[280px] flex-col gap-3 p-3"
      >
        <div className="flex flex-col gap-1.5">
          <span className="text-caption text-fg-muted">
            {t("agents.avatar.color")}
          </span>
          <div className="flex flex-wrap gap-1.5">
            {AVATAR_COLORS.map((color) => (
              <button
                key={color}
                type="button"
                aria-label={color}
                aria-pressed={value.color === color}
                disabled={unavailable}
                onClick={() => onChange({ ...value, color })}
                style={{ background: color }}
                className={cn(
                  "size-6 rounded-full",
                  value.color === color &&
                    "ring-2 ring-line-focus ring-offset-2",
                )}
              />
            ))}
          </div>
        </div>
        <div className="flex flex-col gap-1.5">
          <span className="text-caption text-fg-muted">
            {t("agents.avatar.moods")}
          </span>
          <div className="grid grid-cols-6 gap-1">
            <button
              type="button"
              aria-label={t("agents.avatar.icon")}
              aria-pressed={value.kind === "icon"}
              disabled={unavailable}
              onClick={() => onChange({ ...value, kind: "icon", value: "bot" })}
              className={cn(
                "flex h-8 items-center justify-center rounded-md hover:bg-surface-muted",
                value.kind === "icon" && "bg-surface-active",
              )}
            >
              <Icon name="openfox-robot-head" />
            </button>
            {AVATAR_MOODS.map((mood) => (
              <button
                key={mood}
                type="button"
                aria-label={mood}
                aria-pressed={value.kind === "mood" && value.value === mood}
                disabled={unavailable}
                onClick={() =>
                  onChange({ ...value, kind: "mood", value: mood })
                }
                className={cn(
                  "flex h-8 items-center justify-center rounded-md text-caption hover:bg-surface-muted",
                  value.kind === "mood" &&
                    value.value === mood &&
                    "bg-surface-active",
                )}
              >
                {mood}
              </button>
            ))}
          </div>
        </div>
        <IconUploadInput
          key={orgId}
          orgId={orgId}
          disabled={disabled}
          label={t("agents.avatar.upload")}
          onBusyChange={setUploading}
          onUploaded={(icon) => {
            onChange({ ...value, kind: "upload", value: icon.url });
            setOpen(false);
          }}
        />
      </Popover>
    </>
  );
}
