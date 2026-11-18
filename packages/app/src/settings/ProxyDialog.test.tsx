import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { I18nProvider } from "@openfox/i18n";
import { ProxyDialog } from "./ProxyDialog";

afterEach(() => {
  delete (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop;
});
it.each(["not_synced", "unverified"])(
  "桌面保存但机器 %s 时明确保留提示",
  async (state) => {
    const onClose = vi.fn();
    const saveProxy = vi.fn(async () => ({ machine_proxy: state }));
    (globalThis as { openfoxDesktop?: unknown }).openfoxDesktop = {
      getProxy: async () => ({ mode: "direct" }),
      saveProxy,
    };
    render(
      <I18nProvider initialLocale="zh-CN">
        <ProxyDialog onClose={onClose} />
      </I18nProvider>,
    );
    const button = await screen.findByRole("button", { name: "保存并生效" });
    await waitFor(() => expect(button).toBeEnabled());
    await userEvent.click(button);
    expect(await screen.findByRole("status")).toHaveTextContent(
      "应用代理已保存；机器代理未同步或尚无法核验",
    );
    expect(onClose).not.toHaveBeenCalled();
    expect(saveProxy).toHaveBeenCalledTimes(1);
  },
);
