/** 个人资料、偏好设置与快捷键（简体中文）。由 zh-CN.ts 挂到 me 键下。 */
export const meZh = {
  profile: {
    description: "这些信息会显示在应用各处。",
    avatar: {
      title: "个人头像",
      help: "PNG、JPG 或 WebP，最大 5MB。点击头像即可更改。",
      choose: "更换头像",
      uploaded: "头像已更新",
    },
    name: {
      title: "全名",
      label: "全名",
      help: "在任务提及和成员列表中显示的真实姓名。",
      empty: "全名不能为空",
    },
    email: {
      title: "邮箱",
      help: "用于登录。",
      change: "修改",
      unlink: "解绑",
      unlinkDisabled: "请至少保留一个登录方式",
    },
    bio: {
      title: "简介",
      placeholder: "简单介绍一下自己。队友可见。",
      edit: "编辑简介",
      dialogTitle: "简介",
    },
    saved: "资料已更新",
    saveFailed: "更新资料失败",
    saveChanges: "保存更改",
    avatarFailed: "上传头像失败",
    verify: {
      title: "验证身份",
      sentTo: "我们已向 {email} 发送验证码。",
      code: "验证码",
      resend: "重新发送验证码",
      resendIn: "{seconds}s 后可重新发送",
      next: "下一步",
    },
    changeEmail: {
      title: "修改邮箱",
      newEmail: "新邮箱",
      code: "验证码",
      send: "发送",
      sendCountdown: "{seconds}s",
      save: "保存更改",
    },
    emailChanged: "邮箱已修改",
    errors: {
      emailTaken: "该邮箱已被占用",
      codeInvalid: "验证码错误或已过期",
      emailInvalid: "请输入有效邮箱",
      rateLimited: "发送太频繁，请稍后再试",
      generic: "操作失败",
    },
  },
  preferences: {
    saveFailed: "操作失败",
    sections: {
      theme: "主题",
      general: "常规",
      density: "密度",
      taskSessions: "任务会话",
    },
    theme: {
      system: "跟随系统",
      light: "浅色",
      dark: "深色",
      magicBlue: "魔法蓝",
      classicDark: "经典深色",
      comingSoon: "即将推出",
    },
    outline: {
      title: "外边框颜色",
      none: "不设置外边框颜色",
      color: "内置外边框颜色 {n}",
    },
    locale: {
      title: "语言",
      description: "设置此账号的语言。",
      zh: "简体中文",
      en: "English",
      syncFailed:
        "语言已在本设备保存，但同步到账号失败。其他设备可能仍显示旧语言。",
    },
    density: {
      description: "调整列表行高和卡片间距，同步到你的账号。",
      comfortable: "标准",
      compact: "紧凑",
    },
    sessions: {
      assign: "指派给 Agent 后自动打开会话",
      assignHelp: "开启后，指派给 Agent 或小队时会自动打开执行者的会话。",
      quick: "使用快捷指令后自动打开会话",
      quickHelp: "开启后，使用快捷指令时会自动打开对应会话。",
      failed: "保存失败，开关已恢复为原来的状态。",
    },
  },
  shortcuts: {
    description: "选择消息和应用级键盘快捷键的行为。",
    send: {
      title: "发送消息",
      description: "适用于对话框、任务评论和回复。",
      enter: "Enter 发送",
      enterHelp: "按 Shift+Enter 换行。",
      shiftEnter: "Shift+Enter 发送",
      shiftEnterHelp: "按 Enter 换行。",
    },
    commands: {
      title: "应用命令",
      description: "自定义导航和任务工作流的全局快捷键。",
      commandMenu: "命令菜单",
      commandMenuHelp: "打开搜索、导航和快捷命令。",
      moveToSpace: "将任务移动到空间",
      moveToSpaceHelp: "打开命令菜单，并预填移动命令。",
      findInView: "在当前视图查找",
      findInViewHelp: "在当前可见的任务列表或视图中搜索。",
      record: "录制",
      recording: "按下按键",
      reset: "重置",
      needModifier: "请按下 Command 或 Ctrl 加另一个键的快捷键",
      unsupported: "不支持的快捷键",
      conflict: "已被 {name} 使用",
    },
    saved: "快捷键偏好已更新",
    saveFailed: "更新快捷键失败",
  },
  // 个人访问令牌
  tokens: {
    title: "个人访问令牌",
    description: "个人访问令牌让 CLI 和外部集成可以代表你的账号进行身份验证。",
    create: "创建令牌",
    empty: "暂无令牌",
    loadFailed: "加载令牌失败",
    columns: {
      name: "名称",
      lastUsed: "最后使用",
      expires: "过期时间",
    },
    /** 形如「ofx_pat_7f3a... · 创建于 2026年10月1日 · 最近使用 3 分钟前」 */
    created: "{prefix}... · 创建于 {date}",
    lastUsed: "最近使用 {time}",
    unused: "未使用",
    never: "从未",
    noExpiry: "永不过期",
    menu: "令牌操作",
    delete: "删除",
    dialog: {
      createTitle: "创建令牌",
      createDescription: "令牌与你的账号权限相同，请妥善保管。",
      name: "名称",
      namePlaceholder: "例如：本机 CLI",
      expires: "过期时间",
      expiry: {
        d30: "30 天",
        d90: "90 天",
        y1: "1 年",
        never: "永不过期",
      },
      save: "保存",
      saving: "保存中",
      createdTitle: "令牌已创建",
      createdDescription: "立即复制你的个人访问令牌——它不会再次显示。",
      close: "关闭",
      copyAndClose: "复制并关闭",
      createFailed: "创建令牌失败",
      limit: "令牌数量已达上限",
    },
    revoke: {
      title: "吊销令牌",
      description: "该令牌将被永久吊销且不再可用。此操作不可撤销。",
      confirm: "吊销",
      done: "已吊销令牌",
      failed: "吊销令牌失败",
    },
  },
  // 通知
  notifications: {
    inbox: {
      title: "OpenFox 收件箱",
      description:
        "关闭的事件将不再出现在收件箱中。你与 Agent 对话的提醒——新回复、等你回复、等你审阅、运行失败——在关闭后不会再产生任何通知，包括桌面横幅和 IM。",
      choices: {
        off: "关闭",
        important: "开启（重要）",
        normal: "开启（非重要）",
      },
      groups: {
        assignments: "任务分配",
        status_changes: "任务状态变更",
        comments: "任务评论与回复",
        mentions: "提及",
        human_chat: "真人对话",
        agent_activity: "Agent 对话",
        automation_runs: "执行自动化",
      },
      agentHelp: "你参与的 Agent 对话的回复与运行状态",
      events: {
        task_assigned_assignee: "任务指派给你时",
        task_unassigned_assignee: "你被移出任务处理人时",
        task_assignee_updated_assignee: "你负责的任务更换处理人时",
        task_assignee_updated_follower: "你关注的任务更换处理人时",
        task_subscribed_follower: "你被添加为任务关注者时",
        task_status_updated_assignee: "你负责的任务状态变化时",
        task_status_updated_follower: "你关注的任务状态变化时",
        task_priority_updated_assignee: "你负责的任务优先级调整时",
        task_priority_updated_follower: "你关注的任务优先级调整时",
        task_start_date_updated_assignee: "你负责的任务开始日期修改时",
        task_start_date_updated_follower: "你关注的任务开始日期修改时",
        task_due_date_updated_assignee: "你负责的任务截止日期修改时",
        task_due_date_updated_follower: "你关注的任务截止日期修改时",
        task_completed_assignee: "你负责的任务完成时",
        task_completed_follower: "你关注的任务完成时",
        comment_created_member_assignee: "团队成员在你负责的任务下发表评论时",
        comment_replied_member_assignee: "团队成员在你负责的任务下回复评论时",
        comment_created_member_follower: "团队成员在你关注的任务下发表评论时",
        comment_replied_member_follower: "团队成员在你关注的任务下回复评论时",
        comment_replied_member_author: "团队成员回复你的评论时",
        reaction_created_member_author: "团队成员对你的评论或内容添加表态时",
        comment_created_agent_assignee: "Agent 在你负责的任务下发表评论时",
        comment_replied_agent_assignee: "Agent 在你负责的任务下回复评论时",
        comment_created_agent_follower: "Agent 在你关注的任务下发表评论时",
        comment_replied_agent_follower: "Agent 在你关注的任务下回复评论时",
        comment_replied_agent_author: "Agent 回复你的评论时",
        reaction_created_agent_author: "Agent 对你的评论或内容添加表态时",
        comment_mentioned_member_recipient: "团队成员在评论中提到你时",
        comment_mentioned_agent_recipient: "Agent 在评论中提到你时",
        chat_mentioned_member_recipient: "团队成员在会话里提到你时",
        chat_mentioned_agent_recipient: "Agent 在会话里提到你时",
        chat_message_received_recipient: "收到真人对话消息时",
        chat_replied_participant: "你与 Agent 的对话有新回复时",
        task_agent_completed_participant: "你与 Agent 的对话完成时",
        task_failed_participant: "你与 Agent 的对话运行失败时",
        task_agent_blocked_participant: "你与 Agent 的对话运行受阻时",
        task_needs_human_input_participant: "你与 Agent 的对话等你回复时",
        task_review_requested_participant: "你与 Agent 的对话等你审阅时",
        automation_run_completed_recipient: "发送给你的自动化运行成功完成时",
        automation_run_failed_recipient: "发送给你的自动化运行失败时",
        automation_run_cancelled_recipient: "发送给你的自动化运行被取消时",
        automation_run_skipped_recipient: "发送给你的自动化运行被跳过时",
      },
      help: {
        chat_replied_participant:
          "你在任务里发起的对话中，Agent 发表了新回复；只有发起人会收到",
        task_agent_completed_participant: "Agent 已完成当前对话中的执行",
        task_failed_participant: "Agent 运行失败，需要你查看错误信息",
        task_agent_blocked_participant: "Agent 运行受阻，需要你处理后继续",
        task_needs_human_input_participant:
          "Agent 将当前对话状态标记为「等人回复」",
        task_review_requested_participant:
          "Agent 将当前对话状态标记为「等人审阅」",
      },
    },
    title: "推送通知",
    description:
      "选择每个渠道推送哪些通知。哪些事件出现在收件箱中，在收件箱渠道页里设置。",
    back: "通知",
    channels: {
      desktop: "桌面端",
      browser: "浏览器",
      slack: "Slack",
      feishu: "飞书",
      lark: "Lark",
      dingtalk: "钉钉",
      wecom: "企业微信",
      mobile: "移动端",
      email: "邮件",
    },
    status: {
      off: "已关闭",
      allOn: "已对全部通知开启",
      allOff: "已关闭全部通知类型",
      named: "已开启：{names}",
      some: "已开启：{names} 等 {count} 项",
      separator: "、",
      soon: "即将推出",
      unbound: "未绑定",
      chooseBot: "尚未选择通知机器人",
      unavailable: "管理员尚未配置邮件发送",
      connect: {
        slack: "未连接 Slack 工作区",
        feishu: "未连接飞书助手",
        lark: "未连接 Lark 助手",
        dingtalk: "未连接钉钉助手",
      },
    },
    integration: {
      title: "未连接 {name}",
      description: "连接对应的集成后，才能把通知推送到这个渠道。",
      open: "打开集成",
      toggle: "显示 {name} 通知",
      toggleHelp: "连接后，这些事件发生时会推送到 {name}。",
      bindFeishu: "去绑定飞书账号",
      feishuNeedAdmin: "需先由管理员连接飞书，再绑定你的飞书账号。",
      feishuBindDesc: "绑定飞书账号后，才能把通知推送到飞书。",
      bindWecom: "去绑定企业微信账号",
      wecomBindDesc:
        "先用一次性验证码绑定企业微信账号，并选择一个机器人作为个人通知目标。",
      wecomSelectDesc:
        "在企业微信集成页选择一个已绑定的机器人作为个人通知目标。",
    },
    detail: {
      feishuTitle: "显示飞书通知",
      feishuHelp: "飞书通知只发给你在各组织绑定的飞书账号。",
      wecomTitle: "发送企业微信通知",
      wecomHelp:
        "开启后，新通知会发送到你选择的企业微信机器人私聊；真人私聊提醒需单独开启。",
      wecomVia: "通过企业微信机器人私聊发送 · {name}",
      emailTitle: "发送邮件通知",
      emailHelp:
        "开启后，新通知会逐条发送安全摘要和收件箱链接；不会补发此前的通知。",
      emailUnavailable:
        "服务器尚未配置 SMTP，请联系管理员。配置完成后可开启邮件通知。",
      feishuVia: "通过飞书机器人私聊发送 · 已绑定 {name}",
      reset: "恢复默认设置",
      resetDone: "已恢复通知默认设置",
      resetFailed: "恢复默认设置失败",
      saveFailed: "更新通知设置失败",
      desktopTitle: "显示桌面通知",
      desktopHelp: "这些事件发生时，在桌面客户端弹出原生通知横幅。",
      browserTitle: "显示浏览器通知",
      browserHelp:
        "这些事件发生时，在浏览器弹出原生通知横幅。这项设置会跟随你的账号，在任何浏览器登录都生效。",
      permission: {
        title: "浏览器权限",
        help: "允许当前浏览器为新的收件箱通知显示原生横幅。",
        enable: "启用",
        enabled: "已启用",
        enabledHelp: "当前浏览器可以显示原生通知横幅。",
        blocked: "通知已被浏览器拦截。请在浏览器设置中为本站点开启通知权限。",
      },
    },
    events: {
      title: "事件类型",
      description:
        "选择这个渠道推送哪些事件。收件箱中的提醒可在收件箱渠道页单独设置。",
      assign: "指派",
      assignHelp: "你被指派或取消指派某个任务时",
      status: "状态变更",
      statusHelp: "任务的状态、优先级和阻塞关系发生变化时",
      comment: "评论与回复",
      commentHelp: "评论、回复和话题解决时",
      mention: "提及",
      mentionHelp: "在评论或内容中提及你时",
      agent: "Agent 活动",
      agentHelp: "Agent 任务完成或失败时",
      automation: "执行自动化",
      automationHelp: "自动化运行完成或失败时",
    },
    // 新通知弹出（页面会话内，只对之后到达的通知生效）
    alert: {
      fallbackTitle: "OpenFox",
    },
  },
  // 开始使用
  getStarted: {
    title: "开始使用 OpenFox",
    subtitle: "完成以下步骤，充分发挥 OpenFox 的价值。",
    allDone: "全部完成！",
    allDoneSubtitle: "你已完成所有步骤，太棒了！",
    scored: "已得分",
    guideTitle: "设置向导",
    guideDescription: "按照以下步骤，为你的团队配置好 OpenFox。",
    hideGuide: "隐藏侧边栏向导",
    showGuide: "显示侧边栏向导",
    soon: "即将开放",
    points: "+{points}",
    progress: "{done} / {total}",
    loadFailed: "加载开始使用进度失败",
    categories: {
      get_started: "开始使用",
      collaboration: "协作",
      agents: "Agents",
      tasks: "任务",
      space_management: "管理空间",
      automations: "自动化",
    },
    steps: {
      connect_machine: {
        title: "连接一台电脑",
        description:
          "把你的电脑或服务器接入 OpenFox，Agent 的任务都在这台电脑上执行。装好 CLI 并登录即可，之后随时可以再加。",
        cta: "连接电脑",
      },
      create_agent: {
        title: "创建你的第一个 Agent",
        description:
          "Agent 是替你干活的角色。起个名字、选好模型和执行电脑，它就能像同事一样接任务。",
        cta: "创建 Agent",
      },
      create_task: {
        title: "创建第一个任务",
        description:
          "把要做的事写成任务。标题写清目标，描述里补上背景和验收标准 —— Agent 和同事都靠它对齐。",
        cta: "新建任务",
      },
      delegate_task_to_agent: {
        title: "把任务指派给 Agent",
        description:
          "把任务的处理人设成 Agent，它就会自己开始干活，过程和产出都能在任务里实时看到。",
        cta: "指派给 Agent",
      },
      connect_jira_or_tapd: {
        title: "导入 Jira、TAPD、禅道等平台里的任务",
        description:
          "导入已有的任务（需求、Bug 等），直接把任务指派给 Agent\n可以直接迁移过来，也可以使用“双向同步”，两边共存",
        cta: "去接入",
      },
      invite_member: {
        title: "邀请团队成员",
        description:
          "把团队成员拉进当前组织或空间，让他们能看到任务、参与讨论，并一起维护协作流程。",
        cta: "邀请成员",
      },
      assign_task_person: {
        title: "把任务指派给成员",
        description:
          "为任务指定明确处理人，团队就知道谁来推进、谁需要更新进展。",
        cta: "指派成员",
      },
      comment_or_reply: {
        title: "在任务下评论和回复",
        description:
          "把问题、结论和补充背景留在任务里，后续接手的人不用再到聊天记录里翻上下文。",
        cta: "发表评论",
      },
      process_inbox: {
        title: "从收件箱处理任务",
        description:
          "从收件箱集中处理分配、提及和待确认事项，先把最需要你响应的任务清掉。",
        cta: "打开收件箱",
      },
      clear_inbox: {
        title: "清理收件箱中的任务",
        description: "处理或归档已读提醒，让收件箱只保留真正需要跟进的任务。",
        cta: "打开收件箱",
      },
      configure_agent_instructions: {
        title: "为 Agent 编写指令",
        description:
          "写清 Agent 的职责、工作边界和交付标准，它执行任务时会按这些规则行动。",
        cta: "编辑指令",
      },
      chat_with_agent: {
        title: "和你的 Agent 对话",
        description:
          "直接向 Agent 补充背景、追问进度或让它解释结果，适合快速校准一次任务。",
        cta: "与 Agent 对话",
      },
      share_agent_to_space: {
        title: "把 Agent 共享到空间",
        description:
          "把配置好的 Agent 开放给某个空间，让空间里的任务都能使用同一套工作角色。",
        cta: "共享 Agent",
      },
      create_crew: {
        title: "创建一个 Agent 小队",
        description:
          "把多个 Agent 组合成小队，让不同角色协同完成更复杂的任务。",
        cta: "创建小队",
      },
      install_skill: {
        title: "安装一个技能",
        description:
          "把常用流程或工具能力安装进来，后续可以复用到 Agent 或自动化中。",
        cta: "安装技能",
      },
      attach_skill_to_agent: {
        title: "为 Agent 装配技能",
        description:
          "把已安装的技能分配给 Agent，让它在执行任务时能调用对应流程和工具。",
        cta: "装配技能",
      },
      follow_task: {
        title: "关注任务",
        description:
          "关注你关心但不负责的任务，后续进展、评论和状态变化会更容易被看见。",
        cta: "打开任务",
      },
      add_label: {
        title: "添加任务标签",
        description:
          "用标签标记任务类型、优先级或来源，之后筛选和批量处理会更快。",
        cta: "管理标签",
      },
      create_subtask: {
        title: "创建子任务",
        description: "把大任务拆成可执行的小步骤，方便分派处理人并逐项推进。",
        cta: "新建子任务",
      },
      link_related_task: {
        title: "设置关联任务",
        description:
          "把互相依赖或背景相关的任务连起来，查看影响范围时不用来回搜索。",
        cta: "打开任务",
      },
      create_view: {
        title: "创建自定义视图",
        description:
          "按处理人、状态、标签等条件保存一套筛选视角，快速查看你最常处理的任务集合。",
        cta: "新建视图",
      },
      save_org_view: {
        title: "保存一个团队视图",
        description:
          "把常用视图共享给团队，大家可以用同一套口径查看任务和推进情况。",
        cta: "共享视图",
      },
      create_project: {
        title: "创建专项",
        description:
          "用专项承载一组目标相关的任务，集中管理范围、进度和负责人。",
        cta: "新建专项",
      },
      link_task_to_project: {
        title: "设置任务所属专项",
        description: "把任务归入对应专项，专项页才能准确汇总进度和待办范围。",
        cta: "关联专项",
      },
      configure_task_type_field: {
        title: "为任务类型配置字段",
        description:
          "为不同任务类型设置需要填写的信息，例如优先级、模块、版本或验收口径。",
        cta: "配置字段",
      },
      manage_task_status: {
        title: "管理任务状态",
        description:
          "调整空间里的任务状态流转，让任务从待处理到完成的路径符合团队流程。",
        cta: "管理任务状态",
      },
      create_sprint: {
        title: "开启空间迭代",
        description: "为空间启用迭代节奏，把任务规划到固定周期里跟踪交付。",
        cta: "开启迭代",
      },
      enable_space_estimation: {
        title: "开启任务估时",
        description:
          "启用估时后，团队可以为任务补充工作量，便于排期和评估迭代容量。",
        cta: "开启估时",
      },
      connect_git: {
        title: "设置 Git 仓库关联",
        description:
          "为空间配置 Git 仓库，让 Agent 提交的代码能追踪到具体任务。",
        cta: "配置仓库",
      },
      connect_feishu: {
        title: "集成飞书、钉钉、企业微信等 IM 平台",
        description:
          "让 Agent 入驻到团队协作 IM 平台里\n在 IM 群里 @ Agent，让 Agent 读取群里的上下文，整理成任务，安排给 Agent 执行。",
        cta: "连接协作平台",
      },
      create_automation: {
        title: "创建一个 Agent 自动化",
        description:
          "让 Agent 自主工作\n定时触发、任务状态变更触发、评论关键词触发、Webhook 触发。",
        cta: "新建自动化",
      },
      run_automation: {
        title: "运行一次自动化",
        description:
          "手动触发自动化并检查执行结果，确认规则、权限和输出都符合预期。",
        cta: "打开自动化",
      },
      create_scheduled_automation: {
        title: "创建定时运行的自动化",
        description: "按固定时间运行流程，适合日报、巡检、同步和周期性提醒。",
        cta: "新建自动化",
      },
      create_task_status_automation: {
        title: "创建基于任务状态的自动化",
        description:
          "当任务进入指定状态时自动执行动作，例如提醒处理人、同步频道或更新字段。",
        cta: "新建自动化",
      },
      create_comment_automation: {
        title: "创建基于评论的自动化",
        description:
          "根据新评论或关键词触发流程，把反馈、审批和补充信息及时转成动作。",
        cta: "新建自动化",
      },
      create_webhook_automation: {
        title: "创建基于 Webhook 的自动化",
        description:
          "接收外部系统的 Webhook 后自动触发流程，把代码、告警或业务事件接入任务协作。",
        cta: "新建自动化",
      },
    },
  },
  // 内测功能开关
  beta: {
    title: "内测功能开关",
    empty: "暂无可用的内测功能",
    emptyHelp: "新的内测功能准备好后，会显示在这里供你选择开启。",
    saveFailed: "更新内测功能失败",
  },
};
