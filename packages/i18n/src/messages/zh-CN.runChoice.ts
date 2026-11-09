/** 召唤菜单指派时选择运行对话（简体中文）。由 zh-CN.ts 挂到 tasks.runChoice 键下（独立文件，减少并行修改冲突）。 */
export const runChoiceZh = {
  title: "选择运行对话",
  description: "{agent} 在这个任务上已有指派对话，选择这次在哪里运行。",
  reuse: "在原有对话中运行（{title}）",
  new: "新开对话运行",
  newHint: "新对话将成为此任务的指派对话，之后的运行都在这里进行",
  start: "开始运行",
  cancel: "取消",
  errors: {
    notAssigned: "这个 Agent 已不是任务的处理人，请重新指派",
    notRunnable:
      "任务当前的状态不能让 Agent 运行（只有待办和进行中的任务可以）",
  },
};
