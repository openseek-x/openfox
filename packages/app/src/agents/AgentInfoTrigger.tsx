import {
  createContext,
  useContext,
  useEffect,
  useRef,
  type ReactNode,
} from "react";
import { useT } from "@openfox/i18n";

export interface AgentInfoTarget {
  id: string;
  name: string;
  spaceId?: string;
  anchor: HTMLElement;
  returnFocusTo?: HTMLElement;
}

export const AgentInfoContext = createContext<{
  anchor: HTMLElement | null;
  toggle: (target: AgentInfoTarget) => void;
  dismiss: (anchor: HTMLElement) => void;
} | null>(null);

/** 头像常嵌在菜单项、链接或可拖动的任务行中，单独处理头像上的激活。 */
export function AgentInfoTrigger({
  agentId,
  spaceId,
  name,
  children,
}: {
  agentId?: string | null;
  spaceId?: string | null;
  name: string;
  children: ReactNode;
}) {
  const info = useContext(AgentInfoContext);
  const t = useT();
  const ref = useRef<HTMLSpanElement>(null);
  const open = !!info?.anchor && info.anchor === ref.current;
  const dismiss = info?.dismiss;
  useEffect(() => {
    const anchor = ref.current;
    return () => {
      if (open && anchor) dismiss?.(anchor);
    };
  }, [open, dismiss]);
  if (!agentId || !info) return children;
  const activate = (anchor: HTMLElement) =>
    info.toggle({
      id: agentId,
      spaceId: spaceId ?? undefined,
      name,
      anchor,
      // 鼠标从提及候选开卡时焦点仍在编辑器；关闭卡片后应能从原光标继续输入。
      returnFocusTo:
        document.activeElement instanceof HTMLTextAreaElement &&
        document.activeElement.getAttribute("aria-controls") ===
          anchor.closest('[role="listbox"]')?.id
          ? document.activeElement
          : undefined,
    });
  return (
    <span
      ref={ref}
      role="button"
      tabIndex={0}
      aria-label={t("agents.info.label", { name })}
      aria-haspopup="dialog"
      aria-expanded={open}
      className="relative inline-flex shrink-0 cursor-pointer items-center rounded focus-visible:outline-2 focus-visible:outline-line-focus"
      onPointerDown={(event) => event.stopPropagation()}
      onMouseDown={(event) => {
        event.preventDefault();
        event.stopPropagation();
      }}
      onDragStart={(event) => {
        event.preventDefault();
        event.stopPropagation();
      }}
      onClick={(event) => {
        event.preventDefault();
        event.stopPropagation();
        activate(event.currentTarget);
      }}
      onKeyDown={(event) => {
        if (event.key !== "Enter" && event.key !== " ") return;
        event.preventDefault();
        event.stopPropagation();
        if (!event.repeat) activate(event.currentTarget);
      }}
    >
      {children}
    </span>
  );
}
