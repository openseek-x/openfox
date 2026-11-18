import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { I18nProvider } from "@openfox/i18n";
import type { BrowserSelection } from "../../desktop";
import { readDraft } from "../drafts";
import { BrowserAnnotationEditor } from "./BrowserAnnotationEditor";
import type { BrowserSession } from "./useBrowserSession";

beforeEach(() =>
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  ),
);
afterEach(() => vi.unstubAllGlobals());

function fixture(id = "selected-element", generation = 1) {
  const selection: BrowserSelection = {
    id,
    tabId: "tab",
    source_url: "https://example.com/",
    title: "Example",
    captured_at: "2026-11-17T00:00:00Z",
    anchor: {
      kind: "element",
      live: {
        host_instance_id: "host",
        tab_id: "tab",
        frame_id: "frame",
        document_generation: generation,
        ref: "element",
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
        rect: { x: 20, y: 100, width: 300, height: 40 },
      },
    },
  };
  const command = vi.fn().mockResolvedValue(undefined);
  const view = render(
    <I18nProvider initialLocale="zh-CN">
      <BrowserAnnotationEditor
        selection={selection}
        session={
          {
            conversationId: "conversation",
            command,
          } as unknown as BrowserSession
        }
      />
    </I18nProvider>,
  );
  return {
    unmount: view.unmount,
    command,
    input: screen.getByRole("textbox", { name: "写下你的反馈…" }),
    submit: screen.getByRole("button", { name: "添加到聊天" }),
  };
}

it("starts compact and adds feedback on Enter without sending a chat message", async () => {
  const { input, submit, command } = fixture();
  expect(input).toHaveAttribute("rows", "1");
  expect(input).toHaveAttribute("placeholder", "添加评论…");
  await waitFor(() => expect(input).toHaveFocus());
  expect(submit).toBeDisabled();
  const user = userEvent.setup();
  await user.type(input, "修改标题{Enter}");
  expect(
    readDraft("conversation")?.browserContexts?.[0]?.comments[0]?.body,
  ).toBe("修改标题");
  expect(command).toHaveBeenCalledExactlyOnceWith({
    action: "annotation-cancel",
  });
});

it("preserves multiline text and IME confirmation, and restores a closed draft for the same document", async () => {
  const { input, command, unmount } = fixture();
  const user = userEvent.setup();
  await user.type(input, "第一行{Shift>}{Enter}{/Shift}第二行");
  expect(input).toHaveValue("第一行\n第二行");
  fireEvent.keyDown(input, { key: "Enter", isComposing: true });
  fireEvent.keyDown(input, { key: "Enter", keyCode: 229 });
  expect(command).not.toHaveBeenCalled();
  expect(readDraft("conversation")).toBeUndefined();
  await user.keyboard("{Escape}");
  expect(command).toHaveBeenCalledExactlyOnceWith({
    action: "annotation-cancel",
  });
  unmount();
  const restored = fixture("new-capture-same-element");
  expect(restored.input).toHaveValue("第一行\n第二行");
  restored.unmount();
  expect(fixture("new-document", 2).input).toHaveValue("");
});

it("retains over-limit feedback for correction and rejects whitespace", async () => {
  const { input, submit, command } = fixture();
  fireEvent.change(input, { target: { value: "   " } });
  expect(submit).toBeDisabled();
  fireEvent.keyDown(input, { key: "Enter" });
  expect(command).not.toHaveBeenCalled();
  fireEvent.change(input, { target: { value: "字".repeat(3000) } });
  await userEvent.click(submit);
  expect(screen.getByRole("alert")).toHaveTextContent("批注过长");
  expect(input).toHaveValue("字".repeat(3000));
  expect(command).not.toHaveBeenCalled();
  fireEvent.change(input, { target: { value: "精简反馈" } });
  await userEvent.click(submit);
  expect(
    readDraft("conversation")?.browserContexts?.[0]?.comments[0]?.body,
  ).toBe("精简反馈");
});
