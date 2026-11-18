import { useRef, type KeyboardEvent, type ReactNode } from "react";

import { cn } from "../cn";

export interface TabItem {
  id: string;
  label: ReactNode;
  /** 右侧计数 */
  count?: number;
  disabled?: boolean;
}

export interface TabsProps {
  tabs: TabItem[];
  value: string;
  onChange: (id: string) => void;
  /** 无障碍名称 */
  label: string;
  className?: string;
}

/** 下划线页签：左右方向键切换，选中即激活；面板由调用方渲染。 */
export function Tabs({ tabs, value, onChange, label, className }: TabsProps) {
  const list = useRef<HTMLDivElement | null>(null);
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const enabled = tabs.filter((tab) => !tab.disabled);
    const index = enabled.findIndex((tab) => tab.id === value);
    let next: number;
    if (event.key === "ArrowRight") next = (index + 1) % enabled.length;
    else if (event.key === "ArrowLeft")
      next = (index - 1 + enabled.length) % enabled.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = enabled.length - 1;
    else return;
    event.preventDefault();
    const target = enabled[next];
    if (!target) return;
    onChange(target.id);
    list.current
      ?.querySelector<HTMLElement>(`[data-tab="${CSS.escape(target.id)}"]`)
      ?.focus();
  };
  return (
    <div
      ref={list}
      role="tablist"
      aria-label={label}
      onKeyDown={onKeyDown}
      className={cn("flex gap-5 border-b border-line-default", className)}
    >
      {tabs.map((tab) => {
        const active = tab.id === value;
        return (
          <button
            key={tab.id}
            type="button"
            role="tab"
            data-tab={tab.id}
            aria-selected={active}
            disabled={tab.disabled}
            tabIndex={active ? 0 : -1}
            onClick={() => onChange(tab.id)}
            className={cn(
              "-mb-px flex h-9 items-center gap-1.5 border-b-2 text-body disabled:cursor-not-allowed disabled:opacity-50",
              active
                ? "border-primary font-medium text-fg-default"
                : "border-transparent text-fg-muted hover:text-fg-default",
            )}
          >
            {tab.label}
            {tab.count !== undefined && (
              <span className="text-caption text-fg-muted"> {tab.count}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}
