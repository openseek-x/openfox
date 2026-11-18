/** 阶段 6e · 第三方账号绑定（简体中文）。由 zh-CN.ts 挂到 accountBinding 键下。 */
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
  teambition: {
    title: "确认 Teambition 本人身份",
    description:
      "仅验证身份，用于此企业后续导入的人员归属。不会保存个人令牌或发送远程评论。",
    confirm: "确认绑定此身份",
    reverify: "重新验证 {company}",
    back: "返回第三方账号绑定",
    identity:
      "企业成员邮箱必须与当前 OpenFox 本人邮箱一致，且账号未停用、不是外部成员。请检查账号后重新授权。",
    permission: "此应用没有核验企业成员的权限，请联系管理员后重新授权。",
    rate: "Teambition 暂时限制请求，请稍后重新授权。",
    changed: "组织连接或本人信息已变化，请重新授权。",
    failed: "授权未完成或已失效，请返回重新发起。",
  },
  notReady: {
    zoho_bugtracker: "需先由管理员连接 Zoho 门户。",
    bitbucket:
      "请先让管理员连接 Bitbucket workspace 并配置独立 account-only consumer",
    shortcut: "请先让管理员连接 Shortcut workspace",
    azure_devops: "请先由管理员连接 Azure DevOps 组织，并配置独立身份应用",
    asana: "请先由管理员连接 Asana workspace 并配置独立身份应用",
    teambition:
      "请先让管理员绑定 Teambition 企业应用并配置回调域名和企业查看权限",
    tapd: "请先让管理员连接可读取项目成员邮箱的 TAPD Basic 企业 API",
    yunxiao: "请先让管理员绑定云效组织",
    github: "请先让管理员绑定 GitHub",
    jira: "请先让管理员通过 Jira API 导入项目",
    feishu: "需先由管理员连接飞书助手",
    gitlab_cn: "请先让管理员绑定极狐 GitLab.cn",
    gitlab: "请先让管理员绑定 GitLab 自部署版",
    gitee: "请先让管理员绑定 Gitee",
  },
  purpose: {
    zoho_bugtracker:
      "使用独立 OAuth 核验同门户的本人 login_id、启用用户及本人邮箱。仅保存身份事实，不保存个人 Token；只建议后续导入活动中的来源报告人映射，不改写本地创建者或指派负责人。",
    bitbucket:
      "一次核验本人 UUID，识别后续 PR 和 PR 评论活动作者；不保存个人令牌，不代表持续 workspace 成员资格。",
    shortcut:
      "验证本人身份用于后续任务导入归属；可另行授权用本人凭据发送新评论。",
    azure_devops:
      "一次 OAuth 验证所选组织中的本人身份；不保存个人令牌，不按邮箱匹配。",
    asana:
      "核验本人邮箱与身份，用于此 workspace 后续新增任务归属。不保存个人令牌，不发送评论。父授权、本人邮箱或成员资格变化后须重新验证；Asana 侧撤销不会实时解除本地映射。",
    yunxiao: "验证个人云效身份用于后续导入匹配；不保存个人 PAT，不授权写入。",
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
    tapd: "一次验证你的 TAPD 身份，在选定项目中为后续导入建议本人归属。不会保存个人 Token 或发送评论。",
    zentao: "绑定你的个人禅道账号，让同步评论以你本人的禅道身份发布。",
    pingcode: "绑定个人令牌，让你新建的评论以本人身份同步到 PingCode。",
    teambition:
      "验证你的 Teambition 企业身份，用于后续导入人员归属；不保存个人令牌或授权远程评论。",
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
  shortcut: {
    title: "Shortcut 个人身份与评论授权",
    workspace: "Shortcut workspace",
    manageWorkspace: "管理 {workspace} 身份与评论",
    explanation:
      "仅在所选 workspace 验证本人，邮箱须为 {email}。只影响后续自动归属，人工映射和历史任务保留；不导入 Shortcut 远端或历史评论。",
    identity: "已验证：{name}",
    identityOnly: "仅验证身份",
    commentsOn: "已授权本人新评论",
    allowComments: "允许以本人身份同步新评论",
    identityHelp:
      "个人 Token 只用于本次身份校验，不保存，不发送评论。这是一次身份验证；远端成员状态变化不会实时更新此绑定。",
    commentsWarning:
      "将加密保存你的个人 Token，仅在已有双向同步关系中发送本次授权后的本人新评论。Shortcut V3 Token 本身权限较广；结果不明的评论不会自动重发。",
    token: "Shortcut V3 个人 Token",
    tokenHelp:
      "请提供本人在所选 workspace 的 V3 Token。初次绑定、重新验证或开启评论均需提供；Token 不会显示或保存到浏览器。",
    disableHelp:
      "关闭评论授权可留空，无需再次提供 Token。关闭后删除已保存凭据，保留身份绑定和已发送评论。",
    save: "验证并保存",
    disableComments: "关闭评论授权",
    saved: "Shortcut 身份与评论设置已保存",
    disconnectBody:
      "断开后，后续自动归属不再使用此身份，尚未开始发送的本人评论停止发送，已保存个人 Token 被清除。人工映射、历史任务与已发送评论保留；已进入发送阶段的请求可能完成。",
    errors: {
      auth: "Token 无效，或 Shortcut 账号未启用、已停用。",
      email: "Shortcut 本人邮箱为空或与你的 OpenFox 邮箱不一致。",
      workspace: "此 Token 所属 workspace 与所选组织连接不一致。",
      inUse: "此 Shortcut 身份已绑定。更换身份前，请先解绑。",
      rate: "Shortcut 暂时限制请求，请稍后重试。",
    },
  },
  tapd: {
    title: "验证 TAPD 个人身份",
    explanation:
      "仅验证身份，不保存个人 Token，不发送评论。项目成员邮箱须为 {email}。只影响后续自动归属，人工选择和历史任务不变。",
    parent: "TAPD 企业连接",
    project: "TAPD 项目 ID",
    projectHelp:
      "每次验证一个可访问项目，最多 20 个；移除最后一个项目会断开此身份。邮箱、昵称或企业连接凭据改变后，需逐项重新验证。",
    token: "用户态 OAuth Access Token",
    tokenHelp:
      "使用你已授权的 TAPD 应用签发的用户态 Token，需能读取本人和所选项目。企业 API 密码不适用于此处。",
    verify: "验证并添加项目",
    identity: "已验证昵称：{nick}",
    scopes: "已验证项目范围",
    remove: "移除项目 {project}",
    errors: {
      auth: "用户 Token 无效，或 TAPD 账号已离职、冻结或停用。",
      project: "无法核验该项目及单一成员，请检查项目权限、公司和昵称。",
      email: "项目目录中的本人邮箱为空或与你的 OpenFox 邮箱不一致。",
      basic:
        "需要正常连接且可读取项目成员邮箱的 Basic 企业 API；Token 类型父连接暂不能核验本人邮箱。",
      inUse: "此 TAPD 身份已绑定。更换身份或企业连接前，请先解绑。",
      limit: "每个绑定最多验证 20 个项目，请先移除不再使用的项目。",
      rate: "TAPD 暂时限制请求，请稍后重试。",
    },
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
