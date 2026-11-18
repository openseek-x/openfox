import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { TimelineActivity } from "@openfox/api";
import { I18nProvider, createTranslator } from "@openfox/i18n";
import {
  BitbucketCommentActivity,
  bitbucketCommentSource,
} from "./BitbucketCommentActivity";
import { activityText } from "./Timeline";
const activity: TimelineActivity = {
  id: "a1",
  task_id: "t1",
  kind: "task.git_comment",
  actor_id: "configurer",
  actor_name: "Configuration Administrator",
  created_at: "2026-11-15T00:00:00Z",
  safe_diff: {
    provider: "bitbucket",
    repository: "design/app",
    number: 12,
    comment_id: 34,
    url: "https://bitbucket.org/design/app/pull-requests/12",
    external_author_id: "uuid-author",
    external_author_name: "Remote Author",
    author_user_id: "local-author",
    occurred_at: "2026-11-14T10:00:00Z",
    updated_at: "2026-11-14T10:02:00Z",
    deleted: false,
    body: "must never render body",
  },
};
const book = {
  statuses: [],
  labels: [],
  people: [
    { id: "local-author", name: "Local Author", email: "fixture@example.test" },
  ],
  agents: [],
};
function view(a: TimelineActivity) {
  render(
    <I18nProvider initialLocale="zh-CN">
      <ol>
        <BitbucketCommentActivity
          activity={a}
          people={book.people}
          locale="zh-CN"
        />
      </ol>
    </I18nProvider>,
  );
}
describe("Bitbucket PR comment source activity", () => {
  it("renders verified source author, local identity and metadata without configuration actor or body", () => {
    view(activity);
    expect(
      screen.getByText("Remote Author 的 Bitbucket PR #12 评论 #34"),
    ).toBeVisible();
    expect(screen.getByText("已验证归属：Local Author")).toBeVisible();
    expect(screen.getByText("此源评论已更新")).toBeVisible();
    expect(screen.queryByText("Configuration Administrator")).toBeNull();
    expect(screen.queryByText("must never render body")).toBeNull();
    expect(screen.getByRole("link", { name: "查看来源 PR" })).toHaveAttribute(
      "href",
      "https://bitbucket.org/design/app/pull-requests/12",
    );
    expect(activityText(createTranslator("zh-CN"), activity, book)).toContain(
      "Remote Author",
    );
  });
  it("keeps unknown authors external and marks a source deletion without treating the configurer as author", () => {
    view({
      ...activity,
      safe_diff: {
        ...activity.safe_diff,
        external_author_name: "",
        external_author_id: "",
        author_user_id: undefined,
        deleted: true,
      },
    });
    expect(
      screen.getByText("外部作者 的 Bitbucket PR #12 评论 #34"),
    ).toBeVisible();
    expect(screen.getByText("此源评论已删除")).toBeVisible();
    expect(screen.queryByText(/已验证归属/)).toBeNull();
    expect(screen.queryByText("Configuration Administrator")).toBeNull();
  });
  it("rejects foreign hosts, redirects, changed object paths and unverified comment deep links", () => {
    for (const url of [
      "https://bitbucket.org.evil.test/design/app/pull-requests/12",
      "https://user:secret@bitbucket.org/design/app/pull-requests/12",
      "http://bitbucket.org/design/app/pull-requests/12",
      "https://bitbucket.org/design/other/pull-requests/12",
      "https://bitbucket.org/design/app/pull-requests/12?redirect=elsewhere",
      "https://bitbucket.org/design/app/pull-requests/12/_/diff#comment-34",
    ]) {
      expect(
        bitbucketCommentSource({
          ...activity,
          safe_diff: { ...activity.safe_diff, url },
        }).url,
      ).toBe("");
    }
  });
});
