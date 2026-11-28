import { useEffect, useState } from "react";
import { useT } from "@openfox/i18n";
import { Dialog, Icon, MenuItem } from "@openfox/ui";
import { useWorkspacePanel } from "./artifacts/context";
import { COMMENTS_PANEL, PROPOSALS_PANEL } from "./artifacts/params";
import { WorkspaceComments } from "./comments/WorkspaceComments";
import { WorkspaceProposals } from "./proposals/WorkspaceProposals";

/** 审阅管理入口：在会话工作区内优先以右侧内联标签打开，避免弹窗遮挡原生浏览器视图。 */
export function useWorkspaceReviewMenu(
  orgId: string,
  conversationId: string | undefined,
  disabled: boolean,
) {
  const t = useT();
  const panel = useWorkspacePanel();
  const [view, setView] = useState<"comments" | "proposals" | null>(null);
  useEffect(() => setView(null), [orgId, conversationId]);
  const close = () => setView(null);
  const openComments = () => {
    if (panel) panel.select(COMMENTS_PANEL);
    else setView("comments");
  };
  const openProposals = () => {
    if (panel) panel.select(PROPOSALS_PANEL);
    else setView("proposals");
  };
  return {
    items: conversationId && (
      <>
        <MenuItem icon={<Icon name="message-circle" />} onSelect={openComments}>
          {t("artifacts.comments.saved")}
        </MenuItem>
        <MenuItem icon={<Icon name="layers" />} onSelect={openProposals}>
          {t("artifacts.proposals.title")}
        </MenuItem>
      </>
    ),
    dialog: !panel && conversationId && view && (
      <Dialog
        open
        onClose={close}
        width={520}
        title={t(
          view === "comments"
            ? "artifacts.comments.saved"
            : "artifacts.proposals.title",
        )}
      >
        {view === "comments" ? (
          <WorkspaceComments
            orgId={orgId}
            conversationId={conversationId}
            disabled={disabled}
          />
        ) : (
          <WorkspaceProposals
            orgId={orgId}
            conversationId={conversationId}
            onOpen={close}
          />
        )}
      </Dialog>
    ),
  };
}
