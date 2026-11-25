import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { createApi, HttpClient, type RuntimeUsage } from "@openfox/api";
import { I18nProvider, type Locale } from "@openfox/i18n";
import { ApiProvider } from "../context";
import { QueueFullNotice } from "./QueueFullNotice";

const full: RuntimeUsage = {
  organization_id: "o1",
  queued: 5,
  queue_full: true,
};

function renderNotice(usage?: RuntimeUsage, locale: Locale = "zh-CN") {
  const api = createApi(
    new HttpClient("/api", async (input) => {
      expect(String(input)).toBe(
        "/api/v1/organizations/o1/runtime-config/usage",
      );
      return usage ? Response.json(usage) : new Response(null, { status: 503 });
    }),
  );
  const client = new QueryClient();
  const view = render(
    <I18nProvider initialLocale={locale}>
      <ApiProvider api={api}>
        <QueryClientProvider client={client}>
          <QueueFullNotice orgId="o1" />
        </QueryClientProvider>
      </ApiProvider>
    </I18nProvider>,
  );
  return { ...view, client };
}

describe("队列已满提示", () => {
  it("成员看到消息已保存及恢复条件", async () => {
    renderNotice(full);
    expect(await screen.findByText("组织队列已满")).toBeVisible();
    expect(
      screen.getByText(/你的消息已保存，会在队列有空位后自动开始处理/),
    ).toBeVisible();
    expect(screen.queryByText(/上限 5/)).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("管理员还可以看到当前排队数和上限", async () => {
    renderNotice({ ...full, queue_max: 5 });
    expect(await screen.findByText(/当前排队 5，上限 5/)).toBeVisible();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("支持英文提示", async () => {
    renderNotice(full, "en-US");
    expect(
      await screen.findByText("The organization queue is full"),
    ).toBeVisible();
  });

  it.each([
    { status: "success", usage: { ...full, queue_full: false, queued: 0 } },
    { status: "error", usage: undefined },
  ])("非满队列或请求失败时隐藏提示 ($status)", async ({ status, usage }) => {
    const { client, container } = renderNotice(usage);
    await waitFor(() =>
      expect(client.getQueryState(["org", "o1", "runtime-usage"])?.status).toBe(
        status,
      ),
    );
    expect(container).toBeEmptyDOMElement();
  });
});
