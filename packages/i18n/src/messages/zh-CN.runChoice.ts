/** 阶段 10b · 召唤菜单指派时选择运行对话（简体中文）。由 zh-CN.ts 挂到 tasks.runChoice 键下（独立文件，减少并行修改冲突）。 */
export const runChoiceZh = {
  title: "选择运行对话",
  description: "{agent} 在这个任务上已有指派对话，选择这次在哪里运行。",
  reuse: "在原有对话中运行（{title}）",
  new: "新开对话运行",
  newHint: "新对话将成为此任务的指派对话，之后的运行都在这里进行",
  start: "开始运行",
  cancel: "取消",
  schedule: "选择本次开始时间",
  scheduleAgent: "定时运行 {agent}",
  scheduleDescription: "设置 {agent} 本次任务指派的最早开始时间。",
  notBefore: "本次最早开始时间（本机时区）",
  timeHint: "留空则不设人工延期；若工作时间开启，将顺延至可用窗口。",
  errors: {
    notAssigned: "这个 Agent 已不是任务的处理人，请重新指派",
    notRunnable:
      "任务当前的状态不能让 Agent 运行（只有待办和进行中的任务可以）",
  },
};
