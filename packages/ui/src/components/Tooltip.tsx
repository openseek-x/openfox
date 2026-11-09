import {
  cloneElement,
  useEffect,
  useId,
  useRef,
  useState,
  type ReactElement,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";

export interface TooltipProps {
  content: ReactNode;
  /** 子元素必须能接收 ref 和鼠标/焦点事件 */
  children: ReactElement<Record<string, unknown>>;
  /** 显示延迟（毫秒） */
  delay?: number;
}

/** 文字提示：悬停或聚焦时显示在元素上方，使用深色背景。 */
export function Tooltip({ content, children, delay = 300 }: TooltipProps) {
  const [rect, setRect] = useState<DOMRect | null>(null);
  const timer = useRef<number | undefined>(undefined);
  const id = useId();
  const expanded =
    children.props["aria-expanded"] === true ||
    children.props["aria-expanded"] === "true";
  const visible = rect !== null && !expanded;
  useEffect(() => {
    if (expanded) {
      window.clearTimeout(timer.current);
      setRect(null);
    }
    return () => window.clearTimeout(timer.current);
  }, [expanded]);
  const show = (target: EventTarget) => {
    window.clearTimeout(timer.current);
    if (expanded) return;
    const el = target as HTMLElement;
    timer.current = window.setTimeout(
      () => setRect(el.getBoundingClientRect()),
      delay,
    );
  };
  const hide = () => {
    window.clearTimeout(timer.current);
    setRect(null);
  };
  const props = children.props as Record<
    string,
    ((e: { currentTarget: EventTarget }) => void) | undefined
  >;
  return (
    <>
      {cloneElement(children, {
        "aria-describedby": visible ? id : undefined,
        onClick: (e: { currentTarget: EventTarget }) => {
          hide();
          props.onClick?.(e);
        },
        onMouseEnter: (e: { currentTarget: EventTarget }) => {
          props.onMouseEnter?.(e);
          show(e.currentTarget);
        },
        onMouseLeave: (e: { currentTarget: EventTarget }) => {
          props.onMouseLeave?.(e);
          hide();
        },
        onFocus: (e: { currentTarget: EventTarget }) => {
          props.onFocus?.(e);
          show(e.currentTarget);
        },
        onBlur: (e: { currentTarget: EventTarget }) => {
          props.onBlur?.(e);
          hide();
        },
      })}
      {visible &&
        createPortal(
          <div
            id={id}
            role="tooltip"
            style={{ left: rect.left + rect.width / 2, top: rect.top - 6 }}
            className="pointer-events-none fixed z-[70] max-w-xs -translate-x-1/2 -translate-y-full rounded-sm bg-primary px-2 py-[5px] text-caption leading-4 text-fg-inverse"
          >
            {content}
          </div>,
          document.body,
        )}
    </>
  );
}
