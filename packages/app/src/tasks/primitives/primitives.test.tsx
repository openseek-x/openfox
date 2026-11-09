import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { PriorityIcon } from "./PriorityIcon";
import { STATUS_CATEGORIES, StatusIcon } from "./StatusIcon";
import { TaskTypeIcon, resolveTaskTypeIcon } from "./TaskTypeIcon";
import {
  formatMonthDay,
  formatRelativeTime,
  isOverdue,
  quickDates,
} from "./dates";

describe("StatusIcon", () => {
  it("每个类别都能渲染，且默认是装饰图标", () => {
    for (const category of STATUS_CATEGORIES) {
      const { container, unmount } = render(<StatusIcon category={category} />);
      const svg = container.querySelector("svg")!;
      expect(svg).toHaveAttribute("aria-hidden", "true");
      expect(svg).toHaveAttribute("data-category", category);
      unmount();
    }
    expect(STATUS_CATEGORIES).toHaveLength(7);
  });

  it("labelled 时暴露本地化类别名，并使用传入颜色", () => {
    const { container } = render(
      <StatusIcon category="DONE" labelled color="#123456" />,
    );
    expect(screen.getByRole("img", { name: "已完成" })).toBeInTheDocument();
    expect(container.querySelector("circle")).toHaveAttribute(
      "fill",
      "#123456",
    );
  });
});

describe("PriorityIcon", () => {
  it("五档都能渲染，labelled 时有名称（NORMAL = 中）", () => {
    const names = {
      NONE: "无",
      LOW: "低",
      NORMAL: "中",
      HIGH: "高",
      URGENT: "紧急",
    } as const;
    for (const [priority, name] of Object.entries(names)) {
      const { unmount } = render(
        <PriorityIcon priority={priority as keyof typeof names} labelled />,
      );
      expect(screen.getByRole("img", { name })).toBeInTheDocument();
      unmount();
    }
  });

  it("信号条数量：低 1、中 2、高 3 格点亮", () => {
    const lit = (priority: "LOW" | "NORMAL" | "HIGH") => {
      const { container, unmount } = render(
        <PriorityIcon priority={priority} />,
      );
      const count = container.querySelectorAll("rect.fill-info").length;
      unmount();
      return count;
    };
    expect([lit("LOW"), lit("NORMAL"), lit("HIGH")]).toEqual([1, 2, 3]);
  });
});

describe("TaskTypeIcon", () => {
  it("按名称映射图标，未知或为空时回退到 square-check", () => {
    expect(resolveTaskTypeIcon("bug")).toBe("bug");
    expect(resolveTaskTypeIcon("not-an-icon")).toBe("square-check");
    expect(resolveTaskTypeIcon("toString")).toBe("square-check");
    expect(resolveTaskTypeIcon(null)).toBe("square-check");
    const { container } = render(<TaskTypeIcon icon="bug" color="#ff0000" />);
    expect(container.querySelector("svg")).toHaveStyle({ color: "#ff0000" });
  });
});

describe("dates", () => {
  it("格式化 M月D日 / Oct 28", () => {
    expect(formatMonthDay("2026-10-28", "zh-CN")).toBe("10月28日");
    expect(formatMonthDay("2026-10-28", "en-US")).toBe("Oct 28");
    expect(formatMonthDay("bad", "zh-CN")).toBe("");
  });

  it("相对时间按语言本地化", () => {
    const now = new Date("2026-10-28T10:00:00Z");
    expect(formatRelativeTime("2026-10-28T09:57:00Z", "zh-CN", now)).toBe(
      "3分钟前",
    );
    expect(formatRelativeTime("2026-10-30T10:00:00Z", "en-US", now)).toBe(
      "in 2 days",
    );
  });

  it("快捷日期：周三、周六、周日的本周末与下周末", () => {
    // 2026-10-28 是周三
    expect(quickDates("2026-10-28")).toEqual({
      today: "2026-10-28",
      tomorrow: "2026-10-29",
      thisWeekend: "2026-10-31",
      nextWeek: "2026-11-04",
      nextWeekend: "2026-11-07",
    });
    // 周六：本周末取下周六
    expect(quickDates("2026-10-31").thisWeekend).toBe("2026-11-07");
    expect(quickDates("2026-10-31").nextWeekend).toBe("2026-11-14");
    // 周日：本周末取下周六（6 天后）
    expect(quickDates("2026-11-01").thisWeekend).toBe("2026-11-07");
    // 周一
    expect(quickDates("2026-10-26").thisWeekend).toBe("2026-10-31");
  });

  it("跨月跨年计算", () => {
    expect(quickDates("2026-12-30").nextWeek).toBe("2027-01-06");
    expect(quickDates("2028-02-28").tomorrow).toBe("2028-02-29");
  });

  it("逾期判断", () => {
    expect(isOverdue("2026-10-27", "2026-10-28")).toBe(true);
    expect(isOverdue("2026-10-28", "2026-10-28")).toBe(false);
  });
});
