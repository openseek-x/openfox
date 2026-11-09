import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";

import { Icon, type IconName } from "./Icon";

export type ToastTone = "success" | "error" | "info";

export interface ToastOptions {
  tone?: ToastTone;
  /** 可选操作，比如「撤销」「查看」 */
  action?: { label: string; onClick: () => void };
  /** 显示时长（毫秒），默认 2400。 */
  duration?: number;
}

interface ToastItem extends Required<Pick<ToastOptions, "tone">> {
  id: number;
  message: ReactNode;
  action?: ToastOptions["action"];
}

type ShowToast = (message: ReactNode, options?: ToastOptions) => void;

const ToastContext = createContext<ShowToast | null>(null);

const toneIcon: Record<ToastTone, { name: IconName; className: string }> = {
  success: { name: "check-circle", className: "text-accent" },
  error: { name: "x-circle", className: "text-danger" },
  info: { name: "info", className: "text-info" },
};

/** 全局提示：居中贴底，同时最多 3 条，按时自动消失。 */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const seq = useRef(0);
  const timers = useRef(new Map<number, number>());
  useEffect(() => {
    const pending = timers.current;
    return () => {
      for (const timer of pending.values()) window.clearTimeout(timer);
      pending.clear();
    };
  }, []);
  const dismiss = useCallback((id: number) => {
    window.clearTimeout(timers.current.get(id));
    timers.current.delete(id);
    setItems((list) => list.filter((item) => item.id !== id));
  }, []);
  const show = useCallback<ShowToast>(
    (message, options = {}) => {
      const id = ++seq.current;
      setItems((list) => [
        ...list.slice(-2),
        {
          id,
          message,
          tone: options.tone ?? "success",
          action: options.action,
        },
      ]);
      timers.current.set(
        id,
        window.setTimeout(() => dismiss(id), options.duration ?? 2400),
      );
    },
    [dismiss],
  );
  const value = useMemo(() => show, [show]);
  return (
    <ToastContext.Provider value={value}>
      {children}
      {createPortal(
        <div
          aria-live="polite"
          className="pointer-events-none fixed inset-x-0 bottom-6 z-[60] flex flex-col items-center gap-2"
        >
          {items.map((item) => {
            const icon = toneIcon[item.tone];
            return (
              <div
                key={item.id}
                role={item.tone === "error" ? "alert" : "status"}
                className="pointer-events-auto flex min-w-[340px] max-w-[480px] items-center gap-2.5 rounded-xl border border-line-default bg-surface-card py-3 pr-2.5 pl-3.5 text-body shadow-popover"
              >
                <Icon name={icon.name} className={icon.className} />
                <span className="min-w-0 flex-1">{item.message}</span>
                {item.action && (
                  <button
                    type="button"
                    className="rounded-md px-2 py-0.5 font-medium text-fg-default hover:bg-surface-muted"
                    onClick={() => {
                      item.action?.onClick();
                      dismiss(item.id);
                    }}
                  >
                    {item.action.label}
                  </button>
                )}
              </div>
            );
          })}
        </div>,
        document.body,
      )}
    </ToastContext.Provider>
  );
}

/** 显示提示。必须在 ToastProvider 内使用。 */
export function useToast(): ShowToast {
  const show = useContext(ToastContext);
  if (!show) throw new Error("useToast 必须在 ToastProvider 内使用");
  return show;
}
