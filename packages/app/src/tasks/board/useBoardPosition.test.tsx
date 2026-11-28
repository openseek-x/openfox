import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { useBoardPosition } from "./useBoardPosition";

function Board({ title }: { title: boolean }) {
  const ref = useBoardPosition("first-column");
  return (
    <div ref={ref} aria-label="看板" role="region">
      <section>
        <h3 id="first-column">待开始</h3>
        {title && <span data-board-title>任务标题</span>}
      </section>
    </div>
  );
}

function setup(path: string) {
  const view = (title: boolean) => (
    <MemoryRouter initialEntries={[path]}>
      <Board title={title} />
    </MemoryRouter>
  );
  const result = render(view(false));
  const board = screen.getByRole("region");
  const column = screen.getByRole("heading").parentElement!;
  vi.spyOn(board, "getBoundingClientRect").mockReturnValue(
    new DOMRect(0, 0, 600, 400),
  );
  vi.spyOn(column, "getBoundingClientRect").mockReturnValue(
    new DOMRect(700, 0, 280, 400),
  );
  const load = () => {
    result.rerender(view(true));
    const title = screen.getByText("任务标题");
    title.style.fontSize = "14px";
    vi.spyOn(title, "getBoundingClientRect").mockReturnValue(
      new DOMRect(730, 0, 140, 20),
    );
  };
  return { board, load };
}

describe("看板初始横向位置", () => {
  it("等待首个有任务的列加载，只滚动看板", async () => {
    const { board, load } = setup("/initial");
    load();
    await waitFor(() => expect(board.scrollLeft).toBe(700));
    expect(document.activeElement).toBe(document.body);
  });

  it("等待数据期间用户已操作时保留位置", async () => {
    const { board, load } = setup("/manual");
    fireEvent.pointerDown(board);
    load();
    await Promise.resolve();
    expect(board.scrollLeft).toBe(0);
  });
});
