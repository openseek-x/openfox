import { useEffect, useState } from "react";
import { useT } from "@openfox/i18n";
import { Dialog, MenuItem } from "@openfox/ui";
import { WorkspaceComments } from "./comments/WorkspaceComments";
import { WorkspaceProposals } from "./proposals/WorkspaceProposals";

/** Review management is opened explicitly from the conversation menu. */
export function useWorkspaceReviewMenu(
  orgId: string,
  conversationId: string | undefined,
  disabled: boolean,
) {
  const t = useT();
  const [view, setView] = useState<"comments" | "proposals" | null>(null);
  useEffect(() => setView(null), [orgId, conversationId]);
  const close = () => setView(null);
  return {
    items: conversationId && (
      <>
        <MenuItem onSelect={() => setView("comments")}>
          {t("artifacts.comments.saved")}
        </MenuItem>
        <MenuItem onSelect={() => setView("proposals")}>
          {t("artifacts.proposals.title")}
        </MenuItem>
      </>
    ),
    dialog: conversationId && view && (
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
