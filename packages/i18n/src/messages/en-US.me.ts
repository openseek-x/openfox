import type { Messages } from "../translate";

/** Stage 4b · Profile, preferences and keyboard shortcuts (English). */
export const meEn: Messages["me"] = {
  profile: {
    description: "This information is shown across the app.",
    avatar: {
      title: "Profile picture",
      help: "PNG, JPG or WebP, up to 5MB. Click the picture to change it.",
      choose: "Change profile picture",
      uploaded: "Profile picture updated",
    },
    name: {
      title: "Full name",
      label: "Full name",
      help: "Your real name, shown in task mentions and member lists.",
      empty: "Full name can't be empty",
    },
    email: {
      title: "Email",
      help: "Used to sign in.",
      change: "Change",
      unlink: "Unlink",
      unlinkDisabled: "Keep at least one sign-in method",
    },
    bio: {
      title: "Bio",
      placeholder: "Tell people a little about yourself. Visible to teammates.",
      edit: "Edit bio",
      dialogTitle: "Bio",
    },
    saved: "Profile updated",
    saveFailed: "Couldn't update profile",
    saveChanges: "Save changes",
    avatarFailed: "Couldn't upload profile picture",
    verify: {
      title: "Verify your identity",
      sentTo: "We sent a verification code to {email}.",
      code: "Verification code",
      resend: "Resend code",
      resendIn: "Resend in {seconds}s",
      next: "Next",
    },
    changeEmail: {
      title: "Change email",
      newEmail: "New email",
      code: "Verification code",
      send: "Send",
      sendCountdown: "{seconds}s",
      save: "Save changes",
    },
    emailChanged: "Email changed",
    errors: {
      emailTaken: "This email is already in use",
      codeInvalid: "The code is incorrect or has expired",
      emailInvalid: "Enter a valid email address",
      rateLimited: "Too many attempts. Try again later.",
      generic: "Something went wrong",
    },
  },
  preferences: {
    saveFailed: "Something went wrong",
    sections: {
      theme: "Theme",
      general: "General",
      density: "Density",
      taskSessions: "Task sessions",
    },
    theme: {
      system: "System",
      light: "Light",
      dark: "Dark",
      magicBlue: "Magic blue",
      classicDark: "Classic dark",
      comingSoon: "Coming soon",
    },
    outline: {
      title: "Outline color",
      none: "No outline color",
      color: "Built-in outline color {n}",
    },
    locale: {
      title: "Language",
      description: "Set the language for this account.",
      zh: "简体中文",
      en: "English",
      syncFailed:
        "Language saved on this device, but couldn't sync to your account. Other devices may still show the old language.",
    },
    density: {
      description:
        "Adjust list row heights and card spacing across your account.",
      comfortable: "Comfortable",
      compact: "Compact",
    },
    sessions: {
      assign: "Open session automatically after assigning to an Agent",
      assignHelp:
        "When on, assigning a task to an Agent or squad opens the assignee's session automatically.",
      quick: "Open session automatically after using a quick command",
      quickHelp:
        "When on, using a quick command opens the matching session automatically.",
      failed: "Couldn't save. The switch was restored to its previous state.",
    },
  },
  shortcuts: {
    description:
      "Choose how keyboard shortcuts behave for messages and app-level commands.",
    send: {
      title: "Send messages",
      description: "Applies to dialogs, task comments and replies.",
      enter: "Enter to send",
      enterHelp: "Shift+Enter for a new line.",
      shiftEnter: "Shift+Enter to send",
      shiftEnterHelp: "Enter for a new line.",
    },
    commands: {
      title: "App commands",
      description: "Global shortcuts for navigation and task workflows.",
      commandMenu: "Command menu",
      commandMenuHelp: "Open search, navigation and quick commands.",
      moveToSpace: "Move task to space",
      moveToSpaceHelp: "Open the command menu with the move command filled in.",
      findInView: "Find in current view",
      findInViewHelp: "Search the task list or view you are looking at.",
      record: "Record",
      recording: "Press keys",
      reset: "Reset",
      needModifier: "Press Command or Ctrl together with another key",
      unsupported: "Unsupported shortcut",
      conflict: "Already used by {name}",
    },
    saved: "Keyboard shortcut preferences updated",
    saveFailed: "Couldn't update keyboard shortcuts",
  },
  // Stage 4c · Personal access tokens
  tokens: {
    title: "Personal access tokens",
    description:
      "Personal access tokens let the CLI and external integrations authenticate as your account.",
    create: "Create token",
    empty: "No tokens",
    loadFailed: "Couldn't load tokens",
    columns: {
      name: "Name",
      lastUsed: "Last used",
      expires: "Expires",
    },
    created: "{prefix}... · Created {date}",
    lastUsed: "Last used {time}",
    unused: "Unused",
    never: "Never",
    noExpiry: "No expiration",
    menu: "Token actions",
    delete: "Delete",
    dialog: {
      createTitle: "Create token",
      createDescription:
        "A token has the same permissions as your account. Keep it safe.",
      name: "Name",
      namePlaceholder: "For example: local CLI",
      expires: "Expires",
      expiry: {
        d30: "30 days",
        d90: "90 days",
        y1: "1 year",
        never: "Never expires",
      },
      save: "Save",
      saving: "Saving",
      createdTitle: "Token created",
      createdDescription:
        "Copy your personal access token now. It won't be shown again.",
      close: "Close",
      copyAndClose: "Copy and close",
      createFailed: "Couldn't create token",
      limit: "You've reached the token limit",
    },
    revoke: {
      title: "Revoke token",
      description:
        "This token will be permanently revoked and can no longer be used. This can't be undone.",
      confirm: "Revoke",
      done: "Token revoked",
      failed: "Couldn't revoke token",
    },
  },
  // Stage 4c · Notifications
  notifications: {
    inbox: {
      title: "OpenFox inbox",
      description:
        "Turned-off events stop appearing in the Inbox. Alerts about your Agent conversations — new replies, waiting for your reply or review, run failures — stop producing notifications entirely when turned off, including desktop banners and IMs.",
      choices: {
        off: "Off",
        important: "On (important)",
        normal: "On (normal)",
      },
      groups: {
        assignments: "Task assignments",
        status_changes: "Task status changes",
        comments: "Task comments and replies",
        mentions: "Mentions",
        human_chat: "Human conversations",
        agent_activity: "Agent conversations",
        automation_runs: "Automation runs",
      },
      agentHelp:
        "Replies and run status in Agent conversations you participate in",
      events: {
        task_assigned_assignee: "When a task is assigned to you",
        task_unassigned_assignee:
          "When you are removed as the assignee of a task",
        task_assignee_updated_assignee:
          "When a task you are assigned to gets a new assignee",
        task_assignee_updated_follower:
          "When a task you follow gets a new assignee",
        task_subscribed_follower:
          "When someone adds you as a follower of a task",
        task_status_updated_assignee:
          "When the status of a task you are assigned to changes",
        task_status_updated_follower:
          "When the status of a task you follow changes",
        task_priority_updated_assignee:
          "When the priority of a task you are assigned to changes",
        task_priority_updated_follower:
          "When the priority of a task you follow changes",
        task_start_date_updated_assignee:
          "When the start date of a task you are assigned to changes",
        task_start_date_updated_follower:
          "When the start date of a task you follow changes",
        task_due_date_updated_assignee:
          "When the due date of a task you are assigned to changes",
        task_due_date_updated_follower:
          "When the due date of a task you follow changes",
        task_completed_assignee: "When a task you are assigned to is completed",
        task_completed_follower: "When a task you follow is completed",
        comment_created_member_assignee:
          "When a team member comments on a task you are assigned to",
        comment_replied_member_assignee:
          "When a team member replies to a comment on a task you are assigned to",
        comment_created_member_follower:
          "When a team member comments on a task you follow",
        comment_replied_member_follower:
          "When a team member replies to a comment on a task you follow",
        comment_replied_member_author:
          "When a team member replies to your comment",
        reaction_created_member_author:
          "When a team member reacts to your comment or content",
        comment_created_agent_assignee:
          "When an Agent comments on a task you are assigned to",
        comment_replied_agent_assignee:
          "When an Agent replies to a comment on a task you are assigned to",
        comment_created_agent_follower:
          "When an Agent comments on a task you follow",
        comment_replied_agent_follower:
          "When an Agent replies to a comment on a task you follow",
        comment_replied_agent_author: "When an Agent replies to your comment",
        reaction_created_agent_author:
          "When an Agent reacts to your comment or content",
        comment_mentioned_member_recipient:
          "When a team member mentions you in a comment",
        comment_mentioned_agent_recipient:
          "When an Agent mentions you in a comment",
        chat_mentioned_member_recipient:
          "When a team member mentions you in a conversation",
        chat_mentioned_agent_recipient:
          "When an Agent mentions you in a conversation",
        chat_message_received_recipient:
          "When you receive a message in a human conversation",
        chat_replied_participant:
          "When your conversation with an Agent gets a new reply",
        task_agent_completed_participant:
          "When your conversation with an Agent finishes running",
        task_failed_participant:
          "When the Agent run in your conversation fails",
        task_agent_blocked_participant:
          "When the Agent run in your conversation is blocked",
        task_needs_human_input_participant:
          "When your conversation with an Agent is waiting for your reply",
        task_review_requested_participant:
          "When your conversation with an Agent is waiting for your review",
        automation_run_completed_recipient:
          "When an automation run reported to you completes successfully",
        automation_run_failed_recipient:
          "When an automation run reported to you fails",
        automation_run_cancelled_recipient:
          "When an automation run reported to you is cancelled",
        automation_run_skipped_recipient:
          "When an automation run reported to you is skipped",
      },
      help: {
        chat_replied_participant:
          "The Agent posted a new reply in a task conversation you started; only the starter is notified",
        task_agent_completed_participant:
          "The Agent finished its run in this conversation",
        task_failed_participant:
          "The Agent run failed and needs your attention",
        task_agent_blocked_participant:
          "The Agent run is blocked until you step in",
        task_needs_human_input_participant:
          "The Agent marked the conversation as waiting for a reply",
        task_review_requested_participant:
          "The Agent marked the conversation as waiting for review",
      },
    },
    title: "Push notifications",
    description:
      "Choose what each channel sends. Set which events appear in your inbox on the inbox channel page.",
    back: "Notifications",
    channels: {
      desktop: "Desktop",
      browser: "Browser",
      slack: "Slack",
      feishu: "Feishu",
      lark: "Lark",
      dingtalk: "DingTalk",
      wecom: "WeCom",
      mobile: "Mobile",
      email: "Email",
    },
    status: {
      off: "Off",
      allOn: "On for all notifications",
      allOff: "All notification types off",
      named: "Enabled: {names}",
      some: "Enabled: {names} and {count} more",
      separator: ", ",
      soon: "Coming soon",
      unbound: "Not linked",
      chooseBot: "No notification bot selected",
      unavailable: "Email delivery is not configured",
      connect: {
        slack: "Slack workspace not connected",
        feishu: "Feishu assistant not connected",
        lark: "Lark assistant not connected",
        dingtalk: "DingTalk assistant not connected",
      },
    },
    integration: {
      title: "{name} not connected",
      description:
        "Connect the matching integration before notifications can be sent to this channel.",
      open: "Open integrations",
      toggle: "Show {name} notifications",
      toggleHelp: "After connecting, these events are sent to {name}.",
      bindFeishu: "Link your Feishu account",
      feishuNeedAdmin:
        "An admin needs to connect Feishu first, then you can link your Feishu account.",
      feishuBindDesc:
        "Link your Feishu account before notifications can be sent to Feishu.",
      bindWecom: "Link WeCom account",
      wecomBindDesc:
        "Link your WeCom account with a one-time code, then choose a bot for personal notifications.",
      wecomSelectDesc:
        "Choose one of your linked bots as the personal notification target in WeCom integrations.",
    },
    detail: {
      feishuTitle: "Show Feishu notifications",
      feishuHelp:
        "Feishu notifications are only sent to the Feishu accounts you linked in each organization.",
      wecomTitle: "Send WeCom notifications",
      wecomHelp:
        "New notifications go to a private chat with your selected WeCom bot. Direct messages require separate opt-in.",
      wecomVia: "Sent through a private chat with {name}",
      emailTitle: "Send email notifications",
      emailHelp:
        "New notifications send a safe summary and inbox link one by one. Earlier notifications are not resent.",
      emailUnavailable:
        "SMTP is not configured on the server. Ask an administrator to enable email delivery.",
      feishuVia:
        "Sent as a private chat from the Feishu bot · linked as {name}",
      reset: "Restore defaults",
      resetDone: "Notification defaults restored",
      resetFailed: "Couldn't restore notification defaults",
      saveFailed: "Couldn't update notification settings",
      desktopTitle: "Show desktop notifications",
      desktopHelp:
        "Show a native notification banner in the desktop app when these events happen.",
      browserTitle: "Show browser notifications",
      browserHelp:
        "Show a native notification banner in this browser when these events happen. This setting follows your account and applies to any browser you sign in to.",
      permission: {
        title: "Browser permission",
        help: "Allow this browser to show native banners for new inbox notifications.",
        enable: "Enable",
        enabled: "Enabled",
        enabledHelp: "This browser can show native notification banners.",
        blocked:
          "Notifications are blocked by the browser. Allow notifications for this site in your browser settings.",
      },
    },
    events: {
      title: "Event types",
      description:
        "Choose which events this channel sends. Inbox delivery is configured separately.",
      assign: "Assignments",
      assignHelp: "When a task is assigned to you or unassigned from you",
      status: "Status changes",
      statusHelp: "When a task's status, priority or blocking relations change",
      comment: "Comments and replies",
      commentHelp: "When comments, replies or topic resolutions happen",
      mention: "Mentions",
      mentionHelp: "When you are mentioned in a comment or content",
      agent: "Agent activity",
      agentHelp: "When an agent task completes or fails",
      automation: "Automation runs",
      automationHelp: "When an automation run completes or fails",
    },
    alert: {
      fallbackTitle: "OpenFox",
    },
  },
  // Stage 4c · Get started
  getStarted: {
    title: "Get started with OpenFox",
    subtitle: "Complete these steps to get the most out of OpenFox.",
    allDone: "All done!",
    allDoneSubtitle: "You've completed every step. Well done!",
    scored: "Points earned",
    guideTitle: "Setup guide",
    guideDescription: "Follow these steps to set up OpenFox for your team.",
    hideGuide: "Hide sidebar guide",
    showGuide: "Show sidebar guide",
    soon: "Coming soon",
    points: "+{points}",
    progress: "{done} / {total}",
    loadFailed: "Couldn't load your progress",
    categories: {
      get_started: "Get started",
      collaboration: "Collaboration",
      agents: "Agents",
      tasks: "Tasks",
      space_management: "Space management",
      automations: "Automations",
    },
    steps: {
      connect_machine: {
        title: "Connect a computer",
        description:
          "Connect your computer or server to OpenFox. Agent tasks run on this machine. Install the CLI and sign in; you can add more later.",
        cta: "Connect computer",
      },
      create_agent: {
        title: "Create your first agent",
        description:
          "An agent is a role that works for you. Give it a name, pick a model and an execution computer, and it can take tasks like a teammate.",
        cta: "Create agent",
      },
      create_task: {
        title: "Create your first task",
        description:
          "Write down what needs doing. Make the title state the goal and add context and acceptance criteria to the description, so agents and teammates stay aligned.",
        cta: "New task",
      },
      delegate_task_to_agent: {
        title: "Assign a task to an agent",
        description:
          "Set an agent as the task's assignee and it starts working on its own. Progress and output show up in the task in real time.",
        cta: "Assign to agent",
      },
      connect_jira_or_tapd: {
        title: "Import tasks from Jira, TAPD, Zentao and other platforms",
        description:
          "Import existing tasks (requirements, bugs and more) and assign them straight to an agent.\nYou can migrate them over, or use two-way sync so both sides coexist.",
        cta: "Connect",
      },
      invite_member: {
        title: "Invite team members",
        description:
          "Add teammates to the organization or a space so they can see tasks, join discussions and help maintain the workflow.",
        cta: "Invite members",
      },
      assign_task_person: {
        title: "Assign a task to a member",
        description:
          "Name a clear owner for the task so the team knows who is driving it and who should post updates.",
        cta: "Assign member",
      },
      comment_or_reply: {
        title: "Comment and reply on a task",
        description:
          "Keep questions, conclusions and background in the task so the next person doesn't have to dig through chat history.",
        cta: "Post a comment",
      },
      process_inbox: {
        title: "Work through tasks in your inbox",
        description:
          "Handle assignments, mentions and pending items in one place, and clear the tasks that need your response first.",
        cta: "Open inbox",
      },
      clear_inbox: {
        title: "Clear tasks from your inbox",
        description:
          "Handle or archive read notifications so your inbox only keeps the tasks that need follow-up.",
        cta: "Open inbox",
      },
      configure_agent_instructions: {
        title: "Write instructions for an agent",
        description:
          "Spell out the agent's responsibilities, boundaries and delivery standards. It follows these rules when it runs tasks.",
        cta: "Edit instructions",
      },
      chat_with_agent: {
        title: "Chat with your agent",
        description:
          "Add context, ask about progress or ask the agent to explain its results. A quick way to steer a task.",
        cta: "Chat with agent",
      },
      share_agent_to_space: {
        title: "Share an agent to a space",
        description:
          "Make a configured agent available to a space so every task there can use the same working role.",
        cta: "Share agent",
      },
      create_crew: {
        title: "Create an agent squad",
        description:
          "Combine several agents into a squad so different roles can work together on more complex tasks.",
        cta: "Create squad",
      },
      install_skill: {
        title: "Install a skill",
        description:
          "Install a common workflow or tool capability that you can reuse in agents or automations later.",
        cta: "Install skill",
      },
      attach_skill_to_agent: {
        title: "Attach a skill to an agent",
        description:
          "Assign an installed skill to an agent so it can call the matching workflow and tools while running tasks.",
        cta: "Attach skill",
      },
      follow_task: {
        title: "Follow a task",
        description:
          "Follow tasks you care about without owning them. Progress, comments and status changes will be easier to notice.",
        cta: "Open task",
      },
      add_label: {
        title: "Add task labels",
        description:
          "Use labels to mark task type, priority or source. Filtering and bulk actions get faster.",
        cta: "Manage labels",
      },
      create_subtask: {
        title: "Create a subtask",
        description:
          "Break a large task into small steps that you can assign and track one by one.",
        cta: "New subtask",
      },
      link_related_task: {
        title: "Link related tasks",
        description:
          "Connect tasks that depend on each other or share background, so you can see the impact without searching.",
        cta: "Open task",
      },
      create_view: {
        title: "Create a custom view",
        description:
          "Save a filter by assignee, status, label and more to quickly see the set of tasks you handle most.",
        cta: "New view",
      },
      save_org_view: {
        title: "Save a team view",
        description:
          "Share a common view with the team so everyone looks at tasks and progress the same way.",
        cta: "Share view",
      },
      create_project: {
        title: "Create a project",
        description:
          "Group goal-related tasks into a project and manage scope, progress and owners in one place.",
        cta: "New project",
      },
      link_task_to_project: {
        title: "Put a task in a project",
        description:
          "Add the task to its project so the project page can summarize progress and remaining work accurately.",
        cta: "Link project",
      },
      configure_task_type_field: {
        title: "Configure fields for a task type",
        description:
          "Set the information each task type needs, such as priority, module, version or acceptance criteria.",
        cta: "Configure fields",
      },
      manage_task_status: {
        title: "Manage task statuses",
        description:
          "Adjust the status flow in a space so tasks move from to-do to done in a way that fits your team.",
        cta: "Manage statuses",
      },
      create_sprint: {
        title: "Turn on space iterations",
        description:
          "Enable an iteration cadence for the space and plan tasks into fixed cycles to track delivery.",
        cta: "Turn on iterations",
      },
      enable_space_estimation: {
        title: "Turn on task estimates",
        description:
          "With estimates on, the team can add effort to tasks, which helps scheduling and iteration capacity planning.",
        cta: "Turn on estimates",
      },
      connect_git: {
        title: "Link a Git repository",
        description:
          "Configure a Git repository for the space so the code agents commit can be traced to specific tasks.",
        cta: "Configure repository",
      },
      connect_feishu: {
        title: "Integrate Feishu, DingTalk, WeCom and other IM platforms",
        description:
          "Bring agents into your team's IM platform.\nMention an agent in an IM group so it can read the context, turn it into tasks and assign them to agents.",
        cta: "Connect platform",
      },
      create_automation: {
        title: "Create an agent automation",
        description:
          "Let agents work on their own.\nTriggers: schedule, task status change, comment keyword and webhook.",
        cta: "New automation",
      },
      run_automation: {
        title: "Run an automation once",
        description:
          "Trigger an automation manually and check the result to confirm the rules, permissions and output are as expected.",
        cta: "Open automation",
      },
      create_scheduled_automation: {
        title: "Create a scheduled automation",
        description:
          "Run a workflow at a fixed time. Good for daily reports, checks, syncs and recurring reminders.",
        cta: "New automation",
      },
      create_task_status_automation: {
        title: "Create a task-status automation",
        description:
          "Run actions automatically when a task enters a given status, such as reminding the assignee, syncing a channel or updating a field.",
        cta: "New automation",
      },
      create_comment_automation: {
        title: "Create a comment automation",
        description:
          "Start a workflow from new comments or keywords, turning feedback, approvals and extra information into actions promptly.",
        cta: "New automation",
      },
      create_webhook_automation: {
        title: "Create a webhook automation",
        description:
          "Start a workflow when an external system sends a webhook, bringing code, alerts or business events into task collaboration.",
        cta: "New automation",
      },
    },
  },
  // Stage 4c · Beta features
  beta: {
    title: "Beta features",
    empty: "No beta features available",
    emptyHelp:
      "New beta features will appear here when they are ready to turn on.",
    saveFailed: "Couldn't update beta feature",
  },
};
