import { useCallback } from "react";
import {
  useLocation,
  useMatch,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";

import { moduleOf, withoutParam } from "../shell/navMemory";

/** 路由 state：本应用从列表 push 出来的抽屉历史项，关闭时可直接后退。 */
export interface TaskDrawerState {
  taskDrawer: true;
}

/**
 * 路由 state：任务全页是怎么进来的。
 * from 是进入前的页面（不含 ?task=）；back 是回到该页面要后退的历史条数：
 * 2 = 从本应用 push 的抽屉展开（抽屉项 + 全页项）；1 = 直接 push 到全页；0 = 没有可后退的项（replace 回去）。
 * task 是进入时的任务编号，用于判断页内是否已切到别的任务。
 */
export interface TaskPageState {
  taskPage: { from: string; back: 0 | 1 | 2; task: string };
}

export const DRAWER_STATE: TaskDrawerState = { taskDrawer: true };

const PAGE_PATTERN = "/o/:orgId/tasks/:displayId";

/** 任务全页地址；conversationId 非空时带上 AI 面板参数。 */
export function taskPagePath(
  orgId: string,
  displayId: string,
  conversationId?: string | null,
): string {
  const base = `/o/${orgId}/tasks/${encodeURIComponent(displayId.toUpperCase())}`;
  return conversationId
    ? `${base}?view=chat&conversationId=${encodeURIComponent(conversationId)}`
    : base;
}

export function isDrawerState(state: unknown): boolean {
  return Boolean((state as Partial<TaskDrawerState> | null)?.taskDrawer);
}

function pageState(state: unknown): TaskPageState["taskPage"] | null {
  const value = (state as Partial<TaskPageState> | null)?.taskPage;
  return value && typeof value.from === "string" ? value : null;
}

/** 当前是否在任务全页，返回其地址里的任务编号。 */
export function useTaskPageId(): string | null {
  const match = useMatch(PAGE_PATTERN);
  const id = match?.params.displayId;
  return id ? decodeURIComponent(id).toUpperCase() : null;
}

/** 给地址加上 ?task=（保留已有查询串）。 */
function withTask(target: string, displayId: string): string {
  const url = new URL(target, "http://nav.invalid");
  url.searchParams.set("task", displayId.toUpperCase());
  return url.pathname + url.search;
}

/**
 * 打开任务全页：
 * - 已在全页：replace（不增加历史条数，保留进入方式）；
 * - 在 push 出来的抽屉里：push，关闭全页要后退两条；
 * - 其他：push 一条（深链接进入的抽屉 back=0，关闭时 replace 回去）。
 */
export function useOpenTaskPage() {
  const { orgId = "" } = useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const onPage = useTaskPageId();
  return useCallback(
    (displayId: string, conversationId?: string | null) => {
      let target = taskPagePath(orgId, displayId, conversationId);
      if (moduleOf(orgId, location.pathname, location.search) === "chat")
        target += `${conversationId ? "&" : "?"}rail=chat`;
      if (onPage) {
        navigate(target, { replace: true, state: location.state });
        return;
      }
      const params = new URLSearchParams(location.search);
      const hasDrawer = params.has("task");
      const state: TaskPageState = {
        taskPage: {
          from: withoutParam(location.pathname, location.search, "task"),
          back: hasDrawer ? (isDrawerState(location.state) ? 2 : 0) : 1,
          task: displayId.toUpperCase(),
        },
      };
      navigate(target, { state });
    },
    [orgId, onPage, location, navigate],
  );
}

/** 全页的关闭与还原抽屉。fallback 是没有进入前页面时的任务所在空间任务页。 */
export function useTaskPageExit(fallback: string) {
  const location = useLocation();
  const navigate = useNavigate();
  const entry = pageState(location.state);
  const close = useCallback(() => {
    if (!entry) navigate(fallback, { replace: true });
    else if (entry.back > 0) navigate(-entry.back);
    else navigate(entry.from, { replace: true });
  }, [entry, fallback, navigate]);
  const restore = useCallback(
    (displayId: string) => {
      const id = displayId.toUpperCase();
      if (entry?.back === 2 && entry.task === id) {
        // 后退一条正好是进入时的抽屉项
        navigate(-1);
        return;
      }
      navigate(withTask(entry?.from ?? fallback, id), {
        replace: true,
        // 全页是直接 push 出来的：它前一条就是 from，抽屉关闭时可后退
        state: entry?.back === 1 ? DRAWER_STATE : undefined,
      });
    },
    [entry, fallback, navigate],
  );
  return { close, restore };
}

/** 全页地址里的 AI 面板参数：?view=chat&conversationId=<id>。 */
export function useTaskPanel() {
  const [params, setParams] = useSearchParams();
  const location = useLocation();
  const conversationId =
    params.get("view") === "chat" ? params.get("conversationId") : null;
  const open = useCallback(
    (id: string) =>
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          next.set("view", "chat");
          if (next.get("conversationId") !== id) next.delete("message");
          next.set("conversationId", id);
          return next;
        },
        // 换会话 / 开面板都用 replace：历史条数不变，全页的关闭与后退规则不受影响
        { replace: true, state: location.state },
      ),
    [setParams, location.state],
  );
  const close = useCallback(
    () =>
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          next.delete("view");
          next.delete("conversationId");
          next.delete("message");
          return next;
        },
        { replace: true, state: location.state },
      ),
    [setParams, location.state],
  );
  return { conversationId, open, close };
}
