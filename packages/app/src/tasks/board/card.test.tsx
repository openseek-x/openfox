import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { Task } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";

import { defaultDisplay } from "../model";
import { CardBody } from "./Card";

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: "t-1",
    organization_id: "o-1",
    space_id: "s-1",
    space_prefix: "ope",
    number: 21,
    display_id: "OPE-21",
    title: "打造简约大气视觉风格",
    status_id: "st-1",
    status_name: "进行中",
    status_category: "IN_PROGRESS",
    status_color: "#2171cc",
    type_id: "ty-1",
    type_name: "需求",
    type_icon: "clipboard",
    priority: "NONE",
    assignees: [],
    label_items: [],
    subtask_count: 0,
    completed_subtask_count: 0,
    created_at: "2026-11-27T08:00:00Z",
    updated_at: "2026-11-27T09:00:00Z",
    ...overrides,
  } as Task;
}

describe("看板卡片 CardBody", () => {
  it("无元信息与底栏属性时不渲染多余空容器，并保留双密度 CSS 变量", () => {
    const task = makeTask();
    const display = {
      ...defaultDisplay("space"),
      properties: ["id", "assignee", "status", "labels", "due"] as const,
    };
    const { container } = render(
      <I18nProvider initialLocale="zh-CN">
        <CardBody
          task={task}
          display={{ ...display, properties: [...display.properties] }}
        />
      </I18nProvider>,
    );
    const article = container.querySelector("article")!;
    expect(article.className).toContain("var(--density-card-padding");
    expect(article.className).toContain("var(--density-card-gap");
    // 仅有编号/处理人行 + 标题行，不出现空标签行和空底栏
    expect(article.children).toHaveLength(2);
    expect(screen.getByText("OPE-21")).toBeInTheDocument();
    expect(screen.getByText("打造简约大气视觉风格")).toBeInTheDocument();
  });

  it("存在标签或优先级时完整渲染元信息与底栏", () => {
    const task = makeTask({
      priority: "HIGH",
      label_items: [
        {
          id: "l-1",
          name: "UI",
          color: "#139948",
          group_id: null,
          space_id: "s-1",
        },
      ],
    });
    const display = {
      ...defaultDisplay("space"),
      properties: ["id", "labels", "priority", "created"] as const,
    };
    const { container } = render(
      <I18nProvider initialLocale="zh-CN">
        <CardBody
          task={task}
          display={{ ...display, properties: [...display.properties] }}
        />
      </I18nProvider>,
    );
    const article = container.querySelector("article")!;
    expect(article.children).toHaveLength(4);
    expect(screen.getByText("UI")).toBeInTheDocument();
  });
});
