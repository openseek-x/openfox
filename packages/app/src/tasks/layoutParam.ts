import { useCallback } from "react";
import { useLocation, useSearchParams } from "react-router-dom";

import type { Layout } from "./model";

// 布局写进地址 ?view=board|list|table|gantt，打开链接时优先于本机设置与视图默认。

const LAYOUTS: Layout[] = ["board", "list", "table", "gantt"];

/** 地址参数里的布局；没有或非法时为 null。 */
export function parseLayoutParam(value: string | null): Layout | null {
  return LAYOUTS.includes(value as Layout) ? (value as Layout) : null;
}

/** 读写 ?view=：写入用 replace，不新增历史；保留其他参数与历史状态（例如详情抽屉标记）。 */
export function useLayoutParam() {
  const [params, setParams] = useSearchParams();
  const location = useLocation();
  const layout = parseLayoutParam(params.get("view"));
  const setLayout = useCallback(
    (next: Layout | null) =>
      setParams(
        (prev) => {
          const out = new URLSearchParams(prev);
          if (next) out.set("view", next);
          else out.delete("view");
          return out;
        },
        { replace: true, state: location.state },
      ),
    [setParams, location.state],
  );
  return { layout, setLayout };
}
