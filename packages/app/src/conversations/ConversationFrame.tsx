import type { ReactNode } from "react";

import { ArtifactPanel } from "./artifacts/ArtifactPanel";
import {
  ArtifactOpenContext,
  WorkspacePanelContext,
} from "./artifacts/context";
import { useArtifactPanel } from "./artifacts/params";

/** 会话主容器：会话加载与切换期间保持头部挂载，并与右侧工作区并排/覆盖布局。 */
export function ConversationFrame({
  orgId,
  conversationId,
  reviewDisabled = false,
  header,
  children,
}: {
  orgId: string;
  conversationId: string | null;
  reviewDisabled?: boolean;
  header: ReactNode;
  children: ReactNode;
}) {
  const panel = useArtifactPanel(conversationId);
  return (
    <ArtifactOpenContext.Provider
      value={conversationId ? panel.openArtifact : null}
    >
      <WorkspacePanelContext.Provider value={conversationId ? panel : null}>
        <div className="relative flex h-full min-h-0 min-w-0 flex-1 @container">
          {/* 产物分栏依据外层可用宽度，聊天顶栏依据内层聊天列宽度。 */}
          <div className="@container flex min-h-0 min-w-0 flex-1 flex-col">
            {header}
            {children}
          </div>
          {conversationId && panel.open && (
            <ArtifactPanel
              orgId={orgId}
              conversationId={conversationId}
              panel={panel}
              reviewDisabled={reviewDisabled}
            />
          )}
        </div>
      </WorkspacePanelContext.Provider>
    </ArtifactOpenContext.Provider>
  );
}
