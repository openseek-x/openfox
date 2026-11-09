import {
  render,
  screen,
  waitFor,
  within,
  fireEvent,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@openfox/api";
import { agent } from "../fixtures";
import { AgentTools } from "./AgentTools";
import type { CreateAgentToolInput } from "./agentToolsModel";

const mock = vi.hoisted(() => ({ useConnections: vi.fn() }));
vi.mock("./tool-connections", () => ({
  useAgentToolConnections: mock.useConnections,
}));
vi.mock("./tool-catalog", () => ({
  toolCatalog: [
    {
      id: "docs",
      name: "Docs",
      description: "Read project documents",
      kind: "MCP",
      category: "办公协作",
      endpoint: "https://docs.example.test/mcp",
    },
    {
      id: "custom-git",
      name: "Git Service",
      description: "Self-hosted repository",
      kind: "MCP",
      category: "开发工具",
    },
    {
      id: "apifox",
      name: "Example CLI",
      description: "CLI",
      kind: "CLI",
      category: "开发工具",
    },
  ],
}));
const submit = vi.fn<(input: CreateAgentToolInput) => Promise<void>>();
const defaults = () => ({
  canManage: true,
  canUseDevice: true,
  connections: [],
  inventory: [
    {
      name: "project",
      transport: "stdio",
      source: "User config",
      enabled: true,
      active: false,
    },
  ],
  inventoryState: "ready",
  refreshInventory: vi.fn(),
  submit,
  setInherited: vi.fn(),
  startOAuth: vi.fn(),
  supportsOAuth: true,
  supportedTransports: ["http", "stdio"],
  disconnect: vi.fn(),
  refresh: vi.fn(),
  oauthRedirectUri: "https://openfox.example.test/callback",
});
function mount(extra: Record<string, unknown> = {}) {
  const state = { ...defaults(), ...extra };
  mock.useConnections.mockReturnValue(state);
  render(<AgentTools orgId="o1" agent={agent("a1")} />);
  return state;
}
beforeEach(() => {
  submit.mockReset().mockResolvedValue();
});
const user = () => userEvent.setup();
async function openCustom(u: ReturnType<typeof user>) {
  await u.click(screen.getByRole("button", { name: "添加连接" }));
  await u.click(screen.getByRole("button", { name: "添加自定义 MCP" }));
}
describe("Agent 工具连接", () => {
  it("读者可看继承清单；无管理与设备权限时不能添加或改本机工具", () => {
    mount({ canManage: false, canUseDevice: false, setInherited: undefined });
    expect(screen.getByText("project")).toBeVisible();
    expect(screen.getByText("STDIO · User config")).toBeVisible();
    expect(screen.queryByRole("button", { name: "添加连接" })).toBeNull();
    expect(screen.queryByRole("switch")).toBeNull();
  });
  it("目录按类别和关键字筛选；详情、配置步骤逐层返回，保留筛选", async () => {
    mount();
    const u = user();
    await u.click(screen.getByRole("button", { name: "添加连接" }));
    await u.click(screen.getByRole("button", { name: "开发工具" }));
    await u.type(screen.getByRole("textbox", { name: "搜索连接" }), "git");
    expect(screen.queryByRole("button", { name: /Example CLI/ })).toBeNull();
    await u.click(screen.getByRole("button", { name: /Git Service/ }));
    await u.click(screen.getByRole("button", { name: "连接" }));
    expect(screen.getByRole("textbox", { name: "服务器地址" })).toHaveValue("");
    await u.keyboard("{Escape}");
    expect(screen.getByRole("button", { name: "连接" })).toBeVisible();
    await u.keyboard("{Escape}");
    expect(screen.getByRole("textbox", { name: "搜索连接" })).toHaveValue(
      "git",
    );
  });
  it("HTTP API Key、高级 Header 与连接字段进入真实请求，失败保留草稿", async () => {
    submit
      .mockRejectedValueOnce(new Error("Connection failed"))
      .mockResolvedValue();
    mount();
    const u = user();
    await openCustom(u);
    const button = screen.getByRole("button", { name: "连接" });
    expect(button).toBeDisabled();
    await u.type(screen.getByRole("textbox", { name: "名称" }), "docs");
    await u.type(
      screen.getByRole("textbox", { name: "服务器地址" }),
      "https://mcp.example.test/api",
    );
    await u.click(screen.getByRole("button", { name: "API Key" }));
    await u.type(
      screen.getByLabelText("凭证", { exact: true }),
      "token-for-test",
    );
    await u.click(screen.getByRole("button", { name: "高级设置" }));
    await u.click(screen.getByRole("button", { name: "添加请求头" }));
    await u.type(screen.getByLabelText("额外请求头 名称 1"), "X-Scope");
    await u.type(screen.getByLabelText("额外请求头 值 1"), "docs");
    await u.click(button);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Connection failed",
    );
    expect(screen.getByLabelText("凭证", { exact: true })).toHaveValue(
      "token-for-test",
    );
    await u.click(button);
    expect(submit).toHaveBeenCalledWith({
      catalogId: "custom",
      name: "docs",
      transport: "http",
      scope: "PUBLIC",
      url: "https://mcp.example.test/api",
      authMode: "api_key",
      authHeader: "Authorization",
      credential: "token-for-test",
      headers: { "X-Scope": "docs" },
    });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
  it("CLI 显示官方设置与核验，明确授权后才保存受限本机绑定", async () => {
    mount();
    const u = user();
    await u.click(screen.getByRole("button", { name: "添加连接" }));
    await u.click(screen.getByRole("button", { name: /Example CLI/ }));
    await u.click(screen.getByRole("button", { name: "连接" }));
    expect(screen.getByText("apifox auth login")).toBeVisible();
    expect(screen.getByText("apifox project list")).toBeVisible();
    expect(
      screen.getByRole("link", { name: "官方安装与授权说明" }),
    ).toHaveAttribute("href", "https://docs.apifox.com/install-and-run-cli");
    const connect = screen.getByRole("button", { name: "连接" });
    expect(connect).toBeDisabled();
    await u.click(screen.getByRole("checkbox"));
    await u.click(connect);
    expect(submit).toHaveBeenCalledWith({
      catalogId: "apifox",
      name: "apifox",
      scope: "DEVICE_OWNER",
      transport: "stdio",
      command: "openfox:provider-cli",
      args: ["apifox"],
      authMode: "none",
    });
  });
  it("共享电脑的管理者可看官方说明，无权同意 CLI 授权", async () => {
    mount({ canUseDevice: false, supportedTransports: ["http"] });
    const u = user();
    await u.click(screen.getByRole("button", { name: "添加连接" }));
    await u.click(screen.getByRole("button", { name: /Example CLI/ }));
    await u.click(screen.getByRole("button", { name: "连接" }));
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.getByRole("button", { name: "连接" })).toBeDisabled();
  });
  it("更换已保存连接地址会要求新凭证，原地址的脱敏 Header 不会转发", async () => {
    mount({
      connections: [
        {
          id: "one",
          catalogId: "custom",
          name: "saved",
          status: "connected",
          transport: "http",
          config: {
            authMode: "api_key",
            credential: "••••••",
            url: "https://old.example.test/mcp",
            headers: { "X-Secret": "••••••" },
          },
        },
      ],
    });
    const u = user();
    await u.click(screen.getByRole("button", { name: "saved" }));
    await u.click(screen.getByRole("menuitem", { name: "编辑" }));
    const save = screen.getByRole("button", { name: "保存" });
    expect(save).toBeEnabled();
    fireEvent.change(screen.getByRole("textbox", { name: "服务器地址" }), {
      target: { value: "https://new.example.test/mcp" },
    });
    expect(save).toBeDisabled();
    await u.type(
      screen.getByLabelText("凭证", { exact: true }),
      "new-test-value",
    );
    await u.click(save);
    expect(submit).toHaveBeenCalledWith(
      expect.objectContaining({
        id: "one",
        url: "https://new.example.test/mcp",
        credential: "new-test-value",
        headers: { "X-Secret": "" },
      }),
    );
  });
  it("已过期的 OAuth 可以重启授权，弹窗未打开时保留当前表单", async () => {
    const startOAuth = vi.fn().mockRejectedValue(new Error("Popup blocked"));
    mount({
      startOAuth,
      connections: [
        {
          id: "one",
          catalogId: "docs",
          name: "docs",
          status: "authorizing",
          transport: "http",
          config: { authMode: "oauth", url: "https://example.test/mcp" },
        },
      ],
    });
    const u = user();
    await u.click(screen.getByRole("button", { name: "docs" }));
    await u.click(screen.getByRole("menuitem", { name: "重新授权" }));
    await u.click(screen.getByRole("button", { name: "重新授权" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Popup blocked");
    expect(startOAuth).toHaveBeenCalledWith(
      expect.objectContaining({
        id: "one",
        authMode: "oauth",
        url: "https://example.test/mcp",
      }),
    );
  });
  it("访问受限的公网地址会解释 DNS 阻止原因，表单继续允许修改", async () => {
    submit.mockRejectedValue(
      new ApiError({
        status: 422,
        code: "MCP_ADDRESS_BLOCKED",
        message: "Blocked address",
      }),
    );
    mount();
    const u = user();
    await openCustom(u);
    await u.type(screen.getByRole("textbox", { name: "名称" }), "docs");
    await u.type(
      screen.getByRole("textbox", { name: "服务器地址" }),
      "https://example.test/mcp",
    );
    await u.click(screen.getByRole("button", { name: "连接" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "此地址解析到本地或保留地址",
    );
    expect(screen.getByRole("button", { name: "连接" })).toBeEnabled();
  });
  it("从 JSON 只添加首个服务器；STDIO 将每个参数单独传入", async () => {
    mount();
    const u = user();
    await openCustom(u);
    await u.click(screen.getByRole("tab", { name: "JSON 配置" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Server 配置" }), {
      target: {
        value: JSON.stringify({
          mcpServers: {
            first: { command: ["mcp", "with spaces"], env: { A: "value" } },
            other: { url: "https://other.test" },
          },
        }),
      },
    });
    expect(screen.getByText(/共发现 2 个 Server/)).toBeVisible();
    await u.click(screen.getByRole("button", { name: "连接" }));
    expect(submit).toHaveBeenCalledWith({
      catalogId: "custom",
      name: "first",
      transport: "stdio",
      scope: "DEVICE_OWNER",
      authMode: "none",
      command: "mcp",
      args: ["with spaces"],
      env: { A: "value" },
    });
  });
  it("设备权限不足时不能提交 STDIO；同一权限也不能通过 JSON 绕过", async () => {
    mount({ canUseDevice: false, supportedTransports: ["http"] });
    const u = user();
    await openCustom(u);
    await u.click(screen.getByRole("tab", { name: "JSON 配置" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Server 配置" }), {
      target: {
        value: JSON.stringify({ mcpServers: { first: { command: "mcp" } } }),
      },
    });
    expect(screen.getByRole("button", { name: "连接" })).toBeDisabled();
    expect(submit).not.toHaveBeenCalled();
    await u.click(screen.getByRole("button", { name: "取消" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });
  it("管理者点击启用继承工具后提交真实授权，断开管理连接需明确确认", async () => {
    const state = mount({
      connections: [
        {
          id: "stored",
          catalogId: "docs",
          name: "docs",
          transport: "http",
          status: "connected",
          readTools: ["get_document"],
          resources: ["docs://index"],
        },
      ],
    });
    const u = user();
    await u.click(
      screen.getByRole("switch", { name: "对此 Agent 启用 project" }),
    );
    expect(state.setInherited).toHaveBeenCalledWith("project", true);
    await u.click(screen.getByRole("button", { name: "docs" }));
    await u.click(screen.getByRole("menuitem", { name: "断开连接" }));
    expect(state.disconnect).not.toHaveBeenCalled();
    const modal = screen.getByRole("dialog", { name: "断开 docs？" });
    await u.click(within(modal).getByRole("button", { name: "断开连接" }));
    expect(state.disconnect).toHaveBeenCalledWith("stored");
  });
});
