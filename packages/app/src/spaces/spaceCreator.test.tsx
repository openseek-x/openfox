import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import type { SpaceDetail } from "@openfox/api";

import { json, renderSettings, ts } from "../settings/testing";

function space(id: string, creator?: SpaceDetail["creator"]): SpaceDetail {
  return {
    id,
    name: `Space ${id}`,
    prefix: id.toUpperCase(),
    visibility: "shared",
    version: 1,
    created_at: ts,
    updated_at: ts,
    icon: null,
    purpose: "software_development",
    status: "active",
    retired_at: null,
    position: null,
    my_role: "OWNER",
    can_manage: true,
    member_count: 2,
    task_type_count: 1,
    label_count: 0,
    task_count: 3,
    features: { views: true, projects: true, iterations: false, triage: false },
    multi_assignee: false,
    estimation: {
      scale: "notUsed",
      extended: false,
      allow_zero: false,
      count_unestimated: false,
    },
    ...(creator !== undefined ? { creator } : {}),
  };
}

describe("space creator column", () => {
  it("renders current names, former members and unknown old records without extra member requests", async () => {
    const spaces = [
      space("active", { id: "u1", name: "Current creator", status: "active" }),
      space("former", { id: "u2", name: null, status: "unavailable" }),
      space("legacy", null),
      space("oldserver"),
    ];
    const calls = renderSettings("/o/o1/settings/spaces", ({ path }) =>
      path === "/v1/organizations/o1/spaces"
        ? json(200, { spaces })
        : undefined,
    );
    const table = await screen.findByRole("table", { name: "空间" });
    expect(
      within(table).getByRole("columnheader", { name: "创建人" }),
    ).toBeVisible();
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(4);
    expect(
      within(rows[0]!).getByRole("cell", { name: "Current creator" }),
    ).toBeVisible();
    expect(
      within(rows[1]!).getByRole("cell", { name: "已离开" }),
    ).toBeVisible();
    expect(within(rows[2]!).getByRole("cell", { name: "未知" })).toBeVisible();
    expect(within(rows[3]!).getByRole("cell", { name: "未知" })).toBeVisible();
    expect(calls.filter(({ path }) => path.includes("/members"))).toHaveLength(
      0,
    );

    await userEvent.type(screen.getByPlaceholderText("按名称筛选…"), "former");
    expect(within(table).getAllByRole("row")).toHaveLength(2);
    expect(within(table).getByRole("cell", { name: "已离开" })).toBeVisible();
  });
});
