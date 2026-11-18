import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createApi, HttpClient } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";
import { ApiProvider } from "../context";
import { InstallGuideDialog } from "../computers/parts";
import { RuntimeDefaultsEditor } from "../computers/RuntimeDefaultsEditor";
import { runtimeSelectable } from "../computers/model";
import { RuntimeTestButton } from "./settings/RuntimeTest";
import { agent, device, entry } from "./fixtures";
import { runtimeChoices } from "./model";
import { runtimeOptions } from "./CreateAgentDialog";
import { ORG, chatBase } from "../conversations/fixtures";
import { json, renderSettings } from "../settings/testing";
import { optionsFromDevice } from "../conversations/overrides";
const base = device("d1").runtimes[0]!;
const capability = {
  ...base,
  provider: "hermes" as const,
  version: "v0.23.1",
  runnable: false,
  status: "UNAVAILABLE" as const,
  login_status: "UNKNOWN" as const,
  protocol_ready: true,
  requires_run_configuration: true,
  approval_modes: ["READ_ONLY" as const],
  collaboration_modes: ["DEFAULT" as const, "PLAN" as const],
  models_error: "Explicit model required",
};
afterEach(() => localStorage.clear());
it("keeps protocol-ready Hermes selectable without claiming login, and rejects missing readiness", () => {
  const t = ((key: string) => key) as Parameters<typeof runtimeOptions>[1];
  const ready = device("d1", { runtimes: [capability] });
  expect(
    runtimeOptions(runtimeChoices([ready], "s1", "u1"), t).find((v) =>
      v.value.endsWith(":hermes"),
    )?.disabled,
  ).toBe(false);
  expect(
    runtimeOptions(
      runtimeChoices(
        [
          device("d1", {
            runtimes: [{ ...capability, protocol_ready: false }],
          }),
        ],
        "s1",
        "u1",
      ),
      t,
    ).find((v) => v.value.endsWith(":hermes"))?.disabled,
  ).toBe(true);
  expect(
    runtimeSelectable({ ...capability, requires_run_configuration: false }),
  ).toBe(false);
  expect(runtimeSelectable({ ...capability, provider: "unrecognized" })).toBe(
    false,
  );
  expect(optionsFromDevice(ready).runtimes[0]).toMatchObject({
    runnable: false,
    protocol_ready: true,
    requires_run_configuration: true,
  });
});
it("shows exact configuration requirements, disables writable modes and arguments, and retains PLAN", async () => {
  const original = agent("a1", { name: "Hermes Agent" });
  const current = {
    ...original,
    config: {
      ...original.config!,
      runtime: "hermes",
      model: "default",
      approval_mode: "FOLLOW_RUNTIME" as const,
    },
  };
  renderSettings(
    "/o/o1/agents?agentSettings=a1",
    chatBase((call) => {
      if (call.path === `${ORG}/agent-directory`)
        return json(200, {
          entries: [entry("a1", { name: current.name, runtime: "hermes" })],
          archived_count: 0,
          truncated: false,
        });
      if (call.path === `${ORG}/devices`)
        return json(200, {
          devices: [device("d1", { runtimes: [capability] })],
          next_cursor: null,
        });
      if (call.path === `${ORG}/spaces/s1/agents/a1`) return json(200, current);
      if (call.path.endsWith("/a1/env-vars"))
        return json(200, {
          env_vars: [],
          can_view_values: true,
          secrets_available: true,
        });
      if (call.path.endsWith("/a1/repos")) return json(200, { repos: [] });
      return undefined;
    }),
  );
  const user = userEvent.setup();
  const drawer = await screen.findByRole("complementary", {
    name: "Hermes Agent 的设置",
  });
  await user.click(
    within(drawer).getByRole("button", { name: /电脑 & Runtime/ }),
  );
  expect(
    await within(drawer).findByText(/仅支持 macOS arm64 \/ v0.23.1/),
  ).toHaveTextContent("每次运行创建新会话");
  expect(within(drawer).getByText(/配置 HERMES_API_KEY/)).toHaveTextContent(
    "HERMES_CONTEXT_LENGTH",
  );
  expect(
    within(drawer).getByText(/是用户声明的模型上下文容量/),
  ).toHaveTextContent("64000–2147483647");
  expect(within(drawer).getByText(/仅支持正常公网 DNS/)).toHaveTextContent(
    "HTTPS:443",
  );
  expect(within(drawer).getByText(/设备所有者需在电脑/)).toHaveTextContent(
    "OPENFOX_HERMES_PYTHON",
  );
  await user.click(
    within(drawer).getByRole("button", { name: "权限模式默认" }),
  );
  const modes = screen.getByRole("listbox", { name: "权限模式默认" });
  const options = within(modes).getAllByRole("option");
  expect(options.map((v) => v.getAttribute("aria-disabled"))).toEqual([
    null,
    null,
    "true",
    "true",
  ]);
  await user.keyboard("{Escape}");
  await user.click(
    within(drawer).getByRole("button", { name: "协作模式默认" }),
  );
  const collaboration = screen.getByRole("listbox", { name: "协作模式默认" });
  expect(
    within(collaboration)
      .getAllByRole("option")
      .every((v) => v.getAttribute("aria-disabled") !== "true"),
  ).toBe(true);
  await user.keyboard("{Escape}");
  expect(
    within(drawer)
      .getAllByRole("button", { name: /^添加$/ })
      .at(-1),
  ).toBeDisabled();
});

it("shows manual trusted preparation with no guessed install command or link", () => {
  render(
    <I18nProvider initialLocale="zh-CN">
      <ToastProvider>
        <InstallGuideDialog open provider="hermes" onClose={() => {}} />
      </ToastProvider>
    </I18nProvider>,
  );
  const dialog = screen.getByRole("dialog");
  expect(within(dialog).getByText(/Hermes 仅支持已核验/)).toHaveTextContent(
    "OPENFOX_HERMES_ROOT",
  );
  expect(dialog).toHaveTextContent("OpenFox 不自动安装");
  expect(
    within(dialog).queryByRole("button", { name: "复制安装命令" }),
  ).not.toBeInTheDocument();
  expect(within(dialog).queryByRole("link")).not.toBeInTheDocument();
});

it("saves four model strings via existing owner defaults and rejects reserved daemon paths", async () => {
  const calls: { path: string; method: string; body?: unknown }[] = [];
  const transport = async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({
      path: String(input),
      method: init?.method ?? "GET",
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    });
    return json(200, {
      version: 1,
      model: null,
      secrets_available: true,
      env_vars: [],
    });
  };
  const cache = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={cache}>
      <I18nProvider initialLocale="zh-CN">
        <ApiProvider api={createApi(new HttpClient("/api", transport))}>
          <RuntimeDefaultsEditor orgId="o1" deviceId="d1" runtime="hermes" />
        </ApiProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Runtime 默认配置" }));
  const dialog = await screen.findByRole("dialog");
  const name = await within(dialog).findByLabelText("变量名称");
  const value = within(dialog).getByLabelText("新秘密值");
  expect(
    within(dialog).getByText(/是用户声明的模型上下文容量/),
  ).toHaveTextContent("并未验证供应商能力");
  await user.type(name, "OPENFOX_HERMES_ROOT");
  await user.type(value, "/fixture/not-a-real-install");
  await user.click(within(dialog).getByRole("button", { name: "应用变量" }));
  expect(within(dialog).getByText(/请使用合法且非保留名称/)).toBeVisible();
  await user.clear(name);
  await user.clear(value);
  const env = {
    HERMES_API_KEY: "fixture-only-key",
    HERMES_BASE_URL: "https://models.example.test/v1",
    HERMES_MODEL: "fixture-model",
    HERMES_CONTEXT_LENGTH: "65536",
  };
  for (const [key, val] of Object.entries(env)) {
    await user.type(name, key);
    await user.type(value, val);
    await user.click(within(dialog).getByRole("button", { name: "应用变量" }));
  }
  expect(
    within(dialog).queryByText("fixture-only-key"),
  ).not.toBeInTheDocument();
  await user.click(within(dialog).getByRole("button", { name: "保存" }));
  await waitFor(() =>
    expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1),
  );
  expect(calls.find((c) => c.method === "PATCH")).toMatchObject({
    path: "/api/v1/organizations/o1/devices/d1/runtime-defaults/hermes",
    body: { expected_version: 1, set_env: env, remove_env: [], model: null },
  });
});

it.each([true, false])(
  "reports Hermes protocol readiness without claiming a model connection (ok=%s)",
  async (ok) => {
    const cache = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    });
    const transport = async () =>
      json(200, {
        command: {
          id: "fixture-command",
          device_id: "d1",
          kind: "runtime_test",
          status: "SUCCEEDED",
          result: {
            provider: "hermes",
            ok,
            login_status: "UNKNOWN",
            model: "default",
            model_available: false,
            latency_ms: 12,
            message: ok ? "protocol ready" : "fixture provenance failure",
          },
        },
      });
    render(
      <QueryClientProvider client={cache}>
        <I18nProvider initialLocale="zh-CN">
          <ToastProvider>
            <ApiProvider api={createApi(new HttpClient("/api", transport))}>
              <RuntimeTestButton
                orgId="o1"
                device={device("d1", { runtimes: [capability] })}
                provider="hermes"
                model="default"
                meId="u1"
              />
            </ApiProvider>
          </ToastProvider>
        </I18nProvider>
      </QueryClientProvider>,
    );
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "测试连接" }));
    if (ok)
      expect(
        await screen.findByText(/协议就绪，运行前检查公网模型配置/),
      ).toHaveTextContent("尚未验证");
    else
      expect(
        await screen.findByText("fixture provenance failure"),
      ).toBeVisible();
    expect(screen.queryByText(/已连接 ·/)).not.toBeInTheDocument();
    expect(screen.queryByText(/模型不在列表/)).not.toBeInTheDocument();
  },
);
