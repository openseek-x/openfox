import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { findCall, json, renderSettings } from "../settings/testing";
import { ORG, TS, chatBase, message, run, summary } from "./fixtures";

// 阶段 12d（F-08）：会话页里 worktree 准备失败的运行可确认后改为直接在源目录运行并重试。

afterEach(() => localStorage.clear());

function renderFailed(actorId: string) {
  const conversation = {
    ...summary("c1", { status: "ERROR" }),
    workspace_selection: { mode: "WORKTREE", directory: "team/openfox" },
  };
  return renderSettings(
    "/o/o1/chat/c1",
    chatBase((call) => {
      if (
        call.method === "POST" &&
        call.path === `${ORG}/conversations/c1/runs/r9/retries`
      )
        return json(200, { retry_id: "x1", retry_of: "r9", replayed: false });
      if (call.method !== "GET") return undefined;
      if (call.path === `${ORG}/runs/r9`)
        return json(
          200,
          run("r9", {
            state: "FAILED",
            failure_code: "WORKTREE_PREP_FAILED",
            actor_id: actorId,
            finished_at: TS,
          }),
        );
      if (call.path === `${ORG}/conversations/c1/messages`)
        return json(200, {
          messages: [
            message(1),
            message(2, {
              author_kind: "SYSTEM",
              body: "worktree failed",
              run_id: "r9",
            }),
          ],
          next_cursor: 0,
        });
      if (call.path === `${ORG}/conversations/c1`)
        return json(200, conversation);
      if (call.path === `${ORG}/conversations`)
        return json(200, { conversations: [conversation], next_cursor: null });
      return undefined;
    }),
  );
}

describe("阶段 12d F-08：会话页改为直接在源目录运行并重试", () => {
  it("原输入作者确认后以 DIRECT + 原目录调用重试接口", async () => {
    const calls = renderFailed("u1");
    expect(await screen.findByText("准备工作目录失败")).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "改为直接在源目录运行并重试" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain(
      "Agent 将直接修改源目录中的文件，不再隔离",
    );
    expect(
      findCall(calls, "POST", `${ORG}/conversations/c1/runs/r9/retries`),
    ).toBeUndefined();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "直接运行并重试" }),
    );
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${ORG}/conversations/c1/runs/r9/retries`),
      ).toBeTruthy(),
    );
    const call = findCall(
      calls,
      "POST",
      `${ORG}/conversations/c1/runs/r9/retries`,
    )!;
    expect(call.body).toEqual({
      workspace_selection: { mode: "DIRECT", directory: "team/openfox" },
    });
    expect(call.headers.get("Idempotency-Key")).toBeTruthy();
  });

  it("不是原输入作者时不显示入口", async () => {
    renderFailed("u2");
    expect(await screen.findByText("准备工作目录失败")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "改为直接在源目录运行并重试" }),
    ).not.toBeInTheDocument();
  });
});
