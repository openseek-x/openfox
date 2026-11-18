import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import type { RunResult } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";

import { RetryOfNote, ResultSummary, unverifiedReasons } from "./ResultSummary";
import { ExpandAllContext } from "./rows";

const base = { id: "x" } as unknown as RunResult;
const withGit = (git: unknown) => ({ ...base, git_summary: git }) as RunResult;

const view = (ui: React.ReactElement) =>
  render(<I18nProvider initialLocale="zh-CN">{ui}</I18nProvider>);

describe("ResultSummary", () => {
  it.each([
    undefined,
    null,
    base,
    withGit({ v: 1, repo: false, available: true }),
  ])("缺少 Git 统计或非 Git 聊天不生成空摘要卡：%j", (result) => {
    const { container } = view(<ResultSummary result={result} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("统计失败的警告保持可见，不伪装成无改动", () => {
    view(
      <ResultSummary
        result={withGit({ v: 1, repo: true, available: false })}
      />,
    );
    expect(screen.getByText("摘要暂不可用")).toBeInTheDocument();
    expect(screen.queryByText("无文件变更")).not.toBeInTheDocument();
  });

  it("缺少 Git 统计仍保留核对与未验证警告", () => {
    view(<ResultSummary run={{ reconciled: true }} result={base} />);
    expect(screen.getByText("未验证：曾进入核对流程")).toBeInTheDocument();
  });

  it("默认只显示统计与警告，键盘可展开分支、提交和文件详情", async () => {
    const user = userEvent.setup();
    view(
      <ResultSummary
        run={{ reconciled: true }}
        result={withGit({
          v: 1,
          repo: true,
          available: true,
          branch: "feat/x",
          commit_total: 1,
          commits: [{ hash: "abc1234", title: "add b" }],
          uncommitted: 2,
          impact: {
            files: 2,
            added: 3,
            deleted: 1,
            paths: ["a", "b"],
            paths_total: 2,
          },
          unverified_reasons: ["UNCOMMITTED_CHANGES"],
        })}
      />,
    );
    const trigger = screen.getByRole("button", { name: /变更摘要/ });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("feat/x")).not.toBeInTheDocument();
    expect(screen.queryByText(/add b/)).not.toBeInTheDocument();
    expect(trigger).toHaveTextContent("2 个文件，+3 -1");
    expect(trigger).toHaveTextContent("新增提交 1 个");
    expect(trigger).toHaveTextContent("未提交改动 2 处");
    expect(
      screen.getByText(/工作区有未提交改动；曾进入核对流程/),
    ).toBeVisible();
    await user.tab();
    expect(trigger).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(
      document.getElementById(trigger.getAttribute("aria-controls")!),
    ).toBeVisible();
    expect(screen.getByText("feat/x")).toBeVisible();
    expect(screen.getByText(/add b/)).toBeVisible();
    expect(screen.getByText("a、b")).toBeVisible();
    await user.keyboard(" ");
    expect(screen.queryByText("feat/x")).not.toBeInTheDocument();
    expect(
      screen.getByText(/工作区有未提交改动；曾进入核对流程/),
    ).toBeVisible();
  });

  it("全部展开信号可展开摘要，之后仍可手动收起", async () => {
    const result = withGit({
      v: 1,
      repo: true,
      available: true,
      branch: "main",
    });
    const ui = (signal: number) => (
      <I18nProvider initialLocale="zh-CN">
        <ExpandAllContext.Provider value={signal}>
          <ResultSummary result={result} />
        </ExpandAllContext.Provider>
      </I18nProvider>
    );
    const { rerender } = render(ui(0));
    expect(screen.queryByText("main")).not.toBeInTheDocument();
    expect(screen.queryByText("无文件变更")).not.toBeInTheDocument();
    rerender(ui(1));
    expect(screen.getByText("main")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: /变更摘要/ }));
    expect(screen.queryByText("main")).not.toBeInTheDocument();
  });

  it("有明确的零文件统计时才展示无文件变更", () => {
    view(
      <ResultSummary
        result={withGit({
          v: 1,
          repo: true,
          available: true,
          impact: { files: 0, added: 0, deleted: 0, paths: [], paths_total: 0 },
        })}
      />,
    );
    expect(screen.getByRole("button", { name: /变更摘要/ })).toHaveTextContent(
      "无文件变更",
    );
  });

  it("unverifiedReasons 去重并追加 RECONCILED", () => {
    const r = withGit({
      v: 1,
      repo: true,
      available: true,
      unverified_reasons: ["RECONCILED"],
    });
    expect(unverifiedReasons(r, { reconciled: true })).toEqual(["RECONCILED"]);
    expect(unverifiedReasons(base, { reconciled: true })).toEqual([
      "RECONCILED",
    ]);
    expect(unverifiedReasons(base, { reconciled: false })).toEqual([]);
  });

  it("RetryOfNote 仅在 retry_of 存在时渲染", () => {
    const { container } = view(<RetryOfNote />);
    expect(container.textContent).toBe("");
    view(<RetryOfNote retryOf="12345678-aaaa" />);
    expect(screen.getByText("12345678")).toBeTruthy();
  });
});
