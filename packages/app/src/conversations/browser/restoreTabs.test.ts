import { expect, it, vi } from "vitest";
import type { DesktopBrowserState, DesktopBrowserCommand } from "../../desktop";
import { WorkspaceStore } from "../workspaceStorage";
import { restoreBrowserTabs, saveBrowserTabs } from "./restoreTabs";

const tab = (id: string, url: string) => ({
  id,
  url,
  title: id,
  loading: false,
  error: null,
  canBack: false,
  canForward: false,
});
const initial = (): DesktopBrowserState => ({
  lease: "fresh-lease",
  tabs: [tab("blank", "about:blank")],
  activeTab: "blank",
  downloads: [],
});
it("restores page URLs and ordering with a new lease, without restoring any control grant", async () => {
  const store = new WorkspaceStore("test");
  const old = {
    ...initial(),
    tabs: [
      tab("old-a", "https://example.com/a"),
      tab("old-b", "https://example.com/b"),
    ],
    activeTab: "old-a",
    control: {
      connected: true,
      pending: true,
      active: true,
      epoch: 8,
      runId: "run",
      generation: 2,
      clientMessageId: "message",
      origins: ["https://example.com"],
      error: null,
    },
  };
  saveBrowserTabs(store, "c1", old);
  expect(store.read("browser:c1")).toEqual({
    tabs: old.tabs.map(({ id, url }) => ({ id, url })),
    activeTab: "old-a",
  });
  let state = initial();
  const command = vi.fn(async (input: DesktopBrowserCommand) => {
    if (input.action === "navigate")
      state = { ...state, tabs: [tab("blank", input.url!)] };
    if (input.action === "open")
      state = {
        ...state,
        tabs: [...state.tabs, tab("new-b", input.url!)],
        activeTab: "new-b",
      };
    if (input.action === "focus") state = { ...state, activeTab: input.tabId! };
    return state;
  });
  const event = vi.fn();
  window.addEventListener("openfox:browser-restored", event);
  try {
    const restored = await restoreBrowserTabs(
      store,
      "c1",
      state,
      command,
      () => false,
    );
    expect(command.mock.calls.map(([c]) => c.action)).toEqual([
      "navigate",
      "open",
      "focus",
    ]);
    expect(command.mock.calls.every(([c]) => c.lease === "fresh-lease")).toBe(
      true,
    );
    expect(restored.activeTab).toBe("blank");
    expect(restored.control).toBeUndefined();
    expect(event.mock.calls[0]![0].detail.mapping).toEqual({
      "old-a": "blank",
      "old-b": "new-b",
    });
  } finally {
    window.removeEventListener("openfox:browser-restored", event);
  }
});
it("renderer reattachment leaves existing pages untouched and rejects credential-bearing and non-web URLs", async () => {
  const store = new WorkspaceStore("test"),
    command = vi.fn();
  const state = {
    ...initial(),
    tabs: [tab("existing", "https://example.com")],
  };
  saveBrowserTabs(store, "c1", state);
  expect(
    await restoreBrowserTabs(store, "c1", state, command, () => false),
  ).toBe(state);
  expect(command).not.toHaveBeenCalled();
  saveBrowserTabs(store, "c2", {
    ...initial(),
    tabs: [
      tab("x", "file:///secret"),
      tab("y", "https://user:pass@example.com"),
    ],
  });
  expect(
    store
      .read<{ tabs: { url: string }[] }>("browser:c2")!
      .tabs.map((t) => t.url),
  ).toEqual(["about:blank", "about:blank"]);
});
