import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import {
  isApiError,
  type FeishuBot,
  type FeishuBotsView,
  type FeishuRegistration,
  type FeishuRegistrationInput,
} from "@openfox/api";
import {
  useT as useBaseT,
  type MessageKey,
  type Translate,
} from "@openfox/i18n";
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
  Select,
  Skeleton,
  Spinner,
  Textarea,
  TextInput,
  useToast,
} from "@openfox/ui";

import { runtimeOptions } from "../agents/CreateAgentDialog";
import { runtimeChoices, runtimeLabel } from "../agents/model";
import { runtimeIcon } from "../computers/model";
import { useDevices, useDirectory, useMe, useOrgRole } from "../agents/queries";
import { keys, useApi } from "../context";
import {
  SettingRow,
  SettingsCard,
  SettingsSection,
} from "../spaces/settings/parts";
import { QRCode } from "./QRCode";
import { DINGTALK_APP_KEY_PATTERN } from "./model";
import {
  feishuSubmitErrorText,
  useFeishuBotMutations,
  useFeishuBots,
  useFeishuRegistration,
} from "./queries";

// 飞书任务助手与绑定 Agents。
// 任务助手与 Agent 都通过扫码在飞书里创建机器人，共用 RegistrationDialog 的扫码面板。

const EXTRA_PROMPT_LIMIT = 4000;

const STATUS_TEXT: Record<FeishuBot["status"], MessageKey> = {
  CONNECTING: "integrations.feishu.bots.status.connecting",
  CONNECTED: "integrations.feishu.bots.status.connected",
  FAILED: "integrations.feishu.bots.status.failed",
};

const STATUS_TONE: Record<
  FeishuBot["status"],
  "success" | "warning" | "muted"
> = {
  CONNECTED: "success",
  FAILED: "warning",
  CONNECTING: "muted",
};

/** 扫码会话终态错误码对应的文案；未知错误码走通用文案。 */
export function scanErrorText(t: Translate, code: string): string {
  const known: Record<string, MessageKey> = {
    expired: "integrations.feishu.bots.scan.errors.expired",
    access_denied: "integrations.feishu.bots.scan.errors.access_denied",
    rejected: "integrations.feishu.bots.scan.errors.rejected",
    agent_connected: "integrations.feishu.bots.scan.errors.agent_connected",
    assistant_exists: "integrations.feishu.bots.scan.errors.assistant_exists",
    session_expired: "integrations.feishu.bots.scan.errors.session_expired",
    superseded: "integrations.feishu.bots.scan.errors.superseded",
    save_failed: "integrations.feishu.bots.scan.errors.save_failed",
  };
  return t(known[code] ?? "integrations.feishu.bots.scan.errors.generic");
}

/** 机器人写操作与发起扫码失败的文案。 */
function botErrorText(t: Translate, error: unknown, fallback: MessageKey) {
  if (isApiError(error)) {
    const map: Record<string, MessageKey> = {
      FEISHU_APP_NOT_CONNECTED: "integrations.feishu.bots.errors.notConnected",
      FEISHU_ASSISTANT_EXISTS: "integrations.feishu.bots.assistant.exists",
      FEISHU_AGENT_CONNECTED: "integrations.feishu.bots.agents.exists",
      FEISHU_UNAVAILABLE: "integrations.feishu.bots.errors.unavailable",
      VERSION_CONFLICT: "integrations.feishu.bots.errors.versionConflict",
      FEISHU_CREDENTIALS_INVALID:
        "integrations.feishu.errors.credentialsInvalid",
      FEISHU_BOT_UNAVAILABLE: "integrations.feishu.errors.botUnavailable",
    };
    const key = map[error.code];
    if (key) return t(key);
  }
  return t(fallback);
}

function formatRemaining(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}

/** 加载骨架 / 失败态，成功后把数据交给 children。 */
function BotsLoading({
  query,
  title,
  description,
  children,
}: {
  query: ReturnType<typeof useFeishuBots>;
  title: string;
  description: string;
  children: (view: FeishuBotsView) => ReactNode;
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

function StatusBadge({ status }: { status: FeishuBot["status"] }) {
  const t = useT();
  return (
    <Badge variant="status" small tone={STATUS_TONE[status]}>
      {t(STATUS_TEXT[status])}
    </Badge>
  );
}

/** FAILED 行的「重试」按钮。 */
function RetryButton({ orgId, bot }: { orgId: string; bot: FeishuBot }) {
  const t = useT();
  const toast = useToast();
  const { retry } = useFeishuBotMutations(orgId);
  return (
    <Button
      variant="outline"
      size="28"
      loading={retry.isPending}
      onClick={() =>
        retry.mutate(bot.id, {
          onSuccess: () => toast(t("integrations.feishu.bots.retried")),
          onError: (error) =>
            toast(
              botErrorText(t, error, "integrations.feishu.bots.errors.generic"),
              { tone: "error" },
            ),
        })
      }
    >
      {t("integrations.feishu.bots.retry")}
    </Button>
  );
}

// ---------- 扫码面板（任务助手与 Agent 共用） ----------

/**
 * 带扫码步骤的对话框：先显示 `form`（收集参数），提交后发起扫码会话并切换到二维码面板。
 * 关闭（取消按钮、遮罩、Esc）时若会话仍在等待，则调用取消接口。
 */
function RegistrationDialog({
  orgId,
  title,
  description,
  width = 480,
  successKey,
  renderForm,
  onClose,
}: {
  orgId: string;
  title: string;
  description: string;
  width?: number;
  successKey: MessageKey;
  /** 收集参数的表单；调用 submit 进入扫码步骤 */
  renderForm: (submit: (input: FeishuRegistrationInput) => void) => ReactNode;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const imProvider = useIMProvider();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [input, setInput] = useState<FeishuRegistrationInput | null>(null);
  const finish = () => {
    pending.current = null;
    void queryClient.invalidateQueries({ queryKey: keys.integrations(orgId) });
    toast(t(successKey));
    onClose();
  };
  // 仍在等待授权的会话；关闭对话框时据此决定是否取消
  const pending = useRef<string | null>(null);
  const close = () => {
    const id = pending.current;
    pending.current = null;
    if (id)
      void api.integrations
        .cancelFeishuRegistration(orgId, id, imProvider)
        .catch(() => undefined);
    onClose();
  };
  return (
    <Dialog
      open
      onClose={close}
      title={title}
      description={input ? undefined : description}
      width={width}
    >
      {input && imProvider === "wecom" ? (
        <WeComCredentialPanel
          orgId={orgId}
          input={input}
          onCancel={close}
          onCompleted={finish}
        />
      ) : input && imProvider === "dingtalk" ? (
        // 钉钉官方没有扫码创建应用的接口：用 AppKey + AppSecret 直接连接
        <CredentialPanel
          orgId={orgId}
          input={input}
          onCancel={close}
          onCompleted={finish}
        />
      ) : input ? (
        <ScanPanel
          orgId={orgId}
          input={input}
          onPending={(id) => (pending.current = id)}
          onCancel={close}
          onCompleted={() => {
            pending.current = null;
            void queryClient.invalidateQueries({
              queryKey: keys.integrations(orgId),
            });
            toast(t(successKey));
            onClose();
          }}
        />
      ) : (
        renderForm(setInput)
      )}
    </Dialog>
  );
}

function WeComCredentialPanel({
  orgId,
  input,
  onCancel,
  onCompleted,
}: {
  orgId: string;
  input: FeishuRegistrationInput;
  onCancel: () => void;
  onCompleted: () => void;
}) {
  const t = useBaseT();
  const api = useApi();
  const [botId, setBotId] = useState("");
  const [secret, setSecret] = useState("");
  const [error, setError] = useState("");
  const create = useMutation({
    mutationFn: () =>
      api.integrations.createWeComBot(orgId, {
        ...input,
        bot_id: botId.trim(),
        secret: secret.trim(),
      }),
    onSuccess: onCompleted,
    onError: (cause) =>
      setError(botErrorText(t, cause, "integrations.wecom.connectFailed")),
  });
  return (
    <div className="flex flex-col gap-4">
      <p className="text-body-sm text-fg-muted">
        {t("integrations.wecom.connectHelp")}
      </p>
      <TextInput
        aria-label={t("integrations.wecom.botId")}
        value={botId}
        onChange={(event) => setBotId(event.target.value)}
        placeholder={t("integrations.wecom.botId")}
      />
      <TextInput
        aria-label={t("integrations.wecom.secret")}
        type="password"
        value={secret}
        onChange={(event) => setSecret(event.target.value)}
        placeholder={t("integrations.wecom.secret")}
      />
      {error && (
        <p role="alert" className="text-body-sm text-danger">
          {error}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button variant="outline" onClick={onCancel}>
          {t("common.cancel")}
        </Button>
        <Button
          loading={create.isPending}
          disabled={!botId.trim() || !secret.trim()}
          onClick={() => create.mutate()}
        >
          {t("integrations.wecom.connect")}
        </Button>
      </div>
    </div>
  );
}

/** 钉钉机器人的凭证面板：填写机器人对应的钉钉应用的 AppKey 与 AppSecret，保存时立即校验并建立 Stream 长连接。 */
function CredentialPanel({
  orgId,
  input,
  onCancel,
  onCompleted,
}: {
  orgId: string;
  input: FeishuRegistrationInput;
  onCancel: () => void;
  onCompleted: () => void;
}) {
  const t = useT();
  const api = useApi();
  const [appKey, setAppKey] = useState("");
  const [secret, setSecret] = useState("");
  const [error, setError] = useState("");
  const keyValid = DINGTALK_APP_KEY_PATTERN.test(appKey.trim());
  const create = useMutation({
    mutationFn: () =>
      api.integrations.createDingTalkBot(orgId, {
        ...input,
        app_key: appKey.trim(),
        app_secret: secret.trim(),
      }),
    onSuccess: onCompleted,
    // 绑定任务助手 / Agent 的冲突（空间已有助手、Agent 已连接）给明确的文案，其余走通用的凭证 / 不可达映射
    onError: (e) =>
      setError(
        isApiError(e) &&
          (e.code === "FEISHU_ASSISTANT_EXISTS" ||
            e.code === "FEISHU_AGENT_CONNECTED" ||
            e.code === "FEISHU_APP_NOT_CONNECTED")
          ? botErrorText(t, e, "integrations.feishu.bots.errors.generic")
          : feishuSubmitErrorText(t, e),
      ),
  });
  const secretValid = secret.trim().length >= 16 && secret.trim().length <= 128;
  const ready = keyValid && secretValid;
  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(event) => {
        event.preventDefault();
        if (!ready || create.isPending) return;
        setError("");
        create.mutate();
      }}
    >
      <p className="text-body-sm text-fg-muted">
        {t("integrations.dingtalk.bots.connectDesc")}
      </p>
      <label className="flex flex-col gap-1 text-body-sm font-medium">
        {t("integrations.dingtalk.dialog.appKeyLabel")}
        <TextInput
          aria-label={t("integrations.dingtalk.dialog.appKeyLabel")}
          value={appKey}
          placeholder="dingxxxxxxxxxxxxxxxx"
          onChange={(event) => setAppKey(event.target.value)}
        />
        {appKey !== "" && !keyValid && (
          <span className="text-caption font-normal text-danger">
            {t("integrations.dingtalk.dialog.appKeyInvalid")}
          </span>
        )}
      </label>
      <label className="flex flex-col gap-1 text-body-sm font-medium">
        {t("integrations.dingtalk.dialog.appSecretLabel")}
        <TextInput
          aria-label={t("integrations.dingtalk.dialog.appSecretLabel")}
          type="password"
          autoComplete="off"
          value={secret}
          onChange={(event) => setSecret(event.target.value)}
        />
        {secret !== "" && !secretValid && (
          <span className="text-caption font-normal text-danger">
            {t("integrations.feishu.dialog.secretInvalid")}
          </span>
        )}
      </label>
      {error && <Callout tone="danger">{error}</Callout>}
      <div className="flex justify-end gap-2">
        <Button variant="outline" onClick={onCancel} type="button">
          {t("common.cancel")}
        </Button>
        <Button type="submit" disabled={!ready || create.isPending}>
          {create.isPending
            ? t("integrations.dingtalk.bots.connecting")
            : t("integrations.dingtalk.bots.connect")}
        </Button>
      </div>
    </form>
  );
}

function ScanPanel({
  orgId,
  input,
  onPending,
  onCancel,
  onCompleted,
}: {
  orgId: string;
  input: FeishuRegistrationInput;
  onPending: (id: string | null) => void;
  onCancel: () => void;
  onCompleted: () => void;
}) {
  const t = useT();
  const { start } = useFeishuBotMutations(orgId);
  const [registration, setRegistration] = useState<FeishuRegistration | null>(
    null,
  );
  const [startError, setStartError] = useState<string | null>(null);
  // 每次「重新发起」递增，触发新的 start；StrictMode 的重复副作用按 attempt 去重
  const [attempt, setAttempt] = useState(0);
  const started = useRef(-1);
  const completed = useRef(false);
  const startRef = useRef(start.mutate);
  startRef.current = start.mutate;

  useEffect(() => {
    if (started.current === attempt) return;
    started.current = attempt;
    setRegistration(null);
    setStartError(null);
    startRef.current(input, {
      onSuccess: (result) => {
        setRegistration(result);
        onPending(result.status === "PENDING" ? result.id : null);
      },
      onError: (error) => {
        onPending(null);
        setStartError(
          botErrorText(t, error, "integrations.feishu.bots.scan.startFailed"),
        );
      },
    });
    // 只在 attempt 变化时发起
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [attempt]);

  const polled = useFeishuRegistration(orgId, registration?.id ?? null);
  const current = polled.data ?? registration;

  useEffect(() => {
    if (!current || completed.current) return;
    if (current.status === "COMPLETED") {
      completed.current = true;
      onCompleted();
    } else if (current.status !== "PENDING") {
      onPending(null);
    }
    // onCompleted / onPending 来自父级的稳定闭包
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [current?.status]);

  const restart = () => setAttempt((n) => n + 1);

  if (startError)
    return (
      <PanelMessage
        tone="error"
        message={startError}
        onRestart={restart}
        onCancel={onCancel}
      />
    );
  if (!current)
    return (
      <div
        role="status"
        className="flex min-h-[260px] items-center justify-center gap-2 text-body text-fg-muted"
      >
        <Spinner />
        {t("integrations.feishu.bots.scan.generating")}
      </div>
    );
  if (current.status !== "PENDING" && current.status !== "COMPLETED")
    return (
      <PanelMessage
        tone="error"
        message={scanErrorText(t, current.error_code)}
        onRestart={restart}
        onCancel={onCancel}
      />
    );
  return <QrStep registration={current} onCancel={onCancel} />;
}

function PanelMessage({
  message,
  onRestart,
  onCancel,
}: {
  tone: "error";
  message: string;
  onRestart: () => void;
  onCancel: () => void;
}) {
  const t = useT();
  return (
    <div className="flex flex-col gap-4">
      <Callout tone="warning">
        <span role="alert">{message}</span>
      </Callout>
      <div className="flex justify-end gap-2">
        <Button variant="outline" onClick={onCancel}>
          {t("integrations.feishu.bots.scan.cancel")}
        </Button>
        <Button onClick={onRestart}>
          {t("integrations.feishu.bots.scan.restart")}
        </Button>
      </div>
    </div>
  );
}

function QrStep({
  registration,
  onCancel,
}: {
  registration: FeishuRegistration;
  onCancel: () => void;
}) {
  const t = useT();
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  const remaining = Date.parse(registration.expires_at) - now;
  return (
    <div className="flex flex-col items-center gap-4">
      <div className="rounded-xl border border-line-default p-3">
        <QRCode
          value={registration.verification_url}
          label={t("integrations.feishu.bots.scan.qrLabel")}
        />
      </div>
      <p className="max-w-[360px] text-center text-body-sm text-fg-muted">
        {t("integrations.feishu.bots.scan.hint")}
      </p>
      <Button
        variant="outline"
        size="28"
        onClick={() =>
          window.open(
            registration.verification_url,
            "_blank",
            "noopener,noreferrer",
          )
        }
      >
        <Icon name="external-link" />
        {t("integrations.feishu.bots.scan.openLink")}
      </Button>
      <p
        role="status"
        className="flex items-center gap-2 text-caption text-fg-muted"
      >
        <Spinner />
        {t("integrations.feishu.bots.scan.waiting")}
        {Number.isFinite(remaining) && (
          <span className="tabular-nums">
            {t("integrations.feishu.bots.scan.remaining", {
              time: formatRemaining(remaining),
            })}
          </span>
        )}
      </p>
      <div className="flex w-full justify-end">
        <Button variant="outline" onClick={onCancel}>
          {t("integrations.feishu.bots.scan.cancel")}
        </Button>
      </div>
    </div>
  );
}

// ---------- 任务助手分区 ----------

export function FeishuAssistantSection({
  orgId,
  title,
  description,
}: {
  orgId: string;
  title: string;
  description: string;
}) {
  const query = useFeishuBots(orgId);
  return (
    <BotsLoading query={query} title={title} description={description}>
      {(view) => (
        <AssistantBody
          orgId={orgId}
          view={view}
          title={title}
          description={description}
        />
      )}
    </BotsLoading>
  );
}

type AssistantRow = FeishuBotsView["assistants"][number];
type AssistantDialog = {
  kind: "bind" | "edit" | "remove";
  row: AssistantRow;
} | null;

function AssistantBody({
  orgId,
  view,
  title,
  description,
}: {
  orgId: string;
  view: FeishuBotsView;
  title: string;
  description: string;
}) {
  const t = useT();
  const provider = useIMProvider();
  const appConnected = provider === "wecom" || view.app_connected;
  const [dialog, setDialog] = useState<AssistantDialog>(null);
  const close = () => setDialog(null);
  return (
    <SettingsSection title={title} description={description}>
      {!appConnected && (
        <Callout tone="info">{t("integrations.feishu.bots.needApp")}</Callout>
      )}
      <SettingsCard>
        <SettingRow
          icon="bot"
          title={t("integrations.feishu.bots.assistant.cardTitle")}
          description={t("integrations.feishu.bots.assistant.cardDesc")}
        />
        {view.assistants.map((row) => (
          <AssistantRowView
            key={row.space_id}
            orgId={orgId}
            row={row}
            appConnected={appConnected}
            onOpen={(kind) => setDialog({ kind, row })}
          />
        ))}
      </SettingsCard>
      {dialog?.kind === "bind" && (
        <AssistantBindDialog
          orgId={orgId}
          row={dialog.row}
          builtinPrompt={view.builtin_prompt}
          onClose={close}
        />
      )}
      {dialog?.kind === "edit" && dialog.row.bot && (
        <AssistantEditDialog
          orgId={orgId}
          row={dialog.row}
          bot={dialog.row.bot}
          builtinPrompt={view.builtin_prompt}
          onClose={close}
        />
      )}
      {dialog?.kind === "remove" && dialog.row.bot && (
        <RemoveBotDialog
          orgId={orgId}
          bot={dialog.row.bot}
          title={t("integrations.feishu.bots.assistant.removeTitle")}
          body={t("integrations.feishu.bots.assistant.removeBody")}
          confirmLabel={t("integrations.feishu.bots.assistant.removeConfirm")}
          doneKey="integrations.feishu.bots.assistant.removed"
          onClose={close}
        />
      )}
    </SettingsSection>
  );
}

function AssistantRowView({
  orgId,
  row,
  appConnected,
  onOpen,
}: {
  orgId: string;
  row: AssistantRow;
  appConnected: boolean;
  onOpen: (kind: "bind" | "edit" | "remove") => void;
}) {
  const t = useT();
  // 不带品牌词替换的翻译：下面"已在 X 绑定"里的服务商名是参数，不能被当成当前服务商的品牌词替换掉
  const baseT = useBaseT();
  const imProvider = useIMProvider();
  const { bot } = row;
  const rowTitle = t("integrations.feishu.bots.assistant.rowTitle", {
    space: row.space_name,
  });
  if (!bot) {
    // 每个空间只能有一个任务助手（不分服务商）：已在别的服务商绑定时，这里不能再绑定
    const elsewhere =
      row.bound_provider && row.bound_provider !== imProvider
        ? baseT("integrations.feishu.bots.assistant.boundElsewhere", {
            provider: baseT(`integrations.imNames.${row.bound_provider}`),
          })
        : null;
    const canBind = row.can_manage && appConnected && !elsewhere;
    const reason = !appConnected
      ? t("integrations.feishu.bots.needApp")
      : elsewhere
        ? elsewhere
        : !row.can_manage
          ? t("integrations.feishu.bots.assistant.adminOnly")
          : null;
    return (
      <SettingRow
        title={rowTitle}
        description={reason ?? t("integrations.feishu.bots.assistant.unbound")}
        control={
          <span title={reason ?? undefined} className="inline-flex">
            <Button
              size="28"
              disabled={!canBind}
              onClick={() => onOpen("bind")}
            >
              {t("integrations.feishu.bots.assistant.bind")}
            </Button>
          </span>
        }
      />
    );
  }
  const label = t("integrations.feishu.bots.assistant.menu");
  return (
    <SettingRow
      title={rowTitle}
      description={t("integrations.feishu.bots.assistant.bound")}
      control={
        <span className="flex items-center gap-2">
          <StatusBadge status={bot.status} />
          {bot.status === "FAILED" && row.can_manage && (
            <RetryButton orgId={orgId} bot={bot} />
          )}
          {row.can_manage && (
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
              <MenuItem
                icon={<Icon name="pencil" />}
                onSelect={() => onOpen("edit")}
              >
                {t("integrations.feishu.bots.assistant.edit")}
              </MenuItem>
              <MenuItem
                danger
                icon={<Icon name="trash-2" />}
                onSelect={() => onOpen("remove")}
              >
                {t("integrations.feishu.bots.assistant.remove")}
              </MenuItem>
            </Menu>
          )}
        </span>
      }
    />
  );
}

/** Runtime 下拉的可选项与取值 → 请求体的转换；复用 Agent 创建弹窗的选项与规则。 */
function useRuntimePicker(
  orgId: string,
  spaceId: string,
  current: FeishuBot["runtime"],
) {
  const t = useT();
  const devices = useDevices(orgId);
  const me = useMe();
  const choices = useMemo(
    () => runtimeChoices(devices.data ?? [], spaceId, me.data?.id ?? ""),
    [devices.data, spaceId, me.data?.id],
  );
  const options = runtimeOptions(choices, t);
  const currentValue = current
    ? `${current.computer_id}:${current.runtime}`
    : null;
  // 当前使用的 Runtime 即使已离线也保留在列表里，避免编辑时凭空丢失
  if (current && !choices.some((choice) => choice.value === currentValue))
    options.unshift({
      value: currentValue!,
      label: `${runtimeLabel(current.runtime)} · ${current.computer_name}`,
      icon: <Icon name={runtimeIcon(current.runtime)} />,
      disabled: false,
      description: undefined,
    });
  const toRuntime = (value: string | null) => {
    if (!value) return undefined;
    // 未变化的当前值：原样带回（包括模型），避免编辑提示词时把模型重置为默认
    if (current && value === currentValue)
      return {
        computer_id: current.computer_id,
        grant_id: current.grant_id,
        runtime: current.runtime,
        model: current.model,
      };
    const choice = choices.find((item) => item.value === value);
    if (choice)
      return {
        computer_id: choice.deviceId,
        grant_id: choice.grantId,
        runtime: choice.provider,
      };
    return undefined;
  };
  return { options, toRuntime, loading: devices.isPending };
}

/** 可折叠的附加提示词 + 查看内置提示词的入口。 */
function ExtraPromptField({
  value,
  onChange,
  builtinPrompt,
}: {
  value: string;
  onChange: (value: string) => void;
  builtinPrompt: string;
}) {
  const t = useT();
  const [open, setOpen] = useState(value !== "");
  const [showBuiltin, setShowBuiltin] = useState(false);
  return (
    <div className="flex flex-col gap-2">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="flex w-fit items-center gap-1 text-body-sm font-medium"
      >
        <Icon name={open ? "chevron-down" : "chevron-right"} />
        {t("integrations.feishu.bots.assistant.extraPrompt")}
      </button>
      {open && (
        <>
          <Textarea
            aria-label={t("integrations.feishu.bots.assistant.extraPrompt")}
            placeholder={t(
              "integrations.feishu.bots.assistant.extraPromptPlaceholder",
            )}
            value={value}
            maxLength={EXTRA_PROMPT_LIMIT}
            rows={4}
            onChange={(event) => onChange(event.target.value)}
          />
          <p className="text-caption text-fg-muted">
            {t("integrations.feishu.bots.assistant.extraPromptHint")}
          </p>
        </>
      )}
      <button
        type="button"
        onClick={() => setShowBuiltin(true)}
        className="w-fit text-body-sm font-medium underline"
      >
        {t("integrations.feishu.bots.assistant.viewBuiltin")}
      </button>
      {showBuiltin && (
        <Dialog
          open
          onClose={() => setShowBuiltin(false)}
          title={t("integrations.feishu.bots.assistant.builtinTitle")}
          description={t("integrations.feishu.bots.assistant.builtinDesc")}
          width={560}
          footer={
            <Button variant="outline" onClick={() => setShowBuiltin(false)}>
              {t("common.close")}
            </Button>
          }
        >
          <pre className="max-h-[360px] overflow-auto rounded-lg bg-surface-muted p-3 font-mono text-caption whitespace-pre-wrap">
            {builtinPrompt}
          </pre>
        </Dialog>
      )}
    </div>
  );
}

function AssistantBindDialog({
  orgId,
  row,
  builtinPrompt,
  onClose,
}: {
  orgId: string;
  row: AssistantRow;
  builtinPrompt: string;
  onClose: () => void;
}) {
  const t = useT();
  const picker = useRuntimePicker(orgId, row.space_id, null);
  const [value, setValue] = useState<string | null>(null);
  const [extra, setExtra] = useState("");
  return (
    <RegistrationDialog
      orgId={orgId}
      title={t("integrations.feishu.bots.assistant.bindTitle")}
      description={t("integrations.feishu.bots.assistant.bindSubtitle", {
        space: row.space_name,
      })}
      successKey="integrations.feishu.bots.assistant.bound_toast"
      onClose={onClose}
      renderForm={(submit) => (
        <div className="flex flex-col gap-4">
          <label className="flex flex-col gap-1 text-body-sm font-medium">
            {t("integrations.feishu.bots.assistant.runtime")}
            <Select
              aria-label={t("integrations.feishu.bots.assistant.runtime")}
              value={value}
              onChange={setValue}
              options={picker.options}
              placeholder={
                !picker.loading && picker.options.length === 0
                  ? t("integrations.feishu.bots.assistant.runtimeNone")
                  : t("integrations.feishu.bots.assistant.runtimePlaceholder")
              }
            />
          </label>
          <ExtraPromptField
            value={extra}
            onChange={setExtra}
            builtinPrompt={builtinPrompt}
          />
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button
              disabled={!value}
              onClick={() => {
                const runtime = picker.toRuntime(value);
                if (!runtime) return;
                submit({
                  purpose: "task_assistant",
                  space_id: row.space_id,
                  runtime,
                  ...(extra.trim() ? { extra_prompt: extra.trim() } : {}),
                });
              }}
            >
              {t("integrations.feishu.bots.assistant.next")}
            </Button>
          </div>
        </div>
      )}
    />
  );
}

function AssistantEditDialog({
  orgId,
  row,
  bot,
  builtinPrompt,
  onClose,
}: {
  orgId: string;
  row: AssistantRow;
  bot: FeishuBot;
  builtinPrompt: string;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const { update } = useFeishuBotMutations(orgId);
  const picker = useRuntimePicker(orgId, row.space_id, bot.runtime);
  const initial = bot.runtime
    ? `${bot.runtime.computer_id}:${bot.runtime.runtime}`
    : null;
  const [value, setValue] = useState<string | null>(initial);
  const [extra, setExtra] = useState(bot.extra_prompt);
  const [error, setError] = useState("");
  const save = () => {
    const runtime = picker.toRuntime(value);
    update.mutate(
      {
        id: bot.id,
        body: {
          ...(runtime ? { runtime } : {}),
          extra_prompt: extra.trim(),
          version: bot.version,
        },
      },
      {
        onSuccess: () => {
          toast(t("integrations.feishu.bots.assistant.updated"));
          onClose();
        },
        onError: (e) =>
          setError(
            botErrorText(t, e, "integrations.feishu.bots.errors.generic"),
          ),
      },
    );
  };
  return (
    <Dialog
      open
      onClose={update.isPending ? undefined : onClose}
      title={t("integrations.feishu.bots.assistant.editTitle")}
      description={t("integrations.feishu.bots.assistant.editSubtitle")}
      width={480}
    >
      <div className="flex flex-col gap-4">
        <label className="flex flex-col gap-1 text-body-sm font-medium">
          {t("integrations.feishu.bots.assistant.runtime")}
          <Select
            aria-label={t("integrations.feishu.bots.assistant.runtime")}
            value={value}
            onChange={setValue}
            options={picker.options}
            placeholder={t(
              "integrations.feishu.bots.assistant.runtimePlaceholder",
            )}
          />
        </label>
        <ExtraPromptField
          value={extra}
          onChange={setExtra}
          builtinPrompt={builtinPrompt}
        />
        {error && (
          <p role="alert" className="text-body-sm text-danger">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button disabled={!value} loading={update.isPending} onClick={save}>
            {t("integrations.feishu.bots.assistant.save")}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}

/** 移除任务助手 / 断开 Agent 共用的确认框。 */
function RemoveBotDialog({
  orgId,
  bot,
  title,
  body,
  confirmLabel,
  doneKey,
  onClose,
}: {
  orgId: string;
  bot: FeishuBot;
  title: string;
  body: string;
  confirmLabel: string;
  doneKey: MessageKey;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const { remove } = useFeishuBotMutations(orgId);
  return (
    <ConfirmDialog
      open
      danger
      title={title}
      description={body}
      confirmLabel={confirmLabel}
      loading={remove.isPending}
      onCancel={onClose}
      onConfirm={() =>
        remove.mutate(
          { id: bot.id, version: bot.version },
          {
            onSuccess: () => {
              toast(t(doneKey));
              onClose();
            },
            onError: (error) => {
              toast(
                botErrorText(
                  t,
                  error,
                  "integrations.feishu.bots.errors.generic",
                ),
                { tone: "error" },
              );
              if (isApiError(error, "VERSION_CONFLICT")) onClose();
            },
          },
        )
      }
    />
  );
}

// ---------- 绑定 Agents 分区 ----------

export function FeishuAgentsSection({
  orgId,
  agentId,
  title,
  description,
}: {
  orgId: string;
  agentId?: string;
  title: string;
  description: string;
}) {
  const query = useFeishuBots(orgId);
  return (
    <BotsLoading query={query} title={title} description={description}>
      {(view) => (
        <AgentsBody
          orgId={orgId}
          agentId={agentId}
          view={view}
          title={title}
          description={description}
        />
      )}
    </BotsLoading>
  );
}

function AgentsBody({
  orgId,
  agentId,
  view,
  title,
  description,
}: {
  orgId: string;
  agentId?: string;
  view: FeishuBotsView;
  title: string;
  description: string;
}) {
  const t = useT();
  const provider = useIMProvider();
  const [creating, setCreating] = useState(false);
  const [disconnecting, setDisconnecting] = useState<FeishuBot | null>(null);
  const visible = agentId
    ? view.agents.filter((bot) => bot.agent.id === agentId)
    : view.agents;
  const alreadyBound = !!agentId && visible.length > 0;
  const needsApp = provider === "dingtalk" && !view.app_connected;
  const canCreate = view.can_bind_agent && !needsApp && !alreadyBound;
  const reason = alreadyBound
    ? t("integrations.feishu.bots.agents.exists")
    : needsApp
      ? t("integrations.feishu.bots.needApp")
      : !view.can_bind_agent
        ? t("integrations.feishu.bots.agents.noPermission")
        : null;
  return (
    <SettingsSection title={title} description={description}>
      {needsApp && (
        <Callout tone="info">{t("integrations.feishu.bots.needApp")}</Callout>
      )}
      <SettingsCard>
        <SettingRow
          icon="bot"
          title={t("integrations.feishu.bots.agents.cardTitle")}
          description={reason ?? t("integrations.feishu.bots.agents.cardDesc")}
          control={
            <span title={reason ?? undefined} className="inline-flex">
              <Button
                size="28"
                disabled={!canCreate}
                onClick={() => setCreating(true)}
              >
                {t("integrations.feishu.bots.agents.create")}
              </Button>
            </span>
          }
        />
      </SettingsCard>
      <h3 className="px-1 text-body font-medium">
        {t("integrations.feishu.bots.agents.listTitle")}
      </h3>
      {visible.length === 0 ? (
        <SettingsCard>
          <SettingRow
            title={t("integrations.feishu.bots.agents.empty")}
            description={t("integrations.feishu.bots.agents.emptyDesc")}
          />
        </SettingsCard>
      ) : (
        <SettingsCard>
          {visible.map((bot) => (
            <AgentRowView
              key={bot.id}
              orgId={orgId}
              bot={bot}
              onDisconnect={() => setDisconnecting(bot)}
            />
          ))}
        </SettingsCard>
      )}
      {creating && (
        <AgentCreateDialog
          orgId={orgId}
          presetAgent={agentId}
          bound={view.agents}
          onClose={() => setCreating(false)}
        />
      )}
      {disconnecting && (
        <RemoveBotDialog
          orgId={orgId}
          bot={disconnecting}
          title={t("integrations.feishu.bots.agents.disconnectTitle", {
            name: disconnecting.agent.name,
          })}
          body={t("integrations.feishu.bots.agents.disconnectBody")}
          confirmLabel={t("integrations.feishu.bots.agents.disconnect")}
          doneKey="integrations.feishu.bots.agents.disconnected"
          onClose={() => setDisconnecting(null)}
        />
      )}
    </SettingsSection>
  );
}

function AgentRowView({
  orgId,
  bot,
  onDisconnect,
}: {
  orgId: string;
  bot: FeishuBot;
  onDisconnect: () => void;
}) {
  const t = useT();
  const label = t("integrations.feishu.bots.agents.menu", {
    name: bot.agent.name,
  });
  return (
    <div className="flex min-h-[68px] items-center gap-3 p-4">
      <Avatar name={bot.agent.name} kind="agent" size={32} />
      <span className="min-w-0 flex-1 truncate text-body font-medium">
        {bot.agent.name}
      </span>
      <StatusBadge status={bot.status} />
      {bot.status === "FAILED" && bot.can_manage && (
        <RetryButton orgId={orgId} bot={bot} />
      )}
      {bot.can_manage && (
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
          <MenuItem
            danger
            icon={<Icon name="unplug" />}
            onSelect={onDisconnect}
          >
            {t("integrations.feishu.bots.agents.disconnect")}
          </MenuItem>
        </Menu>
      )}
    </div>
  );
}

function AgentCreateDialog({
  orgId,
  presetAgent,
  bound,
  onClose,
}: {
  orgId: string;
  presetAgent?: string;
  bound: FeishuBot[];
  onClose: () => void;
}) {
  const t = useT();
  const directory = useDirectory(orgId);
  const role = useOrgRole(orgId);
  const isAdmin = role === "OWNER" || role === "ADMIN";
  const [agentId, setAgentId] = useState<string | null>(presetAgent ?? null);
  // 目录里的 Agent：去掉小队、已归档、内置小助手，以及已经连接飞书的；非管理员只留自己的
  const options = useMemo(() => {
    const taken = new Set(bound.map((bot) => bot.agent.id));
    return (directory.data?.entries ?? [])
      .filter(
        (entry) =>
          entry.type === "agent" &&
          !entry.archived_at &&
          !taken.has(entry.id) &&
          (isAdmin || entry.mine),
      )
      .sort((a, b) => Number(b.mine) - Number(a.mine))
      .map((entry) => ({
        value: entry.id,
        label: entry.name,
        icon: <Avatar name={entry.name} kind="agent" size={20} />,
        description: isAdmin
          ? t(
              entry.mine
                ? "integrations.feishu.bots.agents.mine"
                : "integrations.feishu.bots.agents.org",
            )
          : undefined,
      }));
  }, [directory.data, bound, isAdmin, t]);
  return (
    <RegistrationDialog
      orgId={orgId}
      title={t("integrations.feishu.bots.agents.create")}
      description={t("integrations.feishu.bots.agents.dialogSubtitle")}
      successKey="integrations.feishu.bots.agents.connectedToast"
      onClose={onClose}
      renderForm={(submit) => (
        <div className="flex flex-col gap-4">
          <label className="flex flex-col gap-1 text-body-sm font-medium">
            {t("integrations.feishu.bots.agents.agent")}
            <Select
              aria-label={t("integrations.feishu.bots.agents.agent")}
              value={agentId}
              disabled={!!presetAgent}
              onChange={setAgentId}
              options={options}
              placeholder={
                directory.isSuccess && options.length === 0
                  ? t("integrations.feishu.bots.agents.agentNone")
                  : t("integrations.feishu.bots.agents.agentPlaceholder")
              }
            />
          </label>
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button
              disabled={!options.some((option) => option.value === agentId)}
              onClick={() =>
                agentId && submit({ purpose: "agent", agent_id: agentId })
              }
            >
              {t("integrations.feishu.bots.assistant.next")}
            </Button>
          </div>
        </div>
      )}
    />
  );
}
