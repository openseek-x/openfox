import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { vi } from "vitest";

import { createApi, HttpClient } from "@openfox/api";

import { App, createQueryClient } from "../App";

// 全局配置页测试的公共夹具：模拟传输层，按 URL 路由到各测试自己的处理函数。

export interface Call {
  method: string;
  path: string;
  query: URLSearchParams;
  body: unknown;
  headers: Headers;
}

export type Route = (call: Call) => Response | undefined;

export function json(status: number, body?: unknown): Response {
  if (status === 204) return new Response(null, { status });
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

export function apiError(status: number, code: string): Response {
  return json(status, { error: { code, message: code, request_id: "r" } });
}

const TS = "2026-10-20T08:00:00Z";
export const ts = TS;

/** 渲染设置页；role 决定读写权限。返回所有请求记录。 */
export function renderSettings(
  path: string,
  route: Route,
  role: "OWNER" | "ADMIN" | "MEMBER" = "OWNER",
  reactStrictMode = false,
) {
  // 跳过首登向导
  localStorage.setItem("openfox:onboarding:o1", JSON.stringify({}));
  const calls: Call[] = [];
  const transport = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    const raw = init?.body;
    const call: Call = {
      method: init?.method ?? "GET",
      path: url.pathname.replace(/^\/api/, ""),
      query: url.searchParams,
      body: typeof raw === "string" ? (JSON.parse(raw) as unknown) : undefined,
      headers: new Headers(init?.headers),
    };
    calls.push(call);
    const custom = route(call);
    if (custom) return Promise.resolve(custom);
    return Promise.resolve(shell(call, role));
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App
        api={createApi(
          new HttpClient("/api", transport as unknown as typeof fetch),
        )}
        queryClient={queryClient}
        locale="zh-CN"
      />
    </MemoryRouter>,
    { reactStrictMode },
  );
  return calls;
}

/** 应用外壳需要的接口。 */
function shell(call: Call, role: string): Response {
  if (call.path === "/v1/me")
    return json(200, { id: "u1", email: "lin@openfox.dev" });
  if (call.path === "/v1/me/organizations")
    return json(200, {
      organizations: [
        { id: "o1", slug: "lin", name: "林的团队", role, created_at: TS },
      ],
    });
  if (call.path.endsWith("/inbox/stats"))
    return json(200, {
      counts: {
        inbox: 0,
        inbox_unread: 0,
        inbox_important_unread: 0,
        inbox_other_unread: 0,
        later: 0,
        cleared: 0,
      },
    });
  if (call.path.endsWith("/spaces")) return json(200, { spaces: [] });
  if (call.path.endsWith("/direct-chats"))
    return json(200, { chats: [], next_before: "" });
  if (call.path.endsWith("/conversations"))
    return json(200, { conversations: [], next_cursor: null });
  return apiError(404, "NOT_FOUND");
}

export const findCall = (calls: Call[], method: string, path: string) =>
  calls.find((call) => call.method === method && call.path === path);
