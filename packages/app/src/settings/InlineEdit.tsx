import { useRef, useState } from "react";

import { cn } from "@openfox/ui";

/**
 * 行内编辑输入框：Enter 或失焦提交，Esc 取消；去空格后为空或未改动时不提交，直接退出编辑。
 */
export function InlineEdit({
  value,
  label,
  maxLength,
  placeholder,
  onCommit,
  onDone,
  className,
}: {
  value: string;
  /** 输入框的无障碍名称 */
  label: string;
  maxLength?: number;
  placeholder?: string;
  onCommit: (next: string) => void;
  /** 退出编辑（提交或取消后都会调用） */
  onDone: () => void;
  className?: string;
}) {
  const [draft, setDraft] = useState(value);
  // Enter 提交后输入框卸载会再触发一次 blur，用标记避免重复提交
  const finished = useRef(false);
  const finish = (commit: boolean) => {
    if (finished.current) return;
    finished.current = true;
    const next = draft.trim();
    if (commit && next && next !== value.trim()) onCommit(next);
    onDone();
  };
  return (
    <input
      autoFocus
      aria-label={label}
      value={draft}
      maxLength={maxLength}
      placeholder={placeholder}
      onChange={(event) => setDraft(event.target.value)}
      onFocus={(event) => event.currentTarget.select()}
      onBlur={() => finish(true)}
      onClick={(event) => event.stopPropagation()}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          finish(true);
        } else if (event.key === "Escape") {
          event.preventDefault();
          event.stopPropagation();
          finish(false);
        }
      }}
      className={cn(
        "h-7 w-full min-w-0 rounded-md border border-line-focus bg-surface-card px-2 text-body outline-none",
        className,
      )}
    />
  );
}
