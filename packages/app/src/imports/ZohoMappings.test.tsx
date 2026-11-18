import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import type { ImportAnalysis } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import {
  ZohoMappings,
  zohoMappingsValid,
  zohoReconcileStatuses,
  type ZohoMappingsValue,
} from "./ZohoMappings";
import { UsersStep } from "./WizardSteps";
import { nextZohoProjectPage } from "./ZohoProjectPicker";
vi.mock("../tasks/queries", () => ({
  useOrgPeople: () => ({ data: [] }),
  personName: () => "Member",
  useSpaceTypes: () => ({ data: [{ id: "type", name: "Local task" }] }),
  useStatuses: () => ({
    data: [{ id: "status", name: "Open local", category: "TODO" }],
  }),
}));
const analysis = {
  types: [{ source: "Bug", count: 1 }],
  statuses: [
    {
      source: "9007199254740993123",
      source_label: "Open remote",
      count: 1,
      done: false,
      suggested_status_id: "wrong",
    },
  ],
  priority_fields: [
    {
      type_source: "Bug",
      field_id: "severity",
      field_label: "Severity",
      values: [{ source: "3001", option_label: "Major" }],
    },
  ],
  priority_impact: { current_levels: 4 },
} as ImportAnalysis;
it("requires explicit mappings and never reuses a status-name suggestion", () => {
  expect(zohoReconcileStatuses(analysis, [])).toEqual([
    { source: "9007199254740993123" },
  ]);
  expect(
    zohoMappingsValid(analysis, { types: [], statuses: [], priorities: [] }),
  ).toBe(false);
  expect(
    zohoMappingsValid(analysis, {
      types: [{ source: "Bug", use_default: true }],
      statuses: [{ source: "9007199254740993123", status_id: "status" }],
      priorities: [{ source: "3001", priority: "HIGH" }],
    }),
  ).toBe(true);
});
it("maps opaque status/severity IDs and offers only the current four-level priorities", async () => {
  const changed = vi.fn();
  function Form() {
    const [value, set] = useState<ZohoMappingsValue>({
      types: [],
      statuses: [],
      priorities: [],
    });
    return (
      <ZohoMappings
        org="o"
        space="s"
        analysis={analysis}
        value={value}
        onChange={(v) => {
          set(v);
          changed(v);
        }}
      />
    );
  }
  render(
    <I18nProvider initialLocale="zh-CN">
      <Form />
    </I18nProvider>,
  );
  await userEvent.click(screen.getByLabelText("类型「Bug」映射"));
  await userEvent.click(screen.getByRole("option", { name: "空间默认类型" }));
  await userEvent.click(screen.getByLabelText(/Open remote/));
  await userEvent.click(screen.getByRole("option", { name: "Open local" }));
  await userEvent.click(
    screen.getByLabelText("Severity「Severity: Major」映射"),
  );
  expect(screen.queryByRole("option", { name: "最低" })).toBeNull();
  await userEvent.click(screen.getByRole("option", { name: "高" }));
  expect(changed).toHaveBeenLastCalledWith({
    types: [{ source: "Bug", use_default: true }],
    statuses: [{ source: "9007199254740993123", status_id: "status" }],
    priorities: [{ source: "3001", priority: "HIGH" }],
  });
});
it("bounds pagination and refuses repeated cursors", () => {
  const page = { projects: [], next_index: 101 };
  expect(nextZohoProjectPage(page, [page], [1])).toBe(101);
  expect(nextZohoProjectPage(page, [page], [1, 101])).toBeUndefined();
  expect(
    nextZohoProjectPage({ ...page, next_index: 0 }, [page], [1]),
  ).toBeUndefined();
  expect(nextZohoProjectPage(page, Array(50).fill(page), [1])).toBeUndefined();
});

it("Zoho reporter mappings omit invitation while retaining external/member/skip choices", async () => {
  render(
    <I18nProvider initialLocale="zh-CN">
      <UsersStep
        orgId="o"
        brand="Zoho BugTracker"
        allowInvites={false}
        analysis={{
          ...analysis,
          users: [
            {
              source_id: "99",
              name: "Reporter",
              count: 1,
              suggested_user_id: null,
            },
          ],
        }}
        mappings={[{ source_id: "99", mode: "external" }]}
        onChange={() => {}}
      />
    </I18nProvider>,
  );
  await userEvent.click(screen.getByLabelText("Reporter 的导入方式"));
  expect(screen.queryByRole("option", { name: /邀请/ })).toBeNull();
  expect(screen.getAllByRole("option")).toHaveLength(3);
});
