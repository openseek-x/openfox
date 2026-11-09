import { useEffect, useRef, useState } from "react";

import { useT } from "@openfox/i18n";
import { Icon } from "@openfox/ui";

import { effectiveShortcut, isMacPlatform } from "../me/shortcuts";
import { useMyPreferences } from "../me/queries";
import { CommandPalette } from "./CommandPalette";
import { matchesShortcut } from "./model";

/**
 * 顶栏搜索按钮 + 命令面板。快捷键取账号偏好里的 command_menu（默认 mod+k，可在设置 / 快捷键里改）。
 * 已有其他对话框打开时不响应快捷键，避免叠层后焦点还原错乱；按钮点击不受影响。
 */
export function SearchLauncher({ orgId }: { orgId: string }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const openRef = useRef(open);
  openRef.current = open;
  const prefs = useMyPreferences();
  const shortcut = effectiveShortcut(
    prefs.data?.command_shortcuts ?? {},
    "command_menu",
  );

  useEffect(() => {
    const mac = isMacPlatform();
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented || !matchesShortcut(event, shortcut, mac))
        return;
      if (!openRef.current && document.querySelector('[role="dialog"]')) return;
      event.preventDefault();
      setOpen((value) => !value);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [shortcut]);

  const mac = isMacPlatform();
  const keycaps = shortcut
    .split("+")
    .map((part) =>
      part === "mod"
        ? mac
          ? "⌘"
          : "Ctrl"
        : part === "shift"
          ? mac
            ? "⇧"
            : "Shift"
          : part === "alt"
            ? mac
              ? "⌥"
              : "Alt"
            : part.toUpperCase(),
    )
    .join(" ");
  return (
    <>
      <button
        type="button"
        aria-haspopup="dialog"
        onClick={() => setOpen(true)}
        className="flex h-7 w-[300px] items-center gap-1.5 rounded-lg bg-surface-card px-2.5 text-body-sm text-fg-muted hover:bg-surface-hover"
      >
        <Icon name="search" size={14} />
        <span className="flex-1 text-left">{t("shell.search")}</span>
        <span className="text-caption text-fg-subtle">{keycaps}</span>
      </button>
      {open && <CommandPalette orgId={orgId} onClose={() => setOpen(false)} />}
    </>
  );
}
