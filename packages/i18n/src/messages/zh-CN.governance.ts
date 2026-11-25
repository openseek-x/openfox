export const governanceZh = {
  queueFull: {
    title: "组织队列已满",
    desc: "当前排队的运行数已达到组织上限。你的消息已保存，会在队列有空位后自动开始处理。",
    usage: "当前排队 {queued}，上限 {max}。",
  },
  trust: {
    titleWiden: "扩大这台电脑的共享范围？",
    titleAddOrganization: "把这台电脑添加到另一个组织？",
    leadWiden: "更改后，更多成员可以让 Agent 在这台电脑上运行。",
    leadAddOrganization:
      "添加后，该组织的成员也可以让 Agent 在这台电脑上运行。",
    notSandbox: "工作目录隔离不是安全沙箱。",
    runsAsUser:
      "共享对象触发的运行以本机用户身份执行，可读取该用户可读的文件。",
    advice:
      "请只把电脑共享给你信任的成员，并避免在这台电脑上保存不应被他们读取的内容。",
    acknowledge: "我已了解",
    confirm: "确认共享",
    confirmAddOrganization: "确认添加",
  },
  reconcile: {
    evidenceTitle: "核对证据",
    evidenceEmpty: "还没有收到证据。",
    evidenceLoadFailed: "加载证据失败。",
    columns: {
      source: "来源",
      kind: "类型",
      time: "时间",
      detail: "说明",
    },
    source: {
      DAEMON: "电脑",
      CENTER: "中心",
      OPERATOR: "管理员",
      unknown: "其他",
    },
    kind: {
      PROCESS_STARTED: "进程已启动",
      PROCESS_RUNNING: "进程仍在运行",
      PROCESS_EXITED: "进程已退出",
      PROCESS_NOT_FOUND: "找不到进程",
      RESULT_CONFIRMED: "结果已确认",
      HEARTBEAT: "心跳",
      REVOCATION: "授权已撤销",
      unknown: "其他",
    },
    notAccepted: "未受理",
    noDetail: "无",
    acceptResult: "接受已有结果",
    acceptConfirm:
      "电脑已确认这次运行产生了结果。接受后将解除待核对，由电脑提交该结果完成这次运行。确认接受？",
    acceptConfirmAction: "确认接受",
    alreadyResolved: "已处理：这次核对已由其他人或系统处理，已刷新。",
    acceptFailed: "操作失败，请刷新后重试。",
  },
  computer: {
    reconciling: "核对中",
    reconcilingHint: "这台电脑上有 {count} 个运行正在核对状态",
    pending: {
      title: "待核对的运行",
      desc: "这些运行因电脑失联或进程无法确认而暂停收尾，对应的对话在处理前无法继续。电脑恢复后通常会自动处理；长时间没有恢复时，可以在这里把运行标记为失败。",
      columns: {
        computer: "电脑",
        agent: "Agent",
        actor: "发起人",
        since: "进入待核对",
        reason: "原因",
        action: "操作",
      },
      markFailed: "标记为失败",
      noCase: "暂时无法处理",
      confirmTitle: "把这次运行标记为失败？",
      confirmDesc:
        "{agent} 由 {actor} 发起的这次运行会立即结束并记为失败，对应的对话恢复为可以继续输入。系统不会自动重试，需要时由发起人重新发送消息。如果电脑上的任务进程其实还在运行，它之后提交的结果不会被接受。",
      confirmAction: "确认标记为失败",
      marked: "已标记为失败",
      alreadyResolved: "已处理：这次运行已由其他人或系统处理，已刷新。",
      failed: "操作失败，请刷新后重试。",
    },
  },
};
