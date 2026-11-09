import { describe, expect, it } from "vitest";

import { entry } from "../agents/fixtures";
import { message, summary } from "./fixtures";
import { parseBlocks, safeHref } from "./Markdown";
import {
  buildMentions,
  directRows,
  displayStatus,
  groupByAgent,
  lastUserMessage,
  mergeConversationPages,
  mergeMessagePages,
  splitPinned,
  timelineItems,
  plainPreview,
} from "./model";

describe("对话纯函数", () => {
  it("运行、旧版关闭和手工标识分别展示", () => {
    const marked = summary("c", {
      status_mark: "DISCARDED",
      status: "WAITING",
    });
    expect(displayStatus(marked)).toBe("DISCARDED");
    expect(displayStatus({ ...marked, status_mark: null })).toBe("WAITING");
    expect(
      displayStatus({ ...marked, lifecycle: "COMPLETED", status: "DONE" }),
    ).toBe("DONE");
    expect(
      displayStatus({
        ...marked,
        status: "WORKING",
        run: {
          id: "r",
          state: "RUNNING",
          started_at: null,
          finished_at: null,
          wait_reason: "",
        },
      }),
    ).toBe("WORKING");
  });

  it("会话分页合并：按 id 去重、剔除已归档、updated_at 降序（同一时刻按 id 降序）", () => {
    const merged = mergeConversationPages([
      {
        conversations: [
          summary("a", { updated_at: "2026-10-29T09:00:00Z" }),
          summary("b", { updated_at: "2026-10-29T08:00:00Z" }),
        ],
      },
      {
        conversations: [
          summary("b", { updated_at: "2026-10-29T08:00:00Z", title: "新" }),
          summary("c", { updated_at: "2026-10-29T08:00:00Z" }),
          summary("d", { archived_at: "2026-10-29T08:00:00Z" }),
        ],
      },
    ]);
    expect(merged.map((item) => item.id)).toEqual(["a", "c", "b"]);
    expect(merged[2]!.title).toBe("新");
  });

  it("置顶分组与按 Agent 分组", () => {
    const list = [
      summary("a", { pinned: true }),
      summary("b", { agent_id: "a2", updated_at: "2026-10-29T10:00:00Z" }),
      summary("c"),
    ];
    const { pinned, recent } = splitPinned(list);
    expect(pinned.map((i) => i.id)).toEqual(["a"]);
    expect(recent.map((i) => i.id)).toEqual(["b", "c"]);
    const groups = groupByAgent(list);
    expect(groups.map((g) => g.conversations.map((i) => i.id))).toEqual([
      ["b"],
      ["c", "a"],
    ]);
  });

  it("Slack 直接对话：排除小队、不可单聊、已归档、本地移除；有会话的排前面", () => {
    const rows = directRows(
      [
        entry("x", { name: "乙" }),
        entry("y", { name: "甲" }),
        entry("z", { allow_direct_chat: false }),
        entry("k", { type: "crew" }),
        entry("h", { name: "隐藏" }),
      ],
      [summary("c1", { agent_id: "x" })],
      new Set(["h"]),
    );
    expect(rows.map((row) => row.entry.id)).toEqual(["x", "y"]);
    expect(rows[0]!.latest?.id).toBe("c1");
  });

  it("消息分页合并按 seq 去重升序", () => {
    const merged = mergeMessagePages([
      { messages: [message(3), message(4)] },
      { messages: [message(1), message(2), message(3)] },
    ]);
    expect(merged.map((m) => m.seq)).toEqual([1, 2, 3, 4]);
  });

  it("执行时间线锚点：AGENT 消息上方挂 Run；无回复的 Run 挂在用户消息后；活动 Run 未出现时挂末尾", () => {
    const items = timelineItems(
      [
        message(1, { run_id: "r1" }),
        message(2, { run_id: "r1" }),
        message(3, { run_id: "r2" }),
        message(5, { run_id: null }),
      ],
      "r3",
    );
    expect(
      items.map((item) =>
        item.kind === "run"
          ? `run:${item.runId}`
          : `${item.message.seq}${item.runBefore ? `<${item.runBefore}` : ""}`,
      ),
    ).toEqual(["1", "2<r1", "3", "run:r2", "5", "run:r3"]);
    expect(
      lastUserMessage([message(1), message(2), message(3), message(4)])?.seq,
    ).toBe(3);
  });

  it("提及：只保留正文里还在的 @名称，非会话 Agent 丢弃；所有者自动 @ 会话 Agent", () => {
    expect(
      buildMentions(
        "@熊猫 看下 @wang@x.dev",
        [
          { kind: "agent", id: "a1", name: "熊猫" },
          { kind: "user", id: "u2", name: "wang@x.dev" },
          { kind: "user", id: "u3", name: "gone@x.dev" },
          { kind: "agent", id: "a9", name: "熊猫" },
        ],
        "a1",
        true,
      ),
    ).toEqual([{ agent_id: "a1" }, { user_id: "u2" }]);
    expect(buildMentions("你好", [], "a1", false)).toEqual([]);
    expect(buildMentions("你好", [], "a1", true)).toEqual([{ agent_id: "a1" }]);
  });
});

describe("最小安全 Markdown", () => {
  it("链接只放行 http / https / mailto", () => {
    expect(safeHref("https://a.dev/x")).toBe("https://a.dev/x");
    expect(safeHref("mailto:a@b.dev")).toBe("mailto:a@b.dev");
    expect(safeHref("javascript:alert(1)")).toBeNull();
    expect(safeHref(" JAVASCRIPT:alert(1)")).toBeNull();
    expect(safeHref("data:text/html,x")).toBeNull();
    expect(safeHref("//evil.dev")).toBeNull();
  });

  it("块解析：标题、列表、代码块（未闭合延续到末尾）、引用、段落", () => {
    const blocks = parseBlocks(
      "# 标题\n\n- 一\n- 二\n\n1. 甲\n2. 乙\n\n> 引用\n\n正文\n第二行\n\n```ts\nconst a = 1;",
    );
    expect(blocks.map((b) => b.kind)).toEqual([
      "heading",
      "list",
      "list",
      "quote",
      "paragraph",
      "code",
    ]);
    expect(blocks[5]).toMatchObject({
      kind: "code",
      lang: "ts",
      text: "const a = 1;",
    });
  });
  it("列表摘要去掉 Markdown 标记", () => {
    expect(
      plainPreview(
        "已修复。改动如下：\n\n- `LoginForm.tsx`：按钮加 `whitespace-nowrap`\n- 补了一条 **窄屏** 快照测试\n\n详见 [提交说明](https://example.com/pr/1)。",
      ),
    ).toBe(
      "已修复。改动如下： LoginForm.tsx：按钮加 whitespace-nowrap 补了一条 窄屏 快照测试 详见 提交说明。",
    );
    expect(plainPreview("# 标题\n> 引用 a_b_c")).toBe("标题 引用 a_b_c");
  });
});
