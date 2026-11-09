import { useEffect, useRef } from "react";

import { useT } from "@openfox/i18n";
import { Icon, cn } from "@openfox/ui";

import type { SlashState } from "./slash";

// 斜杠菜单弹层：宽 232，最大高 min(26rem, 60vh)，项 13/20，激活项底 accent。

export function SlashMenu({ state }: { state: SlashState }) {
  const t = useT();
  const list = useRef<HTMLDivElement | null>(null);
  // 键盘移动高亮时保持激活项可见
  useEffect(() => {
    list.current
      ?.querySelector<HTMLElement>('[aria-selected="true"]')
      ?.scrollIntoView({ block: "nearest" });
  }, [state.index]);
  const { rect } = state;
  return (
    <div
      ref={list}
      role="listbox"
      aria-label={t("docs.editor.slash.label")}
      // 不抢走编辑器焦点：点击菜单项时光标留在正文里
      onMouseDown={(event) => event.preventDefault()}
      style={{
        top: rect.bottom + 6,
        left: rect.left,
        maxHeight: "min(26rem, 60vh)",
      }}
      className="fixed z-50 flex w-[232px] flex-col overflow-y-auto rounded-xl border border-line-default bg-surface-card p-1 shadow-lg"
    >
      {state.items.length === 0 && (
        <div className="px-2.5 py-2 text-body-sm text-fg-muted">
          {t("docs.editor.slash.empty")}
        </div>
      )}
      {state.items.map((item, index) => (
        <button
          key={item.id}
          type="button"
          role="option"
          aria-selected={index === state.index}
          onMouseEnter={() => state.highlight(index)}
          onClick={() => state.select(item)}
          className={cn(
            "flex h-[34px] w-full items-center gap-2 rounded-lg px-2.5 text-left text-body",
            index === state.index
              ? "bg-surface-active"
              : "hover:bg-surface-hover",
          )}
        >
          <span className="flex size-5 shrink-0 items-center justify-center text-fg-secondary">
            {item.icon ? (
              <Icon name={item.icon} size={14} />
            ) : (
              <span className="text-caption font-medium">{item.short}</span>
            )}
          </span>
          <span className="truncate">{item.label}</span>
        </button>
      ))}
    </div>
  );
}
