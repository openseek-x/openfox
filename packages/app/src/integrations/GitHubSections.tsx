import { useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";

import {
  isApiError,
  type IntegrationConnection,
  type IntegrationDetail,
  type IntegrationOAuthSession,
  type IntegrationSettingsValues,
  type GitAuthorizeInput,
  type IntegrationWebhook,
  type OAuthPurpose,
} from "@openfox/api";
import {
  Avatar,
  Badge,
  Button,
  Callout,
  Checkbox,
  ConfirmDialog,
  Dialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Select,
  Skeleton,
  Switch,
  TextInput,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import { useMutation } from "@tanstack/react-query";
import { useCopy } from "../tasks/parts";
import {
  SettingRow,
  SettingsCard,
  SettingsSection,
} from "../spaces/settings/parts";
import { DisconnectDialog } from "./DisconnectDialog";
import { GitLabTokenDialog } from "./GitLabTokenDialog";
import { GitLabBindDialog } from "./GitLabBindDialog";
import { isGitee, isGitLab, useGitProvider, useGitT } from "./git-provider";
import {
  BRANCH_FORMATS,
  connectionInstanceHost,
  oauthErrorKey,
  parseOAuthMessage,
  popupFeatures,
} from "./model";
import {
  integrationErrorText,
  useCompleteOAuth,
  useIntegrationSettings,
  useIntegrationWebhook,
  useOAuthSession,
  usePutIntegrationSettings,
  useRotateWebhook,
} from "./queries";

// GitHub 详情页：OAuth 绑定、连接列表、Webhook 指引、PR 关联设置。

const fmtTime = (iso: string) => new Date(iso).toLocaleString();

// ---------- OAuth 流程 ----------

/** GitLab 系组织授权带的实例与应用凭据。 */
export type OAuthExtra = Pick<
  GitAuthorizeInput,
  "instance_url" | "application_id" | "application_secret"
>;

interface OAuthApi {
  start: (
    purpose: OAuthPurpose,
    connectionId?: string,
    extra?: OAuthExtra,
    /** 传了 onError 就由调用方处理错误（例如表单内联提示），不再弹 toast */
    handlers?: { onSuccess?: () => void; onError?: (error: unknown) => void },
  ) => void;
  busy: boolean;
}
const OAuthContext = createContext<OAuthApi | null>(null);
function useOAuth(): OAuthApi {
  const value = useContext(OAuthContext);
  if (!value) throw new Error("useOAuth 必须在 GitHubOAuthProvider 内使用");
  return value;
}

/** 一个详情页共用一条授权流程：发起、弹窗、message 与轮询、选择对话框、终态提示。 */
export function GitHubOAuthProvider({
  orgId,
  children,
}: {
  orgId: string;
  children: ReactNode;
}) {
  const t = useGitT();
  const api = useApi();
  const toast = useToast();
  const { id: providerId } = useGitProvider();
  const queryClient = useQueryClient();
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [blocked, setBlocked] = useState<string | null>(null);
  const handled = useRef("");
  const session = useOAuthSession(orgId, sessionId);
  const data = session.data;

  const startMutation = useMutation({
    mutationFn: (input: GitAuthorizeInput) =>
      providerId === "github"
        ? api.integrations.startGitHubAuthorization(orgId, input)
        : api.integrations.startGitAuthorization(orgId, providerId, input),
    onSuccess: (result) => {
      setSessionId(result.session_id);
      const popup = window.open(
        result.authorize_url,
        `openfox-${providerId}-oauth`,
        popupFeatures(),
      );
      setBlocked(popup ? null : result.authorize_url);
    },
  });

  // 弹窗完成页通过 postMessage 通知，立即刷新会话；轮询只是兜底
  useEffect(() => {
    if (!sessionId) return;
    const onMessage = (event: MessageEvent) => {
      const message = parseOAuthMessage(event, window.location.origin);
      if (
        message &&
        message.provider === providerId &&
        message.session === sessionId
      )
        void queryClient.invalidateQueries({
          queryKey: keys.integrationOAuth(orgId, sessionId),
        });
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [orgId, providerId, sessionId, queryClient]);

  // 终态提示（每个会话每个终态只处理一次）
  useEffect(() => {
    if (!data || data.status === "PENDING" || data.status === "SELECTING")
      return;
    const mark = `${data.id}:${data.status}`;
    if (handled.current === mark) return;
    handled.current = mark;
    if (data.status === "COMPLETED") {
      // 绑定组织在选择对话框里已提示「已绑定」，这里只提示个人账号与重新授权
      if (data.purpose === "personal")
        toast(t("integrations.github.personalToast"));
      else if (data.purpose === "reauthorize")
        toast(t("integrations.github.reauthorizedToast"));
      void queryClient.invalidateQueries({
        queryKey: keys.integrations(orgId),
      });
    } else
      toast(
        t(oauthErrorKey(data.status === "EXPIRED" ? "" : data.error_code)),
        { tone: "error" },
      );
    setSessionId(null);
    setBlocked(null);
  }, [data, orgId, queryClient, t, toast]);

  const waiting = sessionId !== null && (!data || data.status === "PENDING");
  const api_: OAuthApi = {
    busy: startMutation.isPending || waiting,
    start: (purpose, connectionId, extra, handlers) =>
      startMutation.mutate(
        {
          purpose,
          ...(connectionId ? { connection_id: connectionId } : {}),
          ...extra,
        },
        {
          onSuccess: () => handlers?.onSuccess?.(),
          onError: (error) =>
            handlers?.onError
              ? handlers.onError(error)
              : toast(integrationErrorText(t, error), { tone: "error" }),
        },
      ),
  };
  return (
    <OAuthContext.Provider value={api_}>
      {blocked && (
        <Callout tone="warning">
          {t("integrations.github.popupBlocked")}{" "}
          <a
            href={blocked}
            target="_blank"
            rel="noopener noreferrer"
            className="font-medium underline"
          >
            {t("integrations.github.openAuthorize")}
          </a>
        </Callout>
      )}
      {waiting && !blocked && (
        <Callout tone="info">{t("integrations.github.waiting")}</Callout>
      )}
      {children}
      {data?.status === "SELECTING" && data.purpose === "organization" && (
        <SelectAccountsDialog
          orgId={orgId}
          session={data}
          onClose={() => setSessionId(null)}
        />
      )}
    </OAuthContext.Provider>
  );
}

function SelectAccountsDialog({
  orgId,
  session,
  onClose,
}: {
  orgId: string;
  session: IntegrationOAuthSession;
  onClose: () => void;
}) {
  const t = useGitT();
  const toast = useToast();
  const complete = useCompleteOAuth(orgId);
  const [picked, setPicked] = useState<string[]>([]);
  const toggle = (login: string) =>
    setPicked((list) =>
      list.includes(login) ? list.filter((x) => x !== login) : [...list, login],
    );
  const submit = () =>
    complete.mutate(
      { sessionId: session.id, accounts: picked },
      {
        onSuccess: () => {
          toast(t("integrations.github.boundToast"));
          onClose();
        },
        onError: (error) => {
          toast(integrationErrorText(t, error), { tone: "error" });
          onClose();
        },
      },
    );
  return (
    <Dialog
      open
      onClose={complete.isPending ? undefined : onClose}
      title={t("integrations.github.selectTitle")}
      description={t("integrations.github.selectDesc")}
      footer={
        <>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={complete.isPending}
          >
            {t("common.cancel")}
          </Button>
          <Button
            onClick={submit}
            loading={complete.isPending}
            disabled={picked.length === 0}
          >
            {t("integrations.github.confirmBind")}
          </Button>
        </>
      }
    >
      {session.accounts.length === 0 ? (
        <p className="text-body-sm text-fg-muted">
          {t("integrations.github.selectNone")}
        </p>
      ) : (
        <ul className="flex flex-col gap-1">
          {session.accounts.map((account) => (
            <li key={account.login}>
              <label className="flex cursor-pointer items-center gap-3 rounded-lg p-2 hover:bg-surface-active">
                <Checkbox
                  aria-label={account.login}
                  checked={picked.includes(account.login)}
                  onChange={() => toggle(account.login)}
                />
                <Avatar
                  name={account.login}
                  src={account.avatar_url || null}
                  size={28}
                />
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate text-body font-medium">
                    {account.login}
                  </span>
                  {account.connection_id && (
                    <span className="text-caption text-fg-muted">
                      {t("integrations.github.alreadyBound")}
                    </span>
                  )}
                </span>
                <Badge tone="neutral">
                  {t(
                    account.kind === "organization"
                      ? "integrations.github.kindOrg"
                      : "integrations.github.kindUser",
                  )}
                </Badge>
              </label>
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  );
}

// ---------- 绑定区 ----------

export function GitHubBindSection({
  orgId,
  detail,
  title,
  description,
}: {
  orgId: string;
  detail: IntegrationDetail;
  title: string;
  description: string;
}) {
  const t = useGitT();
  const oauth = useOAuth();
  const { provider, can_manage: canManage, connections } = detail;
  const [target, setTarget] = useState<IntegrationConnection | null>(null);
  const [form, setForm] = useState(false);
  const gitlab = isGitLab(provider.id);
  const enabler =
    connections.find((c) => c.status !== "DISCONNECTED") ?? connections[0];
  return (
    <SettingsSection
      title={title}
      description={description}
      action={
        <Button
          size="32"
          disabled={!provider.configured || !canManage || oauth.busy}
          onClick={() =>
            // GitLab 系的凭据由管理员在表单里填，GitHub 用服务端应用直接授权
            gitlab ? setForm(true) : oauth.start("organization")
          }
        >
          {t("integrations.github.bindOrg")}
        </Button>
      }
    >
      {!provider.configured && (
        <Callout tone="warning">
          {t("integrations.notConfigured", { name: provider.name })}
        </Callout>
      )}
      {connections.length === 0 ? (
        <SettingsCard>
          <SettingRow
            title={t("integrations.emptyBind", { name: provider.name })}
          />
        </SettingsCard>
      ) : (
        <SettingsCard>
          {enabler && (
            <div className="flex items-center gap-3 p-4">
              <Avatar name={enabler.created_by_email} size={32} />
              <div className="flex min-w-0 flex-col gap-0.5">
                <span className="text-body-sm text-fg-muted">
                  {t("integrations.github.enabler")}
                </span>
                <span className="truncate text-body font-medium">
                  {enabler.created_by_email}
                </span>
              </div>
              <span className="ml-auto text-body-sm text-fg-muted">
                {t("integrations.github.enabledOn", {
                  date: enabler.created_at.slice(0, 10),
                })}
              </span>
            </div>
          )}
          {connections.map((connection) => (
            <OrgConnectionRow
              key={connection.id}
              orgId={orgId}
              connection={connection}
              canManage={canManage}
              onDisconnect={() => setTarget(connection)}
            />
          ))}
        </SettingsCard>
      )}
      {form && provider.id === "gitlab" && (
        <GitLabTokenDialog orgId={orgId} onClose={() => setForm(false)} />
      )}
      {form && provider.id !== "gitlab" && (
        <GitLabBindDialog
          provider={provider}
          onClose={() => setForm(false)}
          start={oauth.start}
        />
      )}
      {target && (
        <DisconnectDialog
          orgId={orgId}
          provider={provider}
          connection={target}
          body={t("integrations.github.disconnectBody", {
            org: target.display_name || target.external_id,
          })}
          onClose={() => setTarget(null)}
        />
      )}
    </SettingsSection>
  );
}

function OrgConnectionRow({
  orgId,
  connection,
  canManage,
  onDisconnect,
}: {
  orgId: string;
  connection: IntegrationConnection;
  canManage: boolean;
  onDisconnect: () => void;
}) {
  const t = useGitT();
  const oauth = useOAuth();
  const [tokenForm, setTokenForm] = useState(false);
  const login = connection.display_name || connection.external_id;
  const { status } = connection;
  const host = connectionInstanceHost(connection);
  return (
    <div className="flex flex-col">
      <div className="flex min-h-[68px] items-center gap-3 p-4">
        <Avatar name={login} src={connection.avatar_url || null} size={32} />
        <span className="flex min-w-0 flex-1 items-center gap-2">
          <span className="truncate text-body font-medium">{login}</span>
          <Badge tone="neutral">
            {t(
              connection.account_kind === "user"
                ? "integrations.github.kindUser"
                : "integrations.github.kindOrg",
            )}
          </Badge>
          {host && (
            <span className="truncate text-body-sm text-fg-muted">
              {t("integrations.gitlab.instanceOf", { host })}
            </span>
          )}
        </span>
        <div className="flex items-center gap-2">
          <Badge
            variant="status"
            small
            tone={
              status === "ACTIVE"
                ? "success"
                : status === "NEEDS_ATTENTION"
                  ? "warning"
                  : "muted"
            }
          >
            {status === "ACTIVE"
              ? t("integrations.status.bound")
              : status === "NEEDS_ATTENTION"
                ? t("integrations.github.needsReauth")
                : t("integrations.status.disconnected")}
          </Badge>
          {status !== "ACTIVE" && canManage && (
            <Button
              size="28"
              variant="outline"
              disabled={oauth.busy}
              onClick={() =>
                connection.provider === "gitlab"
                  ? setTokenForm(true)
                  : oauth.start("reauthorize", connection.id)
              }
            >
              {status === "DISCONNECTED"
                ? t("integrations.github.reconnect")
                : t("integrations.github.reauthorize")}
            </Button>
          )}
          {status !== "DISCONNECTED" && canManage && (
            <Menu
              label={t("integrations.rowMenu", { name: login })}
              placement="bottom-end"
              trigger={(props) => (
                <IconButton
                  ref={props.ref}
                  label={t("integrations.rowMenu", { name: login })}
                  icon={<Icon name="ellipsis" />}
                  size="28"
                  onClick={props.toggle}
                />
              )}
            >
              <MenuItem
                danger
                icon={<Icon name="unplug" />}
                onSelect={onDisconnect}
              >
                {t("integrations.disconnect")}
              </MenuItem>
            </Menu>
          )}
        </div>
      </div>
      {tokenForm && (
        <GitLabTokenDialog
          orgId={orgId}
          connection={connection}
          purpose={status === "DISCONNECTED" ? "organization" : "reauthorize"}
          onClose={() => setTokenForm(false)}
        />
      )}
      {status === "ACTIVE" && (
        <WebhookPanel
          orgId={orgId}
          connection={connection}
          canManage={canManage}
        />
      )}
    </div>
  );
}

// ---------- Webhook 指引 ----------

function WebhookPanel({
  orgId,
  connection,
  canManage,
}: {
  orgId: string;
  connection: IntegrationConnection;
  canManage: boolean;
}) {
  const t = useGitT();
  const [open, setOpen] = useState(false);
  const last = connection.webhook_last_delivery_at;
  return (
    <div className="flex flex-col gap-3 px-4 pb-4">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="flex w-fit items-center gap-1.5 text-body-sm text-fg-secondary hover:text-fg-default"
      >
        <Icon name={open ? "chevron-down" : "chevron-right"} />
        {last
          ? t("integrations.github.webhook.connected", { time: fmtTime(last) })
          : t("integrations.github.webhook.configure")}
      </button>
      {open &&
        (canManage ? (
          <WebhookBody orgId={orgId} connection={connection} />
        ) : (
          <p className="text-body-sm text-fg-muted">
            {t("integrations.github.webhook.adminOnly")}
          </p>
        ))}
    </div>
  );
}

export function WebhookBody({
  orgId,
  connection,
}: {
  orgId: string;
  connection: IntegrationConnection;
}) {
  const t = useGitT();
  const toast = useToast();
  const webhook = useIntegrationWebhook(orgId, connection.id, true);
  const rotate = useRotateWebhook(orgId, connection.id);
  const [confirm, setConfirm] = useState(false);
  const { id: providerId } = useGitProvider();
  const gitlab = isGitLab(providerId);
  const gitee = isGitee(providerId);
  if (webhook.isPending) return <Skeleton className="h-[120px] rounded-lg" />;
  if (webhook.isError)
    return (
      <p role="alert" className="text-body-sm text-fg-muted">
        {t("integrations.github.webhook.loadFailed")}
      </p>
    );
  const data = webhook.data;
  return (
    <div className="flex flex-col gap-3 rounded-lg bg-surface-muted p-3">
      <ol className="flex list-decimal flex-col gap-1 pl-5 text-body-sm text-fg-secondary">
        <li>{t("integrations.github.webhook.step1")}</li>
        <li>{t("integrations.github.webhook.step2")}</li>
      </ol>
      <CopyField
        label={t("integrations.github.webhook.payloadUrl")}
        value={data.payload_url}
      />
      {/* GitLab、Gitee 没有 Content type 这一项 */}
      {!gitlab && !gitee && (
        <CopyField
          label={t("integrations.github.webhook.contentType")}
          value={data.content_type}
        />
      )}
      <CopyField
        label={t("integrations.github.webhook.secret")}
        value={data.secret}
        masked
      />
      {gitlab && (
        <p className="text-body-sm text-fg-muted">
          {t("integrations.gitlab.webhook.secretHint")}
        </p>
      )}
      {gitee && (
        <p className="text-body-sm text-fg-muted">
          {t("integrations.gitee.webhook.secretHint")}
        </p>
      )}
      <div className="flex flex-col gap-1">
        <span className="text-body-sm font-medium">
          {t("integrations.github.webhook.events")}
        </span>
        <span className="text-body-sm text-fg-muted">
          {gitlab || gitee
            ? t(
                gitee
                  ? "integrations.gitee.webhook.eventsDesc"
                  : "integrations.gitlab.webhook.eventsDesc",
                { events: data.events.join("、") },
              )
            : t("integrations.github.webhook.eventsDesc")}
        </span>
      </div>
      <div>
        <Button size="28" variant="outline" onClick={() => setConfirm(true)}>
          {t("integrations.github.webhook.rotate")}
        </Button>
      </div>
      <DeliveryTable webhook={data} />
      <ConfirmDialog
        open={confirm}
        danger
        title={t("integrations.github.webhook.rotateTitle")}
        description={t("integrations.github.webhook.rotateBody")}
        confirmLabel={t("integrations.github.webhook.rotate")}
        loading={rotate.isPending}
        onCancel={() => setConfirm(false)}
        onConfirm={() =>
          rotate.mutate(data.version, {
            onSuccess: () => {
              toast(t("integrations.github.webhook.rotated"));
              setConfirm(false);
            },
            onError: (error) => {
              toast(integrationErrorText(t, error), { tone: "error" });
              setConfirm(false);
            },
          })
        }
      />
    </div>
  );
}

function CopyField({
  label,
  value,
  masked = false,
}: {
  label: string;
  value: string;
  masked?: boolean;
}) {
  const t = useGitT();
  const copy = useCopy();
  const [shown, setShown] = useState(false);
  return (
    <div className="flex flex-col gap-1">
      <span className="text-body-sm font-medium">{label}</span>
      <div className="flex items-center gap-1.5">
        <TextInput
          readOnly
          aria-label={label}
          value={masked && !shown ? "••••••••••••••••" : value}
        />
        {masked && (
          <IconButton
            label={
              shown
                ? t("integrations.github.webhook.hide")
                : t("integrations.github.webhook.show")
            }
            icon={<Icon name={shown ? "eye-off" : "eye"} />}
            size="28"
            onClick={() => setShown((v) => !v)}
          />
        )}
        <IconButton
          label={`${t("integrations.github.webhook.copy")} ${label}`}
          icon={<Icon name="copy" />}
          size="28"
          onClick={() =>
            void copy(value, t("integrations.github.webhook.copied"))
          }
        />
      </div>
    </div>
  );
}

function DeliveryTable({ webhook }: { webhook: IntegrationWebhook }) {
  const t = useGitT();
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-body-sm font-medium">
        {t("integrations.github.webhook.recent")}
      </span>
      {webhook.recent.length === 0 ? (
        <span className="text-body-sm text-fg-muted">
          {t("integrations.github.webhook.noDeliveries")}
        </span>
      ) : (
        <table className="w-full text-left text-body-sm">
          <thead className="text-fg-muted">
            <tr>
              <th className="py-1 font-normal">
                {t("integrations.github.webhook.event")}
              </th>
              <th className="py-1 font-normal">
                {t("integrations.github.webhook.status")}
              </th>
              <th className="py-1 font-normal">
                {t("integrations.github.webhook.detail")}
              </th>
              <th className="py-1 font-normal">
                {t("integrations.github.webhook.linked")}
              </th>
              <th className="py-1 font-normal">
                {t("integrations.github.webhook.time")}
              </th>
            </tr>
          </thead>
          <tbody>
            {webhook.recent.map((row, index) => (
              <tr key={index} className="border-t border-line-soft">
                <td className="py-1">
                  {row.action ? `${row.event}.${row.action}` : row.event}
                </td>
                <td className="py-1">
                  {t(`integrations.github.webhook.statuses.${row.status}`)}
                </td>
                <td className="py-1 text-fg-muted">{row.detail}</td>
                <td className="py-1">{row.linked_count}</td>
                <td className="py-1 text-fg-muted">
                  {fmtTime(row.received_at)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

// ---------- 个人账号 ----------

export function GitHubPersonalSection({
  orgId,
  detail,
  title,
  description,
}: {
  orgId: string;
  detail: IntegrationDetail;
  title: string;
  description: string;
}) {
  const t = useGitT();
  const oauth = useOAuth();
  const [tokenTarget, setTokenTarget] = useState<{
    purpose: "personal" | "reauthorize";
    connection: IntegrationConnection;
  } | null>(null);
  const { provider } = detail;
  const [target, setTarget] = useState<IntegrationConnection | null>(null);
  const mine = detail.personal_connections.filter(
    (c) => c.status !== "DISCONNECTED",
  );
  const first = mine[0];
  // GitLab 系：个人账号挂在某个已连接的实例（组织连接）下，需要先选实例
  const gitlab = isGitLab(provider.id);
  const instances = detail.connections.filter((c) => c.status === "ACTIVE");
  const linked = new Set(mine.map((c) => c.parent_connection_id));
  const free = instances.filter((c) => !linked.has(c.id));
  const [picked, setPicked] = useState("");
  const chosen = free.find((c) => c.id === picked)?.id ?? free[0]?.id ?? "";
  const instanceLabel = (c: IntegrationConnection) => {
    const host = connectionInstanceHost(c);
    const name = c.display_name || c.external_id;
    return host ? `${name} · ${host}` : name;
  };
  const gitlabAction =
    free.length === 0 ? undefined : (
      <div className="flex items-center gap-2">
        <Select
          aria-label={t("integrations.gitlab.pickInstance")}
          value={chosen}
          options={free.map((c) => ({ value: c.id, label: instanceLabel(c) }))}
          onChange={setPicked}
        />
        <Button
          size="32"
          disabled={!chosen || oauth.busy}
          onClick={() => {
            const connection = instances.find((c) => c.id === chosen);
            if (provider.id === "gitlab" && connection)
              setTokenTarget({ purpose: "personal", connection });
            else oauth.start("personal", chosen);
          }}
        >
          {t("integrations.github.linkAccount")}
        </Button>
      </div>
    );
  return (
    <SettingsSection
      title={title}
      description={description}
      action={
        gitlab ? (
          gitlabAction
        ) : first && first.status === "ACTIVE" ? undefined : (
          <Button
            size="32"
            disabled={!provider.configured || oauth.busy}
            onClick={() => oauth.start("personal")}
          >
            {t("integrations.github.linkAccount")}
          </Button>
        )
      }
    >
      <SettingsCard>
        {mine.length === 0 ? (
          <SettingRow
            title={
              gitlab && instances.length === 0
                ? t("integrations.gitlab.noInstance")
                : t("integrations.github.personalDesc")
            }
          />
        ) : (
          mine.map((connection) => {
            const login = connection.display_name || connection.external_id;
            return (
              <div
                key={connection.id}
                className="flex min-h-[68px] items-center gap-3 p-4"
              >
                <Avatar
                  name={login}
                  src={connection.avatar_url || null}
                  size={32}
                />
                <span className="min-w-0 flex-1 truncate text-body font-medium">
                  {login}
                  {gitlab && (
                    <span className="ml-2 text-body-sm font-normal text-fg-muted">
                      {connectionInstanceHost(
                        detail.connections.find(
                          (c) => c.id === connection.parent_connection_id,
                        ) ?? connection,
                      )}
                    </span>
                  )}
                </span>
                {connection.status === "NEEDS_ATTENTION" && (
                  <>
                    <Badge variant="status" small tone="warning">
                      {t("integrations.github.needsReauth")}
                    </Badge>
                    {gitlab && (
                      <Button
                        size="28"
                        variant="outline"
                        disabled={oauth.busy}
                        onClick={() =>
                          provider.id === "gitlab"
                            ? setTokenTarget({
                                purpose: "reauthorize",
                                connection,
                              })
                            : oauth.start("reauthorize", connection.id)
                        }
                      >
                        {t("integrations.github.reauthorize")}
                      </Button>
                    )}
                  </>
                )}
                <Menu
                  label={t("integrations.rowMenu", { name: login })}
                  placement="bottom-end"
                  trigger={(props) => (
                    <IconButton
                      ref={props.ref}
                      label={t("integrations.rowMenu", { name: login })}
                      icon={<Icon name="ellipsis" />}
                      size="28"
                      onClick={props.toggle}
                    />
                  )}
                >
                  <MenuItem
                    danger
                    icon={<Icon name="unplug" />}
                    onSelect={() => setTarget(connection)}
                  >
                    {t("integrations.disconnect")}
                  </MenuItem>
                </Menu>
              </div>
            );
          })
        )}
      </SettingsCard>
      {tokenTarget && (
        <GitLabTokenDialog
          orgId={orgId}
          {...tokenTarget}
          onClose={() => setTokenTarget(null)}
        />
      )}
      {target && (
        <DisconnectDialog
          orgId={orgId}
          provider={provider}
          connection={target}
          onClose={() => setTarget(null)}
        />
      )}
    </SettingsSection>
  );
}

// ---------- 设置：PR / Commit 关联与分支格式 ----------

export function useSettingsSave(orgId: string) {
  const t = useGitT();
  const toast = useToast();
  const { id: providerId } = useGitProvider();
  const settings = useIntegrationSettings(orgId, providerId);
  const put = usePutIntegrationSettings(orgId, providerId);
  const save = (patch: Partial<IntegrationSettingsValues>) => {
    const current = settings.data;
    if (!current) return;
    put.mutate(
      { version: current.version, settings: { ...current.settings, ...patch } },
      {
        onError: (error) =>
          toast(
            isApiError(error, "VERSION_CONFLICT")
              ? t("integrations.errors.versionConflict")
              : integrationErrorText(t, error),
            { tone: "error" },
          ),
      },
    );
  };
  return { settings, put, save };
}

export function GitHubPrSection({
  orgId,
  detail,
  title,
  description,
}: {
  orgId: string;
  detail: IntegrationDetail;
  title: string;
  description: string;
}) {
  const t = useGitT();
  const { settings, put, save } = useSettingsSave(orgId);
  const bound = detail.connections.some((c) => c.status !== "DISCONNECTED");
  const data = settings.data;
  const disabled = !data?.can_manage || put.isPending;
  const values = data?.settings;
  const row = (
    key: keyof IntegrationSettingsValues,
    label: string,
    desc?: string,
  ) => (
    <SettingRow
      key={key}
      title={label}
      description={desc}
      control={
        <Switch
          label={label}
          checked={Boolean(values?.[key])}
          disabled={disabled}
          onCheckedChange={(checked) => save({ [key]: checked })}
        />
      }
    />
  );
  return (
    <SettingsSection title={title} description={description}>
      {!bound && (
        <Callout tone="warning">
          {t("integrations.github.pr.needWebhook")}
        </Callout>
      )}
      {settings.isPending ? (
        <Skeleton className="h-[120px] rounded-xl" />
      ) : settings.isError || !values ? (
        <p role="alert" className="text-body-sm text-fg-muted">
          {t("integrations.github.pr.loadFailed")}
        </p>
      ) : (
        <SettingsCard>
          {row(
            "link_pull_requests",
            t("integrations.github.pr.linkPullRequests"),
            t("integrations.github.pr.linkPullRequestsDesc"),
          )}
          {row(
            "link_commits",
            t("integrations.github.pr.linkCommits"),
            t("integrations.github.pr.linkCommitsDesc"),
          )}
          <SettingRow
            title={t("integrations.github.pr.backlink")}
            description={t("integrations.github.pr.backlinkDesc")}
          />
          {row(
            "pr_backlink_private",
            t("integrations.github.pr.backlinkPrivate"),
          )}
          {row(
            "pr_backlink_public",
            t("integrations.github.pr.backlinkPublic"),
          )}
        </SettingsCard>
      )}
    </SettingsSection>
  );
}

export function GitHubMoreSection({
  orgId,
  title,
  description,
}: {
  orgId: string;
  title: string;
  description: string;
}) {
  const t = useGitT();
  const { settings, put, save } = useSettingsSave(orgId);
  const data = settings.data;
  return (
    <SettingsSection title={title}>
      <SettingsCard>
        <SettingRow
          icon="git-branch"
          title={t("integrations.github.branchFormat")}
          description={description}
          control={
            data ? (
              <Select
                aria-label={t("integrations.github.branchFormat")}
                value={data.settings.branch_format}
                disabled={!data.can_manage || put.isPending}
                options={BRANCH_FORMATS.map((value) => ({
                  value,
                  label: value,
                }))}
                onChange={(branch_format) => save({ branch_format })}
              />
            ) : (
              <Skeleton className="h-8 w-[220px] rounded-lg" />
            )
          }
        />
      </SettingsCard>
    </SettingsSection>
  );
}
