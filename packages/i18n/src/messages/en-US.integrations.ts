import type { Messages } from "../translate";

/** Stage 6a · External app integrations (English). Provider names are brand names and stay untranslated. */
export const integrationsEn: Messages["integrations"] = {
  sitePersonal: {
    title: "Connect your {brand} account",
    zenDescription:
      "Bind your own ZenTao account to reply to synced tasks as yourself.",
    jiraDescription:
      "Bind your own Jira account so synced comments are posted as you. Otherwise the integration account is used.",
    zenNote:
      "After validation, your account and password are encrypted and used only to post your synced comments. Disconnecting stops your comment sync without changing the organization connection.",
    jiraNote:
      "Your credentials are encrypted and used only to post your own synced comments. Existing identity-only bindings are kept.",
    instance: "Instance",
    auth: "Authentication",
    account: "Account or email",
    password: "Password",
    token: "Token",
  },
  title: "Integrations",
  description:
    "Connect task trackers, code hosts and collaboration tools so data and notifications flow between OpenFox and external apps.",
  loadFailed: "Failed to load. Please try again later.",
  notFound: "This provider was not found",
  comingSoon: "Coming soon",
  comingSoonDesc: "The {name} integration is coming soon.",
  openSoon: "Available soon",
  groups: {
    enabled: "Enabled",
    task: "Task management",
    git: "Git repositories",
    collaboration: "Collaboration",
  },
  enabled: {
    instances: "{count} {name} instance(s) bound",
    githubOrgs: "{count} GitHub organization(s) bound",
    accounts: "{count} {name} account(s) bound",
    enterprise: "Workspace connected",
    connections: "{count} connection(s) bound",
  },
  status: {
    ok: "Healthy",
    attention: "Needs attention",
    bound: "Bound",
    needsAttention: "Needs attention",
    disconnected: "Disconnected",
  },
  templateDesc: {
    A: "Sync {name} task data and import historical issues.",
    B: "Link {name} repositories, pull requests and commits.",
    C1: "Connect a {name} enterprise app for notifications, the task assistant and Agents.",
    C2: "Connect {name} with bot credentials for the task assistant and Agents.",
    C3: "Bind Agents to {name}.",
    C4: "Connect a {name} workspace for notifications, the task assistant and Agents.",
  },
  readOnly: "Only organization owners and admins can bind {name}",
  notConfigured: "The server has not been configured with a {name} app yet",
  wechat: {
    cardTitle: "Talk to an Agent from WeChat",
    cardDesc:
      "Pick an Agent and confirm by scanning with WeChat; that WeChat account can then chat with it directly. Messages run as you. Text messages only.",
    bindButton: "Scan to bind",
    noPermission:
      "Only organization owners, admins, or the Agent's owner can bind",
    notConfigured:
      "The server has not configured WeChat (ClawBot) access, so binding is unavailable.",
    limit: "You can bind at most {max} WeChat accounts. Unbind one first.",
    listTitle: "Bound WeChat accounts",
    empty: "No WeChat accounts bound yet.",
    emptyDesc:
      "Once bound, you can message the Agent in WeChat and get replies there.",
    initiator: "Initiated by {name}",
    menu: "More actions for {name}",
    unbind: "Unbind",
    unbindTitle: "Unbind WeChat from {name}?",
    unbindBody:
      "This WeChat account will no longer be able to chat with the Agent. Existing OpenFox conversations are kept. You can scan again later.",
    unbound: "WeChat unbound",
    rescan: "Scan again",
    status: {
      connected: "Connected",
      connecting: "Connecting",
      expired: "Login expired",
    },
    expiredHint: "The WeChat login has expired. Please scan again.",
    dialog: {
      title: "Bind WeChat by scanning",
      subtitle: "Choose the Agent to bind, then scan with WeChat to confirm.",
      agent: "Agent",
      agentPlaceholder: "Select an Agent",
      agentNone: "No Agent available to bind.",
      mine: "Mine",
      org: "Organization",
      next: "Next",
      generating: "Generating QR code…",
      qrLabel: "WeChat binding QR code",
      hint: "Scan the QR code with WeChat and confirm on your phone.",
      scanned: "Scanned. Please confirm in WeChat.",
      waiting: "Waiting for scan confirmation…",
      verifyHint: "Enter the verification code shown in WeChat.",
      verifyLabel: "Verification code",
      verify: "Submit",
      verifying: "Verifying…",
      cancel: "Cancel",
      restart: "Generate a new QR code",
      success: "WeChat bound",
    },

    errors: {
      generic: "Something went wrong. Please try again later.",
      unavailable: "WeChat is temporarily unavailable. Please try again later.",
      versionConflict: "The binding changed. Refresh and try again.",
      expired: "The QR code has expired. Generate a new one.",
      blocked: "Too many wrong verification codes. Generate a new QR code.",
      untrusted_host:
        "WeChat returned an untrusted address; binding was refused.",
      invalid_response: "WeChat returned an invalid result. Please retry.",
      forbidden: "You no longer have permission to bind this Agent.",
      limit_reached: "Binding limit reached. Unbind an unused WeChat first.",
      bot_in_use: "This WeChat bot is already bound. Please try again later.",
      already_bound:
        "This WeChat account is bound elsewhere. Use another account.",
      cancelled: "Binding was cancelled.",
    },
  },
  sections: {
    bind: "Binding",
    personal: "My {name} account",
    sync: "Sync issue data",
    syncImport: "Sync & import issue data",
    pr: "Git PR & commit linking",
    more: "More settings",
    assistant: "Task assistant",
    agents: "Bound Agents",
  },
  sectionDesc: {
    bind: "An organization owner or admin binds {name} for the whole organization.",
    personal:
      "Bind your own {name} account so synced changes are attributed to you.",
    sync: "Connect external projects to spaces. Only new changes are synced after you turn it on.",
    pr: "Put a task identifier (for example OPEN-12) in a PR title, description or branch name to link it to the task automatically. Consider documenting this in the repository's AGENTS.md.",
    more: "Suggested branch naming format, used to link tasks automatically.",
    assistant: "Configure a task assistant for each space you have joined.",
    agents:
      "Bind Agents to {name} so they can receive and handle messages there.",
  },
  disabled: {
    assistant:
      "The {name} task assistant is not available yet. You will configure it per space here once it opens.",
    agents:
      "Agent binding for {name} is not available yet. You will manage it here once it opens.",
  },
  emptyBind: "{name} is not bound yet",
  emptyPersonal: "You have not bound a personal {name} account",
  emptySync: "No project-to-space connections yet",
  bind: "Bind",
  bindMine: "Bind my account",
  importIssues: "Import issue data",
  newSync: "New sync",
  prTitle: "Link PRs and commits automatically",
  branchTitle: "Branch format",
  enabledBy: "Enabled by {email} on {date}",
  rowMenu: "More actions for {name}",
  disconnect: "Disconnect",
  disconnected: "Disconnected",
  disconnectTitle: "Disconnect {name}?",
  disconnectBody:
    "This only deactivates the binding and its sync connections. No tasks or projects are deleted. Personal accounts under this binding are disconnected too.",
  disconnectPersonalTitle: "Disconnect your {name} account?",
  disconnectPersonalBody:
    "This only removes your personal credentials. The organization binding is not affected.",
  confirmLabel: "Type “{phrase}” to confirm",
  confirmInput: "Confirmation phrase",
  sync: {
    active: "Syncing",
    paused: "Paused",
    needsAttention: "Needs attention",
    inbound: "GitHub → OpenFox",
    bidirectional: "GitHub ↔ OpenFox",
    bidirectionalDesc:
      "Tasks created in OpenFox automatically create Issues in the repository; title, description, status, assignee and comments sync both ways",
    needAdmin: "Only organization owners and admins can create a sync",
    needConnection: "Bind a GitHub organization first",
    dialogTitle: "New project sync",
    confirm: "Start syncing",
    created: "Sync created",
    pickAccount: "GitHub organization or account",
    pickRepo: "Repository",
    searchRepo: "Search repositories",
    noRepos: "No repositories available",
    private: "Private repository",
    linkedTo: "Linked to {space}",
    prevPage: "Previous",
    nextPage: "Next",
    pickSpace: "Target space",
    pickSpacePlaceholder: "Choose a space",
    direction: "Sync direction",
    importExisting: "Import existing open Issues",
    importExistingDesc:
      "Backfills up to 5,000 open Issues and keeps syncing future changes. No separate import record or preview is created; removing the sync keeps imported tasks.",
    webhookReminder:
      "Remember to select the Issues and Issue comments events in the GitHub webhook.",
    openGuide: "Show webhook guide",
    openRepo: "Open repository",
    edit: "Edit",
    editTitle: "Edit sync for {name}",
    pause: "Pause",
    resume: "Resume",
    remove: "Remove",
    removeTitle: "Remove sync for {name}?",
    removeBody: "Tasks and Issues are both kept. They just stop syncing.",
    removed: "Sync removed",
    lastSynced: "Last synced {time}",
    neverSynced: "Not synced yet",
    backfillRunning: "Importing existing Issues · {n} imported",
    backfillFailed: "Importing existing Issues failed: {error}",
    outboundFailed: "{n} items could not be synced to GitHub",
    retry: "Retry",
    retried: "Requeued",
    errors: {
      linkExists: "This repository is already linked to another space",
      bidirectionalExists: "This space already has a two-way sync",
      needsReauth:
        "GitHub authorization expired. Reauthorize in the section above",
      invalidRepository:
        "The repository does not belong to the selected GitHub organization or account",
      upstream:
        "The connected service is temporarily unreachable. Please try again later",
    },
  },
  errors: {
    versionConflict: "Someone else changed this. Refreshed.",
    mismatch: "The confirmation phrase is incorrect",
    forbidden: "Only organization owners and admins can do this",
    generic: "Something went wrong. Please try again later.",
  },
  github: {
    bindOrg: "Bind GitHub organization",
    popupBlocked:
      "Your browser blocked the authorization popup. Click the link below to continue.",
    openAuthorize: "Open GitHub authorization",
    waiting: "Waiting for GitHub authorization…",
    starting: "Starting authorization…",
    selectTitle: "Choose GitHub organizations to bind",
    selectDesc:
      "Select the organizations or accounts to bind to OpenFox. Already bound ones will get a refreshed authorization.",
    kindOrg: "Organization",
    kindUser: "Personal account",
    alreadyBound: "Bound · authorization will be updated",
    selectNone: "No accounts available to bind",
    confirmBind: "Bind selected",
    boundToast: "GitHub bound",
    reauthorizedToast: "Reauthorized",
    personalToast: "GitHub account linked",
    enabler: "Enabled by",
    enabledOn: "Enabled on {date}",
    needsReauth: "Reauthorization needed",
    reauthorize: "Reauthorize",
    reconnect: "Reconnect",
    disconnectBody:
      "Disconnect {org}? Pull Request links and task backlinks from this organization will stop working.",
    errors: {
      access_denied: "You canceled the authorization",
      exchange_failed: "GitHub authorization failed. Please try again",
      account_mismatch:
        "The authorized GitHub account cannot access this organization",
      invalid_state: "The authorization expired. Please try again",
    },
    personalDesc:
      "Once linked, Pull Requests you open on GitHub show up as your own actions.",
    linkAccount: "Link GitHub account",
    syncSoon: "Coming soon",
    webhook: {
      configure: "Configure webhook",
      connected: "Webhook connected · last delivery {time}",
      loadFailed: "Failed to load webhook settings",
      adminOnly:
        "Only organization owners and admins can view the webhook settings",
      step1: "Open the organization's Settings → Webhooks",
      step2: "Click Add webhook and fill in the values below",
      payloadUrl: "Payload URL",
      contentType: "Content type",
      secret: "Secret",
      events: "Events",
      eventsDesc:
        "Choose Let me select individual events, then select Pull requests, Pushes, Issues and Issue comments",
      copy: "Copy",
      copied: "Copied",
      show: "Show",
      hide: "Hide",
      rotate: "Regenerate",
      rotateTitle: "Regenerate webhook?",
      rotateBody:
        "The old URL stops working immediately. Update the Payload URL and Secret on GitHub.",
      rotated: "Regenerated",
      recent: "Recent deliveries",
      noDeliveries: "No deliveries yet",
      event: "Event",
      status: "Result",
      detail: "Detail",
      linked: "Linked tasks",
      time: "Time",
      statuses: {
        PROCESSED: "Processed",
        IGNORED: "Ignored",
        REJECTED: "Rejected",
        FAILED: "Failed",
      },
    },
    pr: {
      needWebhook:
        "Webhook required · this feature needs a webhook configured on GitHub",
      linkPullRequests: "Link Pull Requests",
      linkPullRequestsDesc:
        "Link automatically when a task identifier appears in the PR title, description or branch name.",
      linkCommits: "Link Git commits",
      linkCommitsDesc:
        "Link automatically when a task identifier appears in the commit message.",
      backlink: "Pull Request backlinks",
      backlinkDesc: "After linking, comment the task link on the PR.",
      backlinkPrivate: "Private repositories",
      backlinkPublic: "Public repositories",
      loadFailed: "Failed to load settings",
      saveFailed: "Failed to save. Please try again later",
    },
    issue: {
      backlink: "Issue backlinks",
      backlinkDesc:
        "Comment a task link on the GitHub issue that was synced as a task.",
      backlinkPrivate: "Private repositories",
      backlinkPublic: "Public repositories",
    },
    branchFormat: "Branch format",
    links: {
      title: "GitHub links",
      open: "Open",
      draft: "Draft",
      merged: "Merged",
      closed: "Closed",
      commit: "Commit",
    },
  },
  gitlab: {
    proxy: "Use an intranet proxy",
    noProxy: "Do not use an intranet proxy",
    tokenTitle: "Personal access token",
    tokenDescription:
      "Create a token with the api scope in GitLab user settings. Synced projects require Maintainer access or higher. Requires GitLab 15.5 or later.",
    tokenConnect: "Connect",
    tokenInvalid:
      "GitLab rejected this token. Check the token, api scope, and account.",

    // Stage 8 · GitLab self-managed / JiHu GitLab: shares most copy with GitHub (brand word is substituted); only the differences live here
    templateDesc: "Link {name} repositories, merge requests and commits.",
    sections: { pr: "Git MR & commit linking" },
    sectionDesc: {
      pr: "Put a task identifier (for example OPEN-12) in an MR title, description or branch name to link it to the task automatically. Consider documenting this in the repository's AGENTS.md.",
    },
    bindInstance: "Bind instance",
    bindTitle: "Bind {name}",
    bindDesc:
      "An admin creates an OAuth application on the instance and enters its credentials here. The credentials are stored encrypted and never shown again.",
    instanceUrl: "Instance URL",
    instanceUrlHint: "A public https address, e.g. https://gitlab.example.com",
    instanceUrlHintCn: "Leave empty to use the default {url}",
    instanceUrlRequired: "Enter the instance URL",
    applicationId: "Application ID",
    applicationSecret: "Application Secret",
    required: "Required",
    showSecret: "Show Application Secret",
    hideSecret: "Hide Application Secret",
    redirectUri: "Redirect URI",
    redirectGuide:
      "On the instance, go to User Settings → Applications, create an application with the api scope and use the address above as the Redirect URI.",
    submit: "Save and authorize",
    instanceOf: "Instance {host}",
    selectTitle: "Choose groups to bind",
    selectDesc:
      "Select the top-level groups or your personal account to bind to OpenFox. Already bound ones will get a refreshed authorization.",
    kindOrg: "Group",
    disconnectBody:
      "Disconnect {org}? Merge request links and task backlinks from this group will stop working.",
    personalDesc:
      "Once linked, merge requests you submit on {name} are attributed to you.",
    pickInstance: "Choose instance",
    linkAccount: "Link {name} account",
    noInstance:
      "No instance is bound yet, so personal accounts cannot be linked.",
    allLinked: "You have linked an account on every instance.",
    errors: {
      invalidInstanceUrl: "The instance URL must be a public https address.",
    },
    webhook: {
      step1: "Open Settings → Webhooks of the project or group",
      step2: "Click Add new webhook and fill in the values below",
      payloadUrl: "URL",
      secret: "Secret token",
      secretHint:
        "Enter it in the Secret token field. GitLab sends it in plain text as X-Gitlab-Token on every request; it is not an HMAC signature.",
      eventsDesc: "Under Trigger, tick: {events}",
      rotateBody:
        "The old address stops working immediately. Update the URL and Secret token in {name} too.",
    },
    pr: {
      needWebhook:
        "Webhook required · this feature needs a webhook configured on {name}",
      linkPullRequests: "Link merge requests (MR)",
      linkPullRequestsDesc:
        "Link automatically when a task identifier appears in the MR title, description or branch name.",
      backlink: "Merge request (MR) backlinks",
      backlinkDesc: "After linking, comment the task link on the MR.",
    },
    links: { title: "GitLab links", mixedTitle: "Git links" },
  },
  gitee: {
    // Stage 7g-3 · Gitee: same page structure as GitHub (one server-configured SaaS app, no instance URL); only the differing copy lives here.
    webhook: {
      step1: "Open the repository (or enterprise) Manage → WebHooks",
      step2: "Click Add and fill in the values below",
      payloadUrl: "URL",
      secret: "Password / signing secret",
      secretHint:
        "Use it either as the Password, or choose Signing secret and paste it there. In password mode Gitee sends it in clear text with each request; in signing mode requests are HMAC-SHA256 signed with a timestamp, and requests more than 1 hour away from the server clock are rejected.",
      eventsDesc: "Tick these WebHook events: {events}",
      rotateBody:
        "The old URL stops working immediately. Update the URL and password / signing secret in Gitee.",
    },
    links: { title: "Gitee links" },
  },
  feishu: {
    appId: "App ID: {id}",
    oauthOkTitle: "Feishu account linked",
    bind: {
      title: "Bind a Feishu app",
      empty: "No Feishu app is bound yet.",
      button: "Bind Feishu",
      adminOnly:
        "Only organization owners and admins can bind a Feishu app. Please contact one of them.",
    },
    status: {
      pending: "Pending",
      connected: "Connected",
      needsAttention: "Needs verification",
    },
    menu: {
      label: "Assistant connection options",
      verify: "Verify again",
      update: "Update credentials",
      guide: "Setup guide",
      verified: "Verified again",
    },
    linkPreview: {
      title: "Task link preview",
      desc: "When on, OpenFox task links sent in Feishu show the task title and status. Only people who linked a Feishu account and can view the task see the title.",
    },
    disconnectBody:
      "After disconnecting, OpenFox stops receiving Feishu events and sending Feishu notifications, and members' Feishu account links are removed.",
    diag: {
      credentialsInvalid:
        "The Feishu app credentials are no longer valid. Update them and verify again.",
      connectionLimit:
        "This Feishu app has reached its limit of 50 long connections. Close other programs using the app, then verify again.",
      pendingSlow:
        "Establishing the long connection… If it never connects, check that the server can reach the Feishu Open Platform and that Event & Callback is set to receive events over a long connection.",
      healthy: "Long connection is healthy · last event {time}",
      healthyNoEvent: "Long connection is healthy · no events received yet",
    },
    time: {
      justNow: "just now",
      minutes: "{n} min ago",
      hours: "{n} h ago",
      days: "{n} d ago",
    },
    dialog: {
      title: "Bind a Feishu app",
      subtitle:
        "Two steps: create the app on the Feishu Open Platform, then enter its credentials here.",
      step1Title: "Open the Feishu Open Platform",
      step1Desc:
        "Capabilities, permissions and callbacks are configured on the Feishu Open Platform. Follow the guide on the right.",
      openPlatform: "Open Feishu Open Platform",
      step2Title: "Enter App ID and App Secret",
      step2Desc:
        "Copy both values from the Credentials & Basic Info page. They are verified and the long connection starts right after saving.",
      appIdInvalid:
        "App ID starts with cli_ followed by 8–40 letters or digits.",
      secretInvalid:
        "The app secret must be 16–128 characters. Copy the complete value from the open platform.",
      showSecret: "Show App Secret",
      hideSecret: "Hide App Secret",
      submit: "Save and connect",
    },
    guide: {
      title: "Setup guide",
      intro:
        "Everything except the last step happens on the Feishu Open Platform, in this order.",
      stepOf: "Step {n} / {total}",
      previewOnly: "Link preview only",
      inOpenfox: "Do in OpenFox",
      s1Title: "Create a custom enterprise app",
      s1Desc: "Suggested app name: OpenFox.",
      s2Title: "Add app capabilities",
      s2Bot: "Bot (required for notifications)",
      s2Preview: "Link preview (required for task link preview). URL rule:",
      s3Title: "Grant permissions",
      s3Desc: "In Permission Management, batch import this scope JSON:",
      s4Title: "Configure events and callbacks",
      s4Mode: "Choose to receive events / callbacks over a long connection.",
      s4Events: "Subscribe to events:",
      s4Callbacks: "Subscribe to callbacks:",
      s4Redirect:
        "Add this redirect URL under Security Settings (needed for members to link their Feishu account):",
      s5Title: "Publish a version",
      s5Desc:
        "Create a version and submit it. It takes effect after an admin approves it.",
      s6Title: "Back in OpenFox",
      s6Desc: "Enter the App ID and App Secret on the left.",
      copyPattern: "Copy URL rule",
      copyScopes: "Copy scope JSON",
      copyRedirect: "Copy redirect URL",
    },
    toast: {
      bound: "Feishu app bound. Establishing the long connection…",
      updated: "Credentials updated",
      copied: "Copied",
      copyFailed:
        "Copy failed. Select the text and copy it manually; browsers disable the clipboard on insecure origins.",
    },
    errors: {
      credentialsInvalid:
        "The App ID or App Secret is incorrect. Copy them again from the Credentials & Basic Info page.",
      botUnavailable:
        "Could not read the bot info from Feishu. Make sure the Bot capability is enabled; notifications cannot be sent without it.",
      appInUse:
        "This Feishu app is already bound to another OpenFox organization. A Feishu app can only be bound to one organization.",
      appMismatch:
        "The App ID cannot change when updating credentials. To use another Feishu app, disconnect and bind again.",
      unavailable:
        "Cannot reach the Feishu Open Platform right now. Please try again later.",
      versionConflict:
        "The Feishu app was just modified by someone else. Refresh and try again.",
      notConnected:
        "This organization has no Feishu assistant yet. Ask an owner or admin to bind a Feishu app first.",
      account_in_use:
        "This Feishu account is already linked to another member of this organization.",
    },
    bots: {
      needApp: "An admin needs to connect the Feishu app first",
      status: {
        connecting: "Connecting",
        connected: "Connected",
        failed: "Connection failed",
      },
      retry: "Retry",
      retried: "Reconnecting",
      scan: {
        generating: "Generating QR code…",
        qrLabel: "Feishu authorization QR code",
        hint: "Scan with Feishu, or open the link, then finish creating the Feishu agent.",
        openLink: "Open link",
        waiting: "Waiting for you to authorize in Feishu…",
        remaining: "{time} left",
        cancel: "Cancel",
        restart: "Start over",
        startFailed: "Could not generate the QR code. Please try again later.",
        errors: {
          expired:
            "The link expired before it was authorized. Please start over.",
          access_denied: "Authorization was canceled in Feishu.",
          rejected: "Feishu rejected the request. Please try again.",
          agent_connected:
            "This agent is already connected to Feishu and cannot be created again.",
          assistant_exists: "This space already has a task assistant.",
          session_expired: "The session expired. Please start over.",
          superseded: "The session expired. Please start over.",
          save_failed: "Saving failed. Please try again.",
          generic: "Authorization was not completed. Please start over.",
        },
      },
      assistant: {
        cardTitle: "OpenFox task assistant",
        cardDesc:
          "Once a Feishu bot is bound, @ it in a Feishu group to turn the chat into tasks, or look up and update existing tasks.",
        rowTitle: "OpenFox task assistant - {space}",
        bound: "Bound to Feishu",
        unbound: "No Feishu bot bound yet",
        bind: "Bind",
        adminOnly: "Only space admins can bind",
        menu: "Task assistant options",
        edit: "Edit",
        remove: "Remove assistant",
        bindTitle: "Bind Feishu task assistant",
        bindSubtitle:
          'Choose the Runtime for the "{space}" task assistant, then scan with Feishu to create the bot.',
        runtime: "Runtime",
        runtimePlaceholder: "Select a Runtime",
        runtimeNone:
          "No Runtime is available. Connect a computer on the Computers page first.",
        extraPrompt: "Additional prompt",
        extraPromptPlaceholder: "Click to add an additional prompt",
        extraPromptHint:
          "The prompt for creating tasks in Feishu is built in. Only add conventions specific to this space.",
        viewBuiltin: "View built-in prompt",
        builtinTitle: "Built-in prompt",
        builtinDesc:
          "The prompt built into the Feishu task assistant. It cannot be edited. Add this space's conventions in the additional prompt.",
        next: "Next",
        editTitle: "Edit task assistant",
        editSubtitle:
          "Change the Runtime it runs on, or add a prompt just for this space.",
        save: "Save",
        removeTitle: "Remove the task assistant?",
        removeBody:
          "The bot in Feishu will stop responding. Existing tasks and conversations are kept. You need to delete the app on the Feishu Open Platform yourself.",
        removeConfirm: "Remove",
        bound_toast: "Feishu task assistant bound",
        updated: "Task assistant updated",
        removed: "Task assistant removed",
        failed: "Could not create the task assistant",
        exists:
          "This space already has a task assistant. Each space can have only one; remove the existing one first.",
        boundElsewhere:
          "A task assistant is already bound in {provider}. Each space can have only one; remove it in {provider} first.",
      },
      agents: {
        cardTitle: "Create a Feishu agent",
        cardDesc:
          "Pick an agent, then use the QR code or Feishu link to create a Feishu agent so you can chat with it in Feishu.",
        create: "Create Feishu agent",
        noPermission:
          "Only organization owners, admins or the agent's owner can create one",
        dialogSubtitle:
          "Choose the agent to connect to Feishu, then scan to finish.",
        agent: "Agent",
        agentPlaceholder: "Select an agent",
        agentNone: "No agent is available to connect.",
        mine: "Mine",
        org: "Organization",
        listTitle: "Connected agents",
        empty: "No agent is bound to Feishu yet.",
        emptyDesc:
          "Once an agent is connected, it can receive and reply to messages in Feishu.",
        menu: "More actions for {name}",
        disconnect: "Disconnect",
        disconnectTitle: "Disconnect {name} from Feishu?",
        disconnectBody:
          "The agent will stop responding to messages in Feishu. You need to delete the app on the Feishu Open Platform yourself.",
        connectedToast: "Feishu agent connected",
        disconnected: "Disconnected",
        exists: "This agent is already connected to Feishu",
      },
      errors: {
        versionConflict:
          "The bot was just changed by someone else. Refresh and try again.",
        unavailable:
          "Cannot reach the Feishu Open Platform right now. Please try again later.",
        notConnected: "An admin needs to connect the Feishu app first",
        generic: "The action did not complete. Please try again later.",
      },
    },
    personal: {
      desc: "Link your own Feishu account to receive OpenFox notifications in a private chat with the Feishu bot.",
      link: "Link Feishu account",
      emptyDesc:
        "Once linked, OpenFox sends you notifications in a private chat with the Feishu bot.",
      needAdminTitle: "An admin needs to connect the Feishu assistant first",
      needAdminDesc:
        "This organization has no assistant connected yet. An owner or admin must bind a Feishu app first.",
      bound: "Linked",
      boundToast: "Feishu account linked. Feishu notifications are on.",
      unbind: "Unlink",
      unbindTitle: "Unlink Feishu account",
      unbindBody:
        "After unlinking, you will no longer get notifications in Feishu.",
      unbound: "Feishu account unlinked",
      popupBlocked:
        "The browser blocked the authorization window. Use the link below to continue.",
      openAuthorize: "Open Feishu authorization",
      waiting: "Waiting for Feishu authorization…",
      identityHint:
        "Feishu uses this binding to identify you in the task assistant and Feishu agents.",
    },
  },
  // Stage 7g-1: IM provider names (brand names, used by cross-provider hints such as "already bound in X")
  imNames: {
    feishu: "Feishu",
    lark: "Lark",
    dingtalk: "DingTalk",
    wecom: "WeCom",
  },
  wecom: {
    connectHelp:
      "Create an intelligent bot with a long connection in WeCom, then enter its BotID and Secret. Members can bind their own WeCom account with a one-time code.",
    botId: "BotID",
    secret: "Secret",
    connect: "Connect bot",
    connectFailed:
      "Could not connect the WeCom bot. Check its BotID, Secret, and network connection.",
    noBots: "No WeCom bot is available",
    noBotsHelp:
      "An admin needs to connect a task assistant or agent bot first.",
    boundAs: "Linked WeCom account {user}",
    unbound: "Your account is not linked",
    bind: "Get one-time code",
    codeHelp:
      "Send this one-time code in a private chat with the selected WeCom bot. The code expires after binding.",
    codeExpires: "Code expires: {time}",
    codeFailed: "Could not create a code. Try again later.",
    unbind: "Unlink",
    unbindConfirm:
      "After unlinking, this bot will no longer recognize you or send you personal notifications.",
    unbindFailed: "Could not unlink the account. Try again later.",
    target: "Notification target",
    enableTarget: "Use for notifications",
    disableTarget: "Stop notifications",
    targetFailed: "Could not update the notification target. Try again later.",
    bots: {
      assistantCardDesc:
        "After connecting a WeCom bot, members can view and manage space tasks in a private chat.",
      assistantBindSubtitle:
        "Choose a Runtime for the task assistant in {space}, then enter the bot's BotID and Secret.",
      agentsCardDesc:
        "Choose an agent and enter a WeCom bot's BotID and Secret to chat with it privately.",
      agentsDialogSubtitle:
        "Choose the agent to connect, then enter the WeCom bot credentials.",
      assistantRemoveBody:
        "The bot will stop responding. Existing tasks and conversations remain.",
      agentsDisconnectBody:
        "The agent will stop responding through this bot. Existing conversations remain.",
    },
  },
  pingcodePersonal: {
    title: "My PingCode account",
    description:
      "After you link a personal access token, comments you write in OpenFox are synced using your PingCode identity. Task fields still use the organization connection.",
    bind: "Link personal token",
    boundHelp: "Comments sync as this PingCode member",
    unbind: "Unlink",
    empty: "No personal token linked",
    emptyHelp:
      "An admin needs to connect the PingCode company before you can link your personal token.",
    tokenHelp:
      "The token needs pcp:read:account:personal, pcp:read:global:team, and pcp:write:pjm:workitem. The server verifies the owner and company; PingCode checks comment write access on the first write.",
    company: "Company connection",
    token: "Personal access token",
  },
  // Stage 7g-1 · DingTalk: shares the page components with Feishu; only field names, the setup guide and how bots connect differ.
  dingtalk: {
    appKey: "AppKey: {id}",
    errors: {
      credentialsInvalid:
        "The AppKey or AppSecret is incorrect. Copy them again from the Credentials page in the DingTalk console.",
      appMismatch:
        "The AppKey cannot be changed when updating credentials. To use another DingTalk app, disconnect first and bind again.",
    },
    dialog: {
      appKeyLabel: "AppKey (Client ID)",
      appSecretLabel: "AppSecret (Client Secret)",
      appKeyInvalid:
        "The AppKey starts with ding followed by 8-40 letters or digits.",
      showSecret: "Show AppSecret",
      hideSecret: "Hide AppSecret",
      step1Desc:
        "Capabilities, permissions and callbacks are configured in the DingTalk developer console. Follow the guide on the right.",
      step2Title: "Enter the AppKey and AppSecret",
      step2Desc:
        "Copy both values from the Credentials page in the DingTalk console. They are validated right away and a Stream connection is opened.",
    },
    guide: {
      intro:
        "Every step except the last one is done in the DingTalk developer console, in this order.",
      s1Title: "Create an internal app",
      s1Desc:
        "Create an app under App Development > Internal Development, then find its AppKey and AppSecret on the Credentials page.",
      s2Title: "Grant permissions",
      s2Desc: "Grant these permissions under Permission Management:",
      copyPermissions: "Copy permissions",
      s3Title: "Enable the bot with Stream mode",
      s3Desc:
        "Enable the bot under App Capabilities > Bot, choose Stream mode for receiving messages, and publish the app. Bot message callback:",
      s4Title: "Set the login callback URL",
      s4Desc:
        "Add this address to the redirect URLs under Security Settings. It is used when members bind their DingTalk account:",
      copyRedirect: "Copy callback URL",
      s5Title: "Enter the credentials",
      s5Desc: "Go back to the left side and enter the AppKey and AppSecret.",
    },
    // DingTalk has no QR flow: these override the QR-related copy of the Feishu version (swapped by useIMT per provider).
    bots: {
      assistantBindSubtitle:
        'Choose the Runtime for the "{space}" task assistant, then enter the AppKey and AppSecret of the DingTalk app to connect the bot.',
      agentsCardDesc:
        "Pick an Agent and enter the AppKey and AppSecret of a DingTalk app to talk to it in DingTalk.",
      agentsDialogSubtitle:
        "Choose the Agent to connect to DingTalk, then enter the DingTalk app credentials.",
      assistantRemoveBody:
        "After removal the bot in DingTalk stops responding; existing tasks and conversations are kept. Delete the app in the DingTalk developer console yourself.",
      agentsDisconnectBody:
        "After disconnecting, this Agent no longer responds to DingTalk messages. Delete the app in the DingTalk developer console yourself.",
      connectTitle: "Connect a DingTalk bot",
      connectDesc:
        "DingTalk has no official QR-code flow for creating apps. Create an internal app with a bot in the DingTalk developer console first (in the same DingTalk organization as the app bound to this workspace), then enter its AppKey and AppSecret here.",
      connect: "Connect",
      connecting: "Connecting...",
      connected: "DingTalk bot connected",
    },
  },
  oauth: {
    okTitle: "Authorization complete",
    okDesc: "You can close this window and return to OpenFox.",
    failedTitle: "Authorization failed",
    working: "Returning…",
    backToIntegrations: "Back to integrations",
    backHome: "Back to home",
  },
};
