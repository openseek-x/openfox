import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";
import { ChatMarkdown } from "./ChatMarkdown";

const markdown = (source: string) => (
  <I18nProvider initialLocale="en-US">
    <ToastProvider>
      <ChatMarkdown source={source} />
    </ToastProvider>
  </I18nProvider>
);

describe("chat-only prompt-kit Markdown", () => {
  it("does not report an old clipboard request as copying updated streaming code", async () => {
    const user = userEvent.setup();
    let finishCopy!: () => void;
    vi.spyOn(navigator.clipboard, "writeText").mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          finishCopy = resolve;
        }),
    );
    const { rerender } = render(markdown("```text\nold\n```"));
    await user.click(screen.getByRole("button", { name: "Copy" }));
    rerender(markdown("```text\nnew\n```"));
    await act(async () => finishCopy());
    expect(screen.getByRole("button", { name: "Copy" })).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Copied" }),
    ).not.toBeInTheDocument();
  });

  it("isolates footnote navigation and accessible labels between messages", async () => {
    const user = userEvent.setup();
    const scroll = vi.fn();
    const source =
      "Claim[^note] and again[^note].\n\n[^note]: A source.\n\n[blocked](#outside)";
    const { container } = render(
      <>
        {markdown(source)}
        {markdown(source)}
      </>,
    );
    const references = container.querySelectorAll<HTMLAnchorElement>(
      "a[data-footnote-ref]",
    );
    expect(references).toHaveLength(4);
    const ids = [...container.querySelectorAll("[id]")].map((node) => node.id);
    expect(new Set(ids).size).toBe(ids.length);
    for (const reference of references) {
      expect(reference).not.toHaveAttribute("target");
      const target = document.getElementById(reference.hash.slice(1));
      expect(target).toHaveTextContent("A source.");
      expect(
        document.getElementById(reference.getAttribute("aria-describedby")!),
      ).toHaveTextContent("Footnotes");
      target!.scrollIntoView = scroll;
      await user.click(reference);
    }
    expect(scroll).toHaveBeenCalledTimes(4);
    expect(window.location.hash).toBe("");
    const backs = container.querySelectorAll<HTMLAnchorElement>(
      "a[data-footnote-backref]",
    );
    expect(backs).toHaveLength(4);
    for (const back of backs)
      expect(document.getElementById(back.hash.slice(1))).toHaveAttribute(
        "data-footnote-ref",
      );
    expect(
      screen.queryByRole("link", { name: "blocked" }),
    ).not.toBeInTheDocument();
  });

  it("ignores a pending copy result after the code block unmounts", async () => {
    const user = userEvent.setup();
    let finishCopy!: () => void;
    vi.spyOn(navigator.clipboard, "writeText").mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          finishCopy = resolve;
        }),
    );
    const { unmount } = render(markdown("```text\nremoved\n```"));
    await user.click(screen.getByRole("button", { name: "Copy" }));
    unmount();
    const schedule = vi.spyOn(globalThis, "setTimeout");
    await act(async () => finishCopy());
    expect(schedule.mock.calls.some(([, delay]) => delay === 1500)).toBe(false);
  });

  it("renders GFM tables, task lists, strikeout, nesting and soft line breaks", () => {
    const { container } = render(
      markdown(
        "| Name | State |\n| --- | --- |\n| Parser | Ready |\n\n- [x] done\n- [ ] pending\n\n~~old~~\nnext\n\n1. parent\n   - child",
      ),
    );
    expect(screen.getByRole("table")).toHaveTextContent("ParserReady");
    expect(screen.getAllByRole("checkbox")[0]).toBeChecked();
    expect(screen.getAllByRole("checkbox")[1]).toBeDisabled();
    expect(container.querySelector("del")).toHaveTextContent("old");
    expect(container.querySelector("br")).not.toBeNull();
    expect(container.querySelector("ol li ul li")).toHaveTextContent("child");
  });

  it("resolves reference links across blocks without allowing unsafe destinations or tracking images", () => {
    const { container } = render(
      markdown(
        "[Docs][reference]\n\nAnother paragraph.\n\n[reference]: https://example.com/docs\n\n[bad](javascript:alert%281%29) [data](data:text/html,test) [relative](/private) [mail](mailto:person@example.com)\n\n![tracking](https://example.com/track.png)\n\n<script>alert(1)</script>\n<img src=x onerror=alert(1)>",
      ),
    );
    expect(screen.getByRole("link", { name: "Docs" })).toHaveAttribute(
      "href",
      "https://example.com/docs",
    );
    expect(screen.getByRole("link", { name: "Docs" })).toHaveAttribute(
      "rel",
      "noopener noreferrer",
    );
    expect(screen.getByRole("link", { name: "mail" })).toHaveAttribute(
      "href",
      "mailto:person@example.com",
    );
    expect(screen.getAllByRole("link")).toHaveLength(2);
    expect(container.querySelector("script, img, iframe")).toBeNull();
    expect(screen.getByText("tracking")).toBeInTheDocument();
  });

  it("renders and copies incomplete fenced code as plain text, then updates streaming content", async () => {
    const user = userEvent.setup();
    const write = vi
      .spyOn(navigator.clipboard, "writeText")
      .mockResolvedValue();
    const { container, rerender } = render(
      markdown("```made-up-language\nconst value = '<script>';"),
    );
    expect(container.querySelector("pre code")).toHaveTextContent(
      "const value = '<script>';",
    );
    expect(screen.getByText("made-up-language")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Copy" }));
    expect(write).toHaveBeenCalledWith("const value = '<script>';");
    expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument();
    rerender(
      markdown(
        "```made-up-language\nconst value = '<script>';\nnext();\n```\n\nFinished",
      ),
    );
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Copy" })).toBeInTheDocument(),
    );
    expect(container.querySelector("pre code")).toHaveTextContent("next();");
    expect(container.querySelector("script")).toBeNull();
    expect(screen.getByText("Finished")).toBeInTheDocument();
  });

  it("keeps inline code inline and supports indented blocks without language", () => {
    const { container } = render(
      markdown("Use `inline` here.\n\n    printf('hello')"),
    );
    expect(container.querySelector("p code")).toHaveTextContent("inline");
    expect(container.querySelectorAll('[data-slot="code-block"]')).toHaveLength(
      1,
    );
    expect(container.querySelector("pre code")).toHaveTextContent(
      "printf('hello')",
    );
  });

  it("shows a copy failure without reporting success", async () => {
    const user = userEvent.setup();
    vi.spyOn(navigator.clipboard, "writeText").mockRejectedValue(
      new Error("denied"),
    );
    render(markdown("```text\ncopy me\n```"));
    await user.click(screen.getByRole("button", { name: "Copy" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Copy failed");
    expect(
      screen.queryByRole("button", { name: "Copied" }),
    ).not.toBeInTheDocument();
  });
});
