import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import type { TimelineActivity } from "@openfox/api";
import { createTranslator } from "@openfox/i18n";
import { activityText } from "./Timeline";
const t = createTranslator("zh-CN"),
  book = { statuses: [], labels: [], people: [], agents: [] };
const a: TimelineActivity = {
  id: "a",
  task_id: "t",
  kind: "task.imported",
  actor_id: "local-importer",
  actor_name: "Local Administrator",
  created_at: "2026-11-15T00:00:00Z",
  safe_diff: {
    source: "zoho_bugtracker",
    source_reporter_region: "US",
    source_reporter_id: "9007199254740993123",
    source_reporter_name: "<img src=x onerror=alert(1)>",
    mapped_user_id: "local-member",
    mapped_user_name: "Alice",
  },
};
it("preserves real importer and renders remote reporter and mapping as escaped text", () => {
  const { container } = render(<p>{activityText(t, a, book)}</p>);
  expect(screen.getByText(/Local Administrator/)).toHaveTextContent(
    "来源报告人：<img src=x onerror=alert(1)>（US · 9007199254740993123）",
  );
  expect(container).toHaveTextContent("映射成员：Alice");
  expect(container.querySelector("img,a,script")).toBeNull();
  expect(a.actor_id).toBe("local-importer");
});
it("distinguishes external and no mapping without pretending remote reporter is local creator", () => {
  expect(
    activityText(
      t,
      {
        ...a,
        safe_diff: {
          ...a.safe_diff,
          mapped_user_id: undefined,
          mapped_external_account_id: "external",
        },
      },
      book,
    ),
  ).toContain("来源外部账号");
  expect(
    activityText(
      t,
      { ...a, safe_diff: { ...a.safe_diff, mapped_user_id: undefined } },
      book,
    ),
  ).toContain("未映射本地成员");
});
it("keeps other providers and malformed source facts on existing fallback", () => {
  for (const d of [
    { source: "asana" },
    { ...a.safe_diff, source_reporter_id: 3 },
    { ...a.safe_diff, source_reporter_region: "EU" },
  ])
    expect(activityText(t, { ...a, safe_diff: d }, book)).toBe(
      t("tasks.activity.unknown", { actor: "Local Administrator" }),
    );
});
