import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { I18nProvider } from "@openfox/i18n";
import type { DesktopBrowserCommand, DesktopBrowserState } from "../../desktop";
import { BrowserPanel } from "./BrowserPanel";
import { useBrowserSession } from "./useBrowserSession";
import { grantBrowserForMessage, consumeBrowserDraft } from "./draftControl";

vi.mock("../queries", () => ({ useConversation: () => ({ data: undefined }) }));
vi.mock("../steps/queries", () => ({ useRun: () => ({ data: undefined }) }));

const blank = (lease = "lease-a"): DesktopBrowserState => ({
  lease,
  activeTab: "tab-a",
  tabs: [
    {
      id: "tab-a",
      title: "",
      url: "about:blank",
      loading: false,
      error: null,
      canBack: false,
      canForward: false,
    },
  ],
  downloads: [],
});
let current: DesktopBrowserState;
let listener: ((state: DesktopBrowserState) => void) | undefined;
let invoke =
  vi.fn<(command: DesktopBrowserCommand) => Promise<DesktopBrowserState>>();
let mount =
  vi.fn<
    (input: {
      orgId: string;
      conversationId: string;
    }) => Promise<DesktopBrowserState>
  >();
beforeEach(() => {
  current = blank();
  listener = undefined;
  invoke = vi.fn(async (command) => {
    if (command.action === "navigate")
      current = {
        ...current,
        tabs: [{ ...current.tabs[0]!, url: command.url!, loading: true }],
      };
    if (command.action === "allow-control") {
      current = {
        ...current,
        control: {
          connected: true,
          pending: false,
          active: true,
          epoch: 1,
          runId: command.runId || null,
          generation: command.generation || null,
          clientMessageId: command.clientMessageId || null,
          origins: command.origins || [],
          error: null,
        },
      };
      listener?.(current);
    }
    if (command.action === "take-over") {
      if (current.control)
        current = {
          ...current,
          control: {
            ...current.control,
            active: false,
            clientMessageId: null,
            runId: null,
          },
        };
      listener?.(current);
    }
    return current;
  });
  mount = vi.fn(async () => current);
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  vi.stubGlobal("openfoxDesktop", {
    browserMount: mount,
    browserCommand: invoke,
    onBrowserState: (callback: typeof listener) => {
      listener = callback;
      return () => {
        listener = undefined;
      };
    },
  });
});
afterEach(() => vi.unstubAllGlobals());
function Fixture({ conversationId }: { conversationId: string }) {
  const session = useBrowserSession("org-a", conversationId, true);
  return <BrowserPanel session={session} />;
}
const panel = (conversationId = "conversation-a") => (
  <I18nProvider initialLocale="zh-CN">
    <Fixture conversationId={conversationId} />
  </I18nProvider>
);

it("mounts without navigating, submits the entered address and hides native content on unmount", async () => {
  const user = userEvent.setup();
  const view = render(panel());
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "前往" })).toBeEnabled(),
  );
  expect(invoke.mock.calls.some(([call]) => call.action === "navigate")).toBe(
    false,
  );
  await user.type(
    screen.getByRole("textbox", { name: "输入网址" }),
    "https://example.com/page{Enter}",
  );
  expect(invoke).toHaveBeenCalledWith({
    lease: "lease-a",
    action: "navigate",
    tabId: "tab-a",
    url: "https://example.com/page",
  });
  expect(screen.getByRole("button", { name: "停止加载" })).toBeEnabled();
  expect(screen.getByRole("button", { name: "批注" })).toHaveAttribute(
    "aria-pressed",
    "false",
  );
  view.unmount();
  expect(invoke).toHaveBeenCalledWith({ lease: "lease-a", action: "hide" });
});

it("ignores another conversation's events and disables controls after identity reset", async () => {
  render(panel());
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "前往" })).toBeEnabled(),
  );
  act(() =>
    listener?.({
      ...blank("different-lease"),
      tabs: [{ ...blank().tabs[0]!, title: "Wrong conversation" }],
    }),
  );
  expect(screen.queryByText("Wrong conversation")).not.toBeInTheDocument();
  act(() => listener?.({ ...current, reset: true, tabs: [] }));
  expect(screen.getByRole("alert")).toHaveTextContent("登录或网络设置已变化");
  expect(screen.getByRole("button", { name: "前往" })).toBeDisabled();
});

it("a late mount from a removed conversation is hidden without replacing the current session", async () => {
  let resolveOld!: (state: DesktopBrowserState) => void;
  mount
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveOld = resolve;
        }),
    )
    .mockResolvedValueOnce(blank("lease-b"));
  const view = render(panel());
  expect(screen.getByRole("textbox", { name: "输入网址" })).toBeDisabled();
  view.rerender(panel("conversation-b"));
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "前往" })).toBeEnabled(),
  );
  await act(async () => resolveOld(blank("lease-old")));
  expect(invoke).toHaveBeenCalledWith({ lease: "lease-old", action: "hide" });
  await userEvent
    .setup()
    .type(
      screen.getByRole("textbox", { name: "输入网址" }),
      "https://example.com{Enter}",
    );
  await waitFor(() =>
    expect(invoke).toHaveBeenCalledWith({
      lease: "lease-b",
      action: "navigate",
      tabId: "tab-a",
      url: "https://example.com",
    }),
  );
});

it("grants only the next explicitly sent message and preserves control during an Agent tab switch", async () => {
  current = {
    ...blank(),
    tabs: [{ ...blank().tabs[0]!, url: "https://example.test/page" }],
  };
  render(panel());
  const allow = await screen.findByRole("button", {
    name: "允许本轮使用浏览器",
  });
  await waitFor(() => expect(allow).toBeEnabled());
  await userEvent.setup().click(allow);
  expect(screen.getByText("下一条消息可使用浏览器")).toBeInTheDocument();
  expect(
    invoke.mock.calls.some(([call]) => call.action === "allow-control"),
  ).toBe(false);
  await act(() =>
    grantBrowserForMessage("org-a", "conversation-a", "message-one"),
  );
  expect(invoke).toHaveBeenCalledWith({
    lease: "lease-a",
    action: "allow-control",
    clientMessageId: "message-one",
    origins: ["https://example.test"],
    replace: false,
  });
  consumeBrowserDraft("org-a", "conversation-a", "message-one");
  const grants = () =>
    invoke.mock.calls.filter(([call]) => call.action === "allow-control")
      .length;
  await grantBrowserForMessage("org-a", "conversation-a", "message-two");
  expect(grants()).toBe(1);
  const hides = () =>
    invoke.mock.calls.filter(([call]) => call.action === "hide").length;
  const before = hides();
  act(() =>
    listener?.({
      ...current,
      activeTab: "tab-b",
      tabs: [
        ...current.tabs,
        {
          ...current.tabs[0]!,
          id: "tab-b",
          url: "https://example.test/second",
        },
      ],
    }),
  );
  expect(hides()).toBe(before);
  expect(screen.getByRole("button", { name: "接管" })).toBeInTheDocument();
  await userEvent.setup().click(screen.getByRole("button", { name: "接管" }));
  expect(invoke).toHaveBeenCalledWith({
    lease: "lease-a",
    action: "take-over",
  });
});

it("drops next-message intent when its browser panel is closed", async () => {
  current = {
    ...blank(),
    tabs: [{ ...blank().tabs[0]!, url: "https://example.test" }],
  };
  const view = render(panel());
  const allow = await screen.findByRole("button", {
    name: "允许本轮使用浏览器",
  });
  await waitFor(() => expect(allow).toBeEnabled());
  await userEvent.setup().click(allow);
  view.unmount();
  await grantBrowserForMessage("org-a", "conversation-a", "message");
  expect(
    invoke.mock.calls.some(([call]) => call.action === "allow-control"),
  ).toBe(false);
});

it.each([
  ["NETWORK_DENIED", true, "该地址被网络访问策略阻止"],
  ["NETWORK_DENIED", false, "该地址被网络访问策略阻止"],
  ["PROTECTED_ENDPOINT", false, "该地址属于受保护的本机服务，无法访问"],
  [
    "PUBLIC_DNS_FAILED",
    false,
    "代理返回了虚拟地址，暂时无法解析真实网站地址。请检查代理连接后重试。",
  ],
  ["DNS_FAILED", false, "无法解析网站地址，请检查网址和网络后重试"],
])(
  "offers a local grant only for eligible private addresses (%s, %s)",
  async (error, canAllowLocal, message) => {
    current.tabs[0] = {
      ...current.tabs[0]!,
      url: "https://baidu.com/",
      error,
      canAllowLocal,
    };
    render(panel());
    await screen.findByText(message);
    expect(
      Boolean(screen.queryByRole("button", { name: "允许访问此本地地址" })),
    ).toBe(canAllowLocal);
  },
);

it("shows the host until focused, edits the full address and cancels edits with Escape", async () => {
  current.tabs[0]!.url = "https://example.com/path?q=1";
  render(panel());
  const user = userEvent.setup();
  const input = screen.getByRole("textbox", { name: "输入网址" });
  await waitFor(() => expect(input).toHaveValue("example.com"));
  await user.click(input);
  expect(input).toHaveValue("https://example.com/path?q=1");
  await user.clear(input);
  await user.type(input, "https://example.org/draft{Escape}");
  expect(input).toHaveValue("example.com");
  await user.click(input);
  expect(input).toHaveValue("https://example.com/path?q=1");
  expect(invoke.mock.calls.some(([call]) => call.action === "navigate")).toBe(
    false,
  );
});

it("only enables DOM element selection after explicitly clicking annotation", async () => {
  current.tabs[0]!.url = "https://example.com";
  current.annotation = {
    enabled: false,
    mode: "region",
    frozen: false,
    error: null,
    selection: null,
  };
  render(panel());
  const picker = screen.getByRole("button", { name: "批注" });
  await waitFor(() => expect(picker).toBeEnabled());
  expect(picker).toHaveAttribute("aria-pressed", "false");
  expect(invoke.mock.calls.some(([call]) => call.action === "annotate")).toBe(
    false,
  );
  await userEvent.setup().click(picker);
  expect(invoke).toHaveBeenCalledWith({
    lease: "lease-a",
    action: "annotate",
    enabled: true,
    mode: "element",
  });
  expect(screen.queryByRole("combobox", { name: "选择方式" })).toBeNull();
});

it("keeps control, download reveal and confirmed site-data clearing accessible in More", async () => {
  current.tabs[0]!.url = "https://example.com";
  current.downloads = [
    {
      id: "file-a",
      filename: "report.pdf",
      state: "completed",
      received: 20,
      total: 20,
    },
  ];
  const confirm = vi
    .spyOn(window, "confirm")
    .mockReturnValueOnce(false)
    .mockReturnValueOnce(true);
  render(panel());
  const user = userEvent.setup();
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "批注" })).toBeEnabled(),
  );
  const more = () =>
    user.click(screen.getByRole("button", { name: "浏览器更多操作" }));
  await more();
  await user.click(
    screen.getByRole("menuitem", { name: "允许本轮使用浏览器" }),
  );
  expect(screen.getByRole("status")).toHaveTextContent("下一条消息");
  await more();
  await user.click(screen.getByRole("menuitem", { name: /report.pdf/ }));
  expect(invoke).toHaveBeenCalledWith({
    lease: "lease-a",
    action: "reveal-download",
    downloadId: "file-a",
  });
  await more();
  await user.click(screen.getByRole("menuitem", { name: "清除网站数据" }));
  expect(invoke.mock.calls.some(([call]) => call.action === "clear")).toBe(
    false,
  );
  await more();
  await user.click(screen.getByRole("menuitem", { name: "清除网站数据" }));
  expect(invoke).toHaveBeenCalledWith({ lease: "lease-a", action: "clear" });
  confirm.mockRestore();
});
