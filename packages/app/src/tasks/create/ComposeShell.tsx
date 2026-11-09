import type { ReactNode, RefObject } from "react";
import { createPortal } from "react-dom";

import { cn } from "@openfox/ui";

/**
 * 「撰写式」弹窗外壳：遮罩 + 圆角卡片 + 顶部一行（面包屑与右侧操作）。
 * 正文、属性胶囊行与底栏由调用方放进 children；extra 渲染在卡片外（如放弃确认）。
 */
export function ComposeShell({
  panelRef,
  label,
  onBackdrop,
  style,
  breadcrumb,
  actions,
  children,
  extra,
}: {
  panelRef?: RefObject<HTMLDivElement | null>;
  label: string;
  onBackdrop: () => void;
  style: { width: number; height?: string };
  breadcrumb: ReactNode;
  actions: ReactNode;
  children: ReactNode;
  extra?: ReactNode;
}) {
  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-scrim/25 p-6"
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onBackdrop();
      }}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label={label}
        tabIndex={-1}
        style={style}
        className="flex max-h-full max-w-full flex-col rounded-2xl border border-line-default bg-surface-card shadow-dialog outline-none"
      >
        <div className="flex h-12 shrink-0 items-center gap-1.5 px-4">
          <nav className="flex min-w-0 items-center gap-1.5 text-body-sm">
            {breadcrumb}
          </nav>
          <span className="ml-auto" />
          {actions}
        </div>
        {children}
      </div>
      {extra}
    </div>,
    document.body,
  );
}

/** 大号无边框标题 + 描述输入（Cmd/Ctrl+Enter 提交）。 */
export function ComposeFields({
  title,
  onTitle,
  titlePlaceholder,
  description,
  onDescription,
  descPlaceholder,
  titleMax = 500,
  rows = 3,
  fill = false,
  onSubmit,
  children,
}: {
  title: string;
  onTitle: (value: string) => void;
  titlePlaceholder: string;
  description: string;
  onDescription: (value: string) => void;
  descPlaceholder: string;
  titleMax?: number;
  rows?: number;
  /** 撑满剩余高度（展开态） */
  fill?: boolean;
  onSubmit: () => void;
  /** 标题之前的内容（任务类型选择） */
  children?: ReactNode;
}) {
  const submitOnEnter = (event: React.KeyboardEvent) => {
    if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) onSubmit();
  };
  return (
    <div
      className={cn(
        "flex min-h-0 flex-col gap-2 overflow-y-auto px-5",
        fill && "flex-1",
      )}
    >
      {children}
      <input
        data-autofocus
        aria-label={titlePlaceholder}
        placeholder={titlePlaceholder}
        value={title}
        maxLength={titleMax}
        onChange={(event) => onTitle(event.target.value)}
        onKeyDown={submitOnEnter}
        className="w-full bg-transparent text-title font-medium outline-none placeholder:text-fg-subtle focus-visible:shadow-none"
      />
      <textarea
        aria-label={descPlaceholder}
        placeholder={descPlaceholder}
        value={description}
        maxLength={10000}
        rows={rows}
        onChange={(event) => onDescription(event.target.value)}
        onKeyDown={submitOnEnter}
        className={cn(
          "w-full resize-none bg-transparent text-body outline-none placeholder:text-fg-subtle focus-visible:shadow-none",
          fill && "flex-1",
        )}
      />
    </div>
  );
}
