import { useLayoutEffect, useState } from "react";

import { readLocal, writeLocal } from "../storage";

export function usePanelWidth({
  storageKey,
  initial,
  min,
  max,
  remaining,
}: {
  storageKey: string;
  initial: number;
  min: number;
  max: number;
  remaining: number;
}) {
  const [preferred, setPreferred] = useState(() => {
    const stored = Number(readLocal(storageKey));
    return Number.isFinite(stored) && stored >= min && stored <= max
      ? stored
      : initial;
  });
  const [container, containerRef] = useState<HTMLElement | null>(null);
  const [preview, setPreview] = useState<number | null>(null);
  const [available, setAvailable] = useState<number | null>(null);
  useLayoutEffect(() => {
    if (!container) return;
    const measure = () => setAvailable(container.clientWidth);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(container);
    return () => observer.disconnect();
  }, [container]);

  const limit =
    available === null
      ? max
      : Math.max(min, Math.min(max, available - remaining));
  const width = Math.min(preview ?? preferred, limit);
  const onWidthPreview = (next: number | null) =>
    setPreview(next === null ? null : Math.max(0, Math.min(limit, next)));
  const onWidthChange = (next: number) => {
    const value = Math.round(Math.max(min, Math.min(limit, next)));
    setPreferred(value);
    writeLocal(storageKey, String(value));
  };
  return {
    width,
    min,
    max: limit,
    resizing: preview !== null,
    onWidthPreview,
    onWidthChange,
    containerRef,
  };
}
