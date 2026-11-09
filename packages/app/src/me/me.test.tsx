import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App, createQueryClient } from "../App";
import { chatBase, message, summary } from "../conversations/fixtures";
import { keys } from "../context";
import { backend, json, ORG, type Override } from "../tasks/test-backend";

// 个人资料、偏好设置、快捷键与主题的全局应用。

const NEW_EMAIL = "new@openfox.dev";
const TAKEN_EMAIL = "taken@openfox.dev";

const apiErr = (status: number, code: string) =>
  json(status, { error: { code, message: code, request_id: "r" } });

const challenge = (id: string) => ({
  challenge_id: id,
  expires_at: "2026-10-30T12:10:00Z",
  resend_after: new Date(Date.now() + 60_000).toISOString(),
});

interface Account {
  me: Record<string, unknown>;
  prefs: Record<string, unknown>;
}

function freshAccount(prefs: Record<string, unknown> = {}): Account {
  return {
    me: {
      id: "u1",
      email: "lin@openfox.dev",
      full_name: "",
      bio: "",
      avatar_url: null,
    },
    prefs: {
      theme: "system",
      density: "comfortable",
      locale: null,
      outline_color: 0,
      open_session_on_assign: true,
      open_session_on_quick_command: true,
      send_shortcut: "enter",
      command_shortcuts: {},
      version: 2,
      ...prefs,
    },
  };
}

/** 模拟账号接口：资料、偏好（带 version 校验）与修改邮箱四步。 */
function accountRoute(account: Account): Override {
  return (call) => {
    const body = call.body as Record<string, unknown> | undefined;
    if (call.path === "/v1/me" && call.method === "GET")
      return json(200, account.me);
    if (call.path === "/v1/me" && call.method === "PATCH") {
      account.me = { ...account.me, ...body };
      return json(200, account.me);
    }
    if (call.path === "/v1/me/avatar") {
      account.me = { ...account.me, avatar_url: "/api/v1/users/u1/avatar?v=1" };
      return json(200, account.me);
    }
    if (call.path === "/v1/me/preferences" && call.method === "GET")
      return json(200, account.prefs);
    if (call.path === "/v1/me/preferences" && call.method === "PATCH") {
      const { version, ...patch } = body as { version: number };
      if (version !== account.prefs.version)
        return apiErr(409, "VERSION_CONFLICT");
      account.prefs = { ...account.prefs, ...patch, version: version + 1 };
      return json(200, account.prefs);
    }
    if (call.path === "/v1/me/email-change/start")
      return json(200, challenge("c1"));
    if (call.path === "/v1/me/email-change/verify-current")
      return new Response(null, { status: 204 });
    if (call.path === "/v1/me/email-change/request-new") {
      if (body?.email === TAKEN_EMAIL) return apiErr(409, "EMAIL_TAKEN");
      return json(200, challenge("c2"));
    }
    if (call.path === "/v1/me/email-change/confirm") {
      account.me = { ...account.me, email: NEW_EMAIL };
      return json(200, account.me);
    }
    return undefined;
  };
}

function renderApp(
  path: string,
  extra: Override = () => undefined,
  prefs: Record<string, unknown> = {},
) {
  const account = freshAccount(prefs);
  const route = accountRoute(account);
  const server = backend((call) => extra(call) ?? route(call));
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  const view = render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return { server, queryClient, account, unmount: view.unmount };
}

const profile = `/o/${ORG}/settings/profile`;
const preferences = `/o/${ORG}/settings/preferences`;
const shortcuts = `/o/${ORG}/settings/shortcuts`;

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
  // jsdom 没有 matchMedia：系统主题按浅色处理
  window.matchMedia = vi.fn((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })) as unknown as typeof window.matchMedia;
});

afterEach(() => {
  localStorage.clear();
  delete document.documentElement.dataset.theme;
});

describe("个人资料", () => {
  it("全名失焦保存；清空后失焦不发请求、还原并提示", async () => {
    const user = userEvent.setup();
    const { server } = renderApp(profile);
    const name = await screen.findByRole("textbox", { name: "全名" });
    await user.type(name, "林小北");
    await user.tab();
    await waitFor(() => expect(server.find("PATCH", "/v1/me")).toHaveLength(1));
    expect(server.find("PATCH", "/v1/me")[0]!.body).toEqual({
      full_name: "林小北",
    });
    expect(await screen.findByText("资料已更新")).toBeInTheDocument();

    await user.clear(name);
    await user.tab();
    expect(await screen.findByText("全名不能为空")).toBeInTheDocument();
    expect(server.find("PATCH", "/v1/me")).toHaveLength(1);
    expect(name).toHaveValue("林小北");
  });

  it("简介在弹窗里编辑；内容没变化直接关闭，不发请求", async () => {
    const user = userEvent.setup();
    const { server } = renderApp(profile);
    await user.click(await screen.findByRole("button", { name: "编辑简介" }));
    let dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByRole("textbox"), "做研发");
    await user.click(within(dialog).getByRole("button", { name: "保存更改" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/v1/me")[0]?.body).toEqual({
        bio: "做研发",
      }),
    );

    await user.click(await screen.findByRole("button", { name: "编辑简介" }));
    dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "保存更改" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(server.find("PATCH", "/v1/me")).toHaveLength(1);
  });

  it("头像超过 5 MB 直接提示，不发上传请求", async () => {
    const user = userEvent.setup();
    const { server } = renderApp(profile);
    await screen.findByRole("button", { name: "更换头像" });
    const input =
      document.querySelector<HTMLInputElement>('input[type="file"]')!;
    await user.upload(
      input,
      new File([new Uint8Array(5 * 1024 * 1024 + 1)], "big.png", {
        type: "image/png",
      }),
    );
    expect(await screen.findByText("上传头像失败")).toBeInTheDocument();
    expect(server.find("PUT", "/v1/me/avatar")).toHaveLength(0);
  });

  it("头像上传成功后提示并刷新资料", async () => {
    const user = userEvent.setup();
    const { server } = renderApp(profile);
    await screen.findByRole("button", { name: "更换头像" });
    const input =
      document.querySelector<HTMLInputElement>('input[type="file"]')!;
    await user.upload(
      input,
      new File(["png"], "me.png", { type: "image/png" }),
    );
    expect(await screen.findByText("头像已更新")).toBeInTheDocument();
    expect(server.find("PUT", "/v1/me/avatar")).toHaveLength(1);
  });

  it("修改邮箱：验证当前邮箱 → 新邮箱发码 → 确认修改", async () => {
    const user = userEvent.setup();
    const { server } = renderApp(profile);
    await openChangeDialog(user, server);
    const change = await screen.findByRole("dialog");
    await user.type(screen.getByLabelText("新邮箱"), NEW_EMAIL);
    await user.click(within(change).getByRole("button", { name: "发送" }));
    await waitFor(() =>
      expect(server.find("POST", "/request-new")).toHaveLength(1),
    );
    expect(server.find("POST", "/request-new")[0]!.body).toEqual({
      challenge_id: "c1",
      email: NEW_EMAIL,
    });
    expect(
      await within(change).findByRole("button", { name: "60s" }),
    ).toBeDisabled();

    await enterCode(user, "654321");
    await user.click(screen.getByRole("button", { name: "保存更改" }));
    await waitFor(() =>
      expect(server.find("POST", "/confirm")).toHaveLength(1),
    );
    expect(server.find("POST", "/confirm")[0]!.body).toEqual({
      challenge_id: "c2",
      code: "654321",
    });
    expect(await screen.findByText("邮箱已修改")).toBeInTheDocument();
  });

  it("新邮箱已被占用时提示「该邮箱已被占用」", async () => {
    const user = userEvent.setup();
    const { server } = renderApp(profile);
    await openChangeDialog(user, server);
    await user.type(screen.getByLabelText("新邮箱"), TAKEN_EMAIL);
    await user.click(screen.getByRole("button", { name: "发送" }));
    expect(await screen.findByText("该邮箱已被占用")).toBeInTheDocument();
  });

  it("「解绑」始终禁用", async () => {
    renderApp(profile);
    expect(await screen.findByRole("button", { name: "解绑" })).toBeDisabled();
  });
});

/** 打开「修改」→ 验证身份弹窗，并输入正确的验证码进入修改邮箱。 */
async function openChangeDialog(
  user: ReturnType<typeof userEvent.setup>,
  server: ReturnType<typeof renderApp>["server"],
) {
  await user.click(await screen.findByRole("button", { name: "修改" }));
  await waitFor(() =>
    expect(server.find("POST", "/email-change/start")).toHaveLength(1),
  );
  await enterCode(user, "123456");
  await user.click(screen.getByRole("button", { name: "下一步" }));
  await waitFor(() =>
    expect(server.find("POST", "/verify-current")).toHaveLength(1),
  );
  expect(server.find("POST", "/verify-current")[0]!.body).toEqual({
    challenge_id: "c1",
    code: "123456",
  });
  expect(await screen.findByText("修改邮箱")).toBeInTheDocument();
}

/** 在当前弹窗的验证码组里粘贴 6 位数字。 */
async function enterCode(
  user: ReturnType<typeof userEvent.setup>,
  code: string,
) {
  const group = screen.getByRole("group", { name: "验证码" });
  const digits = within(group).getAllByRole("textbox");
  await user.click(digits[0]!);
  await user.paste(code);
}

describe("偏好设置", () => {
  it("密度保存中禁用选择器，重新聚焦不覆盖未完成的乐观修改", async () => {
    const user = userEvent.setup();
    const { server } = renderApp(preferences);
    let finish!: () => void;
    const pending = new Promise<void>((resolve) => {
      finish = resolve;
    });
    const original = server.api.me.preferences.update;
    vi.spyOn(server.api.me.preferences, "update").mockImplementation(
      async (input) => {
        await pending;
        return original(input);
      },
    );
    await user.click(await screen.findByRole("button", { name: "密度" }));
    await user.click(await screen.findByRole("option", { name: "紧凑" }));
    await waitFor(() =>
      expect(document.documentElement.dataset.density).toBe("compact"),
    );
    expect(screen.getByRole("button", { name: "密度" })).toBeDisabled();
    const reads = server.find("GET", "/v1/me/preferences").length;
    fireEvent.focus(window);
    expect(server.find("GET", "/v1/me/preferences")).toHaveLength(reads);
    finish();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "密度" })).toBeEnabled(),
    );
    expect(document.documentElement.dataset.density).toBe("compact");
  });

  it("旧偏好响应缺少密度时使用标准模式", async () => {
    renderApp(preferences, () => undefined, { density: undefined });
    expect(
      await screen.findByRole("button", { name: "密度" }),
    ).toHaveTextContent("标准");
    await waitFor(() =>
      expect(document.documentElement.dataset.density).toBe("comfortable"),
    );
  });

  it("密度即时应用并带版本保存，重新聚焦读取其他设备修改", async () => {
    const user = userEvent.setup();
    const { server, account } = renderApp(preferences);
    await user.click(await screen.findByRole("button", { name: "密度" }));
    await user.click(await screen.findByRole("option", { name: "紧凑" }));
    await waitFor(() =>
      expect(document.documentElement.dataset.density).toBe("compact"),
    );
    await waitFor(() => expect(account.prefs.version).toBe(3));
    expect(server.find("PATCH", "/v1/me/preferences")[0]?.body).toEqual({
      density: "compact",
      version: 2,
    });
    account.prefs = { ...account.prefs, density: "comfortable", version: 4 };
    fireEvent.focus(window);
    await waitFor(() =>
      expect(document.documentElement.dataset.density).toBe("comfortable"),
    );
    expect(screen.getByRole("button", { name: "密度" })).toHaveTextContent(
      "标准",
    );
    expect(server.find("PATCH", "/v1/me/preferences")).toHaveLength(1);
  });

  it.each([500, 409])(
    "密度保存失败 %s 时回滚并重新读取账号偏好",
    async (status) => {
      const user = userEvent.setup();
      renderApp(preferences, (call) =>
        call.path === "/v1/me/preferences" && call.method === "PATCH"
          ? apiErr(
              status,
              status === 409 ? "VERSION_CONFLICT" : "INTERNAL_ERROR",
            )
          : undefined,
      );
      await user.click(await screen.findByRole("button", { name: "密度" }));
      await user.click(await screen.findByRole("option", { name: "紧凑" }));
      await waitFor(() =>
        expect(document.documentElement.dataset.density).toBe("comfortable"),
      );
      expect(screen.getByRole("button", { name: "密度" })).toHaveTextContent(
        "标准",
      );
      expect(await screen.findByRole("status")).toBeInTheDocument();
    },
  );

  it("登录加载账号密度，离开登录区清除全局密度", async () => {
    const { unmount } = renderApp(preferences, () => undefined, {
      density: "compact",
    });
    await screen.findByRole("button", { name: "密度" });
    await waitFor(() =>
      expect(document.documentElement.dataset.density).toBe("compact"),
    );
    unmount();
    await waitFor(() =>
      expect(document.documentElement.dataset.density).toBeUndefined(),
    );
  });

  it("点选深色即时应用到 <html>，并带当前 version 保存", async () => {
    const user = userEvent.setup();
    const { server } = renderApp(preferences);
    await user.click(await screen.findByRole("radio", { name: "深色" }));
    await waitFor(() =>
      expect(document.documentElement.dataset.theme).toBe("dark"),
    );
    expect(server.find("PATCH", "/v1/me/preferences")[0]!.body).toEqual({
      theme: "dark",
      version: 2,
    });
  });

  it.each([
    ["魔法蓝", "magic-blue"],
    ["经典深色", "classic-dark"],
  ])("保存并应用%s", async (label, theme) => {
    const user = userEvent.setup();
    const { server } = renderApp(preferences);
    await user.click(await screen.findByRole("radio", { name: label }));
    await waitFor(() =>
      expect(document.documentElement.dataset.palette).toBe(theme),
    );
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(server.find("PATCH", "/v1/me/preferences")[0]?.body).toEqual({
      theme,
      version: 2,
    });
  });

  it("外边框色点选后保存，并覆盖外壳底色", async () => {
    const user = userEvent.setup();
    const { server } = renderApp(preferences);
    await user.click(
      await screen.findByRole("radio", { name: "内置外边框颜色 3" }),
    );
    await waitFor(() =>
      expect(server.find("PATCH", "/v1/me/preferences")[0]?.body).toEqual({
        outline_color: 3,
        version: 2,
      }),
    );
    await waitFor(() =>
      expect(
        document
          .querySelector<HTMLElement>(".h-screen")
          ?.style.getPropertyValue("--color-surface-app"),
      ).toBe("#ffe4e6"),
    );
  });

  it("语言同步失败时提示，本机语言已切换", async () => {
    const user = userEvent.setup();
    renderApp(preferences, (call) =>
      call.method === "PATCH" &&
      call.path === "/v1/me/preferences" &&
      "locale" in (call.body as object)
        ? apiErr(500, "INTERNAL")
        : undefined,
    );
    await user.click(await screen.findByRole("button", { name: "语言" }));
    await user.click(await screen.findByRole("option", { name: "English" }));
    expect(
      await screen.findByText(
        "Language saved on this device, but couldn't sync to your account. Other devices may still show the old language.",
      ),
    ).toBeInTheDocument();
  });

  it("任务会话开关保存失败时回滚并提示", async () => {
    const user = userEvent.setup();
    renderApp(preferences, (call) =>
      call.method === "PATCH" &&
      call.path === "/v1/me/preferences" &&
      "open_session_on_assign" in (call.body as object)
        ? apiErr(500, "INTERNAL")
        : undefined,
    );
    const toggle = await screen.findByRole("switch", {
      name: "指派给 Agent 后自动打开会话",
    });
    expect(toggle).toHaveAttribute("aria-checked", "true");
    await user.click(toggle);
    expect(
      await screen.findByText("保存失败，开关已恢复为原来的状态。"),
    ).toBeInTheDocument();
    await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"));
  });
});

describe("快捷键", () => {
  it("发送方式切换到 Shift+Enter 发送，带 version 保存", async () => {
    const user = userEvent.setup();
    const { server } = renderApp(shortcuts);
    const option = await screen.findByRole("radio", {
      name: /Shift\+Enter 发送/,
    });
    await user.click(option);
    await waitFor(() =>
      expect(server.find("PATCH", "/v1/me/preferences")[0]?.body).toEqual({
        send_shortcut: "shift_enter",
        version: 2,
      }),
    );
    expect(option).toHaveAttribute("aria-checked", "true");
  });

  it("录制冲突时提示并保持录制，成功后保存自定义按键", async () => {
    const user = userEvent.setup();
    const { server } = renderApp(shortcuts);
    const record = (await screen.findAllByRole("button", { name: "录制" }))[0]!;
    await user.click(record);
    expect(record).toHaveTextContent("按下按键");

    // ⌘⇧M 已是「将任务移动到空间」的默认键
    fireEvent.keyDown(window, {
      key: "m",
      code: "KeyM",
      metaKey: true,
      shiftKey: true,
    });
    expect(
      await screen.findByText("已被 将任务移动到空间 使用"),
    ).toBeInTheDocument();
    expect(record).toHaveTextContent("按下按键");

    fireEvent.keyDown(window, { key: "j", code: "KeyJ", metaKey: true });
    await waitFor(() =>
      expect(server.find("PATCH", "/v1/me/preferences")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/v1/me/preferences")[0]!.body).toEqual({
      command_shortcuts: { command_menu: "mod+j" },
      version: 2,
    });
  });

  it("录制时不带 ⌘ 或 Ctrl 提示，不支持的键提示", async () => {
    const user = userEvent.setup();
    renderApp(shortcuts);
    const record = (await screen.findAllByRole("button", { name: "录制" }))[0]!;
    await user.click(record);
    fireEvent.keyDown(window, { key: "j", code: "KeyJ" });
    expect(
      await screen.findByText("请按下 Command 或 Ctrl 加另一个键的快捷键"),
    ).toBeInTheDocument();
    fireEvent.keyDown(window, {
      key: "ArrowUp",
      code: "ArrowUp",
      metaKey: true,
    });
    expect(await screen.findByText("不支持的快捷键")).toBeInTheDocument();
  });

  it("重置去掉自定义按键，整体替换 command_shortcuts", async () => {
    const user = userEvent.setup();
    const { server } = renderApp(shortcuts, undefined, {
      command_shortcuts: { command_menu: "mod+j" },
    });
    const reset = (await screen.findAllByRole("button", { name: "重置" }))[0]!;
    await waitFor(() => expect(reset).toBeEnabled());
    await user.click(reset);
    await waitFor(() =>
      expect(server.find("PATCH", "/v1/me/preferences")[0]?.body).toEqual({
        command_shortcuts: {},
        version: 2,
      }),
    );
  });
});

describe("会话输入框的发送方式", () => {
  it("Shift+Enter 发送模式下 Enter 只换行，Shift+Enter 发送", async () => {
    const user = userEvent.setup();
    const chat = chatBase((call) => {
      if (
        call.method === "POST" &&
        call.path === `/v1/organizations/${ORG}/conversations/c1/messages`
      ) {
        const body = call.body as { body: string; client_message_id: string };
        return json(201, message(10, body));
      }
      if (call.path === `/v1/organizations/${ORG}/conversations/c1/messages`)
        return json(200, { messages: [], next_cursor: 0 });
      if (call.path === `/v1/organizations/${ORG}/conversations/c1`)
        return json(200, summary("c1"));
      if (call.path === `/v1/organizations/${ORG}/conversations`)
        return json(200, { conversations: [summary("c1")], next_cursor: null });
      return undefined;
    });
    const { server, queryClient } = renderApp("/o/o1/chat/c1", chat, {
      send_shortcut: "shift_enter",
    });
    await waitFor(() =>
      expect(queryClient.getQueryData(keys.preferences)).toBeDefined(),
    );
    const box = await screen.findByRole("textbox", { name: "输入消息" });
    await user.type(box, "第一行{Enter}");
    expect(box).toHaveValue("第一行\n");
    expect(server.find("POST", "/messages")).toHaveLength(0);

    await user.type(box, "第二行{Shift>}{Enter}{/Shift}");
    await waitFor(() =>
      expect(server.find("POST", "/messages")).toHaveLength(1),
    );
    expect(server.find("POST", "/messages")[0]!.body).toMatchObject({
      body: "第一行\n第二行",
    });
  });
});
