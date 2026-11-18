import { createContext, useContext, type ReactNode } from "react";
import { createPortal } from "react-dom";

// 文件预览将分页控件放进共同顶栏；独立文档窗口仍可就地显示。
export const PreviewNavigationTarget = createContext<HTMLElement | null>(null);
export function PreviewNavigation({ children }: { children: ReactNode }) {
  const target = useContext(PreviewNavigationTarget);
  const controls = (
    <div className="flex shrink-0 items-center gap-0.5 rounded-full border border-line-default bg-surface-muted p-0.5 text-caption text-fg-secondary">
      {children}
    </div>
  );
  return target ? createPortal(controls, target) : controls;
}
