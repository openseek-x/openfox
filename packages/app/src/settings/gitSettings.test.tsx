import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { apiError, json, renderSettings } from "./testing";

const path = "/v1/organizations/o1/git-settings";
const initial = {
  author_name: "",
  author_email: "",
  co_author_enabled: false,
  version: 1,
  can_manage: true,
  effective: { author_name: "", author_email: "", co_author_enabled: false },
};

afterEach(() => localStorage.clear());

describe("Git 默认设置", () => {
  it("保存作者与邮箱后保持最新版本，无改动不重复保存", async () => {
    const user = userEvent.setup();
    let current = initial;
    const calls = renderSettings("/o/o1/settings/git", (call) => {
      if (call.path !== path) return undefined;
      if (call.method === "PUT")
        current = {
          ...current,
          ...(call.body as typeof initial),
          version: current.version + 1,
        };
      return json(200, current);
    });
    await user.click(await screen.findByRole("button", { name: "提交人名称" }));
    expect(screen.getAllByRole("option")).toHaveLength(6);
    await user.click(screen.getByRole("option", { name: "手动填写" }));
    const name = screen.getByRole("textbox", { name: "提交人名称" });
    await user.click(screen.getByRole("button", { name: "提交人邮箱" }));
    expect(screen.getAllByRole("option")).toHaveLength(4);
    await user.click(screen.getByRole("option", { name: "手动填写" }));
    const email = screen.getByRole("textbox", { name: "提交人邮箱" });
    const save = screen.getByRole("button", { name: "保存" });
    expect(save).toBeDisabled();
    await user.type(name, "Release Bot");
    await user.type(email, "release@example.test");
    await user.click(save);
    await waitFor(() => expect(save).toBeDisabled());
    expect(
      calls.filter((call) => call.method === "PUT").map((call) => call.body),
    ).toEqual([
      {
        version: 1,
        author_name_mode: "MANUAL",
        author_email_mode: "MANUAL",
        author_name: "Release Bot",
        author_email: "release@example.test",
        co_author_enabled: false,
      },
    ]);
  });

  it("只读成员不能编辑，加载失败可以重试", async () => {
    const user = userEvent.setup();
    let failed = true;
    renderSettings(
      "/o/o1/settings/git",
      (call) =>
        call.path === path
          ? failed
            ? apiError(503, "UNAVAILABLE")
            : json(200, { ...initial, can_manage: false })
          : undefined,
      "MEMBER",
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "无法加载 Git 设置",
    );
    failed = false;
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(
      await screen.findByRole("button", { name: "提交人名称" }),
    ).toBeDisabled();
    expect(
      screen.getByRole("switch", { name: "Co-authored-by" }),
    ).toBeDisabled();
    expect(
      screen.queryByRole("button", { name: "保存" }),
    ).not.toBeInTheDocument();
  });

  it("版本冲突保留草稿，明确重新加载后取新版本", async () => {
    const user = userEvent.setup();
    let current = initial;
    renderSettings("/o/o1/settings/git", (call) => {
      if (call.path !== path) return undefined;
      if (call.method === "PUT") {
        current = { ...initial, author_name: "Other editor", version: 2 };
        return apiError(409, "VERSION_CONFLICT");
      }
      return json(200, current);
    });
    await user.click(await screen.findByRole("button", { name: "提交人名称" }));
    await user.click(screen.getByRole("option", { name: "手动填写" }));
    const name = screen.getByRole("textbox", { name: "提交人名称" });
    await user.type(name, "My draft");
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "设置已被其他人修改",
    );
    expect(name).toHaveValue("My draft");
    await user.click(
      screen.getByRole("button", { name: "重新加载已保存设置" }),
    );
    await waitFor(() => expect(name).toHaveValue("Other editor"));
    expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
  });

  it("首次展开空间才加载设置和仓库，折叠保留未保存草稿", async () => {
    const user = userEvent.setup();
    const spacePath = "/v1/organizations/o1/spaces/s1";
    const calls = renderSettings("/o/o1/settings/git", (call) => {
      if (call.path === path)
        return json(200, {
          ...initial,
          effective: {
            ...initial.effective,
            author_name_mode: "MANUAL",
            author_name: "Parent Bot",
          },
        });
      if (call.path.endsWith("/spaces"))
        return json(200, {
          spaces: [
            { id: "s1", name: "研发空间", status: "active", can_manage: true },
          ],
        });
      if (call.path === `${spacePath}/git-settings`)
        return json(200, {
          ...initial,
          author_name_mode: "INHERIT",
          author_email_mode: "INHERIT",
          co_author_enabled: null,
        });
      if (call.path === `${spacePath}/repos`) return json(200, { repos: [] });
      return undefined;
    });
    const expand = await screen.findByRole("button", { name: "研发空间" });
    expect(expand).toHaveAttribute("aria-expanded", "false");
    expect(calls.some((call) => call.path.startsWith(`${spacePath}/`))).toBe(
      false,
    );
    await user.click(expand);
    const space = within(screen.getByRole("region", { name: "研发空间" }));
    await user.click(await space.findByRole("button", { name: "提交人名称" }));
    expect(screen.getAllByRole("option")).toHaveLength(7);
    expect(
      screen.getByRole("option", { name: /继承组织设置/ }),
    ).toHaveTextContent("Parent Bot");
    await user.click(screen.getByRole("option", { name: "手动填写" }));
    await user.type(
      space.getByRole("textbox", { name: "提交人名称" }),
      "Space draft",
    );
    await user.click(expand);
    expect(
      space.queryByRole("textbox", { name: "提交人名称" }),
    ).not.toBeInTheDocument();
    await user.click(expand);
    expect(space.getByRole("textbox", { name: "提交人名称" })).toHaveValue(
      "Space draft",
    );
    expect(space.getByRole("button", { name: "保存" })).toBeEnabled();
    expect(
      calls.filter((call) => call.path === `${spacePath}/repos`),
    ).toHaveLength(1);
    expect(
      calls.filter((call) => call.path === `${spacePath}/git-settings`),
    ).toHaveLength(1);
    expect(calls.some((call) => call.method === "PUT")).toBe(false);
  });

  it("退役空间可以查看来源但不能编辑或新增仓库", async () => {
    const user = userEvent.setup();
    renderSettings("/o/o1/settings/git", (call) => {
      if (call.path === path || call.path.endsWith("/git-settings"))
        return json(200, initial);
      if (call.path.endsWith("/spaces"))
        return json(200, {
          spaces: [
            { id: "s1", name: "退役空间", status: "retired", can_manage: true },
          ],
        });
      if (call.path.endsWith("/repos")) return json(200, { repos: [] });
      return undefined;
    });
    await user.click(await screen.findByRole("button", { name: "退役空间" }));
    const space = within(screen.getByRole("region", { name: "退役空间" }));
    expect(
      await space.findByRole("button", { name: "提交人名称" }),
    ).toBeDisabled();
    expect(
      space.queryByRole("button", { name: "保存" }),
    ).not.toBeInTheDocument();
    expect(
      space.queryByRole("button", { name: "添加 Git 仓库" }),
    ).not.toBeInTheDocument();
  });
});
