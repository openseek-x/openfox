import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { I18nProvider } from "@openfox/i18n";
import { createApi, HttpClient, type AnchorResolution } from "@openfox/api";
import { ApiProvider } from "../../context";
import type { BrowserSelection } from "../../desktop";
import type { BrowserSession } from "./useBrowserSession";
import { BrowserAnnotationEditor } from "./BrowserAnnotationEditor";
import { workspaceStore } from "../workspaceStorage";
import { resetDrafts, readDraft } from "../drafts";
import {
  rebindKey,
  resolutionKey,
  type Rebind,
} from "../comments/resolutionState";

beforeEach(resetDrafts);
it("requires explicit confirmation, retries the same capture and never overwrites the original or starts chat", async () => {
  const selection: BrowserSelection = {
    id: crypto.randomUUID(),
    tabId: "tab",
    source_url: "https://example.com/new",
    title: "New",
    captured_at: "2026-11-17T00:00:00Z",
    anchor: {
      kind: "element",
      live: {
        host_instance_id: crypto.randomUUID(),
        tab_id: crypto.randomUUID(),
        frame_id: "child",
        document_generation: 2,
        ref: crypto.randomUUID(),
      },
      snapshot: {
        role: "heading",
        name: "Title",
        quote: "New title",
        prefix: "",
        suffix: "",
        selector_hint: "h1",
      },
      visual: {
        width: 800,
        height: 600,
        scroll_x: 0,
        scroll_y: 0,
        rect: { x: 10, y: 20, width: 50, height: 30 },
      },
    },
  };
  const original = structuredClone(selection.anchor);
  original.live.document_generation = 1;
  original.live.frame_id = "main";
  const binding: Rebind = {
    id: crypto.randomUUID(),
    version: 3,
    source: {
      name: "Old",
      browser: {
        id: crypto.randomUUID(),
        title: "Old",
        source_url: "https://example.com/old",
        captured_at: selection.captured_at,
        comments: [
          { id: crypto.randomUUID(), body: "Review this", anchor: original },
        ],
      },
    },
  };
  const before = structuredClone(binding);
  workspaceStore().write(rebindKey("c1"), binding);
  const calls: { url: string; body: any }[] = [];
  let lose = true;
  const api = createApi(
    new HttpClient("/api", async (input, init) => {
      calls.push({ url: String(input), body: JSON.parse(String(init?.body)) });
      if (lose) {
        lose = false;
        return Response.json(
          { error: { code: "FAILED", message: "lost" } },
          { status: 500 },
        );
      }
      const body = calls.at(-1)!.body;
      return Response.json({
        id: "resolution",
        thread_id: binding.id,
        thread_version: 3,
        status: "exact",
        reason: "explicit_rebind",
        target: body.browser,
      });
    }),
  );
  const command = vi.fn().mockResolvedValue(undefined);
  render(
    <I18nProvider initialLocale="zh-CN">
      <ApiProvider api={api}>
        <BrowserAnnotationEditor
          selection={selection}
          session={
            {
              orgId: "o1",
              conversationId: "c1",
              command,
            } as unknown as BrowserSession
          }
        />
      </ApiProvider>
    </I18nProvider>,
  );
  expect(calls).toHaveLength(0);
  expect(screen.queryByRole("textbox")).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "确认绑定" }));
  await screen.findByRole("alert");
  expect(command).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "确认绑定" }));
  await waitFor(() =>
    expect(command).toHaveBeenCalledWith({ action: "annotation-cancel" }),
  );
  expect(calls).toHaveLength(2);
  expect(calls[0]).toEqual(calls[1]);
  const b = calls[0]!.body.browser.browser;
  expect(b.id).not.toBe(b.comments[0].id);
  expect(b.comments[0].anchor.live).toMatchObject({
    frame_id: "child",
    document_generation: 2,
  });
  expect(binding).toEqual(before);
  expect(workspaceStore().read(rebindKey("c1"))).toBeUndefined();
  expect(
    workspaceStore().read<AnchorResolution>(resolutionKey("c1", binding.id))
      ?.status,
  ).toBe("exact");
  expect(readDraft("c1")?.browserContexts ?? []).toHaveLength(0);
});
