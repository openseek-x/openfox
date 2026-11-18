import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import { backend, json, makeTask, ORG } from "./test-backend";

// HiFox 详情布局：移除独立验收编辑区，保存描述仍只更新描述字段。
beforeEach(() =>
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({})),
);
afterEach(() => localStorage.clear());

describe("任务验收要求", () => {
  it("详情页编辑描述不覆盖已有验收数据", async () => {
    const user = userEvent.setup();
    const task = {
      ...makeTask(1, "st1", { acceptance: "接口返回 200" }),
      description_reactions: [],
    };
    const server = backend((call) => {
      if (call.method === "GET" && call.path.endsWith("/by-display-id/OPEN-1"))
        return json(200, task);
      if (call.method === "PATCH" && call.path.endsWith("/tasks/task-1"))
        return json(200, { ...task, ...call.body!, version: 2 });
      return undefined;
    });
    const queryClient = createQueryClient();
    queryClient.setDefaultOptions({ queries: { retry: false } });
    render(
      <MemoryRouter initialEntries={[`/o/${ORG}/tasks/OPEN-1`]}>
        <App api={server.api} queryClient={queryClient} locale="zh-CN" />
      </MemoryRouter>,
    );
    await screen.findByRole("textbox", { name: "任务标题" });
    expect(
      screen.queryByRole("textbox", { name: "验收要求" }),
    ).not.toBeInTheDocument();
    const box = screen.getByRole("textbox", { name: "任务描述" });
    expect(box).toHaveAttribute("maxlength", "10000");
    await user.click(box);
    await user.type(box, "更新任务描述");
    await user.tab();
    await waitFor(() =>
      expect(server.find("PATCH", "/tasks/task-1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/tasks/task-1")[0]!.body).toEqual({
      description: "更新任务描述",
      version: 1,
    });
  });
});
