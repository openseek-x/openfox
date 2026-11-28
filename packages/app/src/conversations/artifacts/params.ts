import { workspaceStore } from "../workspaceStorage";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";

// 阶段 10d · 产物面板的地址参数：?artifact=<产物 ID> 打开面板并选中该产物；?artifact=list 打开列表标签；
// 显式 URL 优先；否则按账号/组织/会话恢复标签顺序和选中项。

export const ARTIFACT_PARAM = "artifact";
export const ARTIFACT_LIST = "list";
export const BROWSER_PANEL = "browser";
export const NEW_PANEL = "new";
// Bare artifact IDs are fixed revisions. Only an explicit head key follows updates.
export const currentTabKey = (contentId: string) => `current:${contentId}`;
export const isCurrentTab = (id: string) => id.startsWith("current:");
export const browserTabKey = (id: string) => `browser:${id}`;
export const isBrowserTab = (id: string | null) =>
  Boolean(id?.startsWith("browser:"));
export const proposalTabKey = (id: string) => `proposal:${id}`;
export const isProposalTab = (id: string) => id.startsWith("proposal:");
export const documentTabKey = (space: string, node: string) =>
  `doc:${space}:${node}`;
export const documentTab = (id: string) => {
  const match = /^doc:([^:]+):([^:]+)$/.exec(id);
  return match ? { spaceId: match[1]!, nodeId: match[2]! } : null;
};
export const isArtifactTab = (id: string) =>
  ![ARTIFACT_LIST, NEW_PANEL, BROWSER_PANEL].includes(id) &&
  !isBrowserTab(id) &&
  !isProposalTab(id) &&
  !documentTab(id);

export interface ArtifactPanelState {
  /** 面板是否打开 */
  open: boolean;
  /** 当前选中：文件、网页、产物列表或新标签入口。 */
  selected: string | null;
  /** 已打开的工作区标签，按显示顺序。 */
  tabs: string[];
  openArtifact: (id: string) => void;
  openList: () => void;
  select: (id: string) => void;
  closeTab: (id: string) => void;
  closePanel: () => void;
  /** 地址里的产物在本会话不存在：静默移除 */
  forget: (id: string) => void;
  syncBrowserTabs: (ids: string[]) => void;
  reorder: (id: string, before: string) => void;
}

/** 会话「更多」菜单的「产物」：打开面板的列表标签（保留地址里的其他参数）。 */
export function useOpenArtifactList(selection = ARTIFACT_LIST): () => void {
  const [, setSearchParams] = useSearchParams();
  return useCallback(
    () =>
      setSearchParams((current) => {
        const next = new URLSearchParams(current);
        next.set(ARTIFACT_PARAM, selection);
        return next;
      }),
    [setSearchParams, selection],
  );
}

export function useArtifactPanel(
  conversationId: string | null,
): ArtifactPanelState {
  const [searchParams, setSearchParams] = useSearchParams();
  const store = workspaceStore();
  const key = `workspace:${conversationId ?? "new"}`;
  type Saved = { tabs: string[]; selected: string | null };
  const urlSelected = searchParams.get(ARTIFACT_PARAM);
  const [session, setSession] = useState(() => {
    const saved = store.read<Saved>(key);
    return {
      key,
      tabs: saved?.tabs ?? [],
      selected: urlSelected ?? saved?.selected ?? null,
    };
  });
  if (session.key !== key) {
    const saved = store.read<Saved>(key);
    setSession({
      key,
      tabs: saved?.tabs ?? [],
      selected: urlSelected ?? saved?.selected ?? null,
    });
  }
  const { tabs, selected } = session;
  const setTabs = useCallback(
    (change: string[] | ((tabs: string[]) => string[])) => {
      setSession((current) => ({
        ...current,
        tabs: typeof change === "function" ? change(current.tabs) : change,
      }));
    },
    [],
  );
  const previousURL = useRef({ key, value: urlSelected });
  useEffect(() => {
    const previous = previousURL.current;
    previousURL.current = { key, value: urlSelected };
    if (previous.key === key && previous.value !== urlSelected)
      setSession((current) =>
        current.selected === urlSelected
          ? current
          : { ...current, selected: urlSelected },
      );
  }, [key, urlSelected]);
  useEffect(() => {
    if (!conversationId || session.key !== key) return;
    // 未保存过且处于默认空态时不写入 IndexedDB，避免打开会话立即触发本地保存提示
    if (!store.read(key) && tabs.length === 0 && selected === null) return;
    store.write(key, { tabs, selected });
  }, [store, key, conversationId, session, tabs, selected]);

  // 地址里选中的产物总在标签里（刷新恢复、外部链接打开）
  useEffect(() => {
    if (!selected || selected === BROWSER_PANEL) return;
    setTabs((current) => {
      const retained =
        selected === NEW_PANEL
          ? current
          : current.filter((id) => id !== NEW_PANEL);
      return retained.includes(selected) ? retained : [...retained, selected];
    });
  }, [selected]);

  const setSelected = useCallback(
    (value: string | null, replace = false) => {
      setSession((current) => ({ ...current, selected: value }));
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current);
          if (value) next.set(ARTIFACT_PARAM, value);
          else next.delete(ARTIFACT_PARAM);
          return next;
        },
        { replace },
      );
    },
    [setSearchParams],
  );

  const closeTab = useCallback(
    (id: string) => {
      const index = tabs.indexOf(id);
      const rest = tabs.filter((tab) => tab !== id);
      setTabs(rest);
      if (selected === id)
        setSelected(rest[index] ?? rest[index - 1] ?? null, true);
    },
    [tabs, selected, setSelected],
  );

  useEffect(() => {
    const restore = (event: Event) => {
      const detail = (
        event as CustomEvent<{
          conversationId: string;
          mapping: Record<string, string>;
        }>
      ).detail;
      if (detail.conversationId !== conversationId) return;
      const map = (id: string) =>
        isBrowserTab(id) && detail.mapping[id.slice(8)]
          ? browserTabKey(detail.mapping[id.slice(8)]!)
          : id;
      setTabs((current) => current.map(map));
      if (selected && isBrowserTab(selected)) setSelected(map(selected), true);
    };
    window.addEventListener("openfox:browser-restored", restore);
    return () =>
      window.removeEventListener("openfox:browser-restored", restore);
  }, [conversationId, selected, setSelected, setTabs]);

  const syncBrowserTabs = useCallback((ids: string[]) => {
    const keys = ids.map(browserTabKey);
    setTabs((current) => {
      const retained = current.filter(
        (id) => !isBrowserTab(id) || keys.includes(id),
      );
      const next = [
        ...retained,
        ...keys.filter((id) => !retained.includes(id)),
      ];
      return next.length === current.length &&
        next.every((id, i) => id === current[i])
        ? current
        : next;
    });
  }, []);
  const reorder = useCallback((id: string, before: string) => {
    setTabs((current) => {
      if (id === before || !current.includes(id) || !current.includes(before))
        return current;
      const next = current.filter((value) => value !== id);
      next.splice(next.indexOf(before), 0, id);
      return next;
    });
  }, []);

  return useMemo(
    () => ({
      open: Boolean(selected),
      selected,
      tabs,
      openArtifact: (id: string) => setSelected(id),
      openList: () => setSelected(ARTIFACT_LIST),
      select: (id: string) => setSelected(id, true),
      closeTab,
      syncBrowserTabs,
      reorder,
      closePanel: () => {
        setSelected(null);
      },
      forget: (id: string) => {
        setTabs((current) => current.filter((tab) => tab !== id));
        if (selected === id) setSelected(null, true);
      },
    }),
    [selected, tabs, setSelected, closeTab, syncBrowserTabs, reorder],
  );
}
