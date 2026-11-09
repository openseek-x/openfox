import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { I18nProvider } from "@openfox/i18n";
import { HttpClient, createApi } from "@openfox/api";
import { ApiProvider } from "../context";
import {
  SearchTaskFilters,
  searchTaskFilterParams,
  type SearchTaskCondition,
} from "./SearchTaskFilters";
import { taskFilterOperators } from "./taskFilterModel";

const fid = "11111111-1111-4111-8111-111111111111";
function setup(
  spaceIds: string[] = [],
  scope: "all" | "subset" = "all",
  visibleIds = ["first", "second"],
  orgRole = "MEMBER",
) {
  const calls: string[] = [];
  const api = createApi(
    new HttpClient("/api", async (input) => {
      const url = new URL(String(input), "http://localhost");
      calls.push(url.pathname + url.search);
      const spaces = visibleIds.map((id) => ({
        id,
        name: id,
        visibility: "private",
        status: "active",
        features: { iterations: true, projects: true },
      }));
      const spaceId = url.searchParams.get("space_id");
      function response() {
        switch (url.pathname.split("/").pop()) {
          case "organizations":
            return { organizations: [{ id: "org", role: orgRole }] };
          case "invites":
            return {
              invitations: [
                {
                  id: "iv",
                  email: "future@example.test",
                  role: "MEMBER",
                  status: "PENDING",
                  expires_at: "2099-01-01",
                  space_ids: ["first"],
                },
                {
                  id: "expired",
                  email: "expired@example.test",
                  role: "MEMBER",
                  status: "PENDING",
                  expires_at: "2000-01-01",
                  space_ids: ["first"],
                },
                {
                  id: "revoked",
                  email: "revoked@example.test",
                  role: "MEMBER",
                  status: "REVOKED",
                  expires_at: "2099-01-01",
                  space_ids: ["first"],
                },
                {
                  id: "other",
                  email: "other@example.test",
                  role: "MEMBER",
                  status: "PENDING",
                  expires_at: "2099-01-01",
                  space_ids: ["second"],
                },
              ],
            };
          case "members":
            return { members: [], next_cursor: "" };
          case "spaces":
            return { spaces };
          case "custom-fields":
            return {
              custom_fields: [
                { id: fid, name: "验收数", kind: "NUMBER", options: [] },
              ],
            };
          case "labels":
            if (url.searchParams.get("scope") === "archived")
              return {
                labels: [
                  {
                    id: "old",
                    name: "已归档的标签",
                    archived_at: "2020-01-01",
                  },
                ],
              };
            if (url.searchParams.get("scope") === "space_archived")
              return { labels: [] };
            return {
              labels: [
                { id: "shared", name: "全局标签" },
                { id: spaceId, name: spaceId + " 标签" },
              ],
            };
          case "task-types":
            return {
              spaces: spaces.map((s) => ({
                space_id: s.id,
                space_name: s.name,
                task_types: [{ id: s.id, name: s.name, field_ids: [fid] }],
              })),
            };
          case "task-statuses":
            return {
              statuses: [
                { id: "a", name: "待办", category: "TODO" },
                { id: "b", name: "待办", category: "TODO" },
              ],
            };
          case "agent-directory":
            return { entries: [] };
          case "projects":
            return {
              projects: [{ id: "p-" + spaceId, name: spaceId + " 专项" }],
              next_cursor: "",
            };
          default:
            return { iterations: [], next_cursor: "" };
        }
      }
      return new Response(JSON.stringify(response()), {
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  function Harness() {
    const [value, setValue] = useState<SearchTaskCondition[]>([]);
    return (
      <>
        <SearchTaskFilters
          orgId="org"
          spaceSelection={scope}
          spaceIds={spaceIds}
          value={value}
          onChange={setValue}
        />
        <output aria-label="wire">
          {JSON.stringify(
            searchTaskFilterParams(value, new Date(2026, 10, 6, 12)),
          )}
        </output>
      </>
    );
  }
  render(
    <I18nProvider initialLocale="zh-CN">
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <ApiProvider api={api}>
          <MemoryRouter>
            <Harness />
          </MemoryRouter>
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
  return {
    user: userEvent.setup(),
    wire: () => JSON.parse(screen.getByLabelText("wire").textContent ?? "{}"),
    calls,
  };
}
describe("global task search filters", () => {
  it.each(["OWNER", "ADMIN"])(
    "%s 可筛选有效的受邀处理人，创建者没有邀请候选",
    async (role) => {
      const { user, wire, calls } = setup(
        ["first"],
        "subset",
        ["first", "second"],
        role,
      );
      await user.click(screen.getByRole("button", { name: "创建者" }));
      expect(screen.queryByText("邀请中")).toBeNull();
      expect(calls.filter((path) => path.endsWith("/invites"))).toEqual([]);
      await user.keyboard("{Escape}");
      await user.click(screen.getByRole("button", { name: "处理人" }));
      const option = await screen.findByRole("option", {
        name: /future@example.test/,
      });
      expect(screen.getByText("邀请中")).toBeVisible();
      expect(screen.queryByText("expired@example.test")).toBeNull();
      expect(screen.queryByText("revoked@example.test")).toBeNull();
      expect(screen.queryByText("other@example.test")).toBeNull();
      await user.click(option);
      expect(wire()).toEqual({ assignee: ["invitation:iv"] });
      expect(screen.getByRole("button", { name: "处理人" })).toHaveTextContent(
        "future@example.test",
      );
      await user.click(screen.getByRole("button", { name: "创建者" }));
      expect(
        screen.queryByRole("option", { name: /future@example.test/ }),
      ).toBeNull();
    },
  );

  it.each(["MEMBER", "GUEST"])(
    "%s 打开处理人和创建者都不请求邀请名单",
    async (role) => {
      const { user, calls } = setup([], "all", ["first"], role);
      for (const name of ["处理人", "创建者"]) {
        await user.click(screen.getByRole("button", { name }));
        await screen.findByRole("listbox");
        expect(screen.queryByText("邀请中")).toBeNull();
        await user.keyboard("{Escape}");
      }
      expect(calls.filter((path) => path.endsWith("/invites"))).toEqual([]);
    },
  );

  it("combines positive and negative fields, mixed creator types, and independent assignee/crew conditions", () => {
    expect(
      searchTaskFilterParams([
        { field: "creator", op: "is_not", values: ["user:u", "agent:a"] },
        { field: "assignee", op: "is", values: ["user:u", "none"] },
        { field: "crew", op: "is", values: ["c"] },
        { field: "content", op: "is_not", values: [" words "] },
        { field: "relation", op: "is", values: ["subtasks"] },
        { field: "sprint", op: "is_not", values: ["@rel:previous", "none"] },
        { field: "cf_1", op: "is_empty", values: [] },
        { field: "cf_2", op: "not_between", values: ["1", "3"] },
      ]),
    ).toEqual({
      creator_id_not: ["u"],
      creator_agent_id_not: ["a"],
      assignee: ["user:u", "none"],
      crew_id: ["c"],
      task_q_not: "words",
      relation: ["parent"],
      iteration_id_not: ["@rel:previous", "none"],
      cf_1_op: "is_empty",
      cf_2_op: "not_between",
      cf_2: ["1", "3"],
    });
    expect(taskFilterOperators("crew")).toEqual(["is"]);
  });
  it("normalizes local inclusive dates and clamped calendar month ends", () => {
    const now = new Date(2024, 2, 31, 14, 30);
    expect(
      searchTaskFilterParams(
        [
          { field: "createdAt", op: "is", values: ["past_1_month"] },
          { field: "dueDate", op: "is_not", values: ["next_1_month"] },
        ],
        now,
      ),
    ).toEqual({
      created_range: [new Date(2024, 1, 29, 14, 30).toISOString() + "/"],
      due_range_not: ["2024-03-31/2024-05-01"],
    });
    expect(
      searchTaskFilterParams(
        [
          {
            field: "createdAt",
            op: "is",
            values: ["between:2024-03-01|2024-03-25"],
          },
        ],
        now,
      ),
    ).toEqual({
      created_range: [
        new Date(2024, 2, 1).toISOString() +
          "/" +
          new Date(2024, 2, 26).toISOString(),
      ],
    });
  });
  it("keeps checkboxes open, collapses row selections and combines both date tabs", async () => {
    const { user, wire } = setup();
    await user.click(screen.getByRole("button", { name: "状态" }));
    await user.click(await screen.findByRole("checkbox", { name: "待办" }));
    expect(wire()).toEqual({ status_id: ["a", "b"] });
    await user.click(screen.getByRole("button", { name: "优先级" }));
    const option = screen
      .getAllByRole("option")
      .find((e) => e.textContent === "高")!;
    await user.click(option);
    expect(screen.queryByRole("listbox")).toBeNull();
    await user.click(screen.getByRole("button", { name: "日期" }));
    await user.click(screen.getByRole("checkbox", { name: "已逾期" }));
    await user.click(screen.getByRole("button", { name: "创建时间" }));
    await user.click(screen.getByRole("option", { name: /过去 1 天/ }));
    expect(wire()).toMatchObject({
      status_id: ["a", "b"],
      priority: ["HIGH"],
      due_range: ["/2026-11-06"],
      created_range: [new Date(2026, 10, 5, 12).toISOString() + "/"],
    });
    await user.click(screen.getByRole("button", { name: "移除日期筛选" }));
    expect(wire()).toEqual({ status_id: ["a", "b"], priority: ["HIGH"] });
  });
  it("collects usable choices across all visible spaces and exposes scoped custom operators", async () => {
    const { user, wire } = setup();
    await user.click(screen.getByRole("button", { name: "标签" }));
    expect(
      await screen.findByRole("checkbox", { name: "second 标签" }),
    ).toBeVisible();
    expect(screen.getAllByRole("checkbox", { name: "全局标签" })).toHaveLength(
      1,
    );
    await user.click(screen.getByRole("option", { name: /first 标签/ }));
    await user.click(screen.getByRole("button", { name: "专项" }));
    await user.click(
      await screen.findByRole("option", { name: /second 专项/ }),
    );
    await user.click(screen.getByRole("button", { name: "筛选" }));
    await user.click(await screen.findByRole("menuitem", { name: "验收数" }));
    const dialog = screen.getByRole("dialog", { name: "验收数" });
    await user.click(
      within(dialog).getByRole("button", { name: "切换运算符" }),
    );
    await user.click(screen.getByRole("menuitemradio", { name: "为空" }));
    expect(wire()).toEqual({
      label_id: ["first"],
      project_id: ["p-second"],
      ["cf_" + fid + "_op"]: "is_empty",
    });
  });
  it("does not fetch label candidates from outside a selected space", async () => {
    const { user } = setup(["second"], "subset");
    await user.click(screen.getByRole("button", { name: "标签" }));
    expect(
      await screen.findByRole("checkbox", { name: "second 标签" }),
    ).toBeVisible();
    expect(screen.queryByRole("checkbox", { name: "first 标签" })).toBeNull();
  });
  it("pages label lookups across large organizations eight spaces at a time", async () => {
    const visible = Array.from(
      { length: 54 },
      (_, i) => "s" + String(i).padStart(2, "0"),
    );
    const { user, calls } = setup([], "all", visible);
    await user.click(screen.getByRole("button", { name: "标签" }));
    await screen.findByRole("checkbox", { name: "s07 标签" });
    const requested = () =>
      calls
        .map((path) => new URL(path, "http://localhost"))
        .filter((url) => url.searchParams.get("scope") === "picker")
        .map((url) => url.searchParams.get("space_id"));
    expect(requested()).toEqual(visible.slice(0, 8));
    await user.click(screen.getByRole("button", { name: "查看更多" }));
    await screen.findByRole("checkbox", { name: "s15 标签" });
    expect(requested()).toEqual(visible.slice(0, 16));
  });
});
