/** 任务回收站「最近删除」（简体中文）。由 zh-CN.ts 挂到 tasks.trash 键下（独立文件，减少并行修改冲突）。 */
export const taskTrashZh = {
  title: "最近删除",
  allDescription: "这里是此空间里已删除的任务。恢复后编号不变。",
  mineTitle: "你删除的任务",
  mineDescription:
    "这里只列出你删除的任务。别人删除的任务只有删除人和空间管理员能看到并恢复。",
  retired: "此空间已停用，只能查看，不能恢复任务。",
  columns: {
    task: "任务",
    type: "类型",
    status: "状态",
    deletedBy: "删除人",
    deletedAt: "删除时间",
    actions: "操作",
  },
  listLabel: "已删除的任务",
  unknownUser: "未知用户",
  restore: "恢复",
  restoreLabel: "恢复 {id}",
  restoreTitle: "恢复 {id}？",
  restoreDesc:
    "任务会回到原来的编号。原父任务已删除时会变成顶层任务；删除时脱离的子任务不会自动挂回。",
  restored: "已恢复 {id}",
  open: "打开",
  conflict: "这个任务已被恢复或修改，列表已刷新。",
  empty: "没有最近删除的任务",
  emptyDescription: "删除的任务会出现在这里，可以随时恢复。",
  loadMore: "加载更多",
  loadFailed: "加载失败",
  /** 删除成功提示里的「撤销」 */
  undo: "撤销",
  /** 时间线：恢复动态 */
  activityRestored: "{actor} 恢复了这个任务",
  activityRestoredDetached:
    "{actor} 恢复了这个任务（原父任务已删除，已改为顶层任务）",
};
