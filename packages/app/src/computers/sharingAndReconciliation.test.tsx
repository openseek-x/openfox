import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider, createTranslator, type Locale } from "@openfox/i18n";
import { ReconcilingBadge } from "./ReconcilingBadge";
import {
  SharedTrustConfirm,
  needsSharedTrustConfirm,
} from "./SharedTrustConfirm";

const en = createTranslator("en-US");
const zh = createTranslator("zh-CN");

function Providers({
  children,
  locale = "zh-CN",
}: {
  children: ReactNode;
  locale?: Locale;
}) {
  return <I18nProvider initialLocale={locale}>{children}</I18nProvider>;
}

describe("共享电脑信任说明", () => {
  it("扩大共享范围：说明不是安全沙箱，勾选「我已了解」后才能确认", async () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <Providers>
        <SharedTrustConfirm
          open
          variant="widen"
          onConfirm={onConfirm}
          onCancel={onCancel}
        />
      </Providers>,
    );
    expect(
      screen.getByRole("heading", { name: "扩大这台电脑的共享范围？" }),
    ).toBeInTheDocument();
    expect(screen.getByText("工作目录隔离不是安全沙箱。")).toBeInTheDocument();
    expect(
      screen.getByText(
        "共享对象触发的运行以本机用户身份执行，可读取该用户可读的文件。",
      ),
    ).toBeInTheDocument();
    const confirm = screen.getByRole("button", { name: "确认共享" });
    expect(confirm).toBeDisabled();
    await userEvent.click(confirm);
    expect(onConfirm).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("checkbox", { name: "我已了解" }));
    expect(confirm).toBeEnabled();
    await userEvent.click(confirm);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("添加组织变体与英文文案；重新打开需要重新勾选", async () => {
    const view = (open: boolean) => (
      <Providers locale="en-US">
        <SharedTrustConfirm
          open={open}
          variant="addOrganization"
          onConfirm={() => undefined}
          onCancel={() => undefined}
        />
      </Providers>
    );
    const { rerender } = render(view(true));
    expect(
      screen.getByRole("heading", {
        name: "Add this computer to another organization?",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Working-directory isolation is not a security sandbox.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(/execute as the local user/)).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("checkbox", { name: "I understand" }),
    );
    expect(screen.getByRole("button", { name: "Add" })).toBeEnabled();
    rerender(view(false));
    rerender(view(true));
    expect(screen.getByRole("button", { name: "Add" })).toBeDisabled();
  });

  it("只在范围扩大时需要确认", () => {
    expect(needsSharedTrustConfirm("PERSONAL", "ORGANIZATION")).toBe(true);
    expect(needsSharedTrustConfirm("PERSONAL", "SPACE")).toBe(true);
    expect(needsSharedTrustConfirm("SPACE", "ORGANIZATION")).toBe(true);
    // 换到另一个空间：新的一批人可以使用，同样需要确认
    expect(needsSharedTrustConfirm("SPACE", "SPACE")).toBe(true);
    expect(needsSharedTrustConfirm("ORGANIZATION", "SPACE")).toBe(false);
    expect(needsSharedTrustConfirm("ORGANIZATION", "PERSONAL")).toBe(false);
    expect(needsSharedTrustConfirm("SPACE", "PERSONAL")).toBe(false);
  });
});

describe("电脑「核对中」徽标", () => {
  it("有待核对运行时显示，没有或旧服务端未返回字段时不显示", () => {
    const { container, rerender } = render(
      <Providers>
        <ReconcilingBadge device={{ reconciling_runs: 2 }} />
      </Providers>,
    );
    expect(screen.getByText("核对中")).toBeInTheDocument();
    expect(
      screen.getByTitle("这台电脑上有 2 个运行正在核对状态"),
    ).toBeInTheDocument();
    rerender(
      <Providers>
        <ReconcilingBadge device={{ reconciling_runs: 0 }} />
      </Providers>,
    );
    expect(container).toBeEmptyDOMElement();
    rerender(
      <Providers>
        <ReconcilingBadge device={{}} />
      </Providers>,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("中英文文案齐全", () => {
    expect(zh("governance.computer.reconciling")).toBe("核对中");
    expect(en("governance.computer.reconciling")).toBe("Verifying");
    expect(en("governance.reconcile.acceptResult")).toBe(
      "Accept existing result",
    );
    expect(zh("governance.reconcile.acceptResult")).toBe("接受已有结果");
  });
});
