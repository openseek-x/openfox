import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import type { RunArtifact } from "@openfox/api";

import { App, createQueryClient } from "../../App";
import { backend, json, ORG } from "../../tasks/test-backend";
import { message, run, summary } from "../fixtures";

// 任务全页的侧边会话里也能打开产物面板（窄容器里覆盖在消息流上），地址参数与任务页参数并存。

const o = `/v1/organizations/${ORG}`;

const MD: RunArtifact = {
  organization_id: ORG,
  id: "a-md",
  run_id: "r1",
  name: "方案.md",
  size_bytes: 20,
  sha256: "0".repeat(64),
  mime_type: "text/markdown",
  state: "AVAILABLE",
  retention_until: "2099-01-01T00:00:00Z",
  pinned: false,
  version: 1,
  created_at: "2026-11-04T08:00:00Z",
};

function Search() {
  return <output data-testid="search">{useLocation().search}</output>;
}

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("会话产物 · 任务全页侧边会话", () => {
  it("回复里的产物按钮打开面板；关闭面板只去掉 artifact 参数", async () => {
    const server = backend((call) => {
      if (call.method !== "GET") return undefined;
      if (call.path === `${o}/conversations`)
        return json(200, {
          conversations: [summary("c9", { task_id: "task-1" })],
          next_cursor: null,
        });
      if (call.path === `${o}/conversations/c9`)
        return json(200, summary("c9", { task_id: "task-1" }));
      if (call.path === `${o}/conversations/c9/messages`)
        return json(200, {
          messages: [
            message(1, { conversation_id: "c9", body: "写方案" }),
            message(2, {
              conversation_id: "c9",
              body: "方案写好了",
              run_id: "r1",
            }),
          ],
          next_cursor: 0,
        });
      if (call.path === `${o}/conversations/c9/artifacts`)
        return json(200, { artifacts: [MD] });
      if (call.path === `${o}/runs/r1/artifacts`)
        return json(200, { artifacts: [MD] });
      if (call.path === `${o}/runs/r1/artifacts/a-md`)
        return new Response("# 方案标题", { status: 200 });
      if (call.path === `${o}/runs/r1/events`)
        return json(200, { events: [], next_cursor: 0 });
      if (call.path === `${o}/runs/r1/result`)
        return json(404, { error: { code: "NOT_FOUND", message: "x" } });
      if (call.path === `${o}/runs/r1`)
        return json(
          200,
          run("r1", {
            conversation_id: "c9",
            state: "SUCCEEDED",
            finished_at: "2026-11-04T08:01:00Z",
          }),
        );
      return undefined;
    });
    const queryClient = createQueryClient();
    queryClient.setDefaultOptions({ queries: { retry: false } });
    render(
      <MemoryRouter
        initialEntries={[`/o/${ORG}/tasks/OPEN-1?view=chat&conversationId=c9`]}
      >
        <App api={server.api} queryClient={queryClient} locale="zh-CN" />
        <Search />
      </MemoryRouter>,
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "产物：方案.md" }),
    );
    const aside = await screen.findByRole("complementary", {
      name: "会话产物",
    });
    expect(
      await within(aside).findByRole("heading", { name: "方案标题" }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("search").textContent).toBe(
      "?view=chat&conversationId=c9&artifact=a-md",
    );
    await userEvent.click(
      within(aside).getByRole("button", { name: "关闭产物面板" }),
    );
    expect(screen.getByTestId("search").textContent).toBe(
      "?view=chat&conversationId=c9",
    );
    expect(
      screen.queryByRole("complementary", { name: "会话产物" }),
    ).not.toBeInTheDocument();
  });
});
