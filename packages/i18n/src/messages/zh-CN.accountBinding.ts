/** 第三方账号绑定（简体中文）。由 zh-CN.ts 挂到 accountBinding 键下。 */
export const accountBindingZh = {
  title: "第三方账号绑定",
  description: "绑定第三方账号，用于登录或在应用之间同步你的操作归属。",
  loadFailed: "加载失败，请刷新重试",
  comingSoon: "即将推出",
  manage: "管理",
  bind: "绑定",
  connected: "已连接",
  update: "更新",
  disconnect: "断开连接",
  menu: "{name} 操作",
  disconnectHost: "断开 {host}",
  linkOther: "关联其他实例",
  linkHost: "关联 {host}",
  pickInstance: "选择 {name} 实例",
  connectedToast: "已连接",
  disconnectedToast: "已断开连接",
  disconnectTitle: "断开 {name} 连接？",
  disconnectBody:
    "断开后，同步的内容将不再归属到你本人。已建立的外部账号关联不会撤销，管理员可在「外部账号」中调整。",
  popupBlocked: "浏览器拦截了授权弹窗。",
  openAuthorize: "在新标签页打开授权页",
  waiting: "正在等待授权完成……",
  notReady: {
    github: "请先让管理员绑定 GitHub",
    jira: "请先让管理员通过 Jira API 导入项目",
    feishu: "需先由管理员连接飞书助手",
    gitlab_cn: "请先让管理员绑定极狐 GitLab.cn",
    gitlab: "请先让管理员绑定 GitLab 自部署版",
    gitee: "请先让管理员绑定 Gitee",
  },
  purpose: {
    wechat:
      "在微信里直接和你的 Agent 对话。在微信集成页扫码绑定 Agent，这里汇总你的绑定。",
    github: "绑定你的 GitHub 账号，让同步的 Issue、评论和 PR 归属到你本人。",
    gitlab_cn:
      "绑定你的极狐 GitLab 账号，让同步的 Issue、评论和合并请求归属到你本人。",
    gitlab:
      "绑定你在 GitLab 自部署实例上的账号，让同步的 Issue、评论和合并请求归属到你本人。",
    gitee:
      "绑定你的 Gitee 账号，让同步的 Issue、评论和 Pull Request 归属到你本人。",
    jira: "同步你在 Jira 任务中的参与归属。",
    tapd: "绑定你的 TAPD 个人访问令牌，让已同步 TAPD 条目的变更与评论正确归属到你本人。",
    zentao: "绑定你的个人禅道账号，让同步评论以你本人的禅道身份发布。",
    pingcode: "绑定个人令牌，让你新建的评论以本人身份同步到 PingCode。",
    teambition:
      "连接你的 Teambition 账号，让同步任务与评论以你本人的 Teambition 身份创建。",
    slack: "需先由管理员连接 Slack 工作区",
    feishu: "需先由管理员连接飞书助手",
    lark: "需先由管理员连接 Lark 助手",
    dingtalk: "需先由管理员连接钉钉助手",
  },
  purposeReady: {
    gitlab_cn:
      "绑定后，你在极狐 GitLab 上的 Issue、评论和合并请求会归属到你本人。每个实例需要分别绑定。",
    gitlab:
      "绑定后，你在 GitLab 上的 Issue、评论和合并请求会归属到你本人。每个实例需要分别绑定。",
    gitee:
      "绑定后，你在 Gitee 上的 Issue、评论和 Pull Request 会归属到你本人。",
    feishu:
      "绑定后，飞书机器人会私聊给你发送 OpenFox 通知，你也可以在飞书里使用任务助手和智能体。",
    lark: "绑定后，Lark 机器人会私聊给你发送 OpenFox 通知，你也可以在 Lark 里使用任务助手和智能体。",
    dingtalk:
      "绑定后，钉钉机器人会私聊给你发送 OpenFox 通知，你也可以在钉钉里使用任务助手和智能体。",
  },
  jira: {
    title: "绑定 Jira 账号",
    description:
      "将你的个人 Jira 账号绑定到 {host}，让同步任务与评论以你本人身份写回。",
    site: "Jira 站点",
    email: "Jira 邮箱",
    emailHelp: "须与你的 OpenFox 邮箱一致",
    apiToken: "API Token",
    apiTokenHelp:
      "在 Atlassian 账号的「安全」页面创建 API Token，再粘贴到这里。",
    pat: "个人访问令牌",
    patHelp: "在 Jira 个人资料的「个人访问令牌」页面创建，再粘贴到这里。",
    notStored: "令牌只用来确认你的身份，OpenFox 不会保存。",
    submit: "连接",
    linked: "已自动关联 {n} 个 Jira 外部账号",
    errors: {
      JIRA_AUTH_FAILED: "Jira 凭证验证失败，请检查后重试。",
      JIRA_EMAIL_MISMATCH: "Jira 邮箱必须与你的 OpenFox 邮箱一致。",
      JIRA_ACCOUNT_IN_USE: "这个 Jira 账号已经绑定到本组织的其他成员。",
      JIRA_UNAVAILABLE: "暂时连不上 Jira，请稍后重试。",
      JIRA_BLOCKED_ADDRESS: "这个 Jira 地址不允许访问。",
      JIRA_SITE_UNKNOWN: "这个 Jira 站点不在本组织的导入记录里。",
      fallback: "连接失败，请重试",
    },
  },
};
