import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import type { ImportAnalysis, ImportConfig } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { AsanaTypeMappings } from "./AsanaTypeMappings";

vi.mock("../tasks/queries", () => ({
  useSpaceTypes: () => ({
    data: [{ id: "type-1", name: "Local task" }],
    isError: false,
  }),
}));

it("requires an explicit type or default choice and retains the exact resource subtype", async () => {
  const changed = vi.fn();
  function Form() {
    const [value, set] = useState<ImportConfig["types"]>([]);
    return (
      <AsanaTypeMappings
        org="o"
        space="s"
        analysis={
          {
            types: [{ source: "approval", source_label: "Approval", count: 1 }],
          } as ImportAnalysis
        }
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
  expect(screen.getByLabelText("类型「Approval」映射")).toHaveTextContent(
    "请选择",
  );
  expect(changed).not.toHaveBeenCalled();
  await userEvent.click(screen.getByLabelText("类型「Approval」映射"));
  await userEvent.click(screen.getByRole("option", { name: "Local task" }));
  expect(changed).toHaveBeenLastCalledWith([
    { source: "approval", type_id: "type-1" },
  ]);
  await userEvent.click(screen.getByLabelText("类型「Approval」映射"));
  await userEvent.click(screen.getByRole("option", { name: "空间默认类型" }));
  expect(changed).toHaveBeenLastCalledWith([
    { source: "approval", use_default: true },
  ]);
  expect(screen.queryByText(/优先级/)).toBeNull();
});
