import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "@openfox/i18n";

import { RunMetaInfo } from "./RunTranscriptDialog";

const view = (ui: React.ReactElement) =>
  render(<I18nProvider initialLocale="zh-CN">{ui}</I18nProvider>);

describe("RunMetaInfo", () => {
  it("显示触发人与来源，无 retry_of 与核对时不显示对应项", () => {
    view(
      <RunMetaInfo
        run={{ actor_id: "12345678-aaaa" }}
        summary={{ conversation_title: "t", task_display_id: "T-1" }}
        personName="张三"
      />,
    );
    expect(screen.getByText("张三")).toBeTruthy();
    expect(screen.getByText("任务")).toBeTruthy();
    expect(screen.queryByText(/重试自/)).toBeNull();
    expect(screen.queryByText(/核对流程/)).toBeNull();
  });

  it("有 retry_of 时来源为重试并可点击定位，核对时显示提示", async () => {
    const onLocate = vi.fn();
    view(
      <RunMetaInfo
        run={{
          actor_id: "12345678-aaaa",
          retry_of: "abcdef12-0000",
          reconciled: true,
        }}
        onLocate={onLocate}
      />,
    );
    expect(screen.getByText("12345678")).toBeTruthy();
    await userEvent.click(screen.getByText("abcdef12"));
    expect(onLocate).toHaveBeenCalledWith("abcdef12-0000");
    expect(screen.getByText("重试")).toBeTruthy();
    expect(screen.getByText(/核对流程/)).toBeTruthy();
  });
});
