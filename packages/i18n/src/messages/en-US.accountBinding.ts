import type { Messages } from "../translate";

/** Stage 6e · Third-party account binding (English). */
export const accountBindingEn: Messages["accountBinding"] = {
  title: "Third-party accounts",
  description:
    "Link third-party accounts to sign in or keep your actions attributed to you across apps.",
  loadFailed: "Failed to load. Please refresh and try again.",
  comingSoon: "Coming soon",
  manage: "Manage",
  bind: "Link",
  connected: "Connected",
  update: "Update",
  disconnect: "Disconnect",
  menu: "{name} actions",
  disconnectHost: "Disconnect {host}",
  linkOther: "Link another instance",
  linkHost: "Link {host}",
  pickInstance: "Choose a {name} instance",
  connectedToast: "Connected",
  disconnectedToast: "Disconnected",
  disconnectTitle: "Disconnect {name}?",
  disconnectBody:
    "Synced content will no longer be attributed to you. Existing external account links are kept; admins can change them under External accounts.",
  popupBlocked: "Your browser blocked the authorization popup.",
  openAuthorize: "Open the authorization page in a new tab",
  waiting: "Waiting for authorization to finish...",
  teambition: {
    title: "Confirm your Teambition identity",
    description:
      "Verify identity for future import attribution in this enterprise. No personal token is stored or remote comments sent.",
    confirm: "Confirm this identity",
    reverify: "Verify {company} again",
    back: "Back to third-party accounts",
    identity:
      "Your enabled internal enterprise member email must match your OpenFox email. Check the account and authorize again.",
    permission:
      "This application cannot verify enterprise membership. Contact an administrator before authorizing again.",
    rate: "Teambition is rate limiting requests. Authorize again later.",
    changed:
      "The organization connection or your identity changed. Authorize again.",
    failed:
      "Authorization did not finish or has expired. Go back and start again.",
  },
  notReady: {
    zoho_bugtracker: "An administrator must connect a Zoho portal first.",
    bitbucket:
      "Ask an administrator to connect a Bitbucket workspace and configure a separate account-only consumer.",
    shortcut: "Ask an administrator to connect a Shortcut workspace first",
    azure_devops:
      "An administrator must connect an Azure DevOps organization and configure the separate identity application.",
    asana:
      "Ask an administrator to connect an Asana workspace and configure the separate identity app.",
    teambition:
      "Ask an admin to connect the Teambition app and configure its callback domain and enterprise read permission",
    tapd: "Ask an admin to connect a TAPD Basic API account that can read project member emails",
    yunxiao: "Ask an administrator to connect the Yunxiao organization first",
    github: "Ask an admin to connect GitHub first",
    jira: "Ask an admin to import a project through the Jira API first",
    feishu: "An admin needs to connect the Feishu assistant first",
    gitlab_cn: "Ask an admin to connect JiHu GitLab.cn first",
    gitlab: "Ask an admin to connect self-managed GitLab first",
    gitee: "Ask an admin to connect Gitee first",
  },
  purpose: {
    zoho_bugtracker:
      "A separate OAuth flow verifies your login_id, active membership and email in the same portal. Only identity facts are saved, never personal tokens. It suggests source reporter mappings in future import activities; it does not change local creators or assign tasks.",
    bitbucket:
      "Verify your UUID once to identify future PR and PR comment activity authors. No personal token is retained; this does not prove ongoing workspace membership.",
    shortcut:
      "Verify your identity for future task imports; optionally authorize new comments to be sent as you.",
    azure_devops:
      "One OAuth authorization verifies your identity in the selected organization. Personal tokens are not retained and email is not used for matching.",
    asana:
      "Verify your own email and identity for future new tasks in this workspace. Personal tokens are not stored and comments are not sent. Verify again after parent authorization, email or membership changes. Revoking access in Asana does not immediately remove the local mapping.",
    yunxiao:
      "Verify your identity for future import matching. Your PAT is not stored and no write access is granted.",
    wechat:
      "Chat with your Agents right in WeChat. Bind Agents by scanning on the WeChat integration page; they are summarized here.",
    github:
      "Link your GitHub account so synced issues, comments and PRs are attributed to you.",
    gitlab_cn:
      "Link your JiHu GitLab account so synced issues, comments and merge requests are attributed to you.",
    gitlab:
      "Link your account on a self-managed GitLab instance so synced issues, comments and merge requests are attributed to you.",
    gitee:
      "Link your Gitee account so synced issues, comments and pull requests are attributed to you.",
    jira: "Attribute your participation in Jira tasks to you.",
    tapd: "Verify your TAPD identity once to suggest attribution for future imports in selected projects. No personal token is stored or comments sent.",
    zentao:
      "Link your personal ZenTao account so synced comments are posted as you.",
    pingcode:
      "Link your personal token so new comments sync to PingCode as you.",
    teambition:
      "Verify your Teambition enterprise identity for future import attribution. No personal token is stored or remote comments authorized.",
    slack: "An admin must connect the Slack workspace first",
    feishu: "An admin must connect the Feishu assistant first",
    lark: "An admin must connect the Lark assistant first",
    dingtalk: "An admin must connect the DingTalk assistant first",
  },
  purposeReady: {
    gitlab_cn:
      "Once linked, your issues, comments and merge requests on JiHu GitLab are attributed to you. Link each instance separately.",
    gitlab:
      "Once linked, your issues, comments and merge requests on GitLab are attributed to you. Link each instance separately.",
    gitee:
      "Once linked, your issues, comments and pull requests on Gitee are attributed to you.",
    feishu:
      "Once linked, the Feishu bot sends you OpenFox notifications and you can use the task assistant and agents in Feishu.",
    lark: "Once linked, the Lark bot sends you OpenFox notifications and you can use the task assistant and agents in Lark.",
    dingtalk:
      "Once linked, the DingTalk bot sends you OpenFox notifications and you can use the task assistant and agents in DingTalk.",
  },
  shortcut: {
    title: "Shortcut identity and comment access",
    workspace: "Shortcut workspace",
    manageWorkspace: "Manage identity and comments for {workspace}",
    explanation:
      "Verify yourself only in the selected workspace, using email {email}. Only future automatic attribution changes; manual mappings and historical tasks stay. Shortcut incoming and historical comments are not imported.",
    identity: "Verified: {name}",
    identityOnly: "Identity only",
    commentsOn: "New comments authorized",
    allowComments: "Allow new comments to be sent as me",
    identityHelp:
      "Your token is used once to verify identity, then discarded. No comments are sent. This is a one-time proof; remote membership changes do not immediately update this binding.",
    commentsWarning:
      "Your personal token will be encrypted and stored, only to send your new comments after this authorization through existing two-way sync links. Shortcut V3 tokens have broad platform access. Comments with an unknown result are never retried automatically.",
    token: "Personal Shortcut V3 token",
    tokenHelp:
      "Use your own V3 token for the selected workspace. A token is required to link, reverify or enable comments; it is not displayed or persisted in the browser.",
    disableHelp:
      "Leave this empty to disable comments. Stored credentials are deleted while your identity binding and comments already sent remain.",
    save: "Verify and save",
    disableComments: "Disable comment access",
    saved: "Shortcut identity and comment settings saved",
    disconnectBody:
      "Future automatic attribution will stop using this identity, comments not yet being sent will stop, and stored personal credentials will be cleared. Manual mappings, historical tasks and comments already sent remain. Requests already being sent may finish.",
    errors: {
      auth: "The token is invalid or the Shortcut account is not enabled or has been deactivated.",
      email:
        "Your Shortcut email is missing or does not match your OpenFox email.",
      workspace:
        "This token belongs to a different workspace from the selected organization connection.",
      inUse:
        "This Shortcut identity is already linked. Disconnect before changing identity.",
      rate: "Shortcut is rate limiting requests. Try again later.",
    },
  },
  tapd: {
    title: "Verify TAPD personal identity",
    explanation:
      "Identity verification only. Your personal token is not stored and no comments are sent. The project member email must match {email}. Only future automatic attribution changes; manual choices and historical tasks stay unchanged.",
    parent: "TAPD organization connection",
    project: "TAPD project ID",
    projectHelp:
      "Verify one accessible project at a time, up to 20. Removing the last project disconnects this identity. Reverify each project after changing your email, nickname, or organization credentials.",
    token: "User OAuth Access Token",
    tokenHelp:
      "Use a user token from your authorized TAPD application with access to your identity and the selected project. An enterprise API password cannot be used here.",
    verify: "Verify and add project",
    identity: "Verified nickname: {nick}",
    scopes: "Verified project scopes",
    remove: "Remove project {project}",
    errors: {
      auth: "The user token is invalid or the TAPD account is inactive, frozen or departed.",
      project:
        "The project and exact member could not be verified. Check project access, company and nickname.",
      email:
        "Your project member email is missing or does not match your OpenFox email.",
      basic:
        "An active Basic enterprise API connection that can read project member emails is required. Token-based parent connections cannot verify your email here.",
      inUse:
        "This TAPD identity is already bound. Disconnect before changing identity or parent connection.",
      limit:
        "A binding can include up to 20 projects. Remove an unused project first.",
      rate: "TAPD temporarily limited requests. Try again later.",
    },
  },
  jira: {
    title: "Link Jira account",
    description:
      "Link your personal Jira account on {host} so synced tasks and comments are written back as you.",
    site: "Jira site",
    email: "Jira email",
    emailHelp: "Must match your OpenFox email",
    apiToken: "API token",
    apiTokenHelp:
      "Create an API token on the Security page of your Atlassian account and paste it here.",
    pat: "Personal access token",
    patHelp:
      "Create one on the Personal Access Tokens page of your Jira profile and paste it here.",
    notStored:
      "The token is only used to confirm your identity. OpenFox does not store it.",
    submit: "Connect",
    linked: "{n} Jira external accounts linked automatically",
    errors: {
      JIRA_AUTH_FAILED: "Jira rejected the credentials. Check them and retry.",
      JIRA_EMAIL_MISMATCH: "The Jira email must match your OpenFox email.",
      JIRA_ACCOUNT_IN_USE:
        "This Jira account is already linked to another member of this organization.",
      JIRA_UNAVAILABLE: "Can't reach Jira right now. Try again later.",
      JIRA_BLOCKED_ADDRESS: "This Jira address isn't allowed.",
      JIRA_SITE_UNKNOWN:
        "This Jira site isn't in this organization's import history.",
      fallback: "Couldn't connect. Please try again.",
    },
  },
};
