import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef, useState } from "react";
import { describe, expect, it, vi } from "vitest";

import { addDays, formatDateLabel, quickDates } from "../date";
import { Calendar } from "./Calendar";
import { DateFieldPopover } from "./DateFieldPopover";

// 2026-10-28 是周三
const TODAY = "2026-10-28";

describe("Calendar", () => {
  it("显示本地化月份标题，周一起始，标记今天和选中日", () => {
    render(<Calendar value="2026-10-15" today={TODAY} onChange={() => {}} />);
    expect(screen.getByText("2026年10月")).toBeInTheDocument();
    const headers = screen.getAllByRole("columnheader");
    expect(headers.map((h) => h.textContent)).toEqual([
      "一",
      "二",
      "三",
      "四",
      "五",
      "六",
      "日",
    ]);
    // 2026-10-01 是周四，第一行前三格是 9 月的 28/29/30
    const firstRow = screen.getAllByRole("row")[1]!;
    expect(
      within(firstRow)
        .getAllByRole("gridcell")
        .map((c) => c.textContent),
    ).toEqual(["28", "29", "30", "1", "2", "3", "4"]);
    expect(
      screen.getByRole("button", { name: /2026年10月28日/ }),
    ).toHaveAttribute("aria-current", "date");
    expect(
      screen
        .getByRole("button", { name: /2026年10月15日/ })
        .closest("[role=gridcell]"),
    ).toHaveAttribute("aria-selected", "true");
  });

  it("点击日期触发 onChange；上下月切换", async () => {
    const onChange = vi.fn();
    render(<Calendar today={TODAY} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: "下个月" }));
    expect(screen.getByText("2026年11月")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "上个月" }));
    await userEvent.click(screen.getByRole("button", { name: "上个月" }));
    expect(screen.getByText("2026年9月")).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: /2026年9月30日/ }),
    );
    expect(onChange).toHaveBeenCalledWith("2026-09-30");
  });

  it("方向键移动焦点并可跨月", async () => {
    render(<Calendar value="2026-10-31" today={TODAY} onChange={() => {}} />);
    await userEvent.tab(); // 上月
    await userEvent.tab(); // 下月
    await userEvent.tab(); // 网格内当前日
    const day31 = screen.getByRole("button", { name: /2026年10月31日/ });
    expect(day31).toHaveFocus();
    await userEvent.keyboard("{ArrowRight}");
    expect(screen.getByText("2026年11月")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /2026年11月1日/ })).toHaveFocus();
    await userEvent.keyboard("{ArrowDown}");
    expect(screen.getByRole("button", { name: /2026年11月8日/ })).toHaveFocus();
    await userEvent.keyboard("{PageUp}");
    expect(screen.getByText("2026年10月")).toBeInTheDocument();
  });

  it("英文环境显示英文标题", () => {
    expect(formatDateLabel("2026-10-28", "en-US")).toBe("Wed, Oct 28");
    expect(formatDateLabel("2026-10-28", "zh-CN")).toBe("周三, 10月28日");
  });
});

function Harness({ initial = null as string | null, onChange = vi.fn() }) {
  const ref = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(true);
  const [value, setValue] = useState<string | null>(initial);
  return (
    <>
      <button ref={ref} type="button">
        due
      </button>
      <DateFieldPopover
        open={open}
        onOpenChange={setOpen}
        anchorRef={ref}
        value={value}
        today={TODAY}
        onChange={(next) => {
          setValue(next);
          onChange(next);
        }}
      />
      <output>{value ?? "none"}</output>
    </>
  );
}

describe("DateFieldPopover", () => {
  it("快捷日期显示「周三, 10月28日」格式", () => {
    render(<Harness />);
    const menu = screen.getByRole("menu", { name: "快捷日期" });
    const items = within(menu).getAllByRole("menuitem");
    expect(items.map((i) => i.textContent)).toEqual([
      "今天周三, 10月28日",
      "明天周四, 10月29日",
      "本周末周六, 10月31日",
      "一周后周三, 11月4日",
      "下周末周六, 11月7日",
    ]);
  });

  it("选择快捷日期后写入值并关闭", async () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    await userEvent.click(screen.getByRole("menuitem", { name: /本周末/ }));
    expect(onChange).toHaveBeenCalledWith("2026-10-31");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("2026-10-31");
  });

  it("有值时出现「清除日期」，点击传 null", async () => {
    const onChange = vi.fn();
    render(<Harness initial="2026-10-20" onChange={onChange} />);
    await userEvent.click(screen.getByRole("menuitem", { name: "清除日期" }));
    expect(onChange).toHaveBeenCalledWith(null);
  });

  it("没有值时不显示清除，点击月历日期可选择", async () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    expect(screen.queryByText("清除日期")).not.toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: /2026年10月5日/ }),
    );
    expect(onChange).toHaveBeenCalledWith("2026-10-05");
  });
});

describe("date 工具", () => {
  it("addDays 跨月", () => {
    expect(addDays("2026-10-31", 1)).toBe("2026-11-01");
    expect(quickDates("2026-10-28").nextWeekend).toBe("2026-11-07");
  });
});
