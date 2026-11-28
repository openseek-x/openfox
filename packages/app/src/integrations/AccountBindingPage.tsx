import { zohoApi, zohoAuthorizeURL } from "@openfox/api";
import { QQSections } from "./QQSections";
import { TeamsPersonalSection } from "./TeamsPersonal";
import { TelegramPersonalSection } from "./TelegramPersonal";
import { DiscordSections } from "./DiscordSections";
import { SlackSections } from "./SlackSections";
import { bitbucketApi } from "@openfox/api";
import { bitbucketAuthorizeURL } from "./BitbucketComplete";
import { azureApi, azureAuthorizeURL } from "./azure-api";
import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "react-router-dom";

import {
  isApiError,
  type AccountBinding,
  type AccountBindingInstance,
  type AccountBindingProvider,
  type AccountBindingsView,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  Callout,
  ConfirmDialog,
  Dialog,
  Field,
  Icon,
  Menu,
  MenuItem,
  Select,
  Skeleton,
  TextInput,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import { SettingsCard, SettingsColumn } from "../spaces/settings/parts";
import { parseOAuthMessage, popupFeatures, oauthErrorKey } from "./model";
import { ProviderIcon } from "./ProviderIcon";
import { integrationErrorText, useDisconnectConnection } from "./queries";
import { TAPDIdentityDialog } from "./TAPDIdentityDialog";
import { ShortcutIdentityDialog } from "./ShortcutIdentityDialog";
import { asanaApi, asanaAuthorizeURL } from "./asana-api";

// 阶段 6e · 第三方账号绑定（Figma 第 7 节，/settings/account-binding）：
// 14 行服务商卡（7g-4 在 GitHub 后加极狐、GitLab 自部署版、Gitee）；GitHub / Gitee / GitLab 系 / 飞书弹窗授权，Jira 填凭证（令牌只用于一次校验，不保存）。

const JIRA_ERROR_CODES = [
  "JIRA_AUTH_FAILED",
  "JIRA_EMAIL_MISMATCH",
  "JIRA_ACCOUNT_IN_USE",
  "JIRA_UNAVAILABLE",
  "JIRA_BLOCKED_ADDRESS",
  "JIRA_SITE_UNKNOWN",
] as const;

/** 把 Jira 绑定的错误映射成文案，兜底「连接失败，请重试」。 */
export function jiraBindingErrorKey(error: unknown): MessageKey {
  for (const code of JIRA_ERROR_CODES)
    if (isApiError(error, code)) return `accountBinding.jira.errors.${code}`;
  return "accountBinding.jira.errors.fallback";
}

function useAccountBindings(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.accountBindings(orgId),
    queryFn: () => api.accountBindings.list(orgId),
    enabled: Boolean(orgId),
  });
}

type OAuthProvider =
  | "zoho_bugtracker"
  | "bitbucket"
  | "github"
  | "gitlab"
  | "gitlab_cn"
  | "gitee"
  | "feishu"
  | "lark"
  | "dingtalk"
  | "teambition"
  | "asana"
  | "azure_devops";
const OAUTH_PROVIDERS = new Set<string>([
  "zoho_bugtracker",
  "bitbucket",
  "azure_devops",
  "asana",
  "teambition",
  "github",
  "gitlab",
  "gitlab_cn",
  "gitee",
  "feishu",
  "lark",
  "dingtalk",
]);
const isGitLabRow = (id: string) => id === "gitlab" || id === "gitlab_cn";

/** 弹窗授权（Git 服务商 personal / 飞书）：发起、postMessage 回执、用户关掉弹窗时结束等待。GitLab 系带实例的 connection_id。 */
function useOAuthBinding(orgId: string) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<{
    provider: OAuthProvider;
    session: string;
  } | null>(null);
  const [blocked, setBlocked] = useState<string | null>(null);
  const popup = useRef<Window | null>(null);
  // 完成页可能重复 postMessage，同一会话只处理一次
  const handled = useRef<string | null>(null);

  const refresh = () => {
    void queryClient.invalidateQueries({
      queryKey: keys.accountBindings(orgId),
    });
    void queryClient.invalidateQueries({ queryKey: keys.integrations(orgId) });
  };

  const start = useMutation({
    mutationFn: (input: {
      provider: OAuthProvider;
      connectionId?: string;
      version?: number;
    }) =>
      input.provider === "zoho_bugtracker"
        ? zohoApi(api.http).start(orgId, {
            purpose: "personal",
            connection_id: input.connectionId ?? "",
            version: input.version ?? 0,
          })
        : input.provider === "bitbucket"
          ? bitbucketApi(api.http).start(orgId, {
              purpose: "personal",
              connection_id: input.connectionId ?? "",
              version: input.version ?? 0,
            })
          : input.provider === "azure_devops"
            ? azureApi(api.http).start(orgId, {
                purpose: "personal",
                connection_id: input.connectionId ?? "",
                version: input.version ?? 0,
              })
            : input.provider === "asana"
              ? asanaApi(api.http).start(orgId, {
                  purpose: "personal",
                  connection_id: input.connectionId ?? "",
                  version: input.version ?? 0,
                })
              : input.provider === "teambition"
                ? api.accountBindings.startTeambition(orgId, {
                    connection_id: input.connectionId ?? "",
                    version: input.version ?? 0,
                  })
                : input.provider === "github"
                  ? api.integrations.startGitHubAuthorization(orgId, {
                      purpose: "personal",
                    })
                  : input.provider === "gitee" || isGitLabRow(input.provider)
                    ? api.integrations.startGitAuthorization(
                        orgId,
                        input.provider,
                        {
                          purpose: "personal",
                          ...(input.connectionId
                            ? { connection_id: input.connectionId }
                            : {}),
                        },
                      )
                    : api.integrations.startFeishuAuthorization(
                        orgId,
                        input.provider as "feishu" | "lark" | "dingtalk",
                      ),
    onSuccess: (result, { provider }) => {
      if (
        provider === "zoho_bugtracker" &&
        !zohoAuthorizeURL(result.authorize_url)
      ) {
        toast(t("integrations.zoho.failed"), { tone: "error" });
        return;
      }
      if (
        provider === "bitbucket" &&
        !bitbucketAuthorizeURL(result.authorize_url)
      ) {
        toast(t("integrations.bitbucket.failed"), { tone: "error" });
        return;
      }
      if (
        provider === "azure_devops" &&
        !azureAuthorizeURL(result.authorize_url)
      ) {
        toast(t("integrations.azure.failed"), { tone: "error" });
        return;
      }
      if (provider === "asana" && !asanaAuthorizeURL(result.authorize_url)) {
        toast(t("integrations.asana.failed"), { tone: "error" });
        return;
      }
      setPending({ provider, session: result.session_id });
      const win = window.open(
        result.authorize_url,
        `openfox-${provider}-oauth`,
        popupFeatures(),
      );
      popup.current = win;
      setBlocked(win ? null : result.authorize_url);
    },
    onError: (error) =>
      toast(integrationErrorText(t, error), { tone: "error" }),
  });

  // A popup-blocked fallback has no opener. Read the initiating user's session
  // so that completion can still refresh this page without trusting a message.
  const asanaSession = useQuery<{ status: string; expires_at: string }>({
    queryKey: [
      "personal-project-oauth",
      orgId,
      pending?.provider,
      pending?.session,
    ],
    queryFn: () =>
      pending?.provider === "zoho_bugtracker"
        ? zohoApi(api.http).session(orgId, pending.session)
        : pending?.provider === "bitbucket"
          ? bitbucketApi(api.http).session(orgId, pending!.session)
          : pending?.provider === "azure_devops"
            ? azureApi(api.http).session(orgId, pending!.session)
            : asanaApi(api.http).session(orgId, pending!.session),
    enabled:
      pending?.provider === "zoho_bugtracker" ||
      pending?.provider === "bitbucket" ||
      pending?.provider === "asana" ||
      pending?.provider === "azure_devops",
    retry: false,
    refetchInterval: (q) =>
      (pending?.provider === "zoho_bugtracker" ||
        pending?.provider === "bitbucket" ||
        pending?.provider === "asana" ||
        pending?.provider === "azure_devops") &&
      q.state.data &&
      ["PENDING", "SELECTING"].includes(q.state.data.status) &&
      new Date(q.state.data.expires_at).getTime() > Date.now()
        ? 2000
        : false,
  });
  useEffect(() => {
    if (
      (pending?.provider !== "zoho_bugtracker" &&
        pending?.provider !== "bitbucket" &&
        pending?.provider !== "asana" &&
        pending?.provider !== "azure_devops") ||
      !asanaSession.data ||
      !["COMPLETED", "FAILED", "EXPIRED"].includes(asanaSession.data.status)
    )
      return;
    const completed = asanaSession.data.status === "COMPLETED";
    toast(
      t(
        completed
          ? "accountBinding.connectedToast"
          : pending?.provider === "zoho_bugtracker"
            ? "integrations.zoho.failed"
            : pending?.provider === "bitbucket"
              ? "integrations.bitbucket.failed"
              : pending?.provider === "azure_devops"
                ? "integrations.azure.failed"
                : "integrations.asana.failed",
      ),
      completed ? undefined : { tone: "error" },
    );
    handled.current = pending.session;
    setPending(null);
    setBlocked(null);
    void queryClient.invalidateQueries({
      queryKey: keys.accountBindings(orgId),
    });
    void queryClient.invalidateQueries({ queryKey: keys.integrations(orgId) });
  }, [asanaSession.data, pending, queryClient, orgId, toast, t]);

  useEffect(() => {
    if (!pending) return;
    const onMessage = (event: MessageEvent) => {
      const message = parseOAuthMessage(event, window.location.origin);
      if (
        !message ||
        message.provider !== pending.provider ||
        message.session !== pending.session ||
        handled.current === pending.session
      )
        return;
      if (
        pending.provider === "zoho_bugtracker" ||
        pending.provider === "bitbucket" ||
        pending.provider === "asana" ||
        pending.provider === "azure_devops"
      ) {
        void asanaSession.refetch();
        return;
      }
      handled.current = pending.session;
      if (message.status === "ok") toast(t("accountBinding.connectedToast"));
      else toast(t(oauthErrorKey(message.reason)), { tone: "error" });
      setPending(null);
      setBlocked(null);
      refresh();
    };
    const timer = window.setInterval(() => {
      if (popup.current?.closed) {
        setPending(null);
        setBlocked(null);
        refresh();
      }
    }, 1000);
    window.addEventListener("message", onMessage);
    return () => {
      window.removeEventListener("message", onMessage);
      window.clearInterval(timer);
    };
    // refresh 只依赖 orgId 与 queryClient
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pending, orgId, queryClient, toast, t]);

  return {
    start: (provider: OAuthProvider, connectionId?: string, version?: number) =>
      start.mutate({ provider, connectionId, version }),
    busy: start.isPending || pending !== null,
    activeProvider: pending?.provider ?? null,
    blocked,
  };
}

export function AccountBindingPage() {
  const { orgId = "" } = useParams();
  const navigate = useNavigate();
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const bindings = useAccountBindings(orgId);
  const oauth = useOAuthBinding(orgId);
  const disconnect = useDisconnectConnection(orgId);
  const [discordOpen, setDiscordOpen] = useState(false);
  const [slackOpen, setSlackOpen] = useState(false);
  const [qqOpen, setQQOpen] = useState(false);
  const [teamsOpen, setTeamsOpen] = useState(false);
  const [jiraOpen, setJiraOpen] = useState(false);
  const [tapdOpen, setTapdOpen] = useState(false);
  const [shortcutParent, setShortcutParent] = useState<string | null>(null);
  const [removing, setRemoving] = useState<{
    provider: AccountBindingProvider;
    binding: AccountBinding;
    host?: string;
  } | null>(null);

  const confirmDisconnect = () => {
    if (!removing) return;
    disconnect.mutate(
      {
        connectionId: removing.binding.connection_id,
        version: removing.binding.version,
      },
      {
        onSuccess: () => {
          toast(t("accountBinding.disconnectedToast"));
          setRemoving(null);
        },
        onError: (error) => {
          toast(integrationErrorText(t, error), { tone: "error" });
          setRemoving(null);
        },
        onSettled: () =>
          queryClient.invalidateQueries({
            queryKey: keys.accountBindings(orgId),
          }),
      },
    );
  };

  const view = bindings.data;
  return (
    <SettingsColumn narrow>
      <header className="flex flex-col gap-1 px-1">
        <h2 className="text-heading font-semibold tracking-[-0.015em] text-fg-default">
          {t("accountBinding.title")}
        </h2>
        <p className="text-body-sm text-fg-muted">
          {t("accountBinding.description")}
        </p>
      </header>

      <TelegramPersonalSection key={`telegram:${orgId}`} orgId={orgId} />

      {oauth.blocked && (
        <Callout tone="warning">
          {t("accountBinding.popupBlocked")}{" "}
          <a
            href={oauth.blocked}
            target="_blank"
            rel="noopener noreferrer"
            className="font-medium underline"
          >
            {t("accountBinding.openAuthorize")}
          </a>
        </Callout>
      )}
      {oauth.activeProvider && !oauth.blocked && (
        <Callout tone="info">{t("accountBinding.waiting")}</Callout>
      )}

      {bindings.isError ? (
        <p role="alert" className="text-body text-fg-muted">
          {t("accountBinding.loadFailed")}
        </p>
      ) : (
        <SettingsCard>
          {view
            ? view.providers.map((provider) =>
                provider.id === "qq" &&
                provider.availability === "available" ? (
                  <div
                    key="qq"
                    data-testid="binding-qq"
                    className="flex items-center gap-3 p-4"
                  >
                    <ProviderIcon name="QQ" />
                    <span className="flex-1 text-body font-medium">QQ</span>
                    <Button variant="outline" onClick={() => setQQOpen(true)}>
                      {t("integrations.qq.personal")}
                    </Button>
                  </div>
                ) : provider.id === "teams" &&
                  provider.availability === "available" ? (
                  <div
                    key="teams"
                    data-testid="binding-teams"
                    className="flex items-center gap-3 p-4"
                  >
                    <ProviderIcon name="Microsoft Teams" />
                    <span className="flex-1 text-body font-medium">
                      Microsoft Teams
                    </span>
                    <Button
                      variant="outline"
                      onClick={() => setTeamsOpen(true)}
                    >
                      {t("integrations.teams.personal")}
                    </Button>
                  </div>
                ) : provider.id === "discord" &&
                  provider.availability === "available" ? (
                  <div
                    key="discord"
                    data-testid="binding-discord"
                    className="flex items-center gap-3 p-4"
                  >
                    <ProviderIcon name="Discord" />
                    <span className="flex-1 text-body font-medium">
                      Discord
                    </span>
                    <Button
                      variant="outline"
                      onClick={() => setDiscordOpen(true)}
                    >
                      {t("integrations.discord.personal")}
                    </Button>
                  </div>
                ) : provider.id === "slack" &&
                  provider.availability === "available" ? (
                  <div
                    key="slack"
                    data-testid="binding-slack"
                    className="flex items-center gap-3 p-4"
                  >
                    <ProviderIcon name="Slack" />
                    <span className="flex-1 text-body font-medium">Slack</span>
                    <Button
                      variant="outline"
                      onClick={() => setSlackOpen(true)}
                    >
                      {t("integrations.slack.manageIdentity")}
                    </Button>
                  </div>
                ) : (
                  <ProviderRow
                    key={provider.id}
                    provider={provider}
                    busy={oauth.busy}
                    onBind={(connectionId) => {
                      if (provider.id === "jira") setJiraOpen(true);
                      else if (provider.id === "tapd") setTapdOpen(true);
                      else if (provider.id === "shortcut")
                        setShortcutParent(
                          connectionId ??
                            provider.instances?.[0]?.connection_id ??
                            "",
                        );
                      else if (OAUTH_PROVIDERS.has(provider.id))
                        oauth.start(
                          provider.id as OAuthProvider,
                          connectionId,
                          provider.instances?.find(
                            (i) => i.connection_id === connectionId,
                          )?.binding?.version,
                        );
                    }}
                    onManage={() =>
                      navigate(
                        `/o/${orgId}/settings/integrations/${provider.id}`,
                      )
                    }
                    onUpdate={() =>
                      provider.id === "tapd"
                        ? setTapdOpen(true)
                        : setJiraOpen(true)
                    }
                    onDisconnect={(binding, host) =>
                      setRemoving({ provider, binding, host })
                    }
                  />
                ),
              )
            : Array.from({ length: 14 }, (_, index) => (
                <SkeletonRow key={index} />
              ))}
        </SettingsCard>
      )}

      {qqOpen && (
        <Dialog
          open
          title={t("integrations.qq.personal")}
          width={680}
          onClose={() => setQQOpen(false)}
        >
          <QQSections orgId={orgId} personalOnly />
        </Dialog>
      )}
      {teamsOpen && (
        <Dialog
          open
          onClose={() => setTeamsOpen(false)}
          title={t("integrations.teams.personal")}
          width={640}
        >
          <TeamsPersonalSection key={`teams:${orgId}`} orgId={orgId} />
        </Dialog>
      )}
      {discordOpen && (
        <Dialog
          open
          title={t("integrations.discord.personal")}
          width={680}
          onClose={() => setDiscordOpen(false)}
        >
          <DiscordSections orgId={orgId} personalOnly />
        </Dialog>
      )}
      {slackOpen && (
        <Dialog
          open
          title={t("integrations.slack.personal")}
          width={680}
          onClose={() => setSlackOpen(false)}
        >
          <SlackSections orgId={orgId} personalOnly />
        </Dialog>
      )}
      {jiraOpen && view && (
        <JiraDialog
          orgId={orgId}
          view={view}
          onClose={() => setJiraOpen(false)}
        />
      )}
      {tapdOpen && view && (
        <TAPDIdentityDialog
          orgId={orgId}
          provider={view.providers.find((p) => p.id === "tapd")!}
          email={view.email}
          onClose={() => setTapdOpen(false)}
        />
      )}
      {shortcutParent !== null && view && (
        <ShortcutIdentityDialog
          orgId={orgId}
          provider={view.providers.find((p) => p.id === "shortcut")!}
          connectionId={shortcutParent}
          email={view.email}
          onClose={() => setShortcutParent(null)}
        />
      )}
      <ConfirmDialog
        open={removing !== null}
        onCancel={() => setRemoving(null)}
        onConfirm={confirmDisconnect}
        title={t("accountBinding.disconnectTitle", {
          name: removing?.host
            ? `${removing.provider.name} · ${removing.host}`
            : (removing?.provider.name ?? ""),
        })}
        description={t(
          removing?.provider.id === "shortcut"
            ? "accountBinding.shortcut.disconnectBody"
            : removing?.provider.id === "zoho_bugtracker"
              ? "integrations.zoho.identityDisconnect"
              : removing?.provider.id === "bitbucket"
                ? "integrations.bitbucket.identityDisconnect"
                : removing?.provider.id === "azure_devops"
                  ? "integrations.azure.identityDisconnect"
                  : removing?.provider.id === "asana"
                    ? "integrations.asana.identityDisconnect"
                    : "accountBinding.disconnectBody",
        )}
        confirmLabel={t("accountBinding.disconnect")}
        danger
        loading={disconnect.isPending}
      />
    </SettingsColumn>
  );
}

function SkeletonRow() {
  return (
    <div
      className="flex min-h-[72px] items-center gap-3 p-4"
      aria-busy="true"
      data-testid="binding-skeleton"
    >
      <Skeleton className="size-8 rounded-lg" />
      <div className="flex min-w-0 flex-1 flex-col gap-2">
        <Skeleton className="h-4 w-24" />
        <Skeleton className="h-3 w-2/3" />
      </div>
      <Skeleton className="h-8 w-24" />
    </div>
  );
}

function ProviderRow({
  provider,
  busy,
  onBind,
  onManage,
  onUpdate,
  onDisconnect,
}: {
  provider: AccountBindingProvider;
  busy: boolean;
  onBind: (connectionId?: string) => void;
  onManage: () => void;
  onUpdate: () => void;
  onDisconnect: (binding: AccountBinding, host?: string) => void;
}) {
  const t = useT();
  const { binding } = provider;
  // GitLab 系：每个实例一条个人绑定；单个未绑定实例直接授权，多个时先选实例
  const lab =
    isGitLabRow(provider.id) ||
    provider.id === "teambition" ||
    provider.id === "zoho_bugtracker" ||
    provider.id === "bitbucket" ||
    provider.id === "asana" ||
    provider.id === "azure_devops" ||
    provider.id === "shortcut";
  const instances: AccountBindingInstance[] = provider.instances ?? [];
  const unbound = instances.filter((i) => !i.binding);
  const boundInstances = instances.filter((i) => i.binding);
  // IM 类（飞书等）按设计稿 7.1 / 7.8：组织未连接时说明行就是「需先由管理员连接…」，右侧只有禁用的「绑定」；连接后换成用途说明。
  const im = IM_PROVIDERS.has(provider.id);
  const purposeKey = (
    (im || GIT_PROVIDERS.has(provider.id)) && provider.organization_ready
      ? `accountBinding.purposeReady.${provider.id}`
      : `accountBinding.purpose.${provider.id}`
  ) as MessageKey;
  const notReadyKey = `accountBinding.notReady.${provider.id}` as MessageKey;
  return (
    <div
      className="flex min-h-[72px] items-center gap-3 p-4"
      data-testid={`binding-${provider.id}`}
    >
      <ProviderIcon name={provider.name} />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="text-body font-medium text-fg-default">
          {provider.name}
        </span>
        <span className="text-body-sm text-fg-muted">{t(purposeKey)}</span>
        {binding && (
          <span className="truncate text-caption text-fg-muted">
            {[binding.display_name, binding.detail].filter(Boolean).join(" · ")}
          </span>
        )}
      </div>
      <div className="flex max-w-1/2 shrink-0 flex-wrap items-center justify-end gap-3">
        {provider.availability === "coming_soon" ? (
          <span className="text-body-sm text-fg-muted">
            {t("accountBinding.comingSoon")}
          </span>
        ) : provider.method === "link" ? (
          <Button size="32" variant="outline" onClick={onManage}>
            {t("accountBinding.manage")}
          </Button>
        ) : binding ? (
          <Menu
            label={t("accountBinding.menu", { name: provider.name })}
            placement="bottom-end"
            trigger={({ ref, toggle, open }) => (
              <button
                ref={ref}
                type="button"
                aria-expanded={open}
                onClick={toggle}
                className="flex h-8 items-center gap-1.5 rounded-md px-2 text-body-sm text-fg-default hover:bg-surface-muted"
              >
                <span aria-hidden className="text-success">
                  ●
                </span>
                {t("accountBinding.connected")}
                <Icon name="chevron-down" />
              </button>
            )}
          >
            {provider.method === "form" && (
              <MenuItem onSelect={onUpdate}>
                {t("accountBinding.update")}
              </MenuItem>
            )}
            {lab ? (
              <>
                {provider.id === "shortcut" &&
                  boundInstances.map((i) => (
                    <MenuItem
                      key={`shortcut-${i.connection_id}`}
                      onSelect={() => onBind(i.connection_id)}
                    >
                      {t("accountBinding.shortcut.manageWorkspace", {
                        workspace: i.display_name,
                      })}
                    </MenuItem>
                  ))}
                {(provider.id === "teambition" ||
                  provider.id === "zoho_bugtracker" ||
                  provider.id === "bitbucket" ||
                  provider.id === "asana" ||
                  provider.id === "azure_devops") &&
                  boundInstances.map((i) => (
                    <MenuItem
                      key={`verify-${i.connection_id}`}
                      disabled={
                        provider.id === "bitbucket" &&
                        !provider.organization_ready
                      }
                      onSelect={() => onBind(i.connection_id)}
                    >
                      {t("accountBinding.teambition.reverify", {
                        company: i.host,
                      })}
                    </MenuItem>
                  ))}
                {boundInstances.map((i) => (
                  <MenuItem
                    key={i.connection_id}
                    danger
                    onSelect={() => onDisconnect(i.binding!, i.host)}
                  >
                    {t("accountBinding.disconnectHost", { host: i.host })}
                  </MenuItem>
                ))}
                {unbound.length > 0 &&
                  unbound.map((i) => (
                    <MenuItem
                      key={i.connection_id}
                      onSelect={() => onBind(i.connection_id)}
                    >
                      {unbound.length === 1
                        ? t("accountBinding.linkOther")
                        : t("accountBinding.linkHost", { host: i.host })}
                    </MenuItem>
                  ))}
              </>
            ) : (
              <MenuItem danger onSelect={() => onDisconnect(binding)}>
                {t("accountBinding.disconnect")}
              </MenuItem>
            )}
          </Menu>
        ) : (
          <>
            {!provider.organization_ready && !im && (
              <span className="text-body-sm text-fg-muted">
                {t(notReadyKey)}
              </span>
            )}
            {(provider.organization_ready || im) &&
              (lab && unbound.length > 1 ? (
                <Menu
                  label={t("accountBinding.pickInstance", {
                    name: provider.name,
                  })}
                  placement="bottom-end"
                  trigger={({ ref, toggle, open }) => (
                    <Button
                      ref={ref}
                      size="32"
                      variant="outline"
                      aria-expanded={open}
                      disabled={busy}
                      onClick={toggle}
                    >
                      {t("accountBinding.bind")}
                    </Button>
                  )}
                >
                  {unbound.map((i) => (
                    <MenuItem
                      key={i.connection_id}
                      onSelect={() => onBind(i.connection_id)}
                    >
                      {i.host}
                    </MenuItem>
                  ))}
                </Menu>
              ) : (
                <Button
                  size="32"
                  variant="outline"
                  disabled={!provider.organization_ready || busy}
                  onClick={() =>
                    lab ? onBind(unbound[0]?.connection_id) : onBind()
                  }
                >
                  {t("accountBinding.bind")}
                </Button>
              ))}
          </>
        )}
      </div>
    </div>
  );
}

const IM_PROVIDERS = new Set<string>([
  "discord",
  "slack",
  "feishu",
  "lark",
  "dingtalk",
]);
const GIT_PROVIDERS = new Set<string>(["gitlab", "gitlab_cn", "gitee"]);

/** Jira 绑定对话框（Figma 7.6）：多站点先选站点；Cloud 填邮箱 + API Token，Server 填 PAT。 */
function JiraDialog({
  orgId,
  view,
  onClose,
}: {
  orgId: string;
  view: AccountBindingsView;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [siteUrl, setSiteUrl] = useState(view.jira_sites[0]?.site_url ?? "");
  const [email, setEmail] = useState(view.email);
  const [token, setToken] = useState("");
  const [error, setError] = useState<string | null>(null);
  const site = view.jira_sites.find((s) => s.site_url === siteUrl);
  const cloud = site?.deployment !== "server";

  const submit = useMutation({
    mutationFn: () =>
      api.accountBindings.putJira(orgId, {
        site_url: siteUrl,
        deployment: site?.deployment ?? "cloud",
        ...(cloud ? { email: email.trim() } : {}),
        api_token: token,
      }),
    onSuccess: (result) => {
      toast(t("accountBinding.connectedToast"));
      if (result.linked_accounts > 0)
        toast(t("accountBinding.jira.linked", { n: result.linked_accounts }));
      void queryClient.invalidateQueries({
        queryKey: keys.accountBindings(orgId),
      });
      void queryClient.invalidateQueries({
        queryKey: keys.integrations(orgId),
      });
      setToken("");
      onClose();
    },
    onError: (err) => {
      const message = t(jiraBindingErrorKey(err));
      setError(message);
      toast(message, { tone: "error" });
    },
  });

  const ready =
    site !== undefined &&
    token.trim() !== "" &&
    (!cloud || email.trim() !== "");
  return (
    <Dialog
      open
      onClose={submit.isPending ? undefined : onClose}
      title={t("accountBinding.jira.title")}
      description={t("accountBinding.jira.description", {
        host: site?.host ?? "",
      })}
      footer={
        <>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={submit.isPending}
          >
            {t("common.cancel")}
          </Button>
          <Button
            onClick={() => submit.mutate()}
            loading={submit.isPending}
            disabled={!ready}
          >
            {t("accountBinding.jira.submit")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault();
          if (ready && !submit.isPending) submit.mutate();
        }}
      >
        {view.jira_sites.length > 1 && (
          <Field label={t("accountBinding.jira.site")}>
            <Select
              aria-label={t("accountBinding.jira.site")}
              value={siteUrl}
              options={view.jira_sites.map((s) => ({
                value: s.site_url,
                label: s.host,
              }))}
              onChange={(value) => setSiteUrl(value ?? "")}
            />
          </Field>
        )}
        {cloud && (
          <Field
            label={t("accountBinding.jira.email")}
            helper={t("accountBinding.jira.emailHelp")}
          >
            <TextInput
              type="email"
              autoComplete="email"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
            />
          </Field>
        )}
        <Field
          label={t(
            cloud ? "accountBinding.jira.apiToken" : "accountBinding.jira.pat",
          )}
          helper={t(
            cloud
              ? "accountBinding.jira.apiTokenHelp"
              : "accountBinding.jira.patHelp",
          )}
        >
          <TextInput
            type="password"
            autoComplete="off"
            value={token}
            onChange={(event) => setToken(event.target.value)}
          />
        </Field>
        <p className="text-body-sm text-fg-muted">
          {t("accountBinding.jira.notStored")}
        </p>
        {error && (
          <p role="alert" className="text-body-sm text-danger">
            {error}
          </p>
        )}
      </form>
    </Dialog>
  );
}
