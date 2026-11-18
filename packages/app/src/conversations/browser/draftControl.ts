import { desktopBridge } from "../../desktop";

// Ephemeral intent for the next explicitly sent message. It is never persisted,
// inherited by another conversation, or forwarded as model-controlled input.
type DraftControl = {
  lease: string;
  origins: string[];
  replace: boolean;
  clientMessageId?: string;
};
const drafts = new Map<string, DraftControl>();
const key = (orgId: string, conversationId: string) =>
  `${orgId}/${conversationId}`;

export function armBrowserDraft(
  orgId: string,
  conversationId: string,
  lease: string,
  origins: string[],
  replace = false,
) {
  drafts.set(key(orgId, conversationId), {
    lease,
    origins: [...origins],
    replace,
  });
}

export function clearBrowserDraft(
  orgId: string,
  conversationId: string,
  lease?: string,
) {
  const scope = key(orgId, conversationId);
  if (!lease || drafts.get(scope)?.lease === lease) drafts.delete(scope);
}

export async function grantBrowserForMessage(
  orgId: string,
  conversationId: string,
  clientMessageId: string,
) {
  const scope = key(orgId, conversationId);
  const draft = drafts.get(scope);
  if (!draft) return;
  if (draft.clientMessageId === clientMessageId) return;
  const bridge = desktopBridge();
  if (!bridge?.browserCommand) throw new Error("Browser control unavailable");
  const state = await bridge.browserCommand({
    lease: draft.lease,
    action: "allow-control",
    clientMessageId,
    origins: draft.origins,
    replace: draft.replace,
  });
  if (
    drafts.get(scope) !== draft ||
    !state.control?.active ||
    state.control.clientMessageId !== clientMessageId
  )
    throw new Error("Browser control revoked");
  draft.clientMessageId = clientMessageId;
}

export function consumeBrowserDraft(
  orgId: string,
  conversationId: string,
  clientMessageId: string,
) {
  const scope = key(orgId, conversationId);
  if (drafts.get(scope)?.clientMessageId === clientMessageId)
    drafts.delete(scope);
}
