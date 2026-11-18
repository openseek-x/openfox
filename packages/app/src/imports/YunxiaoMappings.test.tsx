import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import type { ImportAnalysis, ImportJob } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { CheckStep } from "./ApiSteps";
import { YunxiaoMappings, type YunxiaoMapping } from "./YunxiaoMappings";
vi.mock("../tasks/queries", () => ({
  useSpaceTypes: () => ({
    data: [{ id: "existing", name: "Existing type" }],
    isError: false,
  }),
}));
it("requires explicit type and source field choices, retains raw option identity, and permits NONE", async () => {
  const changed = vi.fn();
  const analysis = {
    types: [{ source: "type-id", source_label: "Bug", count: 1 }],
    priority_fields: [
      {
        type_source: "type-id",
        field_id: "rank",
        field_label: "Importance",
        values: [
          { source: "option-key", option_label: "P0", option_id: "999" },
        ],
      },
    ],
  } as ImportAnalysis;
  function Form() {
    const [value, set] = useState<YunxiaoMapping>({});
    return (
      <YunxiaoMappings
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
  const user = userEvent.setup();
  expect(changed).not.toHaveBeenCalled();
  await user.click(screen.getByLabelText("类型「Bug」映射"));
  await user.click(screen.getByRole("option", { name: "空间默认类型" }));
  expect(changed).toHaveBeenLastCalledWith({
    types: [{ source: "type-id", use_default: true }],
  });
  await user.click(screen.getByLabelText("「Bug」的来源优先级字段"));
  await user.click(screen.getByRole("option", { name: "Importance" }));
  expect(screen.getByLabelText("优先级「P0」映射")).toHaveTextContent("请选择");
  await user.click(screen.getByLabelText("优先级「P0」映射"));
  await user.click(screen.getByRole("option", { name: /^无$/ }));
  expect(changed.mock.lastCall?.[0]).toMatchObject({
    priority_fields: [{ type_source: "type-id", field_id: "rank" }],
    priorities: [{ source: "option-key", priority: "NONE" }],
  });
});

it("shows excluded archived count even when a completed fetch has no importable rows", () => {
  const job = {
    source: "yunxiao_api",
    status: "DRAFT",
    phase: "",
    total_count: 0,
    analysis: {
      archived_excluded: 3,
      counts: { total: 0, open: 0, done: 0 },
      statuses: [],
      types: [],
      users: [],
      capabilities: { sprints: false },
    },
  } as unknown as ImportJob;
  render(
    <I18nProvider initialLocale="zh-CN">
      <CheckStep brand="云效" job={job} onBack={() => {}} />
    </I18nProvider>,
  );
  expect(screen.getByText(/3.*归档|归档.*3/)).toBeVisible();
});
