import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it } from "vitest";
import {
  apiError,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";

const path = "/v1/organizations/o1/imports/j1/asana/types";
const view = {
  version: 7,
  space_id: "s1",
  types: [{ source: "default_task", use_default: true }],
  sources: [
    { source: "default_task", count: 1 },
    { source: "future_subtype", count: 2 },
  ],
};

function renderPage(route: Route, source = "asana_api") {
  return renderSettings("/o/o1/settings/integrations/asana", (call) => {
    const result = route(call);
    if (result) return result;
    if (call.path.endsWith("/integrations/asana"))
      return json(200, {
        provider: {
          id: "asana",
          name: "Asana",
          category: "project",
          availability: "available",
          template: "A",
          configured: true,
          bindable: true,
          capabilities: ["import"],
          summary: { connections: 1, status: "ok" },
        },
        can_manage: true,
        connections: [
          {
            id: "c1",
            provider: "asana",
            scope: "ORGANIZATION",
            external_id: "w1",
            display_name: "Design",
            status: "ACTIVE",
            version: 3,
            created_at: ts,
            updated_at: ts,
          },
        ],
        personal_connections: [],
        sync_links: [],
      });
    if (call.path === "/v1/organizations/o1/imports")
      return json(200, {
        imports: [
          {
            id: "j1",
            source,
            source_label: "Tasks",
            space_name: "Local",
            sync: {
              state: "ACTIVE",
              direction: "INBOUND",
              version: 7,
              checking: false,
              error_code: "TYPE_MAPPING",
            },
          },
        ],
        total: 1,
        next_cursor: "",
        can_manage: true,
      });
    if (call.path.includes("/spaces/s1/task-types"))
      return json(200, {
        task_types: [{ id: "type-1", name: "Local task", enabled: true }],
      });
    if (call.path.endsWith("/task-statuses"))
      return json(200, { statuses: [] });
    return undefined;
  });
}

beforeEach(() => localStorage.clear());

it("exposes unknown source types, requires a choice, and saves the fetched version before refreshing imports", async () => {
  const calls = renderPage((call) =>
    call.path === path
      ? json(200, call.method === "PUT" ? {} : view)
      : undefined,
  );
  expect(await screen.findByText(/新增来源类型尚未映射/)).toBeVisible();
  await userEvent.click(screen.getByRole("button", { name: "类型映射" }));
  const select = await screen.findByLabelText("类型「future_subtype」映射");
  expect(select).toHaveTextContent("请选择");
  expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
  expect(calls.some((c) => c.method === "PUT")).toBe(false);
  await userEvent.click(select);
  await userEvent.click(
    await screen.findByRole("option", { name: "Local task" }),
  );
  await userEvent.click(screen.getByRole("button", { name: "保存" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(
    calls.find((c) => c.method === "PUT" && c.path === path)?.body,
  ).toEqual({
    version: 7,
    types: [...view.types, { source: "future_subtype", type_id: "type-1" }],
  });
  expect(
    calls.filter((c) => c.path === "/v1/organizations/o1/imports").length,
  ).toBeGreaterThan(1);
});

it("keeps the dialog open on stale version and reloads the current mappings before a new save", async () => {
  let reads = 0,
    saves = 0;
  const calls = renderPage((call) => {
    if (call.path !== path) return undefined;
    if (call.method === "PUT")
      return ++saves === 1 ? apiError(409, "VERSION_CONFLICT") : json(200, {});
    return json(200, { ...view, version: ++reads === 1 ? 7 : 8 });
  });
  await userEvent.click(
    await screen.findByRole("button", { name: "类型映射" }),
  );
  await userEvent.click(
    await screen.findByLabelText("类型「future_subtype」映射"),
  );
  await userEvent.click(screen.getByRole("option", { name: "空间默认类型" }));
  await userEvent.click(screen.getByRole("button", { name: "保存" }));
  expect(await screen.findByRole("alert")).toBeVisible();
  expect(screen.getByRole("dialog")).toBeVisible();
  await userEvent.click(
    screen.getByRole("button", { name: "重新加载当前映射" }),
  );
  await waitFor(() =>
    expect(
      screen.getByLabelText("类型「future_subtype」映射"),
    ).toHaveTextContent("请选择"),
  );
  expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
  await userEvent.click(screen.getByLabelText("类型「future_subtype」映射"));
  await userEvent.click(screen.getByRole("option", { name: "空间默认类型" }));
  await userEvent.click(screen.getByRole("button", { name: "保存" }));
  await waitFor(() =>
    expect(calls.filter((c) => c.method === "PUT")).toHaveLength(2),
  );
  expect(calls.filter((c) => c.method === "PUT").at(-1)?.body).toMatchObject({
    version: 8,
  });
});

it("does not permit saving when the server rejects access to the mappings", async () => {
  const calls = renderPage((call) =>
    call.path === path ? apiError(403, "FORBIDDEN") : undefined,
  );
  await userEvent.click(
    await screen.findByRole("button", { name: "类型映射" }),
  );
  expect(await screen.findByRole("alert")).toBeVisible();
  expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
  expect(calls.some((c) => c.method === "PUT")).toBe(false);
});

it("does not expose Asana type recovery for another source", async () => {
  renderPage(() => undefined, "teambition_api");
  await screen.findByLabelText("同步方向");
  expect(screen.queryByRole("button", { name: "类型映射" })).toBeNull();
});
