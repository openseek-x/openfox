import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import {
  createApi,
  HttpClient,
  type BuglyBinding,
  type BuglyConfiguration,
  type IntegrationConnection,
  type IntegrationDetail,
} from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ApiProvider } from "../context";
import {
  apiError,
  json,
  renderSettings,
  ts,
  type Call,
} from "../settings/testing";
import { BuglySections } from "./BuglySections";

const org = "/v1/organizations/o1",
  base = `${org}/integrations/bugly`;
const initialConnection: IntegrationConnection = {
  id: "c1",
  provider: "bugly",
  scope: "ORGANIZATION",
  external_id: "inbound",
  display_name: "Bugly",
  status: "NEEDS_ATTENTION",
  status_reason: "inbound_disabled",
  version: 1,
  created_at: ts,
  updated_at: ts,
  account_kind: "",
  credentials_hint: "",
  created_by: "u1",
  created_by_email: "fixture@example.invalid",
  disconnected_at: null,
};
const initialBinding: BuglyBinding = {
  id: "b1",
  connection_id: "c1",
  product_id: "Product-Case",
  project_name: "Crash project",
  space_id: "s1",
  task_type_id: "t1",
  priority_mapping: {},
  authorized_by: "u1",
  status: "ACTIVE",
  status_reason: "",
  version: 1,
};
function detail(manage = true, configured = true): IntegrationDetail {
  return {
    provider: {
      id: "bugly",
      name: "Bugly 专业版",
      category: "task",
      availability: "available",
      template: "A",
      capabilities: ["webhook_inbound"],
      configured,
      bindable: configured,
      summary: { connections: 0, status: "none" },
    },
    can_manage: manage,
    connections: [],
    personal_connections: [],
    sync_links: [],
  };
}
function fixture(
  options: {
    empty?: boolean;
    binding?: BuglyBinding;
    status?: IntegrationConnection["status"];
    levels?: 4 | 5;
    more?: (
      call: Call,
      init?: RequestInit,
    ) => Response | Promise<Response> | undefined;
  } = {},
) {
  let config: BuglyConfiguration = {
    connection: options.empty
      ? null
      : { ...initialConnection, status: options.status ?? "NEEDS_ATTENTION" },
    bindings: options.binding ? [options.binding] : [],
    deliveries: [
      { id: "d1", status: "PROCESSED", detail: "duplicate", received_at: ts },
    ],
  };
  const calls: Call[] = [];
  function route(call: Call, init?: RequestInit) {
    calls.push(call);
    const special = options.more?.(call, init);
    if (special) return special;
    if (call.path === `${base}/configuration`) return json(200, config);
    if (call.path === `${base}/connections` && call.method === "POST") {
      config = { ...config, connection: { ...initialConnection } };
      return json(201, config.connection);
    }
    if (call.path === `${base}/connections/c1/secret`)
      return json(200, {
        url: "https://fixture.invalid/api/v1/hooks/bugly/synthetic-secret",
        version: config.connection!.version,
      });
    const action = call.path.split("/").at(-1);
    if (
      call.path.startsWith(`${base}/connections/c1/`) &&
      ["enable", "disable", "rotate", "disconnect"].includes(action!)
    ) {
      config = {
        ...config,
        connection: {
          ...config.connection!,
          version: config.connection!.version + 1,
          status:
            action === "enable"
              ? "ACTIVE"
              : action === "disable"
                ? "NEEDS_ATTENTION"
                : action === "disconnect"
                  ? "DISCONNECTED"
                  : config.connection!.status,
        },
      };
      return json(200, config.connection);
    }
    if (
      call.path === `${base}/connections/c1/bindings` &&
      call.method === "POST"
    ) {
      const binding = { ...initialBinding, ...(call.body as object) };
      config = { ...config, bindings: [binding] };
      return json(201, binding);
    }
    if (call.path === `${base}/connections/c1/bindings/b1/remove`) {
      config = { ...config, bindings: [] };
      return json(200, { ...initialBinding, status: "REMOVED", version: 2 });
    }
    if (call.path === `${org}/spaces`)
      return json(200, {
        spaces: [
          { id: "s1", name: "目标项目", status: "active", can_manage: true },
          { id: "s2", name: "只读空间", status: "active", can_manage: false },
          { id: "s3", name: "停用空间", status: "retired", can_manage: true },
        ],
      });
    if (call.path === `${org}/task-types`)
      return json(200, {
        spaces: [
          {
            space_id: "s1",
            space_name: "目标项目",
            space_prefix: "BB",
            task_types: [
              { id: "t1", name: "缺陷", icon: "bug", field_ids: [] },
            ],
          },
        ],
      });
    if (call.path === `${org}/settings`)
      return json(200, { priority_levels: options.levels ?? 4, version: 1 });
    return apiError(404, "NOT_FOUND");
  }
  return {
    calls,
    route,
    setConfig: (next: BuglyConfiguration) => {
      config = next;
    },
  };
}
function mount(
  f: ReturnType<typeof fixture>,
  props = { orgId: "o1", detail: detail() },
) {
  const cache = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: Infinity },
      mutations: { retry: false },
    },
  });
  const fetcher = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "https://fixture.invalid");
    return Promise.resolve(
      f.route(
        {
          path: url.pathname.replace(/^\/api/, ""),
          query: url.searchParams,
          method: init?.method ?? "GET",
          body: init?.body ? JSON.parse(String(init.body)) : undefined,
          headers: new Headers(init?.headers),
        },
        init,
      ),
    );
  });
  const api = createApi(new HttpClient("/api", fetcher));
  const wrapper = (p: typeof props) => (
    <I18nProvider initialLocale="zh-CN">
      <QueryClientProvider client={cache}>
        <ApiProvider api={api}>
          <BuglySections {...p} />
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>
  );
  const view = render(wrapper(props));
  return {
    cache,
    ...view,
    change: (p: typeof props) => view.rerender(wrapper(p)),
  };
}
async function choose(
  user: ReturnType<typeof userEvent.setup>,
  label: string,
  option: string,
) {
  await user.click(screen.getByLabelText(label, { exact: false }));
  await user.click(await screen.findByRole("option", { name: option }));
}
async function openBinding(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "绑定产品" }));
  await user.type(
    screen.getByLabelText("ProductID", { exact: false }),
    "Product-Case",
  );
  await waitFor(() =>
    expect(screen.getByLabelText("目标空间", { exact: false })).toBeEnabled(),
  );
  await choose(user, "目标空间", "目标项目");
  await choose(user, "默认任务类型", "缺陷");
}

describe("Bugly inbound settings", () => {
  it("aborts a pending secret across organization navigation and ignores the late value", async () => {
    let resolve!: (r: Response) => void;
    let signal: AbortSignal | null | undefined;
    const f = fixture({
      more: (c, init) => {
        if (c.path.endsWith("/secret")) {
          signal = init?.signal;
          return new Promise<Response>((r) => {
            resolve = r;
          });
        }
        return undefined;
      },
    });
    const view = mount(f),
      user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "显示秘密 URL" }),
    );
    view.change({ orgId: "o2", detail: detail(false) });
    expect(signal?.aborted).toBe(true);
    await act(async () =>
      resolve(
        json(200, {
          version: 1,
          url: "https://fixture.invalid/old-organization-secret",
        }),
      ),
    );
    expect(screen.queryByLabelText("秘密 URL")).not.toBeInTheDocument();
    expect(
      JSON.stringify(
        view.cache
          .getQueryCache()
          .getAll()
          .map((q) => q.state.data),
      ),
    ).not.toContain("old-organization-secret");
    view.unmount();
  });
  it("retains a formerly valid LOWEST mapping after a downgrade until explicitly changed", async () => {
    const f = fixture({
      binding: { ...initialBinding, priority_mapping: { P1: "LOWEST" } },
      levels: 4,
    });
    mount(f);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "编辑产品绑定" }),
    );
    await waitFor(() =>
      expect(
        screen.getByLabelText("默认任务类型", { exact: false }),
      ).toBeEnabled(),
    );
    expect(screen.getByLabelText("OpenFox 优先级 1")).toHaveTextContent("最低");
    expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
    await choose(user, "OpenFox 优先级 1", "中");
    expect(screen.getByRole("button", { name: "保存" })).toBeEnabled();
    expect(f.calls.some((c) => c.method === "PATCH")).toBe(false);
  });
  it("creates disabled; requires a separate explicit confirmation to enable", async () => {
    const f = fixture({ empty: true });
    mount(f);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "创建入站连接" }),
    );
    expect(
      await screen.findByRole("button", { name: "启用接收" }),
    ).toBeEnabled();
    expect(
      f.calls.filter((c) => c.method === "POST").map((c) => c.path),
    ).toEqual([`${base}/connections`]);
    await user.click(screen.getByRole("button", { name: "启用接收" }));
    expect(f.calls.some((c) => c.path.endsWith("/enable"))).toBe(false);
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "启用接收",
      }),
    );
    expect(
      await screen.findByRole("button", { name: "停用接收" }),
    ).toBeEnabled();
    expect(f.calls.find((c) => c.path.endsWith("/enable"))?.body).toEqual({
      version: 1,
    });
    expect(f.calls.some((c) => c.path.endsWith("/secret"))).toBe(false);
    expect(screen.getByText("重复来源，返回原任务")).toBeInTheDocument();
  });
  it("reads secret only on click, keeps it out of caches/storage, clears it before rotate", async () => {
    const f = fixture(),
      view = mount(f),
      user = userEvent.setup();
    await screen.findByRole("button", { name: "显示秘密 URL" });
    expect(f.calls.some((c) => c.path.endsWith("/secret"))).toBe(false);
    await user.click(screen.getByRole("button", { name: "显示秘密 URL" }));
    const input = await screen.findByLabelText("秘密 URL");
    expect(input).toHaveValue(
      "https://fixture.invalid/api/v1/hooks/bugly/synthetic-secret",
    );
    expect(f.calls.find((c) => c.path.endsWith("/secret"))).toMatchObject({
      method: "POST",
      body: { version: 1 },
    });
    expect(
      JSON.stringify(
        view.cache
          .getQueryCache()
          .getAll()
          .map((q) => q.state.data),
      ),
    ).not.toContain("synthetic-secret");
    expect(view.cache.getMutationCache().getAll()).toHaveLength(0);
    expect(JSON.stringify(localStorage)).not.toContain("synthetic-secret");
    await user.click(screen.getByRole("button", { name: "轮换秘密 URL" }));
    expect(screen.queryByLabelText("秘密 URL")).not.toBeInTheDocument();
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "轮换秘密 URL",
      }),
    );
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(f.calls.filter((c) => c.path.endsWith("/secret"))).toHaveLength(1);
  });
  it("aborts and ignores a late secret response when another action starts", async () => {
    let resolve!: (r: Response) => void, signal: AbortSignal | null | undefined;
    const f = fixture({
      more: (c, init) => {
        if (c.path.endsWith("/secret")) {
          signal = init?.signal;
          return new Promise<Response>((r) => {
            resolve = r;
          });
        }
        return undefined;
      },
    });
    mount(f);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "显示秘密 URL" }),
    );
    await user.click(screen.getByRole("button", { name: "断开连接" }));
    expect(signal?.aborted).toBe(true);
    await act(async () =>
      resolve(
        json(200, { version: 1, url: "https://fixture.invalid/late-secret" }),
      ),
    );
    expect(screen.queryByLabelText("秘密 URL")).not.toBeInTheDocument();
  });
  it("does not query configuration for non-managers; removes displayed secrets on permission loss", async () => {
    const f = fixture(),
      view = mount(f, { orgId: "o1", detail: detail(false) }),
      user = userEvent.setup();
    expect(f.calls).toHaveLength(0);
    expect(
      screen.queryByRole("button", { name: "显示秘密 URL" }),
    ).not.toBeInTheDocument();
    view.change({ orgId: "o1", detail: detail() });
    await user.click(
      await screen.findByRole("button", { name: "显示秘密 URL" }),
    );
    await screen.findByLabelText("秘密 URL");
    view.change({ orgId: "o1", detail: detail(false) });
    expect(screen.queryByLabelText("秘密 URL")).not.toBeInTheDocument();
  });
  it("uses exact product/mapping values and NORMAL without changing organization priorities", async () => {
    const f = fixture();
    mount(f);
    const user = userEvent.setup();
    await openBinding(user);
    await user.click(screen.getByRole("button", { name: "添加优先级映射" }));
    await user.type(screen.getByLabelText("外部优先级 1"), "P1");
    await user.click(screen.getByLabelText("OpenFox 优先级 1"));
    expect(
      screen.queryByRole("option", { name: "最低" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("option", { name: "中" }));
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(
        f.calls.some(
          (c) => c.path.endsWith("/bindings") && c.method === "POST",
        ),
      ).toBe(true),
    );
    expect(
      f.calls.find((c) => c.path.endsWith("/bindings") && c.method === "POST")
        ?.body,
    ).toEqual({
      connection_version: 1,
      product_id: "Product-Case",
      project_name: "",
      space_id: "s1",
      task_type_id: "t1",
      priority_mapping: { P1: "NORMAL" },
    });
    expect(
      f.calls.some((c) => c.path === `${org}/settings` && c.method !== "GET"),
    ).toBe(false);
  });
  it("offers LOWEST only for five levels and prevents duplicate external keys", async () => {
    const f = fixture({ levels: 5 });
    mount(f);
    const user = userEvent.setup();
    await openBinding(user);
    await user.click(screen.getByRole("button", { name: "添加优先级映射" }));
    await user.type(screen.getByLabelText("外部优先级 1"), "P1");
    await choose(user, "OpenFox 优先级 1", "最低");
    await user.click(screen.getByRole("button", { name: "添加优先级映射" }));
    await user.type(screen.getByLabelText("外部优先级 2"), "P1");
    expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "移除第 2 项映射" }));
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(
        f.calls.find((c) => c.path.endsWith("/bindings") && c.method === "POST")
          ?.body,
      ).toMatchObject({ priority_mapping: { P1: "LOWEST" } }),
    );
  });
  it("refreshes after CAS conflict without retrying or discarding the form", async () => {
    const f = fixture({
      more: (c) =>
        c.path.endsWith("/bindings") && c.method === "POST"
          ? apiError(409, "VERSION_CONFLICT")
          : undefined,
    });
    mount(f);
    const user = userEvent.setup();
    await openBinding(user);
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByText(/配置版本已变化/)).toBeInTheDocument();
    expect(screen.getByLabelText("ProductID", { exact: false })).toHaveValue(
      "Product-Case",
    );
    expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
    expect(
      f.calls.filter(
        (c) => c.path.endsWith("/bindings") && c.method === "POST",
      ),
    ).toHaveLength(1);
    expect(
      f.calls.filter((c) => c.path.endsWith("/configuration")).length,
    ).toBeGreaterThan(1);
  });
  it("keeps product/space immutable in edit and confirms remove/re-add consequences", async () => {
    const f = fixture({ binding: initialBinding });
    mount(f);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "编辑产品绑定" }),
    );
    expect(screen.getByLabelText("ProductID", { exact: false })).toBeDisabled();
    expect(screen.getByLabelText("目标空间", { exact: false })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "取消" }));
    await user.click(screen.getByRole("button", { name: "移除绑定" }));
    const dialog = screen.getByRole("dialog");
    expect(
      within(dialog).getByText(/同一 Bugly 问题可能产生另一任务/),
    ).toBeInTheDocument();
    expect(f.calls.some((c) => c.path.endsWith("/remove"))).toBe(false);
    await user.click(within(dialog).getByRole("button", { name: "移除绑定" }));
    await screen.findByText(/尚未绑定产品/);
    expect(f.calls.find((c) => c.path.endsWith("/remove"))?.body).toEqual({
      version: 1,
    });
  });
  it("explains reconnect creates a new URL and does not fetch it automatically", async () => {
    const f = fixture({ status: "DISCONNECTED" });
    mount(f);
    const user = userEvent.setup();
    expect(
      screen.queryByRole("button", { name: "显示秘密 URL" }),
    ).not.toBeInTheDocument();
    await user.click(
      await screen.findByRole("button", { name: "重新连接并启用" }),
    );
    expect(screen.getByText(/重新连接将生成新的秘密 URL/)).toBeInTheDocument();
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "重新连接并启用",
      }),
    );
    await screen.findByRole("button", { name: "显示秘密 URL" });
    expect(f.calls.some((c) => c.path.endsWith("/secret"))).toBe(false);
  });
  it("routes Bugly to its own inbound panel, without generic personal/import/sync controls", async () => {
    const calls = renderSettings("/o/o1/settings/integrations/bugly", (c) =>
      c.path === base ? json(200, detail(true, false)) : undefined,
    );
    await screen.findByText(/服务端尚未配置公共地址/);
    expect(
      screen.queryByRole("button", { name: /绑定个人|导入|同步|创建入站连接/ }),
    ).not.toBeInTheDocument();
    expect(calls.some((c) => c.path.endsWith("/configuration"))).toBe(false);
  });
});
