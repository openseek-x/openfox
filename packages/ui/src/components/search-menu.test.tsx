import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef, useState } from "react";
import { describe, expect, it, vi } from "vitest";

import { SearchMenu, type SearchMenuGroup } from "./SearchMenu";

const groups: SearchMenuGroup[] = [
  {
    label: "组织标签",
    options: [
      { value: "bug", label: "缺陷" },
      { value: "ux", label: "体验", keywords: "experience" },
    ],
  },
  { label: "空间标签", options: [{ value: "api", label: "接口" }] },
];

function Harness({
  multiple = false,
  footerSpy = vi.fn(),
  onSelect = vi.fn(),
}: {
  multiple?: boolean;
  footerSpy?: (q: string) => void;
  onSelect?: (v: string) => void;
}) {
  const ref = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(true);
  const [selected, setSelected] = useState<string[]>(["bug"]);
  return (
    <>
      <button ref={ref} type="button">
        trigger
      </button>
      <SearchMenu
        open={open}
        onOpenChange={setOpen}
        anchorRef={ref}
        label="选择标签"
        groups={groups}
        selected={selected}
        multiple={multiple}
        onSelect={(value) => {
          onSelect(value);
          setSelected((list) =>
            multiple
              ? list.includes(value)
                ? list.filter((v) => v !== value)
                : [...list, value]
              : [value],
          );
        }}
        footer={{ label: "创建标签", onSelect: footerSpy }}
      />
    </>
  );
}

describe("SearchMenu", () => {
  it("分组显示选项，选中项 aria-selected 并有勾选标记", () => {
    render(<Harness />);
    expect(screen.getByRole("group", { name: "组织标签" })).toBeInTheDocument();
    const bug = screen.getByRole("option", { name: "缺陷" });
    expect(bug).toHaveAttribute("aria-selected", "true");
    expect(bug.querySelector("svg")).not.toBeNull();
    expect(screen.getByRole("option", { name: "接口" })).toHaveAttribute(
      "aria-selected",
      "false",
    );
    expect(screen.getByRole("combobox")).toHaveFocus();
  });

  it("按文字或关键字过滤；无匹配时显示空态", async () => {
    render(<Harness />);
    await userEvent.type(screen.getByRole("combobox"), "experience");
    expect(screen.getAllByRole("option")).toHaveLength(1);
    expect(screen.getByRole("option", { name: "体验" })).toBeInTheDocument();
    await userEvent.clear(screen.getByRole("combobox"));
    await userEvent.type(screen.getByRole("combobox"), "不存在");
    expect(screen.queryAllByRole("option")).toHaveLength(0);
    expect(screen.getByText("没有匹配项")).toBeInTheDocument();
  });

  it("上下键移动当前项，回车选择；单选后关闭", async () => {
    const onSelect = vi.fn();
    render(<Harness onSelect={onSelect} />);
    const input = screen.getByRole("combobox");
    await userEvent.keyboard("{ArrowDown}{ArrowDown}");
    expect(input).toHaveAttribute(
      "aria-activedescendant",
      screen.getByRole("option", { name: "接口" }).id,
    );
    await userEvent.keyboard("{Enter}");
    expect(onSelect).toHaveBeenCalledWith("api");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("多选：点击切换勾选且保持打开", async () => {
    render(<Harness multiple />);
    await userEvent.click(screen.getByRole("option", { name: "接口" }));
    expect(screen.getByRole("option", { name: "接口" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await userEvent.click(screen.getByRole("option", { name: "缺陷" }));
    expect(screen.getByRole("option", { name: "缺陷" })).toHaveAttribute(
      "aria-selected",
      "false",
    );
    expect(screen.getByRole("listbox")).toHaveAttribute(
      "aria-multiselectable",
      "true",
    );
  });

  it("底部操作项带着搜索词回调并关闭", async () => {
    const footerSpy = vi.fn();
    render(<Harness footerSpy={footerSpy} />);
    await userEvent.type(screen.getByRole("combobox"), " 新标签 ");
    await userEvent.click(screen.getByRole("button", { name: "创建标签" }));
    expect(footerSpy).toHaveBeenCalledWith("新标签");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("Esc 关闭", async () => {
    render(<Harness />);
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
