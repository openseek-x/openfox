import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { I18nProvider } from "@openfox/i18n";

import EmojiPickerPanel, { SUGGESTED_EMOJIS } from "./EmojiPickerPanel";

// 真实的 emoji-picker-react 在 jsdom 里渲染（未 mock）：
// 推荐区、分类名、搜索（中英文）、选中回调、U+FE0F、本地记忆。

function renderPanel(locale: "zh-CN" | "en-US", onPick = vi.fn()) {
  render(
    <I18nProvider initialLocale={locale}>
      <EmojiPickerPanel onPick={onPick} />
    </I18nProvider>,
  );
  return onPick;
}

/** 某个分类（rowgroup）里的表情文本 */
async function category(name: string) {
  const group = await screen.findByRole("rowgroup", { name });
  return within(group);
}
const texts = (scope: ReturnType<typeof within>) =>
  scope.queryAllByRole("gridcell").map((cell: HTMLElement) => cell.textContent);
const searchBox = (name: string) => screen.findByRole("textbox", { name });

afterEach(() => localStorage.clear());

describe("表情选择器", () => {
  it("没有最近使用时推荐区显示 12 个表情及分类名", async () => {
    renderPanel("zh-CN");
    expect(texts(await category("最近使用"))).toEqual(SUGGESTED_EMOJIS);
    const nav = screen.getByRole("tablist", { name: "表情分类" });
    const tabs = within(nav)
      .getAllByRole("tab")
      .map((tab) => tab.getAttribute("aria-label") ?? tab.textContent);
    expect(tabs).toEqual([
      "最近使用",
      "表情与角色",
      "动物与自然",
      "食物与饮品",
      "活动",
      "旅行与景点",
      "物品",
      "符号",
      "旗帜",
    ]);
    expect(await searchBox("输入关键字搜索表情")).toHaveAttribute(
      "placeholder",
      "搜索表情",
    );
  });

  it("选中 ❤️ 得到的字符串与原来 12 个里的完全一致（含 U+FE0F）", async () => {
    const user = userEvent.setup();
    const onPick = renderPanel("zh-CN");
    const smileys = await category("表情与角色");
    await user.click(smileys.getByRole("gridcell", { name: "红心" }));
    expect(onPick).toHaveBeenCalledWith("❤️");
    expect(onPick.mock.calls[0]![0]).toBe(SUGGESTED_EMOJIS[1]);
  });

  it("中文关键字搜索，选中后回调对应表情", async () => {
    const user = userEvent.setup();
    const onPick = renderPanel("zh-CN");
    await user.type(await searchBox("输入关键字搜索表情"), "火箭");
    const travel = await category("旅行与景点");
    await waitFor(() => expect(texts(travel)).toEqual(["🚀"]));
    expect(texts(await category("表情与角色"))).toContain("🧑‍🚀");
    expect(texts(await category("表情与角色"))).not.toContain("😄");
    await user.click(travel.getByRole("gridcell", { name: "火箭" }));
    expect(onPick).toHaveBeenCalledWith("🚀");
  });

  it("英文界面用英文数据，英文关键字搜索", async () => {
    const user = userEvent.setup();
    const onPick = renderPanel("en-US");
    await user.type(await searchBox("Type to search for an emoji"), "rocket");
    const travel = await category("Travel & Places");
    await waitFor(() => expect(texts(travel)).toEqual(["🚀"]));
    await user.click(travel.getByRole("gridcell", { name: "rocket" }));
    expect(onPick).toHaveBeenCalledWith("🚀");
  });

  it("选过的表情排进最近使用并存到浏览器本地", async () => {
    const user = userEvent.setup();
    renderPanel("zh-CN");
    await user.type(await searchBox("输入关键字搜索表情"), "火箭");
    const travel = await category("旅行与景点");
    await waitFor(() => expect(texts(travel)).toEqual(["🚀"]));
    await user.click(travel.getByRole("gridcell", { name: "火箭" }));
    expect(JSON.parse(localStorage.getItem("openfox:emoji:recent")!)).toEqual([
      "1f680",
    ]);
  });

  it("有最近使用时推荐区按最近顺序显示", async () => {
    localStorage.setItem(
      "openfox:emoji:recent",
      JSON.stringify(["1f680", "2764-fe0f"]),
    );
    renderPanel("zh-CN");
    expect(texts(await category("最近使用"))).toEqual(["🚀", "❤️"]);
  });

  it("本地存储读写抛错时不影响打开和选择", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const user = userEvent.setup();
    const onPick = renderPanel("zh-CN");
    const recent = await category("最近使用");
    expect(texts(recent)).toEqual(SUGGESTED_EMOJIS);
    await user.click(recent.getByRole("gridcell", { name: "拉炮彩带" }));
    expect(onPick).toHaveBeenCalledWith("🎉");
    vi.restoreAllMocks();
  });

  it("默认肤色记在浏览器本地", async () => {
    localStorage.setItem("openfox:emoji:skin", "1f3fd");
    renderPanel("zh-CN");
    const smileys = await category("表情与角色");
    // 👍 有肤色变体，按记住的默认肤色显示
    expect(
      smileys.getByRole("gridcell", { name: "拇指向上" }).textContent,
    ).toBe("👍🏽");
  });
});
