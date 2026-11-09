import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  ColorSwatches,
  LABEL_COLORS,
  OPTION_COLORS,
  STATUS_LIFECYCLE_COLORS,
} from "./ColorSwatches";
import { Kbd } from "./Kbd";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from "./Table";
import { Tabs } from "./Tabs";
import { ToastProvider, useToast } from "./Toast";

describe("ColorSwatches", () => {
  it("色板常量", () => {
    expect(LABEL_COLORS).toHaveLength(11);
    expect(OPTION_COLORS).toHaveLength(11);
    expect(Object.keys(STATUS_LIFECYCLE_COLORS)).toEqual([
      "TRIAGE",
      "BACKLOG",
      "TODO",
      "IN_PROGRESS",
      "DONE",
      "CANCELLED",
      "DUPLICATE",
    ]);
    expect(STATUS_LIFECYCLE_COLORS.DONE).toBe("#4cb782");
  });

  it("单选语义：选中项带对勾，点击触发 onChange", async () => {
    const onChange = vi.fn();
    render(
      <ColorSwatches
        colors={LABEL_COLORS}
        value="#7c3aed"
        onChange={onChange}
        label="标签颜色"
      />,
    );
    const group = screen.getByRole("radiogroup", { name: "标签颜色" });
    const radios = within(group).getAllByRole("radio");
    expect(radios).toHaveLength(11);
    const checked = radios.filter(
      (r) => r.getAttribute("aria-checked") === "true",
    );
    expect(checked).toHaveLength(1);
    expect(checked[0]).toHaveAccessibleName("#7C3AED");
    expect(checked[0]!.querySelector("svg")).not.toBeNull();
    await userEvent.click(screen.getByRole("radio", { name: "#DC2626" }));
    expect(onChange).toHaveBeenCalledWith("#DC2626");
  });
});

describe("Kbd", () => {
  it("渲染为 kbd 元素", () => {
    render(<Kbd>⌘ K</Kbd>);
    expect(screen.getByText("⌘ K").tagName).toBe("KBD");
  });
});

describe("Tabs", () => {
  function Harness() {
    const [value, setValue] = useState("all");
    return (
      <Tabs
        label="任务视图"
        value={value}
        onChange={setValue}
        tabs={[
          { id: "all", label: "全部", count: 12 },
          { id: "mine", label: "我的" },
          { id: "off", label: "停用", disabled: true },
          { id: "done", label: "已完成", count: 0 },
        ]}
      />
    );
  }

  it("tablist / tab 角色，选中态与计数", () => {
    render(<Harness />);
    expect(
      screen.getByRole("tablist", { name: "任务视图" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /^全部\s*12$/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByRole("tab", { name: "我的" })).toHaveAttribute(
      "tabindex",
      "-1",
    );
  });

  it("点击和方向键切换，跳过禁用项并循环", async () => {
    render(<Harness />);
    await userEvent.click(screen.getByRole("tab", { name: "我的" }));
    expect(screen.getByRole("tab", { name: "我的" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await userEvent.keyboard("{ArrowRight}");
    expect(screen.getByRole("tab", { name: /^已完成\s*0$/ })).toHaveFocus();
    await userEvent.keyboard("{ArrowRight}");
    expect(screen.getByRole("tab", { name: /^全部\s*12$/ })).toHaveFocus();
    expect(screen.getByRole("tab", { name: /^全部\s*12$/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });
});

describe("Table", () => {
  it("原生表格语义，表头 scope、aria-sort、行选中态与点击", async () => {
    const onClick = vi.fn();
    render(
      <Table aria-label="任务列表">
        <TableHead>
          <TableRow>
            <TableHeaderCell sort="ascending">标题</TableHeaderCell>
            <TableHeaderCell>状态</TableHeaderCell>
          </TableRow>
        </TableHead>
        <TableBody>
          <TableRow selected onClick={onClick}>
            <TableCell>修复登录</TableCell>
            <TableCell>进行中</TableCell>
          </TableRow>
        </TableBody>
      </Table>,
    );
    expect(screen.getByRole("table", { name: "任务列表" })).toBeInTheDocument();
    const title = screen.getByRole("columnheader", { name: "标题" });
    expect(title).toHaveAttribute("aria-sort", "ascending");
    expect(title).toHaveAttribute("scope", "col");
    const row = screen.getByRole("row", { name: /修复登录/ });
    expect(row).toHaveAttribute("aria-selected", "true");
    await userEvent.click(row);
    expect(onClick).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("cell", { name: "进行中" })).toBeInTheDocument();
  });
});

describe("Toast 撤销动作", () => {
  afterEach(() => vi.useRealTimers());
  function Trigger({ onUndo }: { onUndo: () => void }) {
    const toast = useToast();
    return (
      <button
        type="button"
        onClick={() =>
          toast("任务已删除", { action: { label: "撤销", onClick: onUndo } })
        }
      >
        删除
      </button>
    );
  }

  it("点击撤销回调并关闭提示", async () => {
    const onUndo = vi.fn();
    render(
      <ToastProvider>
        <Trigger onUndo={onUndo} />
      </ToastProvider>,
    );
    await userEvent.click(screen.getByRole("button", { name: "删除" }));
    expect(screen.getByRole("status")).toHaveTextContent("任务已删除");
    await userEvent.click(screen.getByRole("button", { name: "撤销" }));
    expect(onUndo).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("页面卸载时取消所有提示定时器，不再更新已卸载的状态", () => {
    vi.useFakeTimers();
    const { unmount } = render(
      <ToastProvider>
        <Trigger onUndo={vi.fn()} />
      </ToastProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    expect(vi.getTimerCount()).toBe(2);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("提前关闭会取消到期回调，普通提示仍按时自动消失", () => {
    vi.useFakeTimers();
    const onUndo = vi.fn();
    render(
      <ToastProvider>
        <Trigger onUndo={onUndo} />
      </ToastProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    fireEvent.click(screen.getByRole("button", { name: "撤销" }));
    expect(onUndo).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    act(() => vi.advanceTimersByTime(2399));
    expect(screen.getByRole("status")).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(1));
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(vi.getTimerCount()).toBe(0);
  });
});
