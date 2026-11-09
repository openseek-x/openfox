import { useEffect, useState, type RefObject } from "react";

/**
 * 容器是否窄于阈值：用 ResizeObserver 量容器宽度。
 * 没有 ResizeObserver（jsdom）、还没量到或量到 0（未布局）时按「宽」处理，保证宽布局是默认值。
 */
export function useIsNarrow(
  ref: RefObject<HTMLElement | null>,
  threshold: number,
): boolean {
  const [narrow, setNarrow] = useState(false);
  useEffect(() => {
    const node = ref.current;
    if (!node || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => {
      const width = entries[entries.length - 1]?.contentRect.width ?? 0;
      // 宽度为 0 说明还没布局（例如隐藏），保持当前判断
      if (width > 0) setNarrow(width < threshold);
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, [ref, threshold]);
  return narrow;
}
