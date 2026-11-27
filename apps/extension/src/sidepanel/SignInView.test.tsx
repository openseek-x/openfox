import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { SignInView } from "./SignInView";

afterEach(() => vi.unstubAllGlobals());

it("未登录时在当前浏览器打开工作台登录页，无需设置连接", () => {
  const create = vi.fn().mockResolvedValue({});
  vi.stubGlobal("chrome", { tabs: { create } });
  render(<SignInView origin="https://work.example.com" retry={() => {}} />);
  expect(screen.queryByRole("textbox")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "登录 OpenFox" }));
  expect(create).toHaveBeenCalledWith({
    url: "https://work.example.com/login",
  });
});

it("工作台暂不可用时可以重新检查", () => {
  const retry = vi.fn();
  render(
    <SignInView
      origin="https://work.example.com"
      error="无法连接服务器"
      retry={retry}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "重新检查登录状态" }));
  expect(retry).toHaveBeenCalledOnce();
});
