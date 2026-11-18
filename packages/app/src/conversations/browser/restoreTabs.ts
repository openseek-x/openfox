import type { DesktopBrowserState, DesktopBrowserCommand } from "../../desktop";
import type { WorkspaceStore } from "../workspaceStorage";

type SavedTabs = {
  tabs: { id: string; url: string }[];
  activeTab: string | null;
};
const safeURL = (value: string) => {
  if (value === "about:blank") return true;
  try {
    const url = new URL(value);
    return (
      ["http:", "https:"].includes(url.protocol) &&
      !url.username &&
      !url.password
    );
  } catch {
    return false;
  }
};
export function saveBrowserTabs(
  store: WorkspaceStore,
  conversationId: string,
  state: DesktopBrowserState,
) {
  store.write(`browser:${conversationId}`, {
    tabs: state.tabs.slice(0, 8).map((tab) => ({
      id: tab.id,
      url: safeURL(tab.url) ? tab.url : "about:blank",
    })),
    activeTab: state.activeTab,
  } satisfies SavedTabs);
}
export async function restoreBrowserTabs(
  store: WorkspaceStore,
  conversationId: string,
  initial: DesktopBrowserState,
  command: (input: DesktopBrowserCommand) => Promise<DesktopBrowserState>,
  cancelled: () => boolean,
) {
  const saved = store.read<SavedTabs>(`browser:${conversationId}`);
  // A renderer refresh can reattach to existing native tabs. Never replace those pages.
  if (
    !saved?.tabs.length ||
    initial.tabs.some((tab) => saved.tabs.some((old) => old.id === tab.id)) ||
    initial.tabs.some((tab) => tab.url !== "about:blank")
  )
    return initial;
  let state = initial;
  const mapping: Record<string, string> = {};
  for (const [i, tab] of saved.tabs.slice(0, 8).entries()) {
    if (cancelled()) return state;
    const url = safeURL(tab.url) ? tab.url : "about:blank";
    state = await command({
      lease: initial.lease,
      action: i === 0 && initial.activeTab ? "navigate" : "open",
      tabId: i === 0 ? (initial.activeTab ?? undefined) : undefined,
      url,
    });
    if (state.activeTab) mapping[tab.id] = state.activeTab;
  }
  if (!cancelled() && saved.activeTab && mapping[saved.activeTab])
    state = await command({
      lease: initial.lease,
      action: "focus",
      tabId: mapping[saved.activeTab],
    });
  if (!cancelled())
    window.dispatchEvent(
      new CustomEvent("openfox:browser-restored", {
        detail: { conversationId, mapping },
      }),
    );
  return state;
}
