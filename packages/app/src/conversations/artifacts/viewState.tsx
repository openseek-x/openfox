import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  type RefObject,
} from "react";
import { workspaceStore } from "../workspaceStorage";

export const PreviewStateKey = createContext("");
export function useViewState<T>(name: string, initial: T) {
  const scope = useContext(PreviewStateKey),
    store = workspaceStore();
  const key = `view:${scope}`;
  const state = useState<T>(() =>
    scope ? (store.read<Record<string, T>>(key)?.[name] ?? initial) : initial,
  );
  const [value] = state;
  useEffect(() => {
    if (scope) store.write(key, { ...store.read<object>(key), [name]: value });
  }, [store, key, name, scope, value]);
  return state;
}

/** Restore only when content has real dimensions; do not save the loading placeholder's zero scroll. */
export function usePreviewScroll(
  ref: RefObject<HTMLElement | null>,
  ready = true,
  page = "",
) {
  const scope = useContext(PreviewStateKey),
    store = workspaceStore();
  const key = `view:${scope}`,
    field = `scroll:${page}`;
  const restored = useRef("");
  useEffect(() => {
    const el = ref.current;
    if (!scope || !ready || !el) return;
    const position =
      store.read<Record<string, { x: number; y: number }>>(key)?.[field];
    if (restored.current !== key + field && position)
      el.scrollTo?.(position.x, position.y);
    restored.current = key + field;
    const save = () =>
      store.write(key, {
        ...store.read<object>(key),
        [field]: { x: el.scrollLeft, y: el.scrollTop },
      });
    el.addEventListener("scroll", save, { passive: true });
    return () => el.removeEventListener("scroll", save);
  }, [store, key, field, ready, scope, ref]);
}
