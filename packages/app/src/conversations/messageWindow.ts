import { useInfiniteQuery } from "@tanstack/react-query";
import { useLayoutEffect, useRef, type RefObject } from "react";
import { useLocation, useSearchParams } from "react-router-dom";

type Cursor = number | { around: string } | { after: number };
type Page<M> = { messages: M[]; next_cursor: number };

/** Pages run newest to oldest. From a search hit, the initial page may be in
 * the middle; fetching in either direction keeps the timeline contiguous. */
export function useMessagePages<M extends { seq: number }>(
  key: readonly unknown[],
  enabled: boolean,
  target: string | null,
  read: (cursor: Cursor) => Promise<Page<M>>,
) {
  return useInfiniteQuery({
    enabled,
    queryKey: target ? [...key, "around", target] : key,
    initialPageParam: (target ? { around: target } : 0) as Cursor,
    queryFn: ({ pageParam }) => read(pageParam),
    getNextPageParam: (last, _pages, cursor) =>
      (typeof cursor === "object" && "after" in cursor
        ? last.messages[0]?.seq
        : last.next_cursor) || undefined,
    getPreviousPageParam: (first, _pages, cursor) => {
      if (!target) return undefined;
      const after =
        typeof cursor === "object" && "after" in cursor
          ? first.next_cursor
          : first.messages.at(-1)?.seq;
      return after ? { after } : undefined;
    },
  });
}

export function useMessageTarget() {
  const [params, setParams] = useSearchParams();
  const location = useLocation();
  return {
    target: params.get("message"),
    leave: () =>
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          next.delete("message");
          return next;
        },
        { replace: true, state: location.state },
      ),
  };
}

/** Center exactly once per destination, after React has rendered its messages. */
export function useLocateMessage(
  target: string | null,
  container: RefObject<HTMLElement | null>,
  data: unknown,
  onFound: () => void,
) {
  const found = useRef<string | null>(null);
  useLayoutEffect(() => {
    if (!target) {
      found.current = null;
      return;
    }
    if (found.current === target || !data) return;
    const node = Array.from(
      container.current?.querySelectorAll<HTMLElement>("[data-message-id]") ??
        [],
    ).find((el) => el.dataset.messageId === target);
    if (node) {
      found.current = target;
      onFound();
      node.scrollIntoView?.({ block: "center" });
    }
  }, [target, container, data, onFound]);
}
