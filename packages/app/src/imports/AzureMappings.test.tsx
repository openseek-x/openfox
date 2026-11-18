import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import type { ImportAnalysis } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import {
  AzureMappings,
  azureMappingsValid,
  azureReconcileStatuses,
  type AzureMappingsValue,
} from "./AzureMappings";
vi.mock("../tasks/queries", () => ({
  useSpaceTypes: () => ({ data: [{ id: "t1", name: "Local task" }] }),
  useStatuses: () => ({
    data: [{ id: "s1", name: "New", category: "TODO", position: 0 }],
  }),
}));
it("does not infer type/state/priority mappings from names and preserves exact metadata keys", async () => {
  const analysis = {
    types: [{ source: "Custom.Bug", source_label: "Bug", count: 0 }],
    statuses: [
      {
        source: "Custom.Bug/New",
        source_label: "Bug / New",
        suggested_status_id: "s1",
        done: false,
        count: 0,
      },
    ],
    priority_fields: [
      {
        field_id: "Custom.Priority",
        field_label: "Priority",
        values: [{ source: "Custom.Priority=1", option_label: "1" }],
      },
    ],
  } as ImportAnalysis;
  const changed = vi.fn();
  function Form() {
    const [value, set] = useState<AzureMappingsValue>({
      types: [],
      statuses: azureReconcileStatuses(analysis, []),
      priorities: [],
    });
    return (
      <>
        <AzureMappings
          org="o"
          space="s"
          analysis={analysis}
          value={value}
          onChange={(v) => {
            changed(v);
            set(v);
          }}
        />
        <button disabled={!azureMappingsValid(analysis, value)}>Save</button>
      </>
    );
  }
  render(
    <I18nProvider initialLocale="zh-CN">
      <Form />
    </I18nProvider>,
  );
  expect(changed).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  await userEvent.click(screen.getByLabelText("类型「Bug」映射"));
  await userEvent.click(screen.getByRole("option", { name: "空间默认类型" }));
  await userEvent.click(screen.getByLabelText("Bug / New 导入为"));
  await userEvent.click(screen.getByRole("option", { name: "New" }));
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  await userEvent.click(screen.getByLabelText("优先级「Priority: 1」映射"));
  await userEvent.click(screen.getByRole("option", { name: "中" }));
  expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
  expect(changed).toHaveBeenLastCalledWith({
    types: [{ source: "Custom.Bug", use_default: true }],
    statuses: [{ source: "Custom.Bug/New", status_id: "s1" }],
    priorities: [{ source: "Custom.Priority=1", priority: "NORMAL" }],
  });
});
