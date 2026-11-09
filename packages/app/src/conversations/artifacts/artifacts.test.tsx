import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RunArtifact } from "@openfox/api";

import { keys } from "../../context";
import {
  EVENT_COALESCE_MS,
  createEventBatcher,
} from "../../realtime/useOrgEvents";
import { json, renderSettings, type Call } from "../../settings/testing";
import { ORG, chatBase, message, run, summary } from "../fixtures";
import { artifactPreviewKind, artifactPreviewable } from "./model";

// 会话产物：回复里的「产物：<文件名>」按钮、右侧标签面板（列表 + 文件标签）、?artifact= 恢复、
// 各类型预览（Markdown / 图片 / 文本 / CSV / 二进制 / 已失效）、「更多」菜单入口与实时失效。

const LIST = `${ORG}/conversations/c1/artifacts`;
const RUN_ARTIFACTS = `${ORG}/runs/r1/artifacts`;

const artifact = (
  id: string,
  name: string,
  mime: string,
  extra: Partial<RunArtifact> = {},
): RunArtifact => ({
  organization_id: "o1",
  id,
  run_id: "r1",
  name,
  size_bytes: 12,
  sha256: "0".repeat(64),
  mime_type: mime,
  state: "AVAILABLE",
  retention_until: "2099-01-01T00:00:00Z",
  pinned: false,
  version: 1,
  created_at: "2026-11-04T08:00:00Z",
  ...extra,
});

const MD = artifact("a-md", "报告.md", "text/markdown");
const PNG = artifact("a-png", "图表.png", "image/png");
const TXT = artifact("a-txt", "main.go", "text/plain");
const CSV = artifact("a-csv", "数据.csv", "text/csv");
const ZIP = artifact("a-zip", "产物.zip", "application/octet-stream", {
  size_bytes: 2048,
});
const OLD = artifact("a-old", "旧报告.md", "text/markdown", {
  retention_until: "2020-01-01T00:00:00Z",
});
const ALL = [MD, PNG, TXT, CSV, ZIP, OLD];

const CONTENT: Record<string, () => Response> = {
  "a-md": () => new Response("# 周报标题\n\n正文段落", { status: 200 }),
  "a-png": () =>
    new Response(new Uint8Array([137, 80, 78, 71]), {
      status: 200,
    }),
  "a-txt": () => new Response("package main\n", { status: 200 }),
  "a-csv": () => new Response("城市,人数\n上海,3\n北京,5\n", { status: 200 }),
};

beforeEach(() => {
  localStorage.clear();
  URL.createObjectURL = vi.fn(() => "blob:artifact");
  URL.revokeObjectURL = vi.fn();
});
afterEach(() => localStorage.clear());

function open(
  path = "/o/o1/chat/c1",
  options: { list?: RunArtifact[]; runArtifacts?: RunArtifact[] } = {},
) {
  const list = options.list ?? ALL;
  const runArtifacts = options.runArtifacts ?? list;
  return renderSettings(
    path,
    chatBase((call: Call) => {
      if (call.method !== "GET") return undefined;
      if (call.path === LIST) return json(200, { artifacts: list });
      if (call.path === RUN_ARTIFACTS)
        return json(200, { artifacts: runArtifacts });
      const content = /\/runs\/r1\/artifacts\/([^/]+)$/.exec(call.path);
      if (content) return CONTENT[content[1]!]?.() ?? json(404, {});
      if (call.path === `${ORG}/runs/r1`)
        return json(
          200,
          run("r1", {
            state: "SUCCEEDED",
            finished_at: "2026-11-04T08:01:00Z",
          }),
        );
      if (call.path === `${ORG}/conversations/c1/messages`)
        return json(200, {
          messages: [
            message(1, { body: "帮我写周报" }),
            message(2, { body: "周报已经写好。", run_id: "r1" }),
          ],
          next_cursor: 0,
        });
      if (call.path === `${ORG}/conversations/c1`)
        return json(200, summary("c1"));
      if (call.path === `${ORG}/conversations`)
        return json(200, { conversations: [summary("c1")], next_cursor: null });
      return undefined;
    }),
  );
}

const panel = () => screen.findByRole("complementary", { name: "会话产物" });

describe("会话产物 · 按钮与面板", () => {
  it("回复里显示「产物：<文件名>」；点击打开面板并预览 Markdown；关闭标签回到列表，关闭面板后可再次打开", async () => {
    open("/o/o1/chat/c1", { list: [MD], runArtifacts: [MD] });
    const button = await screen.findByRole("button", {
      name: "产物：报告.md",
    });
    // 按钮在 Agent 回复里，只出现一次（时间线末尾不重复）
    expect(
      screen.getAllByRole("button", { name: "产物：报告.md" }),
    ).toHaveLength(1);
    await userEvent.click(button);
    const aside = await panel();
    expect(
      await within(aside).findByRole("heading", { name: "周报标题" }),
    ).toBeInTheDocument();
    expect(
      within(aside).getByRole("tab", { name: "产物 1 个" }),
    ).toHaveAttribute("aria-selected", "false");
    expect(within(aside).getByRole("tab", { name: "报告.md" })).toHaveAttribute(
      "aria-selected",
      "true",
    );

    await userEvent.click(
      within(aside).getByRole("button", { name: "关闭 报告.md" }),
    );
    expect(within(aside).queryByRole("tab", { name: "报告.md" })).toBeNull();
    expect(
      within(aside).getByRole("tab", { name: "产物 1 个" }),
    ).toHaveAttribute("aria-selected", "true");
    expect(
      within(aside).getByRole("button", { name: "打开 报告.md" }),
    ).toBeInTheDocument();

    await userEvent.click(
      within(aside).getByRole("button", { name: "关闭产物面板" }),
    );
    await waitFor(() =>
      expect(
        screen.queryByRole("complementary", { name: "会话产物" }),
      ).not.toBeInTheDocument(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "产物：报告.md" }),
    );
    expect(
      await within(await panel()).findByRole("heading", { name: "周报标题" }),
    ).toBeInTheDocument();
  });

  it("刷新后按 ?artifact= 恢复面板并选中该产物（图片用 blob URL 显示）", async () => {
    open("/o/o1/chat/c1?artifact=a-png");
    const aside = await panel();
    const image = await within(aside).findByRole("img", { name: "图表.png" });
    expect(image).toHaveAttribute("src", "blob:artifact");
    expect(
      within(aside).getByRole("tab", { name: "图表.png" }),
    ).toHaveAttribute("aria-selected", "true");
    expect(
      within(aside).getByRole("tab", { name: "产物 6 个" }),
    ).toBeInTheDocument();
  });

  it("文本按代码显示、CSV 显示表格", async () => {
    open("/o/o1/chat/c1?artifact=a-txt");
    let aside = await panel();
    expect(await within(aside).findByText(/package main/)).toBeInTheDocument();
    await userEvent.click(
      within(aside).getByRole("tab", { name: "产物 6 个" }),
    );
    await userEvent.click(
      within(aside).getByRole("button", { name: "打开 数据.csv" }),
    );
    aside = await panel();
    const table = await within(aside).findByRole("table");
    expect(within(table).getByText("上海")).toBeInTheDocument();
    expect(within(table).getByText("人数")).toBeInTheDocument();
    // 两个文件标签都在
    expect(
      within(aside).getByRole("tab", { name: "main.go" }),
    ).toBeInTheDocument();
    expect(
      within(aside).getByRole("tab", { name: "数据.csv" }),
    ).toBeInTheDocument();
  });

  it("二进制只显示信息与下载；已失效显示「产物已失效」且不请求内容", async () => {
    const calls = open("/o/o1/chat/c1?artifact=a-zip");
    let aside = await panel();
    expect(
      await within(aside).findByText(/无法预览这种文件/),
    ).toBeInTheDocument();
    expect(
      within(aside).getByRole("link", { name: "下载文件" }),
    ).toHaveAttribute("href", `/api${ORG}/runs/r1/artifacts/a-zip`);
    await userEvent.click(
      within(aside).getByRole("tab", { name: "产物 6 个" }),
    );
    await userEvent.click(
      within(aside).getByRole("button", { name: "打开 旧报告.md" }),
    );
    aside = await panel();
    expect(await within(aside).findByText("产物已失效")).toBeInTheDocument();
    expect(calls.some((call) => call.path.endsWith("/artifacts/a-old"))).toBe(
      false,
    );
    expect(calls.some((call) => call.path.endsWith("/artifacts/a-zip"))).toBe(
      false,
    );
  });

  it("地址里的产物不属于这个会话：重拉一次列表后静默关闭面板", async () => {
    open("/o/o1/chat/c1?artifact=zzz", { list: [MD] });
    await waitFor(() =>
      expect(
        screen.queryByRole("complementary", { name: "会话产物" }),
      ).not.toBeInTheDocument(),
    );
  });
});

describe("会话产物 · 更多菜单", () => {
  it("有产物时「更多」里有「产物」，打开列表标签", async () => {
    open("/o/o1/chat/c1", { list: [MD] });
    await screen.findByRole("button", { name: "产物：报告.md" });
    await userEvent.click(screen.getByRole("button", { name: "更多操作" }));
    await userEvent.click(
      await screen.findByRole("menuitem", { name: "产物" }),
    );
    const aside = await panel();
    expect(
      within(aside).getByRole("tab", { name: "产物 1 个" }),
    ).toHaveAttribute("aria-selected", "true");
    expect(
      within(aside).getByRole("button", { name: "打开 报告.md" }),
    ).toBeInTheDocument();
  });

  it("没有产物时不显示「产物」", async () => {
    const calls = open("/o/o1/chat/c1", { list: [], runArtifacts: [] });
    await screen.findByText("周报已经写好。");
    await waitFor(() =>
      expect(calls.some((call) => call.path === LIST)).toBe(true),
    );
    await userEvent.click(screen.getByRole("button", { name: "更多操作" }));
    await screen.findByRole("menuitem", { name: "复制链接" });
    expect(screen.queryByRole("menuitem", { name: "产物" })).toBeNull();
    expect(screen.queryByRole("button", { name: /^产物：/ })).toBeNull();
  });
});

describe("会话产物 · 预览规则", () => {
  it("只按中心保存的 MIME 判断；超过上限只能下载", () => {
    expect(artifactPreviewKind(MD)).toBe("markdown");
    expect(artifactPreviewKind(CSV)).toBe("table");
    expect(artifactPreviewKind(TXT)).toBe("code");
    expect(artifactPreviewKind(PNG)).toBe("image");
    expect(artifactPreviewKind(ZIP)).toBe("binary");
    // 扩展名是 .svg / .html 但中心存为 text/plain：按源码显示，不渲染
    expect(artifactPreviewKind(artifact("x", "a.svg", "text/plain"))).toBe(
      "code",
    );
    expect(artifactPreviewable(ZIP)).toBe(false);
    expect(
      artifactPreviewable({ ...PNG, size_bytes: 10 * 1024 * 1024 + 1 }),
    ).toBe(false);
    expect(
      artifactPreviewable({ ...MD, size_bytes: 5 * 1024 * 1024 + 1 }),
    ).toBe(false);
  });
});

describe("会话产物 · 实时", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("run.artifact 失效该会话的产物列表与对应 Run 的产物，不动其他会话", () => {
    const client = new QueryClient();
    const seed = (key: readonly unknown[]) => client.setQueryData(key, []);
    seed(keys.conversationArtifacts("o1", "c1"));
    seed(keys.conversationArtifacts("o1", "c2"));
    seed(keys.runSub("o1", "r1", "artifacts"));
    seed(keys.runSub("o1", "r2", "artifacts"));
    const batcher = createEventBatcher(client, "o1");
    batcher.push("run.artifact", {
      type: "run.artifact",
      conversation_id: "c1",
      payload: { run_id: "r1" },
    });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    const stale = client
      .getQueryCache()
      .getAll()
      .filter((query) => query.state.isInvalidated)
      .map((query) => JSON.stringify(query.queryKey))
      .sort();
    expect(stale).toEqual(
      [
        JSON.stringify(keys.conversationArtifacts("o1", "c1")),
        JSON.stringify(keys.runSub("o1", "r1", "artifacts")),
      ].sort(),
    );
  });
});
