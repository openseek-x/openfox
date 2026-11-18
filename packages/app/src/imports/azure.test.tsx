import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it } from "vitest";
import { json, renderSettings, ts, type Route } from "../settings/testing";

const org = "/v1/organizations/o1";
const projectURL = `${org}/imports/azure_devops/projects`;
const createURL = `${org}/imports/azure_devops/project`;
const pageURL = "/o/o1/settings/import-export?source=azure_devops";
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
  provider: "azure_devops",
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
  source: "azure_devops_api",
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
      continuous_sync: true,
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
  if (call.path === `${org}/integrations/azure_devops`)
    return json(200, {
      provider: {
        id: "azure_devops",
        name: "Azure DevOps",
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
  await userEvent.click(await screen.findByLabelText("Azure DevOps 组织"));
  await userEvent.click(screen.getByRole("option", { name: "Design" }));
}

const project = "22222222-2222-4222-8222-222222222222";
const analysis = {
  capabilities: {
    sprints: false,
    keep_numbers: false,
    sprint_count: 0,
    project_sync: true,
  },
  statuses: [
    {
      source: "Custom.Bug/New",
      source_label: "Bug / New",
      count: 0,
      done: false,
      suggested_status_id: "status1",
    },
  ],
  types: [
    { source: "Custom.Bug", source_label: "Bug", count: 0, type_id: null },
  ],
  users: [],
  counts: { total: 0, open: 0, done: 0, invalid: 0 },
  overwrite: { existing: 0, modified: 0 },
  members: { member: 0, invite: 0, external: 0, skip: 0 },
  issues: [],
};
beforeEach(() => localStorage.clear());
it("starts an empty inbound snapshot and requires explicit metadata mappings without name inference", async () => {
  const ready = job({
    phase: "",
    version: 2,
    fetch: { fetched: 0, total: 0 },
    analysis,
  });
  const calls = renderSettings(pageURL, (c) =>
    c.path === projectURL
      ? json(200, {
          projects: [
            {
              id: project,
              name: "Project",
              visibility: "private",
              state: "wellFormed",
            },
          ],
          next_offset: "",
        })
      : c.path === `${org}/imports/azure_devops/metadata`
        ? json(200, { metadata: { types: [], fields: [] } })
        : c.path === createURL
          ? json(201, job())
          : c.path === `${org}/imports/job1`
            ? json(200, ready)
            : c.path === `${org}/imports/job1/config`
              ? (
                  c.body as {
                    config: {
                      statuses: {
                        status_id?: string;
                        new_name?: string;
                        new_category?: string;
                      }[];
                    };
                  }
                ).config.statuses.some(
                  (mapping) =>
                    !mapping.status_id &&
                    !(mapping.new_name && mapping.new_category),
                )
                ? json(400, {
                    error: {
                      code: "INVALID_IMPORT_CONFIG",
                      message: "Missing status target",
                    },
                  })
                : json(200, ready)
              : c.path.endsWith("/task-statuses")
                ? json(200, {
                    statuses: [
                      {
                        id: "status1",
                        name: "New",
                        category: "TODO",
                        position: 0,
                      },
                    ],
                  })
                : base(c),
  );
  await enterPicker();
  await userEvent.click(await screen.findByLabelText("项目"));
  await userEvent.click(screen.getByRole("option", { name: "Project" }));
  expect(screen.getByLabelText("导入已有工作项")).not.toBeChecked();
  await userEvent.click(screen.getByRole("button", { name: "下一步" }));
  await waitFor(() =>
    expect(calls.some((c) => c.path === `${org}/imports/job1`)).toBe(true),
  );
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled(),
  );
  await userEvent.click(screen.getByRole("button", { name: "下一步" }));
  expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled();
  await userEvent.click(screen.getByRole("button", { name: "下一步" }));
  const type = await screen.findByLabelText("类型「Bug」映射");
  expect(
    calls
      .filter((c) => c.path === `${org}/imports/job1/config`)
      .every(
        (c) =>
          (c.body as { config: { statuses: unknown[] } }).config.statuses
            .length === 0,
      ),
  ).toBe(true);
  expect(type).toHaveTextContent("请选择");
  expect(screen.getByLabelText("Bug / New 导入为")).toHaveTextContent("请选择");
  expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
  await userEvent.click(type);
  await userEvent.click(screen.getByRole("option", { name: "空间默认类型" }));
  await userEvent.click(screen.getByLabelText("Bug / New 导入为"));
  await userEvent.click(screen.getByRole("option", { name: "New" }));
  expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled();
  const write = calls.find((c) => c.path === createURL)!;
  expect(write.headers.get("Idempotency-Key")).toBeTruthy();
  expect(write.body).toEqual({
    connection_id: "c1",
    project_id: project,
    space_id: "space1",
    include_history: false,
    priority_field: "",
  });
});
