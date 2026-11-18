import { render, screen, fireEvent } from "@testing-library/react";
import { I18nProvider } from "@openfox/i18n";
import { expect, it, vi } from "vitest";
import { AppErrorBoundary, AppErrorPage } from "./ErrorBoundary";
function Broken(): never {
  throw new Error("render failed");
}
it("局部渲染失败保留周边界面，切换资源后恢复", () => {
  vi.spyOn(console, "error").mockImplementation(() => {});
  const tree = (key: string, broken: boolean) => (
    <I18nProvider initialLocale="zh-CN">
      <div>周边</div>
      <AppErrorBoundary key={key}>
        {broken ? <Broken /> : <div>已恢复</div>}
      </AppErrorBoundary>
    </I18nProvider>
  );
  const view = render(tree("a", true));
  expect(screen.getByText("周边")).toBeInTheDocument();
  expect(screen.getByRole("alert")).toHaveTextContent("暂时无法显示此内容");
  expect(screen.getByRole("button", { name: "重新加载" })).toBeInTheDocument();
  view.rerender(tree("b", false));
  expect(screen.getByText("已恢复")).toBeInTheDocument();
});
it("分包加载失败使用英文刷新提示", () => {
  const reload = vi.fn();
  render(
    <I18nProvider initialLocale="en-US">
      <AppErrorPage
        error={new Error("Failed to fetch dynamically imported module")}
        reload={reload}
      />
    </I18nProvider>,
  );
  expect(screen.getByRole("alert")).toHaveTextContent(
    "A new version may be available",
  );
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  expect(reload).toHaveBeenCalledOnce();
});
