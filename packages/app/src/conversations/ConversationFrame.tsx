import type { ReactNode } from "react";

import { ArtifactPanel } from "./artifacts/ArtifactPanel";
import { ArtifactOpenContext } from "./artifacts/context";
import { useArtifactPanel } from "./artifacts/params";

/** Keep the header mounted while sessions load/change; resize it with the chat. */
export function ConversationFrame({
  orgId,
  conversationId,
  header,
  children,
}: {
  orgId: string;
  conversationId: string | null;
  header: ReactNode;
  children: ReactNode;
}) {
  const panel = useArtifactPanel(conversationId);
  return (
    <ArtifactOpenContext.Provider
      value={conversationId ? panel.openArtifact : null}
    >
      <div className="relative flex h-full min-h-0 min-w-0 flex-1">
        {/* @container 挂在聊天列本身，确保打开产物面板时头部仍按聊天列实际宽度折叠 */}
        <div className="@container flex min-h-0 min-w-0 flex-1 flex-col">
          {header}
          {children}
        </div>
        {conversationId && panel.open && (
          <ArtifactPanel
            orgId={orgId}
            conversationId={conversationId}
            panel={panel}
          />
        )}
      </div>
    </ArtifactOpenContext.Provider>
  );
}
