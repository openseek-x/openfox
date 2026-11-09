import { QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { createApi, HttpClient, type SpaceDetail } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import { createQueryClient } from "../../App";
import { ApiProvider } from "../../context";
import { json, ts } from "../../settings/testing";
import { GeneralPage } from "./GeneralPage";

// D03：空间概览里迭代、分拣台的摘要按真实开关显示（已开启 / 未开启），加载中不显示。

const SPACE: SpaceDetail = {
  id: "s1",
  prefix: "OPEN",
  name: "默认空间",
  visibility: "shared",
  version: 1,
  created_at: ts,
  updated_at: ts,
  icon: null,
  purpose: "software_development",
  features: { views: true, projects: true, iterations: true, triage: false },
  multi_assignee: false,
  estimation: {
    scale: "notUsed",
    extended: false,
    allow_zero: false,
    count_unestimated: false,
  },
  status: "active",
  retired_at: null,
  position: 0,
  my_role: "OWNER",
  can_manage: true,
  member_count: 1,
  task_type_count: 1,
  label_count: 0,
  task_count: 0,
};

const ITERATION = "/v1/organizations/o1/spaces/s1/iteration-settings";
const TRIAGE = "/v1/organizations/o1/spaces/s1/triage-settings";

/** settings 为 null 表示这个请求一直不返回（加载中）；"error" 表示返回 500 */
function renderPage(responses: {
  iteration: { enabled: boolean } | null | "error";
  triage: { enabled: boolean } | null | "error";
}) {
  const reply = (value: { enabled: boolean } | null | "error") =>
    value === null
      ? new Promise<Response>(() => {})
      : Promise.resolve(
          value === "error"
            ? json(500, {
                error: { code: "INTERNAL", message: "x", request_id: "r" },
              })
            : json(200, { ...value, version: 1 }),
        );
  const transport = vi.fn((input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://localhost").pathname.replace(
      /^\/api/,
      "",
    );
    if (path === ITERATION) return reply(responses.iteration);
    if (path === TRIAGE) return reply(responses.triage);
    if (path.endsWith("/repos"))
      return Promise.resolve(json(200, { repos: [] }));
    return Promise.resolve(
      json(404, {
        error: { code: "NOT_FOUND", message: "x", request_id: "r" },
      }),
    );
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <I18nProvider initialLocale="zh-CN">
      <QueryClientProvider client={queryClient}>
        <ApiProvider
          api={createApi(
            new HttpClient("/api", transport as unknown as typeof fetch),
          )}
        >
          <ToastProvider>
            <MemoryRouter>
              <GeneralPage orgId="o1" space={SPACE} canManage retired={false} />
            </MemoryRouter>
          </ToastProvider>
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
  return transport;
}

const row = (name: RegExp) => screen.getByRole("link", { name });

describe("空间概览 · 迭代与分拣台摘要", () => {
  it("开启显示「已开启」，关闭显示「未开启」，不再显示「即将开放」", async () => {
    renderPage({ iteration: { enabled: true }, triage: { enabled: false } });
    await waitFor(() => expect(row(/^迭代/)).toHaveTextContent("已开启"));
    await waitFor(() => expect(row(/^分拣台/)).toHaveTextContent("未开启"));
    expect(screen.queryByText("即将开放")).toBeNull();
  });

  it("反过来：迭代关闭、分拣台开启", async () => {
    renderPage({ iteration: { enabled: false }, triage: { enabled: true } });
    await waitFor(() => expect(row(/^迭代/)).toHaveTextContent("未开启"));
    await waitFor(() => expect(row(/^分拣台/)).toHaveTextContent("已开启"));
  });

  it("加载中或出错时不显示摘要", async () => {
    const transport = renderPage({ iteration: null, triage: "error" });
    await waitFor(() => expect(transport).toHaveBeenCalled());
    // 等出错的请求落定后再断言
    await new Promise((resolve) => setTimeout(resolve, 20));
    for (const name of [/^迭代/, /^分拣台/]) {
      expect(row(name)).not.toHaveTextContent("已开启");
      expect(row(name)).not.toHaveTextContent("未开启");
      expect(row(name)).not.toHaveTextContent("即将开放");
    }
  });
});
