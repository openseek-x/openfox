import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type ReactNode } from "react";

import {
  isApiError,
  type FeishuApp,
  type FeishuAppView,
  type FeishuGuide,
  type IntegrationConnection,
  type IntegrationProvider,
} from "@openfox/api";
import { type MessageKey, type Translate } from "@openfox/i18n";
// useIMT 与 useT 用法相同，只是把文案里的品牌词换成当前服务商（飞书 / Lark）。
import { useIMProvider, useIMT as useT } from "./im-provider";
import {
  Avatar,
  Badge,
  Button,
  Callout,
  ConfirmDialog,
  Dialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Skeleton,
  Switch,
  TextInput,
  cn,
  useToast,
} from "@openfox/ui";

import { keys } from "../context";
import {
  SettingRow,
  SettingsCard,
  SettingsSection,
} from "../spaces/settings/parts";
import { copyText } from "../tasks/parts";
import { DisconnectDialog } from "./DisconnectDialog";
import {
  DINGTALK_APP_KEY_PATTERN,
  FEISHU_APP_ID_PATTERN,
  FEISHU_PENDING_SLOW_MS,
  oauthErrorKey,
  parseOAuthMessage,
  popupFeatures,
} from "./model";
import {
  feishuSubmitErrorText,
  integrationErrorText,
  useDisconnectConnection,
  useFeishuApp,
  useFeishuMutations,
} from "./queries";

// 飞书详情页：绑定应用、诊断条、配置指引、我的飞书账号。

const STEP_COUNT = 6;

const STATUS_TEXT: Record<FeishuApp["status"], MessageKey> = {
  PENDING: "integrations.feishu.status.pending",
  CONNECTED: "integrations.feishu.status.connected",
  NEEDS_ATTENTION: "integrations.feishu.status.needsAttention",
};

/** 与 connectionTone 同语义：已连接为成功，需要重新验证为警告，待连接为中性。 */
const STATUS_TONE: Record<
  FeishuApp["status"],
  "success" | "warning" | "muted"
> = {
  CONNECTED: "success",
  NEEDS_ATTENTION: "warning",
  PENDING: "muted",
};

/** 把飞书应用 / 个人绑定转成 DisconnectDialog 需要的连接对象（只用到 id、version、scope）。 */
function asConnection(
  provider: string,
  scope: IntegrationConnection["scope"],
  id: string,
  version: number,
  name: string,
): IntegrationConnection {
  return {
    id,
    provider,
    scope,
    external_id: name,
    display_name: name,
    version,
  } as IntegrationConnection;
}

/** 距今的相对时间：刚刚 / N 分钟前 / N 小时前 / N 天前。 */
export function relativeTime(t: Translate, iso: string, now = Date.now()) {
  const minutes = Math.floor((now - Date.parse(iso)) / 60_000);
  if (minutes < 1) return t("integrations.feishu.time.justNow");
  if (minutes < 60)
    return t("integrations.feishu.time.minutes", { n: minutes });
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return t("integrations.feishu.time.hours", { n: hours });
  return t("integrations.feishu.time.days", { n: Math.floor(hours / 24) });
}

/** 绑定 / 更新凭证的错误码对应文案；其余走通用错误。 */

/** 渲染加载骨架 / 失败态，成功后把数据交给 children。 */
function Loading({
  query,
  title,
  description,
  children,
}: {
  query: ReturnType<typeof useFeishuApp>;
  title: string;
  description: string;
  children: (view: FeishuAppView) => ReactNode;
}) {
  const t = useT();
  if (query.isPending)
    return (
      <SettingsSection title={title} description={description}>
        <Skeleton className="h-[68px] rounded-xl" />
      </SettingsSection>
    );
  if (query.isError)
    return (
      <SettingsSection title={title} description={description}>
        <p role="alert" className="text-body text-fg-muted">
          {t("integrations.loadFailed")}
        </p>
      </SettingsSection>
    );
  return <>{children(query.data)}</>;
}

// ---------- 绑定分区 ----------

export function FeishuBindSection({
  orgId,
  provider,
  title,
  description,
}: {
  orgId: string;
  provider: IntegrationProvider;
  title: string;
  description: string;
}) {
  const query = useFeishuApp(orgId);
  return (
    <Loading query={query} title={title} description={description}>
      {(view) => (
        <BindBody
          orgId={orgId}
          provider={provider}
          view={view}
          title={title}
          description={description}
        />
      )}
    </Loading>
  );
}

function BindBody({
  orgId,
  provider,
  view,
  title,
  description,
}: {
  orgId: string;
  provider: IntegrationProvider;
  view: FeishuAppView;
  title: string;
  description: string;
}) {
  const t = useT();
  const { app, can_manage: canManage } = view;
  const [dialog, setDialog] = useState<"bind" | "update" | "guide" | null>(
    null,
  );
  return (
    <SettingsSection
      title={title}
      description={description}
      action={
        app ? undefined : (
          <Button
            size="32"
            disabled={!canManage}
            onClick={() => setDialog("bind")}
          >
            {t("integrations.feishu.bind.button")}
          </Button>
        )
      }
    >
      {app ? (
        <BoundApp
          orgId={orgId}
          provider={provider}
          view={view}
          app={app}
          onOpen={setDialog}
        />
      ) : (
        <SettingsCard>
          <SettingRow
            title={t("integrations.feishu.bind.title")}
            description={t("integrations.feishu.bind.empty")}
          />
        </SettingsCard>
      )}
      {!app && !canManage && (
        <p className="px-1 text-body-sm text-fg-muted">
          {t("integrations.feishu.bind.adminOnly")}
        </p>
      )}
      {(dialog === "bind" || dialog === "update") && (
        <FeishuBindDialog
          orgId={orgId}
          guide={view.guide}
          app={dialog === "update" ? app : null}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog === "guide" && (
        <Dialog
          open
          onClose={() => setDialog(null)}
          title={t("integrations.feishu.guide.title")}
          description={t("integrations.feishu.guide.intro")}
          width={560}
          footer={
            <Button variant="outline" onClick={() => setDialog(null)}>
              {t("common.close")}
            </Button>
          }
        >
          <GuideTimeline guide={view.guide} />
        </Dialog>
      )}
    </SettingsSection>
  );
}

function BoundApp({
  orgId,
  provider,
  view,
  app,
  onOpen,
}: {
  orgId: string;
  provider: IntegrationProvider;
  view: FeishuAppView;
  app: FeishuApp;
  onOpen: (dialog: "update" | "guide") => void;
}) {
  const t = useT();
  const toast = useToast();
  const { verify, setLinkPreview } = useFeishuMutations(orgId);
  const isDingTalk = useIMProvider() === "dingtalk";
  const [disconnecting, setDisconnecting] = useState(false);
  const canManage = view.can_manage;
  const label = t("integrations.feishu.menu.label");
  const runVerify = () =>
    verify.mutate(undefined, {
      onSuccess: () => toast(t("integrations.feishu.menu.verified")),
      onError: (error) =>
        toast(feishuSubmitErrorText(t, error), { tone: "error" }),
    });
  return (
    <>
      <SettingsCard>
        <div className="flex min-h-[68px] items-center gap-3 p-4">
          <Avatar
            name={app.bot_name || app.app_id}
            src={app.bot_avatar_url || null}
            size={32}
          />
          <span className="flex min-w-0 flex-1 flex-col gap-0.5">
            <span className="truncate text-body font-medium">
              {app.bot_name || app.app_id}
            </span>
            <span className="truncate text-body-sm text-fg-muted">
              {isDingTalk
                ? t("integrations.dingtalk.appKey", { id: app.app_id })
                : t("integrations.feishu.appId", { id: app.app_id })}
            </span>
          </span>
          <Badge variant="status" small tone={STATUS_TONE[app.status]}>
            {t(STATUS_TEXT[app.status])}
          </Badge>
          {canManage && (
            <Menu
              label={label}
              placement="bottom-end"
              trigger={(props) => (
                <IconButton
                  ref={props.ref}
                  label={label}
                  icon={<Icon name="ellipsis" />}
                  size="28"
                  onClick={props.toggle}
                />
              )}
            >
              <MenuItem icon={<Icon name="refresh-cw" />} onSelect={runVerify}>
                {t("integrations.feishu.menu.verify")}
              </MenuItem>
              <MenuItem
                icon={<Icon name="key-round" />}
                onSelect={() => onOpen("update")}
              >
                {t("integrations.feishu.menu.update")}
              </MenuItem>
              <MenuItem
                icon={<Icon name="book-open" />}
                onSelect={() => onOpen("guide")}
              >
                {t("integrations.feishu.menu.guide")}
              </MenuItem>
              <MenuItem
                danger
                icon={<Icon name="unplug" />}
                onSelect={() => setDisconnecting(true)}
              >
                {t("integrations.disconnect")}
              </MenuItem>
            </Menu>
          )}
        </div>
        <DiagnosticBar app={app} />
        {!isDingTalk && (
          <SettingRow
            title={t("integrations.feishu.linkPreview.title")}
            description={t("integrations.feishu.linkPreview.desc")}
            control={
              <Switch
                label={t("integrations.feishu.linkPreview.title")}
                checked={app.link_preview_enabled}
                disabled={!canManage || setLinkPreview.isPending}
                onCheckedChange={(next) =>
                  setLinkPreview.mutate(
                    { link_preview_enabled: next, version: app.version },
                    {
                      onError: (error) =>
                        toast(feishuSubmitErrorText(t, error), {
                          tone: "error",
                        }),
                    },
                  )
                }
              />
            }
          />
        )}
      </SettingsCard>
      {disconnecting && (
        <DisconnectDialog
          orgId={orgId}
          provider={provider}
          connection={asConnection(
            provider.id,
            "ORGANIZATION",
            app.connection_id,
            app.version,
            app.bot_name || app.app_id,
          )}
          body={t("integrations.feishu.disconnectBody")}
          onClose={() => setDisconnecting(false)}
        />
      )}
    </>
  );
}

/** 诊断条：按状态给出原因、排查建议或长连接健康信息。 */
function DiagnosticBar({ app }: { app: FeishuApp }) {
  const t = useT();
  if (app.status === "NEEDS_ATTENTION") {
    const known: Record<string, MessageKey> = {
      credentials_invalid: "integrations.feishu.diag.credentialsInvalid",
      bot_unavailable: "integrations.feishu.errors.botUnavailable",
      connection_limit: "integrations.feishu.diag.connectionLimit",
    };
    const key = known[app.status_reason];
    return (
      <div className="px-4 pb-4">
        <Callout tone="warning">
          {key ? t(key) : app.last_error || t("integrations.errors.generic")}
        </Callout>
      </div>
    );
  }
  if (app.status === "PENDING") {
    const slow =
      Date.now() - Date.parse(app.updated_at) > FEISHU_PENDING_SLOW_MS;
    if (!slow) return null;
    return (
      <div className="px-4 pb-4">
        <Callout tone="info">
          {t("integrations.feishu.diag.pendingSlow")}
          {app.last_error && (
            <span className="mt-1 block text-fg-muted">{app.last_error}</span>
          )}
        </Callout>
      </div>
    );
  }
  return (
    <p className="px-4 pb-4 text-caption text-fg-muted">
      {app.last_event_at
        ? t("integrations.feishu.diag.healthy", {
            time: relativeTime(t, app.last_event_at),
          })
        : t("integrations.feishu.diag.healthyNoEvent")}
    </p>
  );
}

// ---------- 绑定对话框与配置指引 ----------

function FeishuBindDialog({
  orgId,
  guide,
  app,
  onClose,
}: {
  orgId: string;
  guide: FeishuGuide;
  /** 非空表示更新凭证：App ID 预填且只读 */
  app: FeishuApp | null;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const { put } = useFeishuMutations(orgId);
  const isDingTalk = useIMProvider() === "dingtalk";
  const idLabel = isDingTalk
    ? t("integrations.dingtalk.dialog.appKeyLabel")
    : "App ID";
  const secretLabel = isDingTalk
    ? t("integrations.dingtalk.dialog.appSecretLabel")
    : "App Secret";
  const [appId, setAppId] = useState(app?.app_id ?? "");
  const [secret, setSecret] = useState("");
  const [shown, setShown] = useState(false);
  const [error, setError] = useState("");
  const idValid = (
    isDingTalk ? DINGTALK_APP_KEY_PATTERN : FEISHU_APP_ID_PATTERN
  ).test(appId.trim());
  const secretValid = secret.trim().length >= 16 && secret.trim().length <= 128;
  const ready = idValid && secretValid;
  const submit = () => {
    if (!ready || put.isPending) return;
    setError("");
    put.mutate(
      {
        app_id: appId.trim(),
        app_secret: secret.trim(),
        ...(app ? { version: app.version } : {}),
      },
      {
        onSuccess: () => {
          toast(
            t(
              app
                ? "integrations.feishu.toast.updated"
                : "integrations.feishu.toast.bound",
            ),
          );
          onClose();
        },
        onError: (e) => setError(feishuSubmitErrorText(t, e)),
      },
    );
  };
  return (
    <Dialog
      open
      onClose={put.isPending ? undefined : onClose}
      title={t("integrations.feishu.dialog.title")}
      description={t("integrations.feishu.dialog.subtitle")}
      width={880}
    >
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-2 rounded-xl border border-line-default p-4">
            <span className="text-body font-medium">
              {t("integrations.feishu.dialog.step1Title")}
            </span>
            <p className="text-body-sm text-fg-muted">
              {isDingTalk
                ? t("integrations.dingtalk.dialog.step1Desc")
                : t("integrations.feishu.dialog.step1Desc")}
            </p>
            <div>
              <Button
                variant="outline"
                size="28"
                onClick={() =>
                  window.open(
                    guide.open_platform_url,
                    "_blank",
                    "noopener,noreferrer",
                  )
                }
              >
                {t("integrations.feishu.dialog.openPlatform")}
              </Button>
            </div>
          </div>
          <form
            className="flex flex-col gap-3 rounded-xl border border-line-default p-4"
            onSubmit={(event) => {
              event.preventDefault();
              submit();
            }}
          >
            <span className="text-body font-medium">
              {isDingTalk
                ? t("integrations.dingtalk.dialog.step2Title")
                : t("integrations.feishu.dialog.step2Title")}
            </span>
            <p className="text-body-sm text-fg-muted">
              {isDingTalk
                ? t("integrations.dingtalk.dialog.step2Desc")
                : t("integrations.feishu.dialog.step2Desc")}
            </p>
            <label className="flex flex-col gap-1 text-body-sm font-medium">
              {idLabel}
              <TextInput
                aria-label={idLabel}
                value={appId}
                readOnly={app !== null}
                placeholder={
                  isDingTalk ? "dingxxxxxxxxxxxxxxxx" : "cli_xxxxxxxxxxxxxxxx"
                }
                onChange={(event) => setAppId(event.target.value)}
              />
              {appId !== "" && !idValid && (
                <span className="text-caption font-normal text-danger">
                  {isDingTalk
                    ? t("integrations.dingtalk.dialog.appKeyInvalid")
                    : t("integrations.feishu.dialog.appIdInvalid")}
                </span>
              )}
            </label>
            <div className="flex flex-col gap-1">
              <span className="text-body-sm font-medium">{secretLabel}</span>
              <div className="flex items-center gap-1.5">
                <TextInput
                  aria-label={secretLabel}
                  type={shown ? "text" : "password"}
                  autoComplete="off"
                  value={secret}
                  onChange={(event) => setSecret(event.target.value)}
                />
                <IconButton
                  label={t(
                    shown
                      ? isDingTalk
                        ? "integrations.dingtalk.dialog.hideSecret"
                        : "integrations.feishu.dialog.hideSecret"
                      : isDingTalk
                        ? "integrations.dingtalk.dialog.showSecret"
                        : "integrations.feishu.dialog.showSecret",
                  )}
                  icon={<Icon name={shown ? "eye-off" : "eye"} />}
                  size="28"
                  onClick={() => setShown((v) => !v)}
                />
              </div>
            </div>
            {secret !== "" && !secretValid && (
              <p className="text-caption text-danger">
                {t("integrations.feishu.dialog.secretInvalid")}
              </p>
            )}
            {error && (
              <p role="alert" className="text-body-sm text-danger">
                {error}
              </p>
            )}
            <div>
              <Button type="submit" disabled={!ready} loading={put.isPending}>
                {t("integrations.feishu.dialog.submit")}
              </Button>
            </div>
          </form>
        </div>
        <div className="flex flex-col gap-3 rounded-xl border border-line-default p-4">
          <span className="text-body font-medium">
            {t("integrations.feishu.guide.title")}
          </span>
          <p className="text-body-sm text-fg-muted">
            {t("integrations.feishu.guide.intro")}
          </p>
          <GuideTimeline guide={guide} />
        </div>
      </div>
    </Dialog>
  );
}

function CopyButton({ value, label }: { value: string; label: string }) {
  const t = useT();
  const toast = useToast();
  return (
    <IconButton
      label={label}
      icon={<Icon name="copy" />}
      size="28"
      onClick={() =>
        void copyText(value).then((ok) =>
          ok
            ? toast(t("integrations.feishu.toast.copied"))
            : toast(t("integrations.feishu.toast.copyFailed"), {
                tone: "error",
              }),
        )
      }
    />
  );
}

/** 6 步时间线：全部展开，点击切换当前步。 */
function GuideTimeline({ guide }: { guide: FeishuGuide }) {
  const provider = useIMProvider();
  return provider === "dingtalk" ? (
    <DingTalkGuideTimeline guide={guide} />
  ) : (
    <FeishuGuideTimeline guide={guide} />
  );
}

/** 钉钉的 5 步配置指引，沿用飞书指引的版式。 */
function DingTalkGuideTimeline({ guide }: { guide: FeishuGuide }) {
  const t = useT();
  const [current, setCurrent] = useState(1);
  const mono =
    "rounded-md bg-surface-muted px-2 py-1 font-mono text-caption break-all";
  const steps: Array<{ title: string; body: ReactNode }> = [
    {
      title: t("integrations.dingtalk.guide.s1Title"),
      body: <p>{t("integrations.dingtalk.guide.s1Desc")}</p>,
    },
    {
      title: t("integrations.dingtalk.guide.s2Title"),
      body: (
        <div className="flex flex-col gap-1.5">
          <p>{t("integrations.dingtalk.guide.s2Desc")}</p>
          <div className="flex items-start gap-1.5">
            <pre
              className={cn(mono, "max-h-[120px] min-w-0 flex-1 overflow-auto")}
            >
              {guide.scopes_json}
            </pre>
            <CopyButton
              value={guide.scopes_json}
              label={t("integrations.dingtalk.guide.copyPermissions")}
            />
          </div>
        </div>
      ),
    },
    {
      title: t("integrations.dingtalk.guide.s3Title"),
      body: (
        <p className="flex flex-wrap items-center gap-1.5">
          {t("integrations.dingtalk.guide.s3Desc")}{" "}
          {guide.callbacks.map((c) => (
            <code key={c} className={mono}>
              {c}
            </code>
          ))}
        </p>
      ),
    },
    {
      title: t("integrations.dingtalk.guide.s4Title"),
      body: (
        <div className="flex flex-col gap-1.5">
          <p>{t("integrations.dingtalk.guide.s4Desc")}</p>
          <div className="flex items-center gap-1.5">
            <code className={cn(mono, "min-w-0 flex-1")}>
              {guide.redirect_uri}
            </code>
            <CopyButton
              value={guide.redirect_uri}
              label={t("integrations.dingtalk.guide.copyRedirect")}
            />
          </div>
        </div>
      ),
    },
    {
      title: t("integrations.dingtalk.guide.s5Title"),
      body: <p>{t("integrations.dingtalk.guide.s5Desc")}</p>,
    },
  ];
  return (
    <div className="flex flex-col gap-3">
      <ol className="flex flex-col gap-3">
        {steps.map((step, index) => {
          const n = index + 1;
          return (
            <li key={n} className="flex gap-2.5">
              <button
                type="button"
                aria-current={current === n ? "step" : undefined}
                aria-label={t("integrations.feishu.guide.stepOf", {
                  n,
                  total: steps.length,
                })}
                onClick={() => setCurrent(n)}
                className={cn(
                  "flex size-5 shrink-0 items-center justify-center rounded-full text-caption",
                  current === n
                    ? "bg-fg-default text-surface-card"
                    : "bg-surface-muted text-fg-secondary",
                )}
              >
                {n}
              </button>
              <div className="flex min-w-0 flex-1 flex-col gap-1 text-body-sm text-fg-muted">
                <span className="text-body-sm font-medium text-fg-default">
                  {step.title}
                </span>
                {step.body}
              </div>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

function FeishuGuideTimeline({ guide }: { guide: FeishuGuide }) {
  const t = useT();
  const [current, setCurrent] = useState(1);
  const mono =
    "rounded-md bg-surface-muted px-2 py-1 font-mono text-caption break-all";
  const copyRow = (value: string, label: string) => (
    <div className="flex items-center gap-1.5">
      <code className={cn(mono, "min-w-0 flex-1")}>{value}</code>
      <CopyButton value={value} label={label} />
    </div>
  );
  const previewTag = (
    <Badge tone="neutral">{t("integrations.feishu.guide.previewOnly")}</Badge>
  );
  const steps: Array<{ title: string; body: ReactNode }> = [
    {
      title: t("integrations.feishu.guide.s1Title"),
      body: <p>{t("integrations.feishu.guide.s1Desc")}</p>,
    },
    {
      title: t("integrations.feishu.guide.s2Title"),
      body: (
        <div className="flex flex-col gap-1.5">
          <p>{t("integrations.feishu.guide.s2Bot")}</p>
          <p className="flex items-center gap-1.5">
            {t("integrations.feishu.guide.s2Preview")}
            {previewTag}
          </p>
          {copyRow(
            guide.link_preview_pattern,
            t("integrations.feishu.guide.copyPattern"),
          )}
        </div>
      ),
    },
    {
      title: t("integrations.feishu.guide.s3Title"),
      body: (
        <div className="flex flex-col gap-1.5">
          <p>{t("integrations.feishu.guide.s3Desc")}</p>
          <div className="flex items-start gap-1.5">
            <pre
              className={cn(mono, "max-h-[120px] min-w-0 flex-1 overflow-auto")}
            >
              {guide.scopes_json}
            </pre>
            <CopyButton
              value={guide.scopes_json}
              label={t("integrations.feishu.guide.copyScopes")}
            />
          </div>
        </div>
      ),
    },
    {
      title: t("integrations.feishu.guide.s4Title"),
      body: (
        <div className="flex flex-col gap-1.5">
          <p>{t("integrations.feishu.guide.s4Mode")}</p>
          <p>
            {t("integrations.feishu.guide.s4Events")}{" "}
            {guide.events.map((e) => (
              <code key={e} className={mono}>
                {e}
              </code>
            ))}
          </p>
          <p className="flex flex-wrap items-center gap-1.5">
            {t("integrations.feishu.guide.s4Callbacks")}{" "}
            {guide.callbacks.map((c) => (
              <code key={c} className={mono}>
                {c}
              </code>
            ))}
            {previewTag}
          </p>
          <p>{t("integrations.feishu.guide.s4Redirect")}</p>
          {copyRow(
            guide.redirect_uri,
            t("integrations.feishu.guide.copyRedirect"),
          )}
        </div>
      ),
    },
    {
      title: t("integrations.feishu.guide.s5Title"),
      body: <p>{t("integrations.feishu.guide.s5Desc")}</p>,
    },
    {
      title: t("integrations.feishu.guide.s6Title"),
      body: (
        <p className="flex items-center gap-1.5">
          {t("integrations.feishu.guide.s6Desc")}
          <Badge tone="neutral">
            {t("integrations.feishu.guide.inOpenfox")}
          </Badge>
        </p>
      ),
    },
  ];
  return (
    <div className="flex flex-col gap-3">
      <ol className="flex flex-col gap-3">
        {steps.map((step, index) => {
          const n = index + 1;
          return (
            <li key={n} className="flex gap-2.5">
              <button
                type="button"
                aria-current={current === n ? "step" : undefined}
                aria-label={t("integrations.feishu.guide.stepOf", {
                  n,
                  total: STEP_COUNT,
                })}
                onClick={() => setCurrent(n)}
                className={cn(
                  "flex size-5 shrink-0 items-center justify-center rounded-full text-caption",
                  current === n
                    ? "bg-fg-default text-surface-card"
                    : "bg-surface-muted text-fg-secondary",
                )}
              >
                {n}
              </button>
              <div className="flex min-w-0 flex-1 flex-col gap-1 text-body-sm text-fg-muted">
                <span className="text-body-sm font-medium text-fg-default">
                  {step.title}
                </span>
                {step.body}
              </div>
            </li>
          );
        })}
      </ol>
      <span className="text-caption text-fg-muted">
        {t("integrations.feishu.guide.stepOf", {
          n: current,
          total: STEP_COUNT,
        })}
      </span>
    </div>
  );
}

// ---------- 我的飞书账号分区 ----------

export function FeishuPersonalSection({
  orgId,
  title,
}: {
  orgId: string;
  title: string;
}) {
  const t = useT();
  // 飞书的个人账号用于接收通知，不沿用通用的「同步归属」说明
  const description = t("integrations.feishu.personal.desc");
  const query = useFeishuApp(orgId);
  return (
    <Loading query={query} title={title} description={description}>
      {(view) => (
        <PersonalBody
          orgId={orgId}
          view={view}
          title={title}
          description={description}
        />
      )}
    </Loading>
  );
}

function PersonalBody({
  orgId,
  view,
  title,
  description,
}: {
  orgId: string;
  view: FeishuAppView;
  title: string;
  description: string;
}) {
  const t = useT();
  const imProvider = useIMProvider();
  const toast = useToast();
  const queryClient = useQueryClient();
  const { authorize } = useFeishuMutations(orgId);
  const { app, personal } = view;
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [blocked, setBlocked] = useState<string | null>(null);
  const [unbinding, setUnbinding] = useState(false);
  const popup = useRef<Window | null>(null);
  // 完成页可能重复 postMessage（例如开发环境的 StrictMode 重复执行副作用），同一会话只处理一次
  const handled = useRef<string | null>(null);
  const refresh = () =>
    queryClient.invalidateQueries({ queryKey: keys.integrations(orgId) });

  // 弹窗完成页 postMessage 回来：提示结果并刷新；用户直接关掉弹窗时也结束等待
  useEffect(() => {
    if (!sessionId) return;
    const onMessage = (event: MessageEvent) => {
      const message = parseOAuthMessage(event, window.location.origin);
      if (
        !message ||
        message.provider !== imProvider ||
        message.session !== sessionId ||
        handled.current === sessionId
      )
        return;
      handled.current = sessionId;
      if (message.status === "ok")
        toast(t("integrations.feishu.personal.boundToast"));
      else toast(t(oauthErrorKey(message.reason)), { tone: "error" });
      setSessionId(null);
      setBlocked(null);
      void refresh();
    };
    const timer = window.setInterval(() => {
      if (popup.current?.closed) {
        setSessionId(null);
        void refresh();
      }
    }, 1000);
    window.addEventListener("message", onMessage);
    return () => {
      window.removeEventListener("message", onMessage);
      window.clearInterval(timer);
    };
    // refresh 只依赖 orgId 与 queryClient
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, orgId, queryClient, toast, t]);

  const usable = app !== null && app.status !== "NEEDS_ATTENTION";
  const start = () =>
    authorize.mutate(undefined, {
      onSuccess: (result) => {
        setSessionId(result.session_id);
        const win = window.open(
          result.authorize_url,
          "openfox-feishu-oauth",
          popupFeatures(),
        );
        popup.current = win;
        setBlocked(win ? null : result.authorize_url);
      },
      onError: (error) =>
        toast(integrationErrorText(t, error), { tone: "error" }),
    });
  const busy = authorize.isPending || sessionId !== null;
  return (
    <SettingsSection
      title={title}
      description={description}
      action={
        personal ? undefined : (
          <Button size="32" disabled={!usable || busy} onClick={start}>
            {t("integrations.feishu.personal.link")}
          </Button>
        )
      }
    >
      {blocked && (
        <Callout tone="warning">
          {t("integrations.feishu.personal.popupBlocked")}{" "}
          <a
            href={blocked}
            target="_blank"
            rel="noopener noreferrer"
            className="font-medium underline"
          >
            {t("integrations.feishu.personal.openAuthorize")}
          </a>
        </Callout>
      )}
      {sessionId && !blocked && (
        <Callout tone="info">
          {t("integrations.feishu.personal.waiting")}
        </Callout>
      )}
      <p className="px-1 text-body-sm text-fg-muted">
        {t("integrations.feishu.personal.identityHint")}
      </p>
      <SettingsCard>
        {personal ? (
          <div className="flex min-h-[68px] items-center gap-3 p-4">
            <Avatar
              name={personal.display_name}
              src={personal.avatar_url || null}
              size={32}
            />
            <span className="flex min-w-0 flex-1 flex-col gap-0.5">
              <span className="truncate text-body font-medium">
                {personal.display_name}
              </span>
              <span className="text-body-sm text-fg-muted">
                {t("integrations.feishu.personal.bound")}
              </span>
            </span>
            <Button
              variant="outline"
              size="28"
              onClick={() => setUnbinding(true)}
            >
              {t("integrations.feishu.personal.unbind")}
            </Button>
          </div>
        ) : usable ? (
          <SettingRow title={t("integrations.feishu.personal.emptyDesc")} />
        ) : (
          <SettingRow
            title={t("integrations.feishu.personal.needAdminTitle")}
            description={t("integrations.feishu.personal.needAdminDesc")}
          />
        )}
      </SettingsCard>
      {personal && (
        <UnbindDialog
          open={unbinding}
          orgId={orgId}
          connectionId={personal.connection_id}
          version={personal.version}
          onClose={() => setUnbinding(false)}
        />
      )}
    </SettingsSection>
  );
}

function UnbindDialog({
  open,
  orgId,
  connectionId,
  version,
  onClose,
}: {
  open: boolean;
  orgId: string;
  connectionId: string;
  version: number;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const disconnect = useDisconnectConnection(orgId);
  return (
    <ConfirmDialog
      open={open}
      danger
      title={t("integrations.feishu.personal.unbindTitle")}
      description={t("integrations.feishu.personal.unbindBody")}
      confirmLabel={t("integrations.feishu.personal.unbind")}
      loading={disconnect.isPending}
      onCancel={onClose}
      onConfirm={() =>
        disconnect.mutate(
          { connectionId, version },
          {
            onSuccess: () => {
              toast(t("integrations.feishu.personal.unbound"));
              onClose();
            },
            onError: (error) => {
              toast(integrationErrorText(t, error), { tone: "error" });
              if (isApiError(error, "VERSION_CONFLICT")) onClose();
            },
          },
        )
      }
    />
  );
}
