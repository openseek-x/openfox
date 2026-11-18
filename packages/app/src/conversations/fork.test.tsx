import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { apiError, json, renderSettings } from "../settings/testing";
import { agent } from "../agents/fixtures";
import { ORG, chatBase, message, summary } from "./fixtures";

describe("conversation fork", () => {
  it("requires explicit agent/directory, cancels without writes and retries the exact attempt after reopening", async () => {
    let tries = 0;
    const calls = renderSettings(
      "/o/o1/chat/c1",
      chatBase((call) => {
        if (call.path === `${ORG}/spaces/s1/agents`)
          return json(200, { agents: [agent("a1")] });
        if (call.path === `${ORG}/conversations/c1/fork`) {
          tries++;
          return apiError(503, "UNAVAILABLE");
        }
        if (call.path === `${ORG}/conversation-workspace-options`)
          return json(200, {
            device_id: "d1",
            supported: true,
            directories: [],
            git_directories: [],
            recent_directories: [],
            can_manage_directory: false,
            can_reuse: false,
            can_set_baseline: false,
          });
        if (call.path === `${ORG}/conversations/c1/messages`)
          return json(200, {
            messages: [message(1), message(2)],
            next_cursor: 0,
          });
        if (call.path === `${ORG}/conversations/c1`)
          return json(200, summary("c1"));
        if (call.path === `${ORG}/conversations`)
          return json(200, {
            conversations: [summary("c1")],
            next_cursor: null,
          });
        return undefined;
      }),
    );
    const buttons = await screen.findAllByRole("button", {
      name: "从此处分支",
    });
    await userEvent.click(buttons[0]!);
    let dialog = screen.getByRole("dialog");
    expect(
      within(dialog).getByRole("button", { name: "创建分支会话" }),
    ).toBeDisabled();
    await userEvent.click(within(dialog).getByRole("button", { name: "取消" }));
    expect(calls.some((c) => c.path.endsWith("/fork"))).toBe(false);
    for (let i = 0; i < 2; i++) {
      await userEvent.click(buttons[1]!);
      dialog = screen.getByRole("dialog");
      await waitFor(() =>
        expect(
          within(dialog).getByRole("option", { name: agent("a1").name }),
        ).toBeInTheDocument(),
      );
      fireEvent.change(
        within(dialog).getByRole("combobox", { name: "目标 Agent" }),
        { target: { value: "a1" } },
      );
      expect(
        within(dialog).getByRole("button", { name: "创建分支会话" }),
      ).toBeDisabled();
      fireEvent.change(
        within(dialog).getByRole("combobox", { name: "本次工作目录" }),
        { target: { value: "DEFAULT" } },
      );
      await userEvent.click(
        within(dialog).getByRole("button", { name: "创建分支会话" }),
      );
      await within(dialog).findByRole("alert");
      await userEvent.click(
        within(dialog).getByRole("button", { name: "取消" }),
      );
    }
    expect(tries).toBe(2);
    const posts = calls.filter((c) => c.path.endsWith("/fork"));
    expect(posts[0]?.body).toEqual({
      source_message_id: "m2",
      agent_id: "a1",
      workspace_selection: null,
    });
    expect(posts[1]?.body).toEqual(posts[0]?.body);
    expect(posts[1]?.headers.get("Idempotency-Key")).toEqual(
      posts[0]?.headers.get("Idempotency-Key"),
    );
  });
  it("renders inherited pending proposals without execution controls", async () => {
    renderSettings(
      "/o/o1/chat/c1",
      chatBase((call) => {
        if (call.path === `${ORG}/conversations/c1/messages`)
          return json(200, {
            messages: [
              message(2, {
                kind: "ACTION_PROPOSAL",
                is_fork_history: true,
                payload: {
                  action: "create_task",
                  status: "pending",
                  task: {
                    title: "Historical task",
                    description: "Original proposal",
                  },
                },
              }),
            ],
            next_cursor: 0,
          });
        if (call.path === `${ORG}/conversations/c1`)
          return json(200, summary("c1", { fork_prefix_seq: 2 }));
        if (call.path === `${ORG}/conversations`)
          return json(200, {
            conversations: [summary("c1", { fork_prefix_seq: 2 })],
            next_cursor: null,
          });
        return undefined;
      }),
    );
    expect(await screen.findByText("历史快照，不可执行")).toBeInTheDocument();
    expect(screen.getByText("Historical task")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "创建任务" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("note")).toHaveTextContent(
      "附件和工作目录中的文件不复制",
    );
  });
});
