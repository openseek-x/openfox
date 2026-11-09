/** 只属于本机的界面偏好与引导进度。读写失败（隐私模式等）时退回默认值。 */
export function readLocal(key: string): string | null {
  try {
    return globalThis.localStorage?.getItem(key) ?? null;
  } catch {
    return null;
  }
}

export function writeLocal(key: string, value: string | null): void {
  try {
    if (value === null) globalThis.localStorage?.removeItem(key);
    else globalThis.localStorage?.setItem(key, value);
  } catch {
    // 忽略：只影响本机偏好
  }
}

/** 只在当前窗口存活的状态（sessionStorage）：刷新保留，关闭窗口即清空。不可用时退回默认值。 */
export function readSession(key: string): string | null {
  try {
    return globalThis.sessionStorage?.getItem(key) ?? null;
  } catch {
    return null;
  }
}

export function writeSession(key: string, value: string | null): void {
  try {
    if (value === null) globalThis.sessionStorage?.removeItem(key);
    else globalThis.sessionStorage?.setItem(key, value);
  } catch {
    // 忽略：只影响本次打开期间的导航记忆
  }
}

/** 删除所有以 prefix 开头的 sessionStorage 记录。 */
export function clearSessionPrefix(prefix: string): void {
  try {
    const store = globalThis.sessionStorage;
    if (!store) return;
    const doomed: string[] = [];
    for (let i = 0; i < store.length; i += 1) {
      const key = store.key(i);
      if (key?.startsWith(prefix)) doomed.push(key);
    }
    doomed.forEach((key) => store.removeItem(key));
  } catch {
    // 忽略
  }
}

export const localKeys = {
  lastOrg: "openfox:last-org",
  sidebarCollapsed: "openfox:sidebar-collapsed",
  sidebarWidth: "openfox:chat-sidebar-width",
  artifactPanelWidth: "openfox:artifact-panel-width",
  taskAiPanelWidth: "openfox:task-ai-panel-width",
  /** 主导航布局模式：缺省为任务优先，值为 chat 时对话优先；按设备存，不按组织 */
  layoutMode: "openfox:layout-mode",
  onboarding: (orgId: string) => `openfox:onboarding:${orgId}`,
  /** 任务标签页折叠的分组 id（JSON 数组），按组织存 */
  labelGroupsCollapsed: (orgId: string) =>
    `openfox:label-groups-collapsed:${orgId}`,
  /** 对话页签导航模式：recents / organized / sessions */
  chatMode: "openfox:chat-mode",
  /** Slack 模式「直接对话」里被移除的 Agent id（JSON 数组），按组织存 */
  chatHiddenAgents: (orgId: string) => `openfox:chat-hidden-agents:${orgId}`,
  /** 主题缓存（system / light / dark），在偏好加载前决定首屏配色 */
  theme: "openfox:theme",
  /** 隐藏侧栏底部「进阶指引」入口（值为 1 时隐藏），只影响本机显示 */
  advancedGuideHidden: "openfox:advanced-guide-hidden",
  /** 侧栏「进阶指引」是否就地展开（值为 1 时展开） */
  advancedGuideOpen: "openfox:advanced-guide-open",
  /** 文档库二级导航宽度（240–480，默认 300）与折叠状态（1 为折叠） */
  docsNavWidth: "openfox:docs-nav-width",
  docsNavCollapsed: "openfox:docs-nav-collapsed",
  /** 侧栏里折叠的空间 id（JSON 数组），按组织存 */
  spacesCollapsed: (orgId: string) => `openfox:spaces-collapsed:${orgId}`,
  /** 侧栏「任务」下固定显示的子项（JSON 数组：iterations / triage / views / projects），按空间存 */
  taskNavPinned: (spaceId: string) => `openfox:task-nav-pinned:${spaceId}`,
};
