/** 会话产物（简体中文）。由 zh-CN.ts 挂到 artifacts 键下。 */
export const artifactsZh = {
  /** 消息里的产物按钮 */
  button: "产物：{name}",
  buttonsLabel: "本次运行的产物",
  /** 会话「更多」菜单 */
  menu: "产物",
  panel: {
    label: "会话产物",
    tabs: "产物标签",
    list: "产物 {count} 个",
    close: "关闭产物面板",
    resize: "调整产物面板宽度",
    closeTab: "关闭 {name}",
  },
  list: {
    empty: "这个会话还没有产物",
    loadFailed: "加载产物失败",
    open: "打开 {name}",
    more: "只显示最近 {count} 个产物",
  },
  preview: {
    expired: "产物已失效",
    expiredHint: "这个产物已过保留期或已被删除，无法再预览或下载。",
    missing: "找不到这个产物",
    tooLarge: "文件较大，无法在这里预览，请下载后查看。",
    download: "下载",
    meta: "{type} · {size} · {time}",
  },
};
