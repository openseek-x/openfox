import { useRef, useState, useSyncExternalStore } from "react";

import { useT, type MessageKey } from "@openfox/i18n";
import { Icon, Popover, Radio, cn, type IconName } from "@openfox/ui";

import { localKeys, readLocal, writeLocal } from "../storage";
import type { NavModule } from "./navMemory";

// 自定义导航：主导航的「布局模式」，任务优先（默认）/ 对话优先。

export type LayoutMode = "task" | "chat";

const CHANGE_EVENT = "openfox:layout-mode";

/** 读本机偏好；缺失或值不认识时回到默认的任务优先。 */
export function readLayoutMode(): LayoutMode {
  return readLocal(localKeys.layoutMode) === "chat" ? "chat" : "task";
}

/** 写入偏好并通知同页面的订阅者（storage 事件只在其他标签页触发）。 */
export function writeLayoutMode(mode: LayoutMode): void {
  writeLocal(localKeys.layoutMode, mode === "chat" ? "chat" : null);
  window.dispatchEvent(new Event(CHANGE_EVENT));
}

function subscribe(onChange: () => void): () => void {
  window.addEventListener(CHANGE_EVENT, onChange);
  window.addEventListener("storage", onChange);
  return () => {
    window.removeEventListener(CHANGE_EVENT, onChange);
    window.removeEventListener("storage", onChange);
  };
}

/** 订阅布局模式：切换后同页面立即生效，其他标签页通过 storage 事件同步。 */
export function useLayoutMode(): LayoutMode {
  return useSyncExternalStore(subscribe, readLayoutMode, () => "task");
}

export interface RailItem {
  module: NavModule;
  fallback: string;
  icon: IconName;
  label: MessageKey;
}

const home: RailItem = {
  module: "home",
  fallback: "inbox",
  icon: "home",
  label: "shell.rail.home",
};
const chat: RailItem = {
  module: "chat",
  fallback: "chat",
  icon: "messages",
  label: "shell.rail.chat",
};
const members: RailItem = {
  module: "members",
  fallback: "members",
  icon: "users",
  label: "shell.rail.members",
};
const settings: RailItem = {
  module: "settings",
  fallback: "settings",
  icon: "hexagon-cog",
  label: "shell.rail.settings",
};
/** 对话优先时原来的首页入口：仍是 home 模块（落点和主导航记忆不变），换名「任务」、图标 square-check。 */
const tasks: RailItem = {
  ...home,
  icon: "square-check",
  label: "shell.rail.tasks",
};

/** 各布局模式下的主导航四项；module 恒为 home / chat / members / settings，8b 的记忆按模块记录不受顺序影响。 */
export function railItemsFor(mode: LayoutMode): RailItem[] {
  return mode === "chat"
    ? [chat, tasks, members, settings]
    : [home, chat, members, settings];
}

const OPTIONS: Array<{ value: LayoutMode; label: MessageKey }> = [
  { value: "task", label: "shell.taskFirst" },
  { value: "chat", label: "shell.chatFirst" },
];

/** 主导航底部的「自定义导航」按钮：弹出「布局模式」单选（原生 radio，方向键切换、立即生效）。 */
export function LayoutModeButton() {
  const t = useT();
  const mode = useLayoutMode();
  const anchor = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        ref={anchor}
        type="button"
        aria-label={t("shell.rail.customize")}
        title={t("shell.rail.customize")}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className={cn(
          "flex size-9 items-center justify-center rounded-lg text-fg-secondary hover:bg-surface-hover",
          open && "bg-surface-active",
        )}
      >
        <Icon name="settings-2" size={18} />
      </button>
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        placement="right-end"
        offset={8}
        aria-label={t("shell.rail.customize")}
        className="w-[200px] p-3"
      >
        <fieldset className="flex flex-col gap-1">
          <legend className="mb-1 px-1 text-body-sm text-fg-muted">
            {t("shell.layoutMode")}
          </legend>
          {OPTIONS.map((option) => (
            <label
              key={option.value}
              className="flex h-8 cursor-pointer items-center gap-2 rounded-lg px-1.5 text-body hover:bg-surface-hover"
            >
              <Radio
                name="openfox-layout-mode"
                value={option.value}
                checked={mode === option.value}
                onChange={() => writeLayoutMode(option.value)}
              />
              {t(option.label)}
            </label>
          ))}
        </fieldset>
      </Popover>
    </>
  );
}
