import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { apiError, json, renderSettings, ts } from "../settings/testing";
import { bitbucketJobURL } from "./BitbucketJobs";
const base = "/v1/organizations/o1/integrations/bitbucket",
  conn = `${base}/connections/c1`,
  page = "/o/o1/settings/integrations/bitbucket";
const job = {
  id: "j1",
  kind: "BACKLINK",
  repository_id: "r1",
  repository: "design/app",
  url: "https://bitbucket.org/design/app/pull-requests/12",
  state: "UNKNOWN",
  error_code: "unknown_result",
  attempts: 1,
  version: 7,
  next_attempt_at: null,
  updated_at: ts,
  can_retry: true,
  requires_unsent_confirmation: true,
};
const connection = {
  id: "c1",
  provider: "bitbucket",
  scope: "ORGANIZATION",
  external_id: "w1",
  display_name: "Design",
  status: "ACTIVE",
  version: 3,
  created_at: ts,
  updated_at: ts,
};
function setup(value = job, failure = false) {
  return renderSettings(page, (c) =>
    c.path === base
      ? json(200, {
          provider: {
            id: "bitbucket",
            name: "Bitbucket",
            template: "B",
            category: "code",
            availability: "available",
            configured: true,
            bindable: true,
            capabilities: ["pr_link"],
            summary: { connections: 1, status: "ok" },
          },
          can_manage: true,
          connections: [connection],
          personal_connections: [],
          sync_links: [],
        })
      : c.path === `${conn}/repositories`
        ? json(200, { repositories: [] })
        : c.path === `${conn}/jobs`
          ? json(200, { jobs: [value] })
          : c.path === `${conn}/jobs/retry`
            ? failure
              ? apiError(409, "VERSION_CONFLICT")
              : json(204)
            : undefined,
  );
}
async function open() {
  await userEvent.click(
    await screen.findByRole("button", { name: "仓库与 Webhook" }),
  );
  await screen.findByText("关联与回链诊断");
}
describe("Bitbucket UNKNOWN diagnostics", () => {
  beforeEach(() => localStorage.clear());
  it("never sends on read and requires a separate duplicate-risk confirmation for UNKNOWN", async () => {
    const calls = setup();
    await open();
    const action = await screen.findByRole("button", {
      name: "核查并申请重发",
    });
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    await userEvent.click(action);
    const confirm = screen.getByRole("button", {
      name: "确认未发送并重新发送",
    });
    expect(confirm).toBeDisabled();
    expect(screen.getByText(/执行时的当前组织授权账号/)).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    await userEvent.click(screen.getByLabelText(/我已核对源 PR 并确认未发送/));
    await userEvent.click(confirm);
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")?.body).toEqual({
        kind: "BACKLINK",
        id: "j1",
        version: 7,
        confirm_unsent: true,
      }),
    );
  });
  it("keeps UNKNOWN visible when it is not retryable", async () => {
    const calls = setup({ ...job, can_retry: false });
    await open();
    expect(
      await screen.findByText(/发送结果未知：上游可能已经收到评论/),
    ).toBeVisible();
    expect(screen.queryByRole("button", { name: "核查并申请重发" })).toBeNull();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });
  it("ordinary failure retry sends false consent only after clicking", async () => {
    const calls = setup({
      ...job,
      kind: "WORK",
      state: "FAILED",
      requires_unsent_confirmation: false,
    });
    await open();
    await userEvent.click(await screen.findByRole("button", { name: "重试" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")?.body).toEqual({
        kind: "WORK",
        id: "j1",
        version: 7,
        confirm_unsent: false,
      }),
    );
  });
  it("preserves an error after version conflict without claiming success or retrying automatically", async () => {
    const calls = setup(job, true);
    await open();
    await userEvent.click(
      await screen.findByRole("button", { name: "核查并申请重发" }),
    );
    await userEvent.click(screen.getByLabelText(/我已核对源 PR 并确认未发送/));
    await userEvent.click(
      screen.getByRole("button", { name: "确认未发送并重新发送" }),
    );
    expect(await screen.findByText(/刷新.*重试|已被.*修改/)).toBeVisible();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(1);
  });
  it("validates source navigation without following arbitrary URLs", () => {
    expect(bitbucketJobURL(job)).toBe(job.url);
    for (const url of [
      "https://bitbucket.org.evil.test/design/app/pull-requests/12",
      "https://bitbucket.org/design/other/pull-requests/12",
      "https://bitbucket.org/design/app/pull-requests/12#comment-34",
      "https://bitbucket.org/design/app/pull-requests/12?access_token=synthetic",
    ]) {
      expect(bitbucketJobURL({ ...job, url })).toBe("");
    }
  });
});
