import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import {
  applyGroupOrder,
  groupTasks,
  isTrailingGroup,
  orderedGroups,
} from "./grouping";
import {
  defaultDisplay,
  displayQuery,
  displayStorageKey,
  loadDisplay,
  normalizeDisplay,
  saveDisplay,
  sortQuery,
} from "./model";
import { backend, makeTask, ORG, SPACE } from "./test-backend";

const storageKey = displayStorageKey(ORG, `space:${SPACE}`);
const route = `/o/${ORG}/spaces/${SPACE}/tasks`;
function renderTasks(path = route) {
  const server = backend();
  const client = createQueryClient();
  client.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={client} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}
beforeEach(() =>
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({})),
);
afterEach(() => localStorage.clear());

describe("T01 显示排序兼容及分组", () => {
  it("旧配置保留各排序方向；新方向持久化，非法值回退，manual强制asc", () => {
    for (const sort of [
      "manual",
      "due",
      "created",
      "updated",
      "priority",
      "estimate",
      "iteration",
      "project",
    ] as const) {
      const normalized = normalizeDisplay({ sort }, defaultDisplay("space"));
      expect(displayQuery(normalized).order).toBe(sortQuery(sort).order);
      expect(normalized.sortDirection).toBeUndefined();
    }
    const saved = {
      ...defaultDisplay("space"),
      sort: "priority" as const,
      sortDirection: "asc" as const,
      groupOrder: { status: "reverse" as const, crew: "default" as const },
    };
    saveDisplay(ORG, `space:${SPACE}`, saved);
    expect(loadDisplay(ORG, `space:${SPACE}`, "space")).toMatchObject({
      sortDirection: "asc",
      groupOrder: { status: "reverse" },
    });
    expect(sortQuery("manual", "desc")).toEqual({
      sort: "manual",
      order: "asc",
    });
    expect(
      normalizeDisplay(
        {
          sortDirection: "bad" as never,
          groupOrder: { status: "bad" as never },
        },
        defaultDisplay("space"),
      ),
    ).toEqual(defaultDisplay("space"));
  });

  it("仅反转分组，空值/其他迭代留末尾，多归属任务及组内顺序保留", () => {
    const tasks = [
      makeTask(1, "st1", { iteration_id: "it1" }),
      makeTask(2, "st1", { iteration_id: "it1" }),
      makeTask(3, "st1", { iteration_id: "it2" }),
      makeTask(4, "st1", { iteration_id: "old" }),
      makeTask(5, "st1"),
    ];
    const ctx = {
      columns: [],
      people: [],
      agents: [],
      refNames: new Map([
        ["it1", "One"],
        ["it2", "Two"],
      ]),
    };
    const groups = orderedGroups("iteration", tasks, ctx);
    expect(
      applyGroupOrder(groups, "reverse", isTrailingGroup).map((g) => g.key),
    ).toEqual(["it2", "it1", "other", "none"]);
    expect(groups.map((g) => g.key)).toEqual(["it1", "it2", "other", "none"]);
    expect(
      groupTasks(tasks, "iteration", ctx)
        .get("it1")
        ?.map((task) => task.id),
    ).toEqual(["task-1", "task-2"]);
    expect(applyGroupOrder(groups, "default", isTrailingGroup)).toBe(groups);
    expect(
      applyGroupOrder(
        orderedGroups("priority", tasks, ctx),
        "reverse",
        isTrailingGroup,
      ).map((g) => g.key),
    ).toEqual(["LOWEST", "LOW", "NORMAL", "HIGH", "URGENT", "NONE"]);
  });
});

describe("T01 排序入口和消费者", () => {
  it("手动方向禁用，反向状态组改变看板及列表组序并保存", async () => {
    const user = userEvent.setup();
    renderTasks();
    await screen.findByRole("region", { name: "待处理 2" });
    await user.click(screen.getByRole("button", { name: "显示设置" }));
    expect(
      screen.getByRole("combobox", { name: "任务排序方向" }),
    ).toBeDisabled();
    expect(
      screen.getByText("手动排序固定为升序；可在看板中拖动任务调整位置。"),
    ).toBeInTheDocument();
    await user.selectOptions(
      screen.getByRole("combobox", { name: "状态分组顺序" }),
      "reverse",
    );
    const visible = () =>
      screen
        .getAllByRole("region")
        .map((el) => el.getAttribute("aria-label"))
        .filter((s) => s === "待处理 2" || s === "进行中 1");
    expect(visible()).toEqual(["进行中 1", "待处理 2"]);
    await user.click(screen.getByRole("radio", { name: "列表" }));
    await waitFor(() => expect(visible()).toEqual(["进行中 1", "待处理 2"]));
    expect(JSON.parse(localStorage.getItem(storageKey)!)).toMatchObject({
      groupOrder: { status: "reverse" },
    });
  });

  it("选键沿用旧默认方向，显式升序发order=asc，新分页不复用旧游标", async () => {
    const user = userEvent.setup();
    const server = renderTasks();
    await screen.findByRole("region", { name: "待处理 2" });
    await user.click(screen.getByRole("button", { name: "显示设置" }));
    await user.selectOptions(
      screen.getByRole("combobox", { name: "排序" }),
      "created",
    );
    expect(screen.getByRole("combobox", { name: "任务排序方向" })).toHaveValue(
      "desc",
    );
    await user.selectOptions(
      screen.getByRole("combobox", { name: "任务排序方向" }),
      "asc",
    );
    await waitFor(() =>
      expect(
        server
          .find("GET", "/tasks")
          .some(
            (call) =>
              call.query.get("sort") === "created" &&
              call.query.get("order") === "asc" &&
              !call.query.has("cursor"),
          ),
      ).toBe(true),
    );
    expect(JSON.parse(localStorage.getItem(storageKey)!)).toMatchObject({
      sort: "created",
      sortDirection: "asc",
    });
  });

  it("旧甘特保留开始日期优先，显式任务方向切换优先级且开始日期仍可重新开启", async () => {
    const user = userEvent.setup();
    renderTasks(`${route}?view=gantt`);
    const start = await screen.findByRole("button", { name: "开始日期" });
    expect(start).toHaveAttribute("aria-pressed", "true");
    expect(
      screen.getByText("当前按开始日期排序，覆盖任务排序"),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "显示设置" }));
    await user.selectOptions(
      screen.getByRole("combobox", { name: "排序" }),
      "created",
    );
    await screen.findByText("当前使用任务排序");
    await user.click(screen.getByRole("button", { name: "显示设置" }));
    const currentStart = screen.getByRole("button", { name: "开始日期" });
    expect(currentStart).toHaveAttribute("aria-pressed", "false");
    await user.click(currentStart);
    expect(currentStart).toHaveAttribute("aria-pressed", "true");
  });

  it("已持久化的显式方向在新打开的甘特优先于开始日期", async () => {
    saveDisplay(ORG, `space:${SPACE}`, {
      ...defaultDisplay("space"),
      layout: "gantt",
      sort: "priority",
      sortDirection: "asc",
    });
    renderTasks();
    expect(
      await screen.findByRole("button", { name: "开始日期" }),
    ).toHaveAttribute("aria-pressed", "false");
  });
});
