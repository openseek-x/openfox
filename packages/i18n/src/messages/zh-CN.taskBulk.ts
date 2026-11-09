/** 任务批量操作（简体中文）。由 zh-CN.ts 挂到 tasks.bulk 键下（独立文件，减少并行修改冲突）。 */
export const taskBulkZh = {
  bar: "批量操作",
  selected: "已选择 {count} 个任务",
  select: "选择 {task}",
  status: "修改状态",
  priority: "修改优先级",
  assignee: "修改处理人",
  executor: "修改执行 Agent",
  addLabel: "添加标签",
  removeLabel: "移除标签",
  clear: "取消选择",
  updated: "已更新 {count} 个任务",
  conflict: "部分任务已被他人修改，请刷新后重试",
  noCommonStatus: "所选任务的类型没有共同可用的状态",
  multipleSpaces: "所选任务来自多个空间，请在同一空间内批量修改",
  tooMany: "一次最多修改 {max} 个任务",
  noLabels: "所选任务没有标签",
};
