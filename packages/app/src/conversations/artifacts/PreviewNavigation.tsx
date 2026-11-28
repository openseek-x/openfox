import { createContext, useContext, type ReactNode } from "react";
import { createPortal } from "react-dom";

// 文件预览将分页控件放进共同顶栏；独立文档窗口仍可就地显示。
export const PreviewNavigationTarget = createContext<HTMLElement | null>(null);
export function PreviewNavigation({ children }: { children: ReactNode }) {
  const target = useContext(PreviewNavigationTarget);
  const controls = (
    <div className="flex h-7 shrink-0 items-center gap-0.5 rounded-lg border border-line-subtle bg-surface-card px-0.5 text-caption text-fg-secondary">
      {children}
    </div>
  );
  return target ? createPortal(controls, target) : controls;
}

/** 宽侧栏单行顶栏插槽：将文件预览与画布动作收入 44px 工作区顶栏，消除双层工具栏与重复文件名。 */
export const WorkspaceToolbarTarget = createContext<HTMLElement | null>(null);
export const useWorkspaceToolbarTarget = () =>
  useContext(WorkspaceToolbarTarget);
