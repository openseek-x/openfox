import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it } from "vitest";
import type { MessageCreateInput } from "@openfox/api";
import { json, renderSettings } from "../../settings/testing";
import { chatBase, ORG, summary, message } from "../fixtures";
import {
  openWorkspaceDB,
  workspaceStore,
  WorkspaceStore,
} from "../workspaceStorage";
import { saveUnfinished, unfinishedKey } from "./UnfinishedAnnotation";
import {
  addBrowserComment,
  readDraft,
  resetDrafts,
  saveDraft,
  type DraftBrowserContext,
} from "../drafts";

const source = {
  source_url: "https://example.com/page",
  title: "Example",
  captured_at: "2026-11-17T00:00:00Z",
};
const comment = (
  body = "Improve this title",
): DraftBrowserContext["comments"][number] => ({
  id: crypto.randomUUID(),
  body,
  anchor: {
    kind: "element",
    live: {
      host_instance_id: crypto.randomUUID(),
      tab_id: "11111111-1111-4111-8111-111111111111",
      frame_id: "frame",
      document_generation: 1,
      ref: crypto.randomUUID(),
    },
    snapshot: {
      role: "heading",
      name: "",
      quote: "Selected heading",
      prefix: "",
      suffix: "",
      selector_hint: "h1",
    },
    visual: {
      width: 800,
      height: 600,
      scroll_x: 0,
      scroll_y: 0,
      rect: { x: 10, y: 10, width: 200, height: 40 },
    },
  },
});
beforeEach(resetDrafts);
it("restores an unfinished web annotation after restart without reactivating its live selection", async () => {
  const selection = {
    ...source,
    id: "selection",
    tabId: "old-tab",
    anchor: comment().anchor,
  };
  const old = new WorkspaceStore(
    JSON.stringify(["u1", "o1"]),
    await openWorkspaceDB(),
  );
  await old.hydrate();
  const key = unfinishedKey("c1", selection);
  old.write(key, { selection, body: "恢复的输入" });
  await old.close();
  const calls = fixture(() => json(500, {}));
  await screen.findByText("恢复的网页批注输入 · Example");
  await userEvent.click(screen.getByText("恢复的网页批注输入 · Example"));
  const box = screen.getByRole("textbox", { name: "写下你的反馈…" });
  expect(box).toHaveValue("恢复的输入");
  await userEvent.keyboard("{Escape}");
  expect(workspaceStore().read(key)).toBeDefined();
  await userEvent.click(screen.getByText("恢复的网页批注输入 · Example"));
  await userEvent.click(screen.getByRole("button", { name: "添加到聊天" }));
  expect(
    readDraft("c1")?.browserContexts?.[0]?.comments[0]?.anchor.live
      .host_instance_id,
  ).toBe(selection.anchor.live.host_instance_id);
  expect(workspaceStore().read(key)).toBeUndefined();
  expect(
    calls.some(
      (call) => call.method === "POST" && call.path.endsWith("/messages"),
    ),
  ).toBe(false);
  act(() => saveUnfinished("c1", selection, "下一份输入"));
  expect(workspaceStore().read(key)).toBeDefined();
});
function fixture(response: () => Response | Promise<Response>) {
  return renderSettings(
    "/o/o1/chat/c1",
    chatBase((call) => {
      if (call.path === `${ORG}/conversations/c1`)
        return json(200, summary("c1"));
      if (call.path === `${ORG}/conversations/c1/messages`) {
        if (call.method === "POST") return response();
        return json(200, { messages: [], next_cursor: 0 });
      }
      return undefined;
    }),
  );
}
it("keeps annotations on send failure and sends only explicit snapshots", async () => {
  const calls = fixture(() =>
    json(500, { error: { code: "FAILED", message: "failed" } }),
  );
  await waitFor(() =>
    expect(screen.getByRole("textbox", { name: "输入消息" })).toBeEnabled(),
  );
  act(() => {
    expect(addBrowserComment("c1", source, comment())).toBe(true);
  });
  expect(
    calls.filter((c) => c.method === "POST" && c.path.endsWith("/messages")),
  ).toHaveLength(0);
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  await waitFor(() =>
    expect(
      calls.filter((c) => c.method === "POST" && c.path.endsWith("/messages")),
    ).toHaveLength(1),
  );
  const sent = calls.find(
    (c) => c.method === "POST" && c.path.endsWith("/messages"),
  )!.body as MessageCreateInput;
  expect(sent.browser_contexts?.[0]?.comments[0]?.anchor.snapshot.quote).toBe(
    "Selected heading",
  );
  expect(readDraft("c1")?.browserContexts?.[0]?.comments).toHaveLength(1);
  expect(sent).not.toHaveProperty("browser_grant");
});
it("clears only the sent annotation snapshot, retaining feedback added in flight", async () => {
  let resolve!: (value: Response) => void;
  const held = new Promise<Response>((r) => {
    resolve = r;
  });
  const calls = fixture(() => held);
  await waitFor(() =>
    expect(screen.getByRole("textbox", { name: "输入消息" })).toBeEnabled(),
  );
  act(() => {
    addBrowserComment("c1", source, comment("first"));
  });
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  await waitFor(() =>
    expect(
      calls.some((c) => c.method === "POST" && c.path.endsWith("/messages")),
    ).toBe(true),
  );
  act(() => {
    addBrowserComment("c1", source, comment("added while sending"));
  });
  await act(async () => {
    resolve(json(201, message(1, { body: "review" })));
    await held;
  });
  await waitFor(() =>
    expect(
      readDraft("c1")?.browserContexts?.flatMap((c) =>
        c.comments.map((a) => a.body),
      ),
    ).toEqual(["added while sending"]),
  );
});
it("isolates conversations, retains ordinary drafts, and clears references on account reset", () => {
  saveDraft("c1", { body: "existing", picked: [], files: [] });
  addBrowserComment("c1", source, comment());
  expect(readDraft("c1")?.body).toBe("existing");
  expect(readDraft("c2")).toBeUndefined();
  resetDrafts();
  expect(readDraft("c1")).toBeUndefined();
});
