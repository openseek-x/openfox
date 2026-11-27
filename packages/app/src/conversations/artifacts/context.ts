import { createContext, useContext } from "react";
import type { ArtifactPanelState } from "./params";

/**
 * 阶段 10d · 打开产物面板并选中某个产物。由 ConversationFrame 提供；
 * 没有提供者（例如任务详情里的运行时间线）时为 null，此时时间线仍显示原来的生成文件卡片。
 */
export const ArtifactOpenContext = createContext<((id: string) => void) | null>(
  null,
);

export const useArtifactOpener = () => useContext(ArtifactOpenContext);

/** 当前会话的右侧工作区面板控制上下文，供顶栏按钮与「更多」菜单直达使用。 */
export const WorkspacePanelContext = createContext<ArtifactPanelState | null>(
  null,
);

export const useWorkspacePanel = () => useContext(WorkspacePanelContext);
