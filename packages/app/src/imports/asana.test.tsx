import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import {
  apiError,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";
import { nextAsanaProjectPage } from "./AsanaProjectPicker";
import type { AsanaProjectsPage } from "../integrations/asana-api";

const org = "/v1/organizations/o1";
const projectURL = `${org}/imports/asana/projects`;
const createURL = `${org}/imports/asana/project`;
const pageURL = "/o/o1/settings/import-export?source=asana";
const space = {
  id: "space1",
  prefix: "OPEN",
  name: "Tasks",
  visibility: "shared",
  version: 1,
  status: "active",
  my_role: "OWNER",
  can_manage: true,
  created_at: ts,
  features: {},
};
const connections = ["Design", "Engineering"].map((name, i) => ({
  id: `c${i + 1}`,
  provider: "asana",
  scope: "ORGANIZATION",
  external_id: `w${i + 1}`,
  display_name: name,
  status: "ACTIVE",
  version: 3,
  created_at: ts,
  updated_at: ts,
}));
const job = (extra: Record<string, unknown> = {}) => ({
  id: "job1",
  space_id: "space1",
  space_name: "Tasks",
  source: "asana_api",
  source_label: "Design",
  file_name: "Project",
  status: "DRAFT",
  phase: "fetching",
  fetch: { fetched: 1, total: null },
  operation_id: "synthetic-job",
  total_count: 0,
  processed_count: 0,
  created_count: 0,
  updated_count: 0,
  skipped_count: 0,
  failed_count: 0,
  warnings: [],
  error_code: "",
  error_message: "",
  created_by: "u1",
  created_by_email: "lin@openfox.dev",
  created_at: ts,
  version: 1,
  config_version: 1,
  rollback: { eligible: false, reason: "not_finished", deadline: null },
  config: {
    columns: [],
    statuses: [],
    users: [],
    options: {
      import_open: true,
      import_done: true,
      migrated_label: false,
      overwrite: false,
      import_sprints: false,
      keep_numbers: false,
    },
  },
  ...extra,
});
const base: Route = (call) => {
  if (call.path === `${org}/imports`)
    return json(200, {
      imports: [],
      next_cursor: "",
      can_manage: true,
      has_active: false,
      total: 0,
    });
  if (call.path === `${org}/spaces`) return json(200, { spaces: [space] });
  if (call.path === `${org}/integrations/asana`)
    return json(200, {
      provider: {
        id: "asana",
        name: "Asana",
        availability: "available",
        template: "A",
        capabilities: ["import"],
      },
      can_manage: true,
      connections,
      personal_connections: [],
      sync_links: [],
    });
  if (call.path === `${org}/imports/job1`) return json(200, job());
  return undefined;
};
async function enterPicker() {
  await userEvent.click(await screen.findByRole("button", { name: "继续" }));
  await userEvent.click(await screen.findByLabelText("Asana workspace"));
  await userEvent.click(screen.getByRole("option", { name: "Design" }));
}

describe("Asana project import", () => {
  beforeEach(() => localStorage.clear());
  it("loads subsequent project pages with the opaque offset and clears selection when workspace changes", async () => {
    const calls = renderSettings(pageURL, (c) => {
      if (c.path === projectURL) {
        if (c.query.get("connection_id") === "c2")
          return json(200, {
            projects: [{ id: "p3", name: "Second workspace", key: "p3" }],
            next_offset: "",
          });
        return json(
          200,
          c.query.has("offset")
            ? {
                projects: [{ id: "p2", name: "Later project", key: "p2" }],
                next_offset: "",
              }
            : {
                projects: [{ id: "p1", name: "First project", key: "p1" }],
                next_offset: "opaque+/=",
              },
        );
      }
      return base(c);
    });
    await enterPicker();
    await userEvent.click(
      await screen.findByRole("button", { name: "加载更多项目" }),
    );
    expect(await screen.findByText("已加载 2 个可访问项目")).toBeVisible();
    await userEvent.click(screen.getByLabelText("项目"));
    await userEvent.click(
      screen.getByRole("option", { name: "Later project" }),
    );
    expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled();
    expect(
      calls
        .find((c) => c.path === projectURL && c.query.has("offset"))
        ?.query.get("offset"),
    ).toBe("opaque+/=");
    await userEvent.click(screen.getByLabelText("Asana workspace"));
    await userEvent.click(screen.getByRole("option", { name: "Engineering" }));
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
    await userEvent.click(screen.getByLabelText("项目"));
    expect(
      await screen.findByRole("option", { name: "Second workspace" }),
    ).toBeVisible();
    expect(screen.queryByRole("option", { name: "Later project" })).toBeNull();
  });
  it("allows a directly entered gid after discovery fails, reuses the create key, and waits for fetching", async () => {
    let attempt = 0;
    const calls = renderSettings(pageURL, (c) =>
      c.path === projectURL
        ? apiError(429, "ASANA_RATE_LIMITED")
        : c.path === createURL
          ? ++attempt === 1
            ? apiError(502, "UPSTREAM_UNAVAILABLE")
            : json(201, job())
          : base(c),
    );
    await enterPicker();
    await userEvent.click(screen.getByLabelText("直接填写项目 gid"));
    await userEvent.type(
      screen.getByLabelText("Asana 项目 gid"),
      "9007199254740993123",
    );
    await userEvent.click(screen.getByRole("button", { name: "下一步" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.path === createURL)).toHaveLength(1),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled(),
    );
    await userEvent.click(screen.getByRole("button", { name: "下一步" }));
    expect(
      await screen.findByText(/正在分页读取项目任务和子任务/),
    ).toBeVisible();
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
    const writes = calls.filter((c) => c.path === createURL);
    expect(writes).toHaveLength(2);
    expect(writes[0]!.headers.get("Idempotency-Key")).toBeTruthy();
    expect(writes[1]!.headers.get("Idempotency-Key")).toBe(
      writes[0]!.headers.get("Idempotency-Key"),
    );
    expect(writes[1]!.body).toEqual({
      connection_id: "c1",
      project_id: "9007199254740993123",
      space_id: "space1",
    });
    expect(calls.some((c) => c.path.endsWith("/start"))).toBe(false);
  });
  it("reports an incomplete crawl as failure instead of enabling mappings", async () => {
    const calls = renderSettings(pageURL, (c) =>
      c.path === projectURL
        ? json(200, {
            projects: [{ id: "p1", name: "Project", key: "p1" }],
            next_offset: "",
          })
        : c.path === createURL
          ? json(201, job())
          : c.path === `${org}/imports/job1`
            ? json(
                200,
                job({
                  status: "DISCARDED",
                  phase: "",
                  version: 2,
                  error_code: "ASANA_PERMISSION_DENIED",
                  error_message: "Synthetic source permission failure",
                }),
              )
            : base(c),
    );
    await enterPicker();
    await userEvent.click(await screen.findByLabelText("项目"));
    await userEvent.click(
      await screen.findByRole("option", { name: "Project" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "下一步" }));
    expect(
      await screen.findByText("Synthetic source permission failure"),
    ).toBeVisible();
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "返回修改配置" }));
    await userEvent.click(screen.getByLabelText("项目"));
    await userEvent.click(screen.getByRole("option", { name: "Project" }));
    await userEvent.click(screen.getByRole("button", { name: "下一步" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.path === createURL)).toHaveLength(2),
    );
    const writes = calls.filter((c) => c.path === createURL);
    expect(writes[1]!.headers.get("Idempotency-Key")).not.toBe(
      writes[0]!.headers.get("Idempotency-Key"),
    );
  });
  it("stops cyclic cursors and bounds discovery to fifty pages or five thousand projects", () => {
    const one: AsanaProjectsPage = {
      projects: [{ id: "p1", name: "One", key: "p1" }],
      next_offset: "next",
    };
    expect(nextAsanaProjectPage(one, [one], [""])).toBe("next");
    expect(nextAsanaProjectPage(one, [one, one], ["", "next"])).toBeUndefined();
    expect(
      nextAsanaProjectPage(
        one,
        Array.from({ length: 50 }, () => one),
        [""],
      ),
    ).toBeUndefined();
    const full = {
      projects: Array.from({ length: 5000 }, () => one.projects[0]!),
      next_offset: "next",
    };
    expect(nextAsanaProjectPage(full, [full], [""])).toBeUndefined();
    expect(
      nextAsanaProjectPage({ ...one, next_offset: "" }, [one], [""]),
    ).toBeUndefined();
  });
  it("shows a pagination boundary and still offers direct gid entry", async () => {
    renderSettings(pageURL, (c) =>
      c.path === projectURL
        ? json(200, {
            projects: [{ id: "p1", name: "Project", key: "p1" }],
            next_offset: "same",
          })
        : base(c),
    );
    await enterPicker();
    await userEvent.click(
      await screen.findByRole("button", { name: "加载更多项目" }),
    );
    expect(await screen.findByText(/未展示全部项目/)).toBeVisible();
    expect(screen.queryByRole("button", { name: "加载更多项目" })).toBeNull();
    await userEvent.click(screen.getByLabelText("直接填写项目 gid"));
    expect(screen.getByLabelText("Asana 项目 gid")).toBeEnabled();
  });
});
