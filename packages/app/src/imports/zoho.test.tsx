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

const org = "/v1/organizations/o1";
const projectURL = `${org}/imports/zoho_bugtracker/projects`;
const createURL = `${org}/imports/zoho_bugtracker/project`;
const pageURL = "/o/o1/settings/import-export?source=zoho_bugtracker";
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
  provider: "zoho_bugtracker",
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
  source: "zoho_bugtracker_api",
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
  if (call.path === `${org}/integrations/zoho_bugtracker`)
    return json(200, {
      provider: {
        id: "zoho_bugtracker",
        name: "Zoho BugTracker",
        availability: "available",
        template: "A",
        configured: true,
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
  await userEvent.click(await screen.findByLabelText("Zoho 门户"));
  await userEvent.click(screen.getByRole("option", { name: "Design" }));
}

describe("Zoho BugTracker project import", () => {
  beforeEach(() => localStorage.clear());
  it("loads subsequent project pages with the opaque offset and clears selection when workspace changes", async () => {
    const calls = renderSettings(pageURL, (c) => {
      if (c.path === projectURL) {
        if (c.query.get("connection_id") === "c2")
          return json(200, {
            projects: [{ id: "1003", name: "Second workspace", key: "1003" }],
            next_index: 0,
          });
        return json(
          200,
          c.query.get("index") === "101"
            ? {
                projects: [{ id: "1002", name: "Later project", key: "1002" }],
                next_index: 0,
              }
            : {
                projects: [{ id: "1001", name: "First project", key: "1001" }],
                next_index: 101,
              },
        );
      }
      return base(c);
    });
    await enterPicker();
    await userEvent.click(
      await screen.findByRole("button", { name: "加载更多项目" }),
    );
    expect(await screen.findByText("已列出 2 个活动项目")).toBeVisible();
    await userEvent.click(screen.getByLabelText("项目"));
    await userEvent.click(
      screen.getByRole("option", { name: "Later project" }),
    );
    expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled();
    expect(
      calls
        .find((c) => c.path === projectURL && c.query.get("index") === "101")
        ?.query.get("index"),
    ).toBe("101");
    await userEvent.click(screen.getByLabelText("Zoho 门户"));
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
        ? apiError(429, "ZOHO_RATE_LIMITED")
        : c.path === createURL
          ? ++attempt === 1
            ? apiError(502, "UPSTREAM_UNAVAILABLE")
            : json(201, job())
          : base(c),
    );
    await enterPicker();
    await userEvent.click(screen.getByLabelText("使用项目 ID"));
    await userEvent.type(
      screen.getByLabelText("项目 ID（保留完整数字）"),
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
    await waitFor(() =>
      expect(calls.some((c) => c.path === `${org}/imports/job1`)).toBe(true),
    );
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
});
