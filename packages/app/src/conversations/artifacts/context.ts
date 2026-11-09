import { createContext, useContext } from "react";

/**
 * 打开产物面板并选中某个产物。由 ConversationView 提供；
 * 没有提供者（例如任务详情里的运行时间线）时为 null，此时时间线仍显示原来的生成文件卡片。
 */
export const ArtifactOpenContext = createContext<((id: string) => void) | null>(
  null,
);

export const useArtifactOpener = () => useContext(ArtifactOpenContext);
