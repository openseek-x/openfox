import { useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";

// 产物面板的地址参数：?artifact=<产物 ID> 打开面板并选中该产物；?artifact=list 打开列表标签；
// 没有参数表示面板关闭。刷新后按地址恢复（只恢复选中的那一个标签）。

export const ARTIFACT_PARAM = "artifact";
export const ARTIFACT_LIST = "list";

export interface ArtifactPanelState {
  /** 面板是否打开 */
  open: boolean;
  /** 当前选中：产物 ID 或 ARTIFACT_LIST */
  selected: string | null;
  /** 已打开的文件标签（产物 ID，按打开顺序） */
  tabs: string[];
  openArtifact: (id: string) => void;
  openList: () => void;
  select: (id: string) => void;
  closeTab: (id: string) => void;
  closePanel: () => void;
  /** 地址里的产物在本会话不存在：静默移除 */
  forget: (id: string) => void;
}

/** 会话「更多」菜单的「产物」：打开面板的列表标签（保留地址里的其他参数）。 */
export function useOpenArtifactList(): () => void {
  const [, setSearchParams] = useSearchParams();
  return useCallback(
    () =>
      setSearchParams((current) => {
        const next = new URLSearchParams(current);
        next.set(ARTIFACT_PARAM, ARTIFACT_LIST);
        return next;
      }),
    [setSearchParams],
  );
}

export function useArtifactPanel(
  conversationId: string | null,
): ArtifactPanelState {
  const [searchParams, setSearchParams] = useSearchParams();
  const selected = searchParams.get(ARTIFACT_PARAM);
  const [tabs, setTabs] = useState<string[]>([]);

  // 切换会话：清空已打开的标签（地址参数由新会话自己的产物列表校验）
  useEffect(() => {
    setTabs([]);
  }, [conversationId]);

  // 地址里选中的产物总在标签里（刷新恢复、外部链接打开）
  useEffect(() => {
    if (!selected || selected === ARTIFACT_LIST) return;
    setTabs((current) =>
      current.includes(selected) ? current : [...current, selected],
    );
  }, [selected]);

  const setSelected = useCallback(
    (value: string | null, replace = false) =>
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current);
          if (value) next.set(ARTIFACT_PARAM, value);
          else next.delete(ARTIFACT_PARAM);
          return next;
        },
        { replace },
      ),
    [setSearchParams],
  );

  const closeTab = useCallback(
    (id: string) => {
      const index = tabs.indexOf(id);
      const rest = tabs.filter((tab) => tab !== id);
      setTabs(rest);
      if (selected === id)
        setSelected(rest[Math.max(0, index - 1)] ?? ARTIFACT_LIST, true);
    },
    [tabs, selected, setSelected],
  );

  return useMemo(
    () => ({
      open: Boolean(selected),
      selected,
      tabs,
      openArtifact: (id: string) => setSelected(id),
      openList: () => setSelected(ARTIFACT_LIST),
      select: (id: string) => setSelected(id, true),
      closeTab,
      closePanel: () => {
        setTabs([]);
        setSelected(null);
      },
      forget: (id: string) => {
        setTabs((current) => current.filter((tab) => tab !== id));
        if (selected === id) setSelected(null, true);
      },
    }),
    [selected, tabs, setSelected, closeTab],
  );
}
