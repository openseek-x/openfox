import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import type { TimelineActivity } from "@openfox/api";
import { I18nProvider, createTranslator } from "@openfox/i18n";
import {
  DuplicateMarkedActivity,
  duplicateMarkedSource,
} from "./DuplicateMarkedActivity";
import { activityText } from "./Timeline";

// OPE-17：目标任务动态里的 task.duplicate_marked 要写明来源任务编号并可点击，不再落到「更新了这个任务」。
const activity: TimelineActivity = {
  id: "a1",
  task_id: "target",
  kind: "task.duplicate_marked",
  actor_id: "member",
  actor_name: "Alice",
  created_at: "2026-11-26T00:00:00Z",
  safe_diff: { task_id: "source", display_id: "ope-12" },
};
const book = { statuses: [], labels: [], people: [], agents: [] };

function view(a: TimelineActivity, locale: "zh-CN" | "en-US" = "zh-CN") {
  return render(
    <I18nProvider initialLocale={locale}>
      <MemoryRouter>
        <ol>
          <DuplicateMarkedActivity activity={a} orgId="org-1" locale={locale} />
        </ol>
      </MemoryRouter>
    </I18nProvider>,
  );
}

describe("task.duplicate_marked activity", () => {
  it("names the source task and links to it", () => {
    view(activity);
    const link = screen.getByRole("link", { name: "OPE-12" });
    expect(link).toHaveAttribute("href", "/o/org-1/tasks/OPE-12");
    expect(link.closest("span")).toHaveTextContent(
      "Alice 将 OPE-12 标记为本任务的重复",
    );
  });

  it("keeps the link in place for other word orders", () => {
    view(activity, "en-US");
    expect(
      screen.getByRole("link", { name: "OPE-12" }).closest("span"),
    ).toHaveTextContent("Alice marked OPE-12 as a duplicate of this task");
  });

  it("gives activityText a dedicated sentence instead of the generic fallback", () => {
    const t = createTranslator("zh-CN");
    expect(activityText(t, activity, book)).toBe(
      "Alice 将 OPE-12 标记为本任务的重复",
    );
    expect(activityText(t, { ...activity, actor_name: "" }, book)).toBe(
      "某人 将 OPE-12 标记为本任务的重复",
    );
  });

  // 空间标识允许下划线和连字符：真实夹具里的编号是 SP_7E0C31CB87FF-2，旧正则会把它当成不合规而落到兜底文案。
  it("links space keys that contain underscores or hyphens", () => {
    for (const id of ["sp_7e0c31cb87ff-2", "my-space-12"]) {
      expect(
        duplicateMarkedSource({ ...activity, safe_diff: { display_id: id } }),
      ).toBe(id.toUpperCase());
    }
    view({ ...activity, safe_diff: { display_id: "sp_7e0c31cb87ff-2" } });
    expect(
      screen.getByRole("link", { name: "SP_7E0C31CB87FF-2" }),
    ).toHaveAttribute("href", "/o/org-1/tasks/SP_7E0C31CB87FF-2");
  });

  it("falls back to the generic sentence without a link when the source id is missing or malformed", () => {
    const t = createTranslator("zh-CN");
    for (const d of [
      {},
      { task_id: "source" },
      { display_id: 12 },
      { display_id: "javascript:alert(1)" },
      { display_id: "../OPE-12" },
    ]) {
      const a = { ...activity, safe_diff: d };
      expect(duplicateMarkedSource(a)).toBe("");
      expect(activityText(t, a, book)).toBe(
        t("tasks.activity.unknown", { actor: "Alice" }),
      );
    }
    const { container } = view({
      ...activity,
      safe_diff: { display_id: "javascript:alert(1)" },
    });
    expect(container.querySelector("a")).toBeNull();
    expect(container).toHaveTextContent("Alice 更新了这个任务");
  });
});
