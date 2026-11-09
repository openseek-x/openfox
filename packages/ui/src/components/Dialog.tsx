import { useEffect, useId, useRef, type ReactNode } from "react";
import { createPortal } from "react-dom";

import { cn } from "../cn";
import { Button } from "./Button";
import { Icon } from "./Icon";
import { IconButton } from "./IconButton";
import { useT } from "@openfox/i18n";

const FOCUSABLE =
  'button:not([disabled]),[href],input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';

const modalStack: HTMLElement[] = [];
let originalOverflow = "";

/** 打开期间：锁定页面滚动、Tab 焦点不离开容器、Esc 关闭、关闭后焦点回到打开前的元素。 */
export function useModalBehavior(
  open: boolean,
  container: React.RefObject<HTMLElement | null>,
  onEscape?: () => void,
) {
  const onEscapeRef = useRef(onEscape);
  onEscapeRef.current = onEscape;
  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement | null;
    const node = container.current;
    if (!node) return;
    if (modalStack.length === 0)
      originalOverflow = document.body.style.overflow;
    modalStack.push(node);
    const first =
      node?.querySelector<HTMLElement>("[data-autofocus]") ??
      node?.querySelector<HTMLElement>(FOCUSABLE);
    (first ?? node)?.focus();
    document.body.style.overflow = "hidden";
    const onKey = (event: KeyboardEvent) => {
      if (modalStack.at(-1) !== node || event.defaultPrevented) return;
      if (event.key === "Escape" && onEscapeRef.current) {
        event.preventDefault();
        event.stopPropagation();
        onEscapeRef.current();
        return;
      }
      if (event.key !== "Tab" || !node) return;
      const items = Array.from(node.querySelectorAll<HTMLElement>(FOCUSABLE));
      if (items.length === 0) return;
      const head = items[0]!;
      const tail = items[items.length - 1]!;
      if (event.shiftKey && document.activeElement === head) {
        event.preventDefault();
        tail.focus();
      } else if (!event.shiftKey && document.activeElement === tail) {
        event.preventDefault();
        head.focus();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      const wasTop = modalStack.at(-1) === node;
      const index = modalStack.indexOf(node);
      if (index >= 0) modalStack.splice(index, 1);
      if (modalStack.length === 0)
        document.body.style.overflow = originalOverflow;
      if (wasTop && previous?.isConnected) previous.focus();
    };
  }, [open, container]);
}

export interface DialogProps {
  open: boolean;
  /** 不传时对话框不可关闭（首登向导） */
  onClose?: () => void;
  title?: ReactNode;
  /** 搜索面板把关闭按钮放在输入框里，标题只供辅助技术读取。 */
  hideHeader?: boolean;
  description?: ReactNode;
  /** 宽度，默认 420px。 */
  width?: number;
  /** 点遮罩是否关闭，默认 true */
  closeOnBackdrop?: boolean;
  footer?: ReactNode;
  className?: string;
  children?: ReactNode;
}

/** 模态对话框：遮罩 + 圆角 18 卡片 + 对话框阴影。 */
export function Dialog({
  open,
  onClose,
  title,
  hideHeader = false,
  description,
  width = 420,
  closeOnBackdrop = true,
  footer,
  className,
  children,
}: DialogProps) {
  const t = useT();
  const panel = useRef<HTMLDivElement | null>(null);
  const titleId = useId();
  const descId = useId();
  useModalBehavior(open, panel, onClose);
  if (!open) return null;
  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-scrim/25 p-6"
      onPointerDown={(event) => {
        if (event.target === event.currentTarget && closeOnBackdrop)
          onClose?.();
      }}
    >
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={title ? titleId : undefined}
        aria-describedby={description ? descId : undefined}
        tabIndex={-1}
        style={{ width }}
        className={cn(
          "flex max-h-full max-w-full flex-col gap-4 rounded-2xl border border-line-default bg-surface-card px-6 py-5 shadow-dialog outline-none",
          className,
        )}
      >
        {hideHeader && title && (
          <h2 id={titleId} className="sr-only">
            {title}
          </h2>
        )}
        {!hideHeader && (title || onClose) && (
          <div className="flex items-start gap-3">
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              {title && (
                <h2
                  id={titleId}
                  className="text-title font-medium text-fg-default"
                >
                  {title}
                </h2>
              )}
              {description && (
                <p id={descId} className="text-body text-fg-muted">
                  {description}
                </p>
              )}
            </div>
            {onClose && (
              <IconButton
                label={t("common.close")}
                icon={<Icon name="x" />}
                size="28"
                onClick={onClose}
                className="-mt-0.5 -mr-2"
              />
            )}
          </div>
        )}
        {children && <div className="min-h-0 overflow-y-auto">{children}</div>}
        {footer && <div className="flex justify-end gap-2">{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}

export interface ConfirmDialogProps {
  open: boolean;
  onCancel: () => void;
  onConfirm: () => void;
  title: ReactNode;
  description?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  loading?: boolean;
  /** 确认按钮是否可用（例如需要输入名称确认） */
  confirmDisabled?: boolean;
  children?: ReactNode;
}

/** 确认对话框，默认焦点在「取消」。 */
export function ConfirmDialog({
  open,
  onCancel,
  onConfirm,
  title,
  description,
  confirmLabel,
  cancelLabel,
  danger = false,
  loading = false,
  confirmDisabled = false,
  children,
}: ConfirmDialogProps) {
  const t = useT();
  return (
    <Dialog
      open={open}
      onClose={loading ? undefined : onCancel}
      title={title}
      description={description}
      footer={
        <>
          <Button
            variant="outline"
            onClick={onCancel}
            disabled={loading}
            data-autofocus
          >
            {cancelLabel ?? t("common.cancel")}
          </Button>
          <Button
            variant={danger ? "danger" : "primary"}
            onClick={onConfirm}
            loading={loading}
            disabled={confirmDisabled}
          >
            {confirmLabel ?? t("common.confirm")}
          </Button>
        </>
      }
    >
      {children}
    </Dialog>
  );
}
