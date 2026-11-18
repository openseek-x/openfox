import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import {
  createApi,
  HttpClient,
  type CodeCommitConfiguration,
  type CodeCommitJob,
  type CodeCommitRepository,
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
import { CodeCommitSections } from "./CodeCommitSections";
import { codecommitObjectURL } from "./codecommit-model";
import { GitLinksSection } from "../tasks/detail/GitLinksSection";

const base = "/v1/organizations/o1/integrations",
  cc = `${base}/connections/c1/codecommit`;
const connection: IntegrationConnection = {
  id: "c1",
  provider: "codecommit",
  scope: "ORGANIZATION",
  external_id: "aws:123456789012:us-east-1",
  display_name: "123456789012 · us-east-1",
  account_kind: "organization",
  credentials_hint: "",
  created_by: "u1",
  created_by_email: "fixture@example.invalid",
  status: "ACTIVE",
  status_reason: "",
  version: 3,
  created_at: ts,
  updated_at: ts,
  disconnected_at: null,
};
const repository: CodeCommitRepository = {
  id: "row1",
  repository_id: "native1",
  name: "Project-Case",
  arn: "arn:aws:codecommit:us-east-1:123456789012:Project-Case",
  enabled: true,
  generation: 2,
  version: 4,
  configured_by: "u1",
  bridge_id: "bridge1",
  bridge_ready: true,
  last_event_at: null,
  activated_at: ts,
  initialized_at: null,
  scan_kind: "PR_OPEN",
  next_attempt_at: ts,
  last_error: "",
};
const job: CodeCommitJob = {
  id: "j1",
  repository_id: "row1",
  repository_name: "Project-Case",
  kind: "PR",
  state: "DONE",
  backlink_state: "UNKNOWN",
  generation: 2,
  version: 9,
  backlink_generation: 1,
  last_error: "comment_result_unknown",
  next_attempt_at: null,
  requested_at: ts,
  url: "https://us-east-1.console.aws.amazon.com/codesuite/codecommit/repositories/Project-Case/pull-requests/1",
  can_reconcile: true,
};
const settings = {
  link_pull_requests: true,
  link_commits: false,
  pr_backlink_private: true,
  pr_backlink_public: false,
  issue_backlink_private: true,
  issue_backlink_public: false,
  branch_format: "username/identifier-title",
};
function detail(manage = true): IntegrationDetail {
  return {
    provider: {
      id: "codecommit",
      name: "AWS CodeCommit",
      category: "git",
      availability: "available",
      template: "B",
      configured: true,
      bindable: true,
      capabilities: ["pr_link"],
      summary: { connections: 1, status: "ok" },
    },
    can_manage: manage,
    connections: [connection],
    personal_connections: [],
    sync_links: [],
  };
}
function fixture(
  options: {
    repository?: CodeCommitRepository | null;
    job?: CodeCommitJob;
    more?: (
      c: Call,
      init?: RequestInit,
    ) => Response | Promise<Response> | undefined;
  } = {},
) {
  const config: CodeCommitConfiguration = {
    connection,
    account: "123456789012",
    region: "us-east-1",
    principal_arn: "arn:aws:iam::123456789012:user/current",
    repositories:
      options.repository === null ? [] : [options.repository ?? repository],
    repository_next: "",
  };
  const calls: Call[] = [];
  function route(call: Call, init?: RequestInit): Response | Promise<Response> {
    calls.push(call);
    const custom = options.more?.(call, init);
    if (custom) return custom;
    if (call.path === `${base}/codecommit`) return json(200, detail());
    if (call.path === `${base}/codecommit/regions`)
      return json(200, { regions: ["us-east-1", "eu-west-1"] });
    if (call.path === `${base}/codecommit/settings`)
      return json(200, {
        provider: "codecommit",
        can_manage: true,
        version: 5,
        updated_at: ts,
        settings:
          call.method === "PUT"
            ? (call.body as { settings: unknown }).settings
            : settings,
      });
    if (call.path === `${cc}/configuration`) return json(200, config);
    if (call.path === `${cc}/work`)
      return json(200, { jobs: [options.job ?? job], next: "" });
    if (call.path.endsWith("/reconcile"))
      return json(200, { reconciled: true });
    if (
      call.path === `${base}/codecommit/connections` ||
      call.path === `${cc}/credentials`
    )
      return json(call.method === "POST" ? 201 : 200, {
        ...connection,
        version: 4,
      });
    if (call.path === `${cc}/repositories` && call.method === "GET")
      return json(200, {
        repositories: [{ id: "native2", name: "Next-Case" }],
        next_cursor: "",
        connection_version: 3,
      });
    if (call.path === `${cc}/repositories` && call.method === "POST") {
      config.repositories = [{ ...repository, name: "Next-Case" }];
      return json(201, {
        repository: config.repositories[0],
        url: "https://fixture.invalid/api/v1/hooks/git/codecommit/bridge1",
        token: "synthetic-once-token",
      });
    }
    if (call.path.endsWith("/bridge-token"))
      return json(200, {
        repository: { ...repository, version: 5 },
        url: "https://fixture.invalid/api/v1/hooks/git/codecommit/bridge1",
        token: "synthetic-rotated-token",
      });
    if (call.path.endsWith("/disable")) {
      config.repositories = [{ ...repository, enabled: false, version: 5 }];
      return json(200, {
        repository: config.repositories[0],
        url: "",
        token: "",
      });
    }
    return apiError(404, "NOT_FOUND");
  }
  return { calls, route, config };
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
  const transport = (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "https://fixture.invalid");
    return Promise.resolve(
      f.route(
        {
          path: url.pathname.replace(/^\/api/, ""),
          method: init?.method ?? "GET",
          body: init?.body ? JSON.parse(String(init.body)) : undefined,
          query: url.searchParams,
          headers: new Headers(init?.headers),
        },
        init,
      ),
    );
  };
  const api = createApi(new HttpClient("/api", transport));
  const wrapper = (p: typeof props) => (
    <I18nProvider initialLocale="zh-CN">
      <QueryClientProvider client={cache}>
        <ApiProvider api={api}>
          <CodeCommitSections {...p} />
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>
  );
  const view = render(wrapper(props));
  return {
    ...view,
    cache,
    change: (p: typeof props) => view.rerender(wrapper(p)),
  };
}
async function choose(
  user: ReturnType<typeof userEvent.setup>,
  label: string,
  option: string,
) {
  await user.click(screen.getByLabelText(label));
  await user.click(await screen.findByRole("option", { name: option }));
}
async function credentials(
  user: ReturnType<typeof userEvent.setup>,
  fresh = false,
) {
  await user.click(
    await screen.findByRole("button", {
      name: fresh ? "绑定 AWS 账号与区域" : "更新 IAM 凭据",
    }),
  );
  if (fresh) {
    await waitFor(() =>
      expect(screen.getByLabelText("AWS 区域")).toBeEnabled(),
    );
    await choose(user, "AWS 区域", "us-east-1");
  }
  await user.type(
    screen.getByLabelText("Access key ID"),
    "AKIAFIXTURE00000000",
  );
  await user.type(
    screen.getByLabelText("Secret access key"),
    "synthetic-secret-value",
  );
}
async function openSelect(user: ReturnType<typeof userEvent.setup>) {
  await user.click(
    await screen.findByRole("button", { name: "选择并启用仓库" }),
  );
  await waitFor(() => expect(screen.getByLabelText("仓库")).toBeEnabled());
}

describe("CodeCommit organization integration", () => {
  it("retries failed initialization without work, token rotation or a new generation", async () => {
    const stopped = {
      ...repository,
      last_error: "upstream_unavailable",
      next_attempt_at: null,
    };
    const f = fixture({
      repository: stopped,
      more: (c) => {
        if (c.path === `${cc}/work`) return json(200, { jobs: [], next: "" });
        if (c.path === `${cc}/repositories/row1/reconcile`) {
          f.config.repositories = [
            { ...stopped, last_error: "", next_attempt_at: ts },
          ];
          return json(200, {
            repository: f.config.repositories[0],
            url: "",
            token: "",
          });
        }
        return undefined;
      },
    });
    const view = mount(f),
      user = userEvent.setup();
    await screen.findByText("本页没有处理记录。");
    await user.click(
      await screen.findByRole("button", { name: "重试仓库扫描" }),
    );
    expect(
      screen.getByText(/不会轮换桥接令牌或改变仓库代次/),
    ).toBeInTheDocument();
    expect(f.calls.some((c) => c.path.endsWith("/reconcile"))).toBe(false);
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "重试仓库扫描",
      }),
    );
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(
      f.calls.find((c) => c.path === `${cc}/repositories/row1/reconcile`)?.body,
    ).toEqual({ connection_version: 3, version: 4 });
    expect(f.config.repositories[0]).toMatchObject({
      generation: 2,
      bridge_id: "bridge1",
    });
    expect(screen.queryByLabelText("Bearer 令牌")).not.toBeInTheDocument();
    expect(f.calls.some((c) => c.path.endsWith("/bridge-token"))).toBe(false);
    expect(view.cache.getMutationCache().getAll()).toHaveLength(0);
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: "重试仓库扫描" }),
      ).not.toBeInTheDocument(),
    );
  });
  it("does not replay a scan retry rejected by an active lease or stale version", async () => {
    const f = fixture({
      repository: { ...repository, last_error: "rate_limited" },
      more: (c) =>
        c.path === `${cc}/repositories/row1/reconcile`
          ? apiError(409, "VERSION_CONFLICT")
          : undefined,
    });
    mount(f);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "重试仓库扫描" }),
    );
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "重试仓库扫描",
      }),
    );
    await screen.findByText(/已有扫描正在执行/);
    expect(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "重试仓库扫描",
      }),
    ).toBeDisabled();
    expect(
      f.calls.filter((c) => c.path === `${cc}/repositories/row1/reconcile`),
    ).toHaveLength(1);
    expect(screen.queryByLabelText("Bearer 令牌")).not.toBeInTheDocument();
  });
  it.each(["disabled", "no_error", "inactive"])(
    "hides scan retry for %s repositories or connections",
    async (mode) => {
      const f = fixture({
        repository: {
          ...repository,
          enabled: mode !== "disabled",
          last_error: mode === "no_error" ? "" : "upstream_unavailable",
        },
      });
      if (mode === "inactive")
        f.config.connection = { ...connection, status: "NEEDS_ATTENTION" };
      mount(f);
      await screen.findByText("令牌已生成；桥接部署未验证");
      expect(
        screen.queryByRole("button", { name: "重试仓库扫描" }),
      ).not.toBeInTheDocument();
    },
  );
  it("refreshes a mismatched remote-list version once without adopting it for a stale selection", async () => {
    const f = fixture({
      more: (c) =>
        c.path === `${cc}/repositories` && c.method === "GET"
          ? json(200, {
              repositories: [{ id: "native2", name: "Next-Case" }],
              next_cursor: "",
              connection_version: 4,
            })
          : undefined,
    });
    mount(f);
    const user = userEvent.setup();
    await openSelect(user);
    await screen.findByText(/版本已变化/);
    await choose(user, "仓库", "Next-Case");
    await user.click(
      screen.getByRole("checkbox", { name: /我确认仅建立初始基线/ }),
    );
    expect(
      screen.getByRole("button", { name: "启用并建立基线" }),
    ).toBeDisabled();
    await waitFor(() =>
      expect(
        f.calls.filter((c) => c.path.endsWith("/configuration")),
      ).toHaveLength(2),
    );
    expect(
      f.calls.some(
        (c) => c.path.endsWith("/repositories") && c.method === "POST",
      ),
    ).toBe(false);
  });
  it("reenables a retained repository using its reviewed version and a new explicit baseline", async () => {
    const f = fixture({
      repository: { ...repository, enabled: false, version: 8 },
    });
    mount(f);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "重新启用仓库" }),
    );
    expect(screen.queryByLabelText("仓库")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "启用并建立基线" }),
    ).toBeDisabled();
    await user.click(
      screen.getByRole("checkbox", { name: /我确认仅建立初始基线/ }),
    );
    await user.click(screen.getByRole("button", { name: "启用并建立基线" }));
    await screen.findByLabelText("Bearer 令牌");
    expect(
      f.calls.find(
        (c) => c.path.endsWith("/repositories") && c.method === "POST",
      )?.body,
    ).toEqual({
      connection_version: 3,
      version: 8,
      name: "Project-Case",
      confirm_initialization: true,
    });
    expect(
      f.calls.some(
        (c) => c.path.endsWith("/repositories") && c.method === "GET",
      ),
    ).toBe(false);
  });
  it("labels task links as CodeCommit while preserving the stable repository display", async () => {
    const cache = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const link = {
      id: "link1",
      kind: "PULL_REQUEST",
      provider: "codecommit",
      repository: "123456789012:us-east-1:native1",
      repository_display: "123456789012/us-east-1/Project-Case",
      number: 7,
      sha: "",
      title: "Fixture PR",
      url: job.url,
      state: "OPEN",
      author_login: "fixture",
      author_user_id: null,
      head_branch: "feature",
      matched_in: "title",
      created_at: ts,
      updated_at: ts,
      merged_at: null,
    };
    const api = createApi(
      new HttpClient("/api", async () => json(200, { links: [link] })),
    );
    render(
      <I18nProvider initialLocale="zh-CN">
        <QueryClientProvider client={cache}>
          <ApiProvider api={api}>
            <GitLinksSection orgId="o1" spaceId="s1" taskId="t1" people={[]} />
          </ApiProvider>
        </QueryClientProvider>
      </I18nProvider>,
    );
    expect(
      await screen.findByRole("region", { name: "CodeCommit 关联" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("123456789012/us-east-1/Project-Case#7"),
    ).toBeInTheDocument();
    expect(screen.queryByText("GitHub 关联")).not.toBeInTheDocument();
  });
  it("shows no private queries or actions to non-managers", () => {
    const f = fixture();
    mount(f, { orgId: "o1", detail: detail(false) });
    expect(f.calls).toHaveLength(0);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
  it("creates with explicit credentials and clears them without a mutation cache entry", async () => {
    const f = fixture(),
      view = mount(f),
      user = userEvent.setup();
    await credentials(user, true);
    await user.click(screen.getByRole("button", { name: "验证并保存" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(
      f.calls.find((c) => c.path === `${base}/codecommit/connections`)?.body,
    ).toMatchObject({
      region: "us-east-1",
      access_key_id: "AKIAFIXTURE00000000",
      secret_access_key: "synthetic-secret-value",
      version: 0,
    });
    expect(view.cache.getMutationCache().getAll()).toHaveLength(0);
    expect(
      JSON.stringify(
        view.cache
          .getQueryCache()
          .getAll()
          .map((q) => q.state.data),
      ),
    ).not.toContain("synthetic-secret-value");
    expect(JSON.stringify(localStorage)).not.toContain(
      "synthetic-secret-value",
    );
    await credentials(user, true);
    expect(screen.getByLabelText("Secret access key")).toHaveValue(
      "synthetic-secret-value",
    );
    await user.click(screen.getByRole("button", { name: "取消" }));
    await user.click(
      screen.getByRole("button", { name: "绑定 AWS 账号与区域" }),
    );
    expect(screen.getByLabelText("Secret access key")).toHaveValue("");
  });
  it("aborts credential verification on close and ignores a late response", async () => {
    let signal: AbortSignal | null | undefined, resolve!: (r: Response) => void;
    const f = fixture({
      more: (c, init) => {
        if (c.path === `${base}/codecommit/connections`) {
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
    await credentials(user, true);
    await user.click(screen.getByRole("button", { name: "验证并保存" }));
    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(signal?.aborted).toBe(true);
    await act(async () => resolve(json(201, connection)));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(view.cache.getMutationCache().getAll()).toHaveLength(0);
  });
  it("holds account/region fixed for credential replacement and surfaces sanitized failures", async () => {
    const f = fixture({
      more: (c) =>
        c.path.endsWith("/credentials")
          ? apiError(422, "CODECOMMIT_CREDENTIALS_INVALID")
          : undefined,
    });
    mount(f);
    const user = userEvent.setup();
    await credentials(user);
    expect(screen.getByLabelText("AWS 区域")).toBeDisabled();
    expect(screen.getByText(/之后的发送者将使用新身份/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "验证并保存" }));
    await screen.findByText(/IAM 凭据无效或已过期/);
    expect(
      f.calls.find((c) => c.path.endsWith("/credentials"))?.body,
    ).toMatchObject({ region: "us-east-1", version: 3 });
    expect(
      screen.queryByText("CODECOMMIT_CREDENTIALS_INVALID"),
    ).not.toBeInTheDocument();
    expect(f.calls.filter((c) => c.path.endsWith("/credentials"))).toHaveLength(
      1,
    );
  });
  it("loads remote pages explicitly, clears selection, and requires initialization consent", async () => {
    const f = fixture({
      repository: null,
      more: (c) =>
        c.path === `${cc}/repositories` && c.method === "GET"
          ? json(200, {
              repositories: [
                {
                  id: c.query.get("cursor") ? "n2" : "n1",
                  name: c.query.get("cursor") ? "Next-Case" : "First-Case",
                },
              ],
              next_cursor: c.query.get("cursor") ? "" : "opaque-page-two",
              connection_version: 3,
            })
          : undefined,
    });
    mount(f);
    const user = userEvent.setup();
    expect(f.calls.some((c) => c.path.endsWith("/repositories"))).toBe(false);
    await openSelect(user);
    await choose(user, "仓库", "First-Case");
    expect(
      f.calls.filter(
        (c) => c.path.endsWith("/repositories") && c.method === "GET",
      ),
    ).toHaveLength(1);
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "下一页",
      }),
    );
    await waitFor(() => expect(screen.getByLabelText("仓库")).toBeEnabled());
    expect(screen.getByLabelText("仓库")).not.toHaveTextContent("First-Case");
    await choose(user, "仓库", "Next-Case");
    expect(
      screen.getByRole("button", { name: "启用并建立基线" }),
    ).toBeDisabled();
    await user.click(
      screen.getByRole("checkbox", { name: /我确认仅建立初始基线/ }),
    );
    await user.click(screen.getByRole("button", { name: "启用并建立基线" }));
    await screen.findByLabelText("Bearer 令牌");
    expect(
      f.calls.find(
        (c) => c.path.endsWith("/repositories") && c.method === "POST",
      )?.body,
    ).toEqual({
      connection_version: 3,
      version: 0,
      name: "Next-Case",
      confirm_initialization: true,
    });
  });
  it("shows bridge tokens once outside all caches and clears them on permission loss", async () => {
    const f = fixture(),
      view = mount(f),
      user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "轮换桥接令牌" }),
    );
    expect(screen.getByText(/旧令牌立即失效/)).toBeInTheDocument();
    expect(f.calls.some((c) => c.path.endsWith("/bridge-token"))).toBe(false);
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "轮换桥接令牌",
      }),
    );
    expect(await screen.findByLabelText("Bearer 令牌")).toHaveValue(
      "synthetic-rotated-token",
    );
    expect(
      JSON.stringify(
        view.cache
          .getQueryCache()
          .getAll()
          .map((q) => q.state.data),
      ),
    ).not.toContain("synthetic-rotated-token");
    expect(view.cache.getMutationCache().getAll()).toHaveLength(0);
    view.change({ orgId: "o1", detail: detail(false) });
    expect(screen.queryByLabelText("Bearer 令牌")).not.toBeInTheDocument();
    view.change({ orgId: "o1", detail: detail() });
    await screen.findByText("令牌已生成；桥接部署未验证");
    expect(screen.queryByLabelText("Bearer 令牌")).not.toBeInTheDocument();
    expect(
      f.calls.filter((c) => c.path.endsWith("/bridge-token")),
    ).toHaveLength(1);
  });
  it("ignores a late one-time token after navigation and does not replay the request", async () => {
    let resolve!: (r: Response) => void, signal: AbortSignal | null | undefined;
    const f = fixture({
        more: (c, init) => {
          if (c.path.endsWith("/bridge-token")) {
            signal = init?.signal;
            return new Promise<Response>((r) => {
              resolve = r;
            });
          }
          return undefined;
        },
      }),
      view = mount(f),
      user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "轮换桥接令牌" }),
    );
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "轮换桥接令牌",
      }),
    );
    view.change({ orgId: "o2", detail: detail(false) });
    expect(signal?.aborted).toBe(true);
    await act(async () =>
      resolve(
        json(200, {
          repository,
          url: "https://fixture.invalid/hook",
          token: "late-token",
        }),
      ),
    );
    expect(screen.queryByLabelText("Bearer 令牌")).not.toBeInTheDocument();
    expect(
      f.calls.filter((c) => c.path.endsWith("/bridge-token")),
    ).toHaveLength(1);
  });
  it("requires closing and reviewing after a selection version conflict", async () => {
    const f = fixture({
      more: (c) =>
        c.path.endsWith("/repositories") && c.method === "POST"
          ? apiError(409, "VERSION_CONFLICT")
          : undefined,
    });
    mount(f);
    const user = userEvent.setup();
    await openSelect(user);
    await choose(user, "仓库", "Next-Case");
    await user.click(
      screen.getByRole("checkbox", { name: /我确认仅建立初始基线/ }),
    );
    await user.click(screen.getByRole("button", { name: "启用并建立基线" }));
    await screen.findByText(/版本已变化/);
    expect(
      screen.getByRole("button", { name: "启用并建立基线" }),
    ).toBeDisabled();
    expect(
      f.calls.filter(
        (c) => c.path.endsWith("/repositories") && c.method === "POST",
      ),
    ).toHaveLength(1);
    expect(screen.getByLabelText("仓库")).toHaveTextContent("Next-Case");
  });
  it("uses server retry eligibility and explicit original-intent confirmation for UNKNOWN", async () => {
    const f = fixture(),
      view = mount(f),
      user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "恢复原评论意图" }),
    );
    expect(screen.getByText(/结果未知不表示尚未发送/)).toBeInTheDocument();
    const dialog = within(screen.getByRole("dialog"));
    expect(
      dialog.getByRole("button", { name: "恢复原评论意图" }),
    ).toBeDisabled();
    await user.click(dialog.getByRole("checkbox"));
    await user.click(dialog.getByRole("button", { name: "恢复原评论意图" }));
    await waitFor(() =>
      expect(f.calls.some((c) => c.path.endsWith("/reconcile"))).toBe(true),
    );
    expect(f.calls.find((c) => c.path.endsWith("/reconcile"))?.body).toEqual({
      connection_version: 3,
      version: 9,
      confirm_resume_unknown: true,
    });
    view.unmount();
    const blocked = fixture({
      job: {
        ...job,
        state: "RUNNING",
        backlink_state: "SENDING",
        can_reconcile: false,
      },
    });
    mount(blocked);
    await screen.findByText("执行中 · 回链发送中");
    expect(
      screen.queryByRole("button", { name: /恢复原评论意图|重新协调/ }),
    ).not.toBeInTheDocument();
  });
  it("keeps settings defaults and changes only the selected shared field", async () => {
    const f = fixture();
    mount(f);
    const user = userEvent.setup();
    expect(
      await screen.findByRole("switch", { name: "关联 Pull Request" }),
    ).toBeChecked();
    expect(
      screen.getByRole("switch", { name: "关联 Git Commit" }),
    ).not.toBeChecked();
    expect(
      screen.getByRole("switch", { name: "在受控仓库发送 PR 回链" }),
    ).toBeChecked();
    expect(screen.getAllByRole("switch")).toHaveLength(3);
    await user.click(screen.getByRole("switch", { name: "关联 Git Commit" }));
    await waitFor(() =>
      expect(
        f.calls.find((c) => c.method === "PUT" && c.path.endsWith("/settings"))
          ?.body,
      ).toEqual({ version: 5, settings: { ...settings, link_commits: true } }),
    );
  });
  it("paginates retained repositories and diagnostics without automatically fetching next", async () => {
    const f = fixture({
      more: (c) =>
        c.path.endsWith("/configuration")
          ? json(200, {
              ...f.config,
              repository_next: c.query.get("repository_after")
                ? ""
                : "row-next",
            })
          : c.path.endsWith("/work")
            ? json(200, {
                jobs: [job],
                next: c.query.get("before") ? "" : "job-next",
              })
            : undefined,
    });
    mount(f);
    const user = userEvent.setup();
    const repos = await screen.findByRole("region", { name: "已选择的仓库" });
    await user.click(
      await within(repos).findByRole("button", { name: "下一页" }),
    );
    await waitFor(() =>
      expect(
        f.calls.some((c) => c.query.get("repository_after") === "row-next"),
      ).toBe(true),
    );
    const diagnostics = screen.getByRole("region", { name: "处理与回链诊断" });
    await user.click(
      await within(diagnostics).findByRole("button", { name: "下一页" }),
    );
    await waitFor(() =>
      expect(f.calls.some((c) => c.query.get("before") === "job-next")).toBe(
        true,
      ),
    );
  });
  it("routes CodeCommit separately and restricts diagnostic links to AWS consoles", async () => {
    const f = fixture();
    renderSettings("/o/o1/settings/integrations/codecommit", (c) => {
      const response = f.route(c);
      return response instanceof Response && response.status !== 404
        ? response
        : undefined;
    });
    await screen.findByRole("button", { name: "更新 IAM 凭据" });
    expect(
      screen.queryByRole("button", { name: /绑定个人|OAuth|历史导入/ }),
    ).not.toBeInTheDocument();
    expect(codecommitObjectURL("javascript:alert(1)")).toBeUndefined();
    expect(
      codecommitObjectURL(
        "https://console.aws.amazon.com.attacker.invalid/path",
      ),
    ).toBeUndefined();
    expect(codecommitObjectURL(job.url)).toBe(job.url);
  });
});
