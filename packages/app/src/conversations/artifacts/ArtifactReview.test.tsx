import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it } from "vitest";
import type { RunArtifact, MessageCreateInput } from "@openfox/api";
import { json, renderSettings } from "../../settings/testing";
import { ORG, chatBase, summary } from "../fixtures";
import { readDraft, resetDrafts } from "../drafts";
const file: RunArtifact = {
  content_id: "content-a1",
  content_revision: 1,
  head_artifact_id: "a1",
  organization_id: "o1",
  id: "a1",
  run_id: "r1",
  name: "报告.md",
  mime_type: "text/markdown",
  size_bytes: 40,
  sha256: "a".repeat(64),
  state: "AVAILABLE",
  pinned: false,
  version: 1,
  created_at: "2026-11-01T00:00:00Z",
};
beforeEach(resetDrafts);
function fixture(csv = false, fail = false, held?: Response) {
  const original = csv
    ? { ...file, name: "数据.csv", mime_type: "text/csv" }
    : file;
  const text = csv ? '名称,说明\r\n上海,"a,b"\r\n' : "# 报告\n前🙂原文\r\n后文";
  const calls = renderSettings(
    "/o/o1/chat/c1?artifact=a1",
    chatBase((call) => {
      if (call.path === `${ORG}/conversations/c1`)
        return json(200, summary("c1"));
      if (call.path === `${ORG}/conversations/c1/messages`) {
        if (call.method === "POST")
          return (
            held ??
            (fail
              ? json(500, { error: { code: "FAILED", message: "failed" } })
              : json(201, {
                  id: "m1",
                  conversation_id: "c1",
                  seq: 1,
                  kind: "TEXT",
                  author_kind: "USER",
                  author_user_id: "u1",
                  body: (call.body as MessageCreateInput).body,
                  attachments: [],
                  created_at: file.created_at,
                  run_id: null,
                }))
          );
        return json(200, { messages: [], next_cursor: 0 });
      }
      if (call.path === `${ORG}/conversations`)
        return json(200, { conversations: [summary("c1")], next_cursor: null });
      if (call.path === `${ORG}/conversations/c1/artifacts`)
        return json(200, { artifacts: [original] });
      if (call.path === `${ORG}/runs/r1/artifacts/a1`)
        return new Response(text);
      return undefined;
    }),
  );
  return { calls, text };
}
it("adds exact selections to the chat draft and sends through normal messages only", async () => {
  const { calls, text } = fixture();
  await userEvent.click(await screen.findByRole("button", { name: "批注" }));
  const part = await screen.findByText(/前🙂原文/);
  const node = part.firstChild!;
  const range = document.createRange();
  range.setStart(node, 3);
  range.setEnd(node, 5);
  range.getBoundingClientRect = () =>
    ({ left: 500, top: 200, width: 40, height: 20 }) as DOMRect;
  window.getSelection()!.removeAllRanges();
  window.getSelection()!.addRange(range);
  fireEvent.mouseUp(part);
  await waitFor(() =>
    expect(
      screen.getByRole("textbox", { name: "针对选区的修改意见" }),
    ).toHaveFocus(),
  );
  expect(screen.queryByRole("button", { name: "批注修改" })).toBeNull();
  await userEvent.type(
    screen.getByRole("textbox", { name: "针对选区的修改意见" }),
    "改为新文",
  );
  await userEvent.click(screen.getByRole("button", { name: "添加到聊天" }));
  const contexts = readDraft("c1")!.fileContexts!;
  expect(contexts[0]!.comments[0]!.anchor).toEqual({
    kind: "text",
    start: new TextEncoder().encode(text.slice(0, text.indexOf("原文"))).length,
    end: new TextEncoder().encode(text.slice(0, text.indexOf("原文") + 2))
      .length,
    quote: "原文",
  });
  expect(
    calls.filter((c) => c.method === "POST" && c.path.endsWith("/messages")),
  ).toHaveLength(0);
  expect(screen.queryByText(/当前版本|候选版待审核|采纳候选/)).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  await waitFor(() =>
    expect(
      calls.some((c) => c.method === "POST" && c.path.endsWith("/messages")),
    ).toBe(true),
  );
  const sent = calls.find(
    (c) => c.method === "POST" && c.path.endsWith("/messages"),
  )!.body as MessageCreateInput;
  expect(sent.file_contexts?.[0]).toMatchObject({
    artifact_id: "a1",
    comments: [{ body: "改为新文" }],
  });
  expect(sent.body).toContain("改为新文");
  await waitFor(() =>
    expect(readDraft("c1")?.fileContexts?.length ?? 0).toBe(0),
  );
  expect(calls.some((c) => c.path.includes("artifact-reviews"))).toBe(false);
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "移除批注" })).toBeNull(),
  );
});
it("keeps CSV feedback on send failure and permits removal from the composer", async () => {
  const { calls } = fixture(true, true);
  await userEvent.click(await screen.findByRole("button", { name: "批注" }));
  await userEvent.click(
    await screen.findByRole("button", { name: "第 2 行，第 2 列" }),
  );
  await userEvent.type(
    screen.getByRole("textbox", { name: "针对选区的修改意见" }),
    "简化内容",
  );
  await userEvent.click(screen.getByRole("button", { name: "添加到聊天" }));
  expect(readDraft("c1")!.fileContexts![0]!.comments[0]!.anchor).toMatchObject({
    kind: "cell",
    row: 1,
    column: 1,
    quote: '"a,b"',
  });
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  await waitFor(() =>
    expect(
      calls.some((c) => c.method === "POST" && c.path.endsWith("/messages")),
    ).toBe(true),
  );
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "移除批注" })).toBeEnabled(),
  );
  expect(readDraft("c1")!.fileContexts).toHaveLength(1);
  await userEvent.click(screen.getByRole("button", { name: "移除批注" }));
  expect(readDraft("c1")?.fileContexts ?? []).toHaveLength(0);
});

it("retains comments added during sending with a fresh context ID for the next message", async () => {
  let release!: () => void;
  const held = new Response(
    new ReadableStream({
      start(controller) {
        release = () => {
          controller.enqueue(
            new TextEncoder().encode(
              JSON.stringify({
                id: "m1",
                conversation_id: "c1",
                seq: 1,
                kind: "TEXT",
                author_kind: "USER",
                author_user_id: "u1",
                body: "first",
                attachments: [],
                created_at: file.created_at,
                run_id: null,
              }),
            ),
          );
          controller.close();
        };
      },
    }),
    { status: 201, headers: { "Content-Type": "application/json" } },
  );
  fixture(true, false, held);
  await userEvent.click(await screen.findByRole("button", { name: "批注" }));
  const add = async (body: string) => {
    await userEvent.click(
      await screen.findByRole("button", { name: "第 2 行，第 2 列" }),
    );
    await userEvent.type(
      screen.getByRole("textbox", { name: "针对选区的修改意见" }),
      body,
    );
    await userEvent.click(screen.getByRole("button", { name: "添加到聊天" }));
  };
  await add("第一条意见");
  const sentID = readDraft("c1")!.fileContexts![0]!.id;
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  await add("第二条意见");
  await act(async () => {
    release();
  });
  await waitFor(() =>
    expect(readDraft("c1")?.fileContexts?.[0]?.comments).toHaveLength(1),
  );
  expect(readDraft("c1")!.fileContexts![0]!.id).not.toBe(sentID);
  expect(readDraft("c1")!.fileContexts![0]!.comments[0]!.body).toBe(
    "第二条意见",
  );
  await waitFor(() =>
    expect(
      screen.getByLabelText("文件批注", { exact: true }),
    ).toHaveTextContent("待发送批注（1）"),
  );
});

it("defaults to preview, requires explicit annotation mode, and closes selection when disabled", async () => {
  fixture(true);
  const cell = await screen.findByRole("button", { name: "第 2 行，第 2 列" });
  const toggle = screen.getByRole("button", { name: "批注" });
  expect(toggle).toHaveAttribute("aria-pressed", "false");
  await userEvent.click(cell);
  expect(
    screen.queryByRole("textbox", { name: "针对选区的修改意见" }),
  ).toBeNull();
  await userEvent.click(toggle);
  await userEvent.click(cell);
  expect(
    screen.getByRole("textbox", { name: "针对选区的修改意见" }),
  ).toBeVisible();
  await userEvent.click(toggle);
  expect(
    screen.queryByRole("textbox", { name: "针对选区的修改意见" }),
  ).toBeNull();
  expect(screen.getByRole("button", { name: "第 2 行，第 2 列" })).toBe(cell);
});

it("keeps a closed selection draft and only adds it to chat after explicit submission", async () => {
  const { calls } = fixture(true);
  await userEvent.click(await screen.findByRole("button", { name: "批注" }));
  const cell = await screen.findByRole("button", { name: "第 2 行，第 2 列" });
  await userEvent.click(cell);
  const input = screen.getByRole("textbox", { name: "针对选区的修改意见" });
  expect(input).toHaveAttribute("rows", "1");
  await userEvent.type(input, "简化这段");
  fireEvent.keyDown(input, { key: "Enter", isComposing: true });
  expect(readDraft("c1")).toBeUndefined();
  await userEvent.keyboard("{Escape}");
  expect(
    screen.queryByRole("textbox", { name: "针对选区的修改意见" }),
  ).toBeNull();
  await userEvent.click(cell);
  expect(
    screen.getByRole("textbox", { name: "针对选区的修改意见" }),
  ).toHaveValue("简化这段");
  await waitFor(() =>
    expect(
      screen.getByRole("textbox", { name: "针对选区的修改意见" }),
    ).toHaveFocus(),
  );
  await userEvent.keyboard("{Enter}");
  expect(readDraft("c1")?.fileContexts?.[0]?.comments[0]?.body).toBe(
    "简化这段",
  );
  expect(calls.some((call) => call.method === "POST")).toBe(false);
  await userEvent.click(cell);
  expect(
    screen.getByRole("textbox", { name: "针对选区的修改意见" }),
  ).toHaveValue("");
});
