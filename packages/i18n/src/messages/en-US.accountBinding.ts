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
  notReady: {
    github: "Ask an admin to connect GitHub first",
    jira: "Ask an admin to import a project through the Jira API first",
    feishu: "An admin needs to connect the Feishu assistant first",
    gitlab_cn: "Ask an admin to connect JiHu GitLab.cn first",
    gitlab: "Ask an admin to connect self-managed GitLab first",
    gitee: "Ask an admin to connect Gitee first",
  },
  purpose: {
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
    tapd: "Link your TAPD personal access token so changes and comments on synced TAPD items are attributed to you.",
    zentao:
      "Link your personal ZenTao account so synced comments are posted as you.",
    pingcode:
      "Link your personal token so new comments sync to PingCode as you.",
    teambition:
      "Connect your Teambition account so synced tasks and comments are created as you.",
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
