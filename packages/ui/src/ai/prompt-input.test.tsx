import { createRef, useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  PromptInput,
  PromptInputActions,
  PromptInputTextarea,
} from "./prompt-input";

afterEach(() => vi.unstubAllGlobals());

describe("prompt-kit controlled input adapter", () => {
  it("forwards refs and controlled updates without owning keyboard submission", async () => {
    const textarea = createRef<HTMLTextAreaElement>();
    const anchor = createRef<HTMLDivElement>();
    const change = vi.fn();
    const keydown = vi.fn();
    function Input() {
      const [value, setValue] = useState("");
      return (
        <PromptInput ref={anchor} value={value} onValueChange={setValue}>
          <PromptInputTextarea
            aria-label="Message"
            ref={textarea}
            onChange={change}
            onKeyDown={keydown}
          />
          <PromptInputActions>
            <button>Menu</button>
          </PromptInputActions>
        </PromptInput>
      );
    }
    render(<Input />);
    await userEvent.type(screen.getByRole("textbox"), "hello{Enter}world");
    expect(textarea.current).toHaveValue("hello\nworld");
    expect(anchor.current).toHaveAttribute("data-slot", "prompt-input");
    expect(change).toHaveBeenCalledTimes(11);
    expect(keydown).toHaveBeenCalledTimes(11);
    await userEvent.click(screen.getByRole("button", { name: "Menu" }));
    expect(screen.getByRole("button", { name: "Menu" })).toHaveFocus();
  });

  it("does not consume IME Enter and carries disabled state", () => {
    const keydown = vi.fn();
    const { rerender } = render(
      <PromptInput value="中文" onValueChange={vi.fn()}>
        <PromptInputTextarea onKeyDown={keydown} />
      </PromptInput>,
    );
    const textarea = screen.getByRole("textbox");
    expect(
      fireEvent.keyDown(textarea, { key: "Enter", isComposing: true }),
    ).toBe(true);
    expect(keydown.mock.calls[0]![0].nativeEvent.isComposing).toBe(true);
    rerender(
      <PromptInput value="中文" onValueChange={vi.fn()} disabled>
        <PromptInputTextarea />
      </PromptInput>,
    );
    expect(textarea).toBeDisabled();
  });

  it("caps height, resets when emptied and remeasures a narrower panel", () => {
    let scrollHeight = 480;
    let clientWidth = 600;
    let resized = () => {};
    const disconnect = vi.fn();
    vi.spyOn(
      HTMLTextAreaElement.prototype,
      "scrollHeight",
      "get",
    ).mockImplementation(() => scrollHeight);
    vi.spyOn(
      HTMLTextAreaElement.prototype,
      "clientWidth",
      "get",
    ).mockImplementation(() => clientWidth);
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(callback: () => void) {
          resized = callback;
        }
        observe() {}
        disconnect = disconnect;
      },
    );
    const { rerender, unmount } = render(
      <PromptInput value="long" onValueChange={vi.fn()}>
        <PromptInputTextarea />
      </PromptInput>,
    );
    const textarea = screen.getByRole("textbox");
    expect(textarea.style.height).toBe("240px");
    scrollHeight = 24;
    rerender(
      <PromptInput value="" onValueChange={vi.fn()}>
        <PromptInputTextarea />
      </PromptInput>,
    );
    expect(textarea.style.height).toBe("24px");
    scrollHeight = 120;
    clientWidth = 300;
    resized();
    expect(textarea.style.height).toBe("120px");
    unmount();
    expect(disconnect).toHaveBeenCalled();
  });
});
