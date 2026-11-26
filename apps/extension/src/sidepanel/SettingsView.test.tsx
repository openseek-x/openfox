import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { SettingsView } from "./SettingsView";

afterEach(() => vi.unstubAllGlobals());

it("拒绝服务器权限时，不发送凭据，也不保存连接", async () => {
  const request = vi.fn().mockResolvedValue(false);
  const save = vi.fn();
  const transport = vi.fn();
  vi.stubGlobal("chrome", {
    permissions: { request },
    storage: { local: { set: save } },
  });
  vi.stubGlobal("fetch", transport);
  render(<SettingsView />);
  fireEvent.change(screen.getByLabelText("服务器地址"), {
    target: { value: "https://work.example.com:8443" },
  });
  fireEvent.change(screen.getByLabelText("个人访问令牌"), {
    target: { value: "ofx_pat_test" },
  });
  fireEvent.click(screen.getByRole("button", { name: "连接" }));
  await screen.findByText("需要授予访问该服务器的权限才能连接");
  expect(request).toHaveBeenCalledWith({
    origins: ["https://work.example.com/*"],
  });
  expect(transport).not.toHaveBeenCalled();
  expect(save).not.toHaveBeenCalled();
});
