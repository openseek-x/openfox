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

// 第三方账号绑定：
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
  "github" | "gitlab" | "gitlab_cn" | "gitee" | "feishu" | "lark" | "dingtalk";
const OAUTH_PROVIDERS = new Set<string>([
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
    mutationFn: (input: { provider: OAuthProvider; connectionId?: string }) =>
      input.provider === "github"
        ? api.integrations.startGitHubAuthorization(orgId, {
            purpose: "personal",
          })
        : input.provider === "gitee" || isGitLabRow(input.provider)
          ? api.integrations.startGitAuthorization(orgId, input.provider, {
              purpose: "personal",
              ...(input.connectionId
                ? { connection_id: input.connectionId }
                : {}),
            })
          : api.integrations.startFeishuAuthorization(
              orgId,
              input.provider as "feishu" | "lark" | "dingtalk",
            ),
    onSuccess: (result, { provider }) => {
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
    start: (provider: OAuthProvider, connectionId?: string) =>
      start.mutate({ provider, connectionId }),
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
  const [jiraOpen, setJiraOpen] = useState(false);
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
        <h2 className="text-[20px] leading-7 font-bold text-fg-default">
          {t("accountBinding.title")}
        </h2>
        <p className="text-body-sm text-fg-muted">
          {t("accountBinding.description")}
        </p>
      </header>

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
            ? view.providers.map((provider) => (
                <ProviderRow
                  key={provider.id}
                  provider={provider}
                  busy={oauth.busy}
                  onBind={(connectionId) => {
                    if (provider.id === "jira") setJiraOpen(true);
                    else if (OAUTH_PROVIDERS.has(provider.id))
                      oauth.start(provider.id as OAuthProvider, connectionId);
                  }}
                  onManage={() =>
                    navigate(`/o/${orgId}/settings/integrations/${provider.id}`)
                  }
                  onUpdate={() => setJiraOpen(true)}
                  onDisconnect={(binding, host) =>
                    setRemoving({ provider, binding, host })
                  }
                />
              ))
            : Array.from({ length: 14 }, (_, index) => (
                <SkeletonRow key={index} />
              ))}
        </SettingsCard>
      )}

      {jiraOpen && view && (
        <JiraDialog
          orgId={orgId}
          view={view}
          onClose={() => setJiraOpen(false)}
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
        description={t("accountBinding.disconnectBody")}
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
  const lab = isGitLabRow(provider.id);
  const instances: AccountBindingInstance[] = provider.instances ?? [];
  const unbound = instances.filter((i) => !i.binding);
  const boundInstances = instances.filter((i) => i.binding);
  // IM 类（飞书等）：组织未连接时说明行就是「需先由管理员连接…」，右侧只有禁用的「绑定」；连接后换成用途说明。
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
      <div className="flex shrink-0 items-center gap-3">
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

const IM_PROVIDERS = new Set<string>(["slack", "feishu", "lark", "dingtalk"]);
const GIT_PROVIDERS = new Set<string>(["gitlab", "gitlab_cn", "gitee"]);

/** Jira 绑定对话框：多站点先选站点；Cloud 填邮箱 + API Token，Server 填 PAT。 */
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
