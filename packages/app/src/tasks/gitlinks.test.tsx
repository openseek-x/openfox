import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App, createQueryClient } from "../App";
import { backend, json, ORG, SPACE, type Override } from "./test-backend";

// 任务详情的 Git 关联区与按设置生成的分支名。

function renderAt(path: string, override?: Override) {
  const server = backend(override);
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

const detailPath = `/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`;
const link = (extra: Record<string, unknown>) => ({
  id: "g1",
  kind: "PULL_REQUEST",
  provider: "github",
  repository: "acme/web",
  number: 7,
  sha: "",
  title: "修复登录",
  url: "https://github.com/acme/web/pull/7",
  state: "OPEN",
  author_login: "octo",
  author_user_id: null,
  head_branch: "open-1-fix",
  matched_in: "title",
  created_at: "2026-10-28T00:00:00Z",
  updated_at: "2026-10-28T00:00:00Z",
  merged_at: null,
  ...extra,
});

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("任务详情 · Git 关联", () => {
  it("PR 与 Commit 一起列出：徽标、外链、作者显示成员名", async () => {
    renderAt(detailPath, (call) =>
      call.path.endsWith("/tasks/task-1/git-links")
        ? json(200, {
            links: [
              link({ author_user_id: "u2" }),
              link({
                id: "g2",
                kind: "COMMIT",
                number: null,
                sha: "abcdef1234567",
                title: "初始提交",
                url: "https://github.com/acme/web/commit/abcdef1",
                state: "COMMITTED",
                author_login: "octo",
              }),
              link({ id: "g3", number: 8, state: "MERGED", title: "已合并" }),
            ],
          })
        : undefined,
    );
    const section = await screen.findByRole("region", {
      name: "GitHub 关联",
    });
    expect(within(section).getByText("Open")).toBeVisible();
    expect(within(section).getByText("Merged")).toBeVisible();
    expect(within(section).getByText("Commit")).toBeVisible();
    expect(within(section).getByText("acme/web#7")).toBeVisible();
    expect(within(section).getByText("acme/web@abcdef1")).toBeVisible();
    const anchor = within(section).getByRole("link", { name: /修复登录/ });
    expect(anchor).toHaveAttribute(
      "href",
      "https://github.com/acme/web/pull/7",
    );
    expect(anchor).toHaveAttribute("target", "_blank");
    expect(anchor).toHaveAttribute("rel", "noopener noreferrer");
    // 绑定了个人账号的作者显示成员名，否则显示 GitHub login
    expect(within(section).getByText("wang@openfox.dev")).toBeVisible();
    expect(within(section).getAllByText("octo").length).toBe(2);
  });

  it("GitLab 系关联：标题改为 GitLab 关联，Issue 图标名称按服务商，混合时标题为 Git 关联", async () => {
    renderAt(detailPath, (call) =>
      call.path.endsWith("/tasks/task-1/git-links")
        ? json(200, {
            links: [
              link({
                id: "l1",
                provider: "gitlab_cn",
                repository: "grp/sub/proj",
                number: 5,
                title: "极狐合并请求",
                url: "https://jihulab.com/grp/sub/proj/-/merge_requests/5",
              }),
              link({
                id: "l2",
                kind: "ISSUE",
                provider: "gitlab",
                repository: "grp/proj",
                number: 3,
                title: "自部署问题",
                url: "https://gitlab.example.com/grp/proj/-/issues/3",
              }),
            ],
          })
        : undefined,
    );
    const section = await screen.findByRole("region", { name: "GitLab 关联" });
    const items = within(section).getAllByRole("listitem");
    expect(within(items[0]!).getByLabelText("GitLab Issue")).toBeVisible();
    expect(within(items[1]!).getByText("grp/sub/proj#5")).toBeVisible();
  });

  it("Gitee 关联：标题为 Gitee 关联，Issue 图标名称是 Gitee；与 GitHub 混合时标题为 Git 关联", async () => {
    renderAt(detailPath, (call) =>
      call.path.endsWith("/tasks/task-1/git-links")
        ? json(200, {
            links: [
              link({
                id: "g1",
                kind: "ISSUE",
                provider: "gitee",
                repository: "acme/web",
                number: 295024870,
                title: "Gitee 问题",
                url: "https://gitee.com/acme/web/issues/IG6E9",
              }),
            ],
          })
        : undefined,
    );
    const section = await screen.findByRole("region", { name: "Gitee 关联" });
    expect(within(section).getByLabelText("Gitee Issue")).toBeVisible();
  });

  it("GitHub 与 Gitee 混合时标题为 Git 关联", async () => {
    renderAt(detailPath, (call) =>
      call.path.endsWith("/tasks/task-1/git-links")
        ? json(200, {
            links: [link({}), link({ id: "g2", provider: "gitee", number: 4 })],
          })
        : undefined,
    );
    expect(
      await screen.findByRole("region", { name: "Git 关联" }),
    ).toBeVisible();
  });

  it("GitHub 与 GitLab 混合时标题为 Git 关联", async () => {
    renderAt(detailPath, (call) =>
      call.path.endsWith("/tasks/task-1/git-links")
        ? json(200, {
            links: [
              link({}),
              link({ id: "l3", provider: "gitlab", number: 9 }),
            ],
          })
        : undefined,
    );
    expect(
      await screen.findByRole("region", { name: "Git 关联" }),
    ).toBeVisible();
  });

  it("Issue 排在最前：GitHub 图标、Open 绿 / Closed 紫灰、repo#number、新窗口外链", async () => {
    renderAt(detailPath, (call) =>
      call.path.endsWith("/tasks/task-1/git-links")
        ? json(200, {
            links: [
              link({}),
              link({
                id: "g9",
                kind: "ISSUE",
                number: 12,
                title: "登录报错",
                url: "https://github.com/acme/web/issues/12",
                state: "CLOSED",
              }),
              link({
                id: "g8",
                kind: "ISSUE",
                number: 13,
                title: "另一个问题",
                url: "https://github.com/acme/web/issues/13",
                state: "OPEN",
              }),
            ],
          })
        : undefined,
    );
    const section = await screen.findByRole("region", { name: "GitHub 关联" });
    const items = within(section).getAllByRole("listitem");
    expect(items).toHaveLength(3);
    // 排序：Issue（服务端顺序）在前，PR 在后
    expect(within(items[0]!).getByText("acme/web#12")).toBeVisible();
    expect(within(items[1]!).getByText("acme/web#13")).toBeVisible();
    expect(within(items[2]!).getByText("acme/web#7")).toBeVisible();
    expect(within(items[0]!).getByLabelText("GitHub Issue")).toBeVisible();
    expect(within(items[0]!).getByText("Closed").className).toContain("6d28d9");
    expect(within(items[1]!).getByText("Open")).toBeVisible();
    const anchor = within(items[0]!).getByRole("link", { name: /登录报错/ });
    expect(anchor).toHaveAttribute(
      "href",
      "https://github.com/acme/web/issues/12",
    );
    expect(anchor).toHaveAttribute("target", "_blank");
  });

  it("没有关联时不显示这个区", async () => {
    renderAt(detailPath, (call) =>
      call.path.endsWith("/git-links") ? json(200, { links: [] }) : undefined,
    );
    await screen.findByRole("textbox", { name: "任务标题" });
    expect(screen.queryByRole("region", { name: "GitHub 关联" })).toBeNull();
  });

  it("复制分支名使用 GitHub 设置里的格式与邮箱前缀", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    renderAt(detailPath, (call) =>
      call.path.endsWith("/integrations/github/settings")
        ? json(200, {
            provider: "github",
            settings: {
              link_pull_requests: true,
              link_commits: false,
              pr_backlink_private: true,
              pr_backlink_public: false,
              branch_format: "username/identifier",
            },
            can_manage: true,
            version: 1,
            updated_at: null,
          })
        : undefined,
    );
    await screen.findByRole("textbox", { name: "任务标题" });
    await user.click(screen.getByRole("button", { name: "更多操作" }));
    await waitFor(() =>
      expect(
        screen.getByRole("menuitem", { name: "复制 git 分支名" }),
      ).toBeVisible(),
    );
    await user.click(screen.getByRole("menuitem", { name: "复制 git 分支名" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("lin/open-1"));
  });
});
