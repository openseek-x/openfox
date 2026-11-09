import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
} from "react";

// 任务多选：列表与表格共用同一套选择逻辑（勾选、Shift 连选、全选、清空）。

export interface TaskSelection {
  selected: ReadonlySet<string>;
  size: number;
  has: (id: string) => boolean;
  /**
   * 勾选 / 取消一个任务。range 为 true（按住 Shift）且 order 同时包含上一次点击的任务时，
   * 把两者之间（含两端）的任务一起设为 on。
   */
  toggle: (
    id: string,
    on: boolean,
    options?: { range?: boolean; order?: readonly string[] },
  ) => void;
  /** 把一组任务全部设为选中 / 未选中（表头全选） */
  setMany: (ids: readonly string[], on: boolean) => void;
  clear: () => void;
}

export function useTaskSelection(): TaskSelection {
  const [selected, setSelected] = useState<ReadonlySet<string>>(
    () => new Set(),
  );
  const anchor = useRef<string | null>(null);
  const toggle = useCallback<TaskSelection["toggle"]>((id, on, options) => {
    const order = options?.order;
    const from = anchor.current;
    anchor.current = id;
    setSelected((prev) => {
      const next = new Set(prev);
      let ids = [id];
      if (options?.range && order && from && from !== id) {
        const a = order.indexOf(from);
        const b = order.indexOf(id);
        if (a >= 0 && b >= 0)
          ids = order.slice(Math.min(a, b), Math.max(a, b) + 1);
      }
      for (const item of ids) {
        if (on) next.add(item);
        else next.delete(item);
      }
      return next;
    });
  }, []);
  const setMany = useCallback((ids: readonly string[], on: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      for (const id of ids) {
        if (on) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  }, []);
  const clear = useCallback(() => {
    anchor.current = null;
    setSelected(new Set());
  }, []);
  return useMemo(
    () => ({
      selected,
      size: selected.size,
      has: (id: string) => selected.has(id),
      toggle,
      setMany,
      clear,
    }),
    [selected, toggle, setMany, clear],
  );
}

/** 列表的分组 / 行组件通过上下文拿到选择状态（避免逐层透传）。 */
export const TaskSelectionContext = createContext<TaskSelection | null>(null);

export const useSelectionContext = () => useContext(TaskSelectionContext);
