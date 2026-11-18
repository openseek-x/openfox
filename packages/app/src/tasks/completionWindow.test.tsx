import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import {
  defaultDisplay,
  displayQuery,
  displayStorageKey,
  loadDisplay,
  normalizeDisplay,
  saveDisplay,
} from "./model";
import { backend, json, ORG, SPACE } from "./test-backend";

afterEach(() => localStorage.clear());

it("preserves old defaults and saves only the relative completion range", () => {
  expect(normalizeDisplay({}, defaultDisplay("space")).completed).toBe("all");
  for (const completed of [
    "all",
    "hide",
    "last_7_days",
    "last_30_days",
  ] as const) {
    const display = { ...defaultDisplay("space"), completed };
    saveDisplay(ORG, "range-test", display);
    expect(loadDisplay(ORG, "range-test", "space").completed).toBe(completed);
    expect(displayQuery(display).completed).toBe(
      completed === "all" ? undefined : completed,
    );
    expect(displayQuery(display).completed_as_of).toBeUndefined();
  }
  expect(
    normalizeDisplay({ completed: "invalid" as never }, defaultDisplay("space"))
      .completed,
  ).toBe("all");
});

it("keeps Gantt's completion switch consistent with range changes and the actual request anchor", async () => {
  const anchor = "2026-11-13T03:04:05.123456Z";
  const server = backend((call) => {
    if (call.path.endsWith("/tasks/counts"))
      return json(200, {
        total: 3,
        by_status: [],
        completed_as_of: call.query.get("completed")?.startsWith("last_")
          ? anchor
          : undefined,
      });
    return undefined;
  });
  saveDisplay(ORG, `space:${SPACE}`, {
    ...defaultDisplay("space"),
    layout: "gantt",
    completed: "last_7_days",
  });
  localStorage.setItem(`openfox:onboarding:${ORG}`, "{}");
  const client = createQueryClient();
  client.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[`/o/${ORG}/spaces/${SPACE}/tasks`]}>
      <App api={server.api} queryClient={client} locale="zh-CN" />
    </MemoryRouter>,
  );
  const user = userEvent.setup();
  const toggle = await screen.findByRole("switch", { name: "显示已完成" });
  const requested = (mode: string | null, at: string | null) =>
    server
      .find("GET", "/tasks")
      .some(
        (c) =>
          c.query.get("completed") === mode &&
          c.query.get("completed_as_of") === at,
      );
  await waitFor(() => expect(requested("last_7_days", anchor)).toBe(true));
  await user.click(toggle);
  await waitFor(() => expect(requested("hide", null)).toBe(true));
  expect(toggle).toHaveAttribute("aria-checked", "false");
  await user.click(screen.getByRole("button", { name: "显示设置" }));
  await user.selectOptions(
    screen.getByRole("combobox", { name: "已完成任务" }),
    "last_30_days",
  );
  await waitFor(() => expect(requested("last_30_days", anchor)).toBe(true));
  expect(screen.getByRole("switch", { name: "显示已完成" })).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await user.selectOptions(
    screen.getByRole("combobox", { name: "已完成任务" }),
    "hide",
  );
  expect(screen.getByRole("switch", { name: "显示已完成" })).toHaveAttribute(
    "aria-checked",
    "false",
  );
  await user.click(screen.getByRole("button", { name: "显示设置" }));
  await user.click(screen.getByRole("switch", { name: "显示已完成" }));
  await waitFor(() => expect(requested(null, null)).toBe(true));
  expect(
    server
      .find("GET", "/tasks")
      .every(
        (c) =>
          !c.query.has("completed_as_of") ||
          c.query.get("completed")?.startsWith("last_"),
      ),
  ).toBe(true);
});

it("shares the counts server anchor across layouts without feeding it back into counts or saved views", async () => {
  const anchor = "2026-11-13T03:04:05.123456Z";
  const server = backend((call) => {
    if (
      call.path.endsWith("/tasks/counts") &&
      call.query.get("completed")?.startsWith("last_")
    )
      return json(200, {
        total: 3,
        by_status: [
          { status_id: "st1", count: 2 },
          { status_id: "st2", count: 1 },
        ],
        completed_as_of: anchor,
      });
    return undefined;
  });
  localStorage.setItem(`openfox:onboarding:${ORG}`, "{}");
  const client = createQueryClient();
  client.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[`/o/${ORG}/spaces/${SPACE}/tasks`]}>
      <App api={server.api} queryClient={client} locale="zh-CN" />
    </MemoryRouter>,
  );
  const user = userEvent.setup();
  await screen.findByRole("region", { name: "待处理 2" });
  await user.click(screen.getByRole("button", { name: "显示设置" }));
  await user.selectOptions(
    screen.getByRole("combobox", { name: "已完成任务" }),
    "last_7_days",
  );
  expect(screen.getByText(/按连续24小时计算/)).toBeVisible();
  await waitFor(() =>
    expect(
      server
        .find("GET", "/tasks")
        .some((c) => c.query.get("completed_as_of") === anchor),
    ).toBe(true),
  );
  for (const layout of ["列表", "表格", "甘特图", "看板"]) {
    await user.click(screen.getByRole("radio", { name: layout }));
  }
  await user.selectOptions(
    screen.getByRole("combobox", { name: "已完成任务" }),
    "last_30_days",
  );
  await waitFor(() =>
    expect(
      server
        .find("GET", "/tasks")
        .some((c) => c.query.get("completed") === "last_30_days"),
    ).toBe(true),
  );
  const ranged = server
    .find("GET", "/tasks")
    .filter((c) => c.query.get("completed")?.startsWith("last_"));
  expect(ranged.length).toBeGreaterThan(0);
  expect(ranged.every((c) => c.query.get("completed_as_of") === anchor)).toBe(
    true,
  );
  expect(
    server
      .find("GET", "/tasks/counts")
      .every((c) => !c.query.has("completed_as_of")),
  ).toBe(true);
  const saved = JSON.parse(
    localStorage.getItem(displayStorageKey(ORG, `space:${SPACE}`))!,
  );
  expect(saved.completed).toBe("last_30_days");
  expect(saved.completed_as_of).toBeUndefined();
});
