import { useLayoutEffect, useRef } from "react";
import { useLocation } from "react-router-dom";

// 只记住本次会话最近访问的历史条目，返回看板时恢复手动滚动的位置。
const positions = new Map<string, number>();

export function useBoardPosition(firstColumn: string | undefined) {
  const board = useRef<HTMLDivElement>(null);
  const location = useLocation();
  const entry = location.key + location.pathname;
  const settled = useRef(false);

  useLayoutEffect(() => {
    const node = board.current;
    if (!node) return;
    const previous = positions.get(entry);
    if (previous !== undefined) {
      node.scrollLeft = previous;
      settled.current = true;
    }
    const stop = () => {
      settled.current = true;
    };
    const remember = () => {
      stop();
      positions.set(entry, node.scrollLeft);
      if (positions.size > 32) positions.delete(positions.keys().next().value!);
    };
    node.addEventListener("scroll", remember);
    node.addEventListener("pointerdown", stop);
    node.addEventListener("wheel", stop);
    node.addEventListener("focusin", stop);
    return () => {
      node.removeEventListener("scroll", remember);
      node.removeEventListener("pointerdown", stop);
      node.removeEventListener("wheel", stop);
      node.removeEventListener("focusin", stop);
    };
  }, [entry]);

  useLayoutEffect(() => {
    const node = board.current;
    if (!node || settled.current) return;
    if (!firstColumn) {
      settled.current = true;
      return;
    }
    // 首列数据按状态独立分页；等待其标题挂载，避免后到的请求改变定位。
    const column = document.getElementById(firstColumn)?.closest("section");
    if (!column) return;
    const position = () => {
      const title = column.querySelector("[data-board-title]");
      if (!title) return;
      observer.disconnect();
      if (settled.current) return;
      settled.current = true;
      const bounds = node.getBoundingClientRect();
      const rect = title.getBoundingClientRect();
      const readableWidth = Math.min(
        rect.width,
        parseFloat(getComputedStyle(title).fontSize),
      );
      if (
        Math.min(bounds.right, rect.right) - Math.max(bounds.left, rect.left) <
        readableWidth
      ) {
        node.scrollLeft +=
          column.getBoundingClientRect().left - bounds.left - node.clientLeft;
      }
    };
    const observer = new MutationObserver(position);
    observer.observe(column, { childList: true, subtree: true });
    position();
    return () => observer.disconnect();
  }, [firstColumn]);

  return board;
}
