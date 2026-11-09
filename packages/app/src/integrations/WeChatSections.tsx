import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";

import {
  isApiError,
  type WeChatBinding,
  type WeChatQRSession,
  type WeChatView,
} from "@openfox/api";
import { useT, type MessageKey, type Translate } from "@openfox/i18n";
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
  TextInput,
  useToast,
} from "@openfox/ui";

import { useDirectory, useOrgRole } from "../agents/queries";
import { keys, useApi } from "../context";
import {
  SettingRow,
  SettingsCard,
  SettingsSection,
} from "../spaces/settings/parts";
import { QRCode } from "./QRCode";
import { useRemoveWeChatBinding, useWeChatBindings } from "./queries";

// 微信（腾讯 ClawBot / iLink）扫码绑定 Agent。沿用「绑定 Agents」的结构，
// 微信没有专门的画板。二维码内容只当作文本渲染，不加载其中的任何外部图片。

function errorText(t: Translate, error: unknown): string {
  if (isApiError(error)) {
    const map: Record<string, MessageKey> = {
      WECHAT_LIMIT_REACHED: "integrations.wechat.errors.limit_reached",
      WECHAT_UNAVAILABLE: "integrations.wechat.errors.unavailable",
      VERSION_CONFLICT: "integrations.wechat.errors.versionConflict",
      INTEGRATION_NOT_CONFIGURED: "integrations.wechat.notConfigured",
    };
    const key = map[error.code];
    if (key) return t(key);
  }
  return t("integrations.wechat.errors.generic");
}

/** 终态会话的说明；error_code 优先，其次按状态。 */
function terminalText(t: Translate, session: WeChatQRSession): string {
  const byCode = `integrations.wechat.errors.${session.error_code}`;
  if (session.error_code) {
    const keyed = byCode as MessageKey;
    const text = t(keyed);
    if (text !== byCode) return text;
  }
  switch (session.status) {
    case "EXPIRED":
      return t("integrations.wechat.errors.expired");
    case "BLOCKED":
      return t("integrations.wechat.errors.blocked");
    case "CANCELLED":
      return t("integrations.wechat.errors.cancelled");
    default:
      return t("integrations.wechat.errors.generic");
  }
}

const POLL_PAUSE_MS = 1000;
const MAX_POLL_FAILURES = 3;

export function WeChatAgentsSection({
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
  const t = useT();
  const query = useWeChatBindings(orgId);
  if (query.isPending)
    return (
      <SettingsSection title={title} description={description}>
        <Skeleton className="h-[72px] rounded-xl" />
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
  return (
    <Body
      orgId={orgId}
      agentId={agentId}
      view={query.data}
      title={title}
      description={description}
    />
  );
}

function Body({
  orgId,
  agentId,
  view,
  title,
  description,
}: {
  orgId: string;
  agentId?: string;
  view: WeChatView;
  title: string;
  description: string;
}) {
  const t = useT();
  const role = useOrgRole(orgId);
  const [binding, setBinding] = useState<WeChatBinding | null>(null);
  const [creating, setCreating] = useState(false);
  const atLimit = view.bindings.length >= view.max_bindings;
  const visible = agentId
    ? view.bindings.filter((binding) => binding.agent.id === agentId)
    : view.bindings;
  const reason = !view.configured
    ? t("integrations.wechat.notConfigured")
    : atLimit
      ? t("integrations.wechat.limit", { max: view.max_bindings })
      : null;
  const [removing, setRemoving] = useState<WeChatBinding | null>(null);
  return (
    <SettingsSection title={title} description={description}>
      {!view.configured && (
        <Callout tone="warning">
          {t("integrations.wechat.notConfigured")}
        </Callout>
      )}
      <SettingsCard>
        <SettingRow
          icon="bot"
          title={t("integrations.wechat.cardTitle")}
          description={reason ?? t("integrations.wechat.cardDesc")}
          control={
            <span title={reason ?? undefined} className="inline-flex">
              <Button
                size="28"
                disabled={!view.can_bind || !view.configured || atLimit}
                onClick={() => setCreating(true)}
              >
                {t("integrations.wechat.bindButton")}
              </Button>
            </span>
          }
        />
      </SettingsCard>
      <h3 className="px-1 text-body font-medium">
        {t("integrations.wechat.listTitle")}
      </h3>
      {visible.length === 0 ? (
        <SettingsCard>
          <SettingRow
            title={t("integrations.wechat.empty")}
            description={t("integrations.wechat.emptyDesc")}
          />
        </SettingsCard>
      ) : (
        <SettingsCard>
          {visible.map((item) => (
            <BindingRow
              key={item.id}
              item={item}
              showInitiator={role === "OWNER" || role === "ADMIN"}
              onRescan={() => {
                setBinding(item);
                setCreating(true);
              }}
              onRemove={() => setRemoving(item)}
            />
          ))}
        </SettingsCard>
      )}
      {creating && (
        <ScanDialog
          orgId={orgId}
          presetAgent={binding?.agent.id ?? agentId ?? null}
          onClose={() => {
            setCreating(false);
            setBinding(null);
          }}
        />
      )}
      {removing && (
        <RemoveDialog
          orgId={orgId}
          item={removing}
          onClose={() => setRemoving(null)}
        />
      )}
    </SettingsSection>
  );
}

function BindingRow({
  item,
  showInitiator,
  onRescan,
  onRemove,
}: {
  item: WeChatBinding;
  showInitiator: boolean;
  onRescan: () => void;
  onRemove: () => void;
}) {
  const t = useT();
  const label = t("integrations.wechat.menu", { name: item.agent.name });
  const expired = item.status === "EXPIRED";
  return (
    <div className="flex min-h-[68px] items-center gap-3 p-4">
      <Avatar name={item.agent.name} kind="agent" size={32} />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="truncate text-body font-medium">
          {item.agent.name}
        </span>
        <span className="truncate text-caption text-fg-muted">
          {[
            item.wechat_user,
            showInitiator
              ? t("integrations.wechat.initiator", {
                  name: item.initiator.name,
                })
              : "",
          ]
            .filter(Boolean)
            .join(" · ")}
        </span>
        {expired && (
          <span role="alert" className="text-caption text-danger">
            {t("integrations.wechat.expiredHint")}
          </span>
        )}
      </div>
      <Badge tone={expired ? "danger" : item.connected ? "success" : "neutral"}>
        {expired
          ? t("integrations.wechat.status.expired")
          : item.connected
            ? t("integrations.wechat.status.connected")
            : t("integrations.wechat.status.connecting")}
      </Badge>
      {expired && item.can_manage && (
        <Button size="28" variant="outline" onClick={onRescan}>
          {t("integrations.wechat.rescan")}
        </Button>
      )}
      {item.can_manage && (
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
          <MenuItem danger icon={<Icon name="unplug" />} onSelect={onRemove}>
            {t("integrations.wechat.unbind")}
          </MenuItem>
        </Menu>
      )}
    </div>
  );
}

function RemoveDialog({
  orgId,
  item,
  onClose,
}: {
  orgId: string;
  item: WeChatBinding;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const remove = useRemoveWeChatBinding(orgId);
  return (
    <ConfirmDialog
      open
      danger
      title={t("integrations.wechat.unbindTitle", { name: item.agent.name })}
      description={t("integrations.wechat.unbindBody")}
      confirmLabel={t("integrations.wechat.unbind")}
      loading={remove.isPending}
      onCancel={onClose}
      onConfirm={() =>
        remove.mutate(
          { id: item.id, version: item.version },
          {
            onSuccess: () => {
              toast(t("integrations.wechat.unbound"));
              onClose();
            },
            onError: (error) => {
              toast(errorText(t, error), { tone: "error" });
              if (isApiError(error, "VERSION_CONFLICT")) onClose();
            },
          },
        )
      }
    />
  );
}

function ScanDialog({
  orgId,
  presetAgent,
  onClose,
}: {
  orgId: string;
  presetAgent: string | null;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const directory = useDirectory(orgId);
  const role = useOrgRole(orgId);
  const isAdmin = role === "OWNER" || role === "ADMIN";
  const [agentId, setAgentId] = useState<string | null>(presetAgent);
  const [started, setStarted] = useState(false);
  const active = useRef<string | null>(null);
  const options = useMemo(
    () =>
      (directory.data?.entries ?? [])
        .filter(
          (entry) =>
            entry.type === "agent" &&
            !entry.archived_at &&
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
                  ? "integrations.wechat.dialog.mine"
                  : "integrations.wechat.dialog.org",
              )
            : undefined,
        })),
    [directory.data, isAdmin, t],
  );
  const close = () => {
    const id = active.current;
    active.current = null;
    if (id)
      void api.integrations.cancelWeChatQR(orgId, id).catch(() => undefined);
    onClose();
  };
  return (
    <Dialog
      open
      onClose={close}
      title={t("integrations.wechat.dialog.title")}
      description={
        started ? undefined : t("integrations.wechat.dialog.subtitle")
      }
      width={480}
    >
      {started && agentId ? (
        <ScanPanel
          orgId={orgId}
          agentId={agentId}
          onActive={(id) => (active.current = id)}
          onCancel={close}
          onDone={() => {
            active.current = null;
            onClose();
          }}
        />
      ) : (
        <div className="flex flex-col gap-4">
          <label className="flex flex-col gap-1 text-body-sm font-medium">
            {t("integrations.wechat.dialog.agent")}
            <Select
              aria-label={t("integrations.wechat.dialog.agent")}
              value={agentId}
              disabled={!!presetAgent}
              onChange={setAgentId}
              options={options}
              placeholder={
                directory.isSuccess && options.length === 0
                  ? t("integrations.wechat.dialog.agentNone")
                  : t("integrations.wechat.dialog.agentPlaceholder")
              }
            />
          </label>
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button
              disabled={!options.some((option) => option.value === agentId)}
              onClick={() => setStarted(true)}
            >
              {t("integrations.wechat.dialog.next")}
            </Button>
          </div>
        </div>
      )}
    </Dialog>
  );
}

const IN_PROGRESS = new Set(["PENDING", "SCANNED"]);

function ScanPanel({
  orgId,
  agentId,
  onActive,
  onCancel,
  onDone,
}: {
  orgId: string;
  agentId: string;
  onActive: (id: string | null) => void;
  onCancel: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [session, setSession] = useState<WeChatQRSession | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [code, setCode] = useState("");
  const [verifying, setVerifying] = useState(false);
  const stop = useRef(false);
  const startRequest = useRef<{
    attempt: number;
    promise: Promise<WeChatQRSession>;
  } | null>(null);

  const apply = (next: WeChatQRSession) => {
    setSession(next);
    onActive(
      IN_PROGRESS.has(next.status) || next.status === "NEED_VERIFY"
        ? next.id
        : null,
    );
    if (next.status === "CONFIRMED") {
      void queryClient.invalidateQueries({
        queryKey: keys.integrations(orgId),
      });
      toast(t("integrations.wechat.dialog.success"));
      onDone();
    }
  };

  // 申请二维码，然后顺序轮询（一次请求结束再发下一次；服务端单次最多约 25 秒），遇到验证码或终态停止。
  useEffect(() => {
    let disposed = false;
    stop.current = false;
    setSession(null);
    setError(null);
    setCode("");
    // StrictMode 重放副作用时复用同一次申请；只有「重新生成」才创建新会话。
    if (startRequest.current?.attempt !== attempt) {
      startRequest.current = {
        attempt,
        promise: api.integrations.startWeChatQR(orgId, agentId),
      };
    }
    const request = startRequest.current.promise;
    void (async () => {
      let current: WeChatQRSession;
      try {
        current = await request;
      } catch (e) {
        if (!disposed) setError(errorText(t, e));
        return;
      }
      if (disposed) {
        // 真正关闭时取消迟到的申请；StrictMode 的新订阅仍在使用时不取消。
        if (stop.current)
          void api.integrations
            .cancelWeChatQR(orgId, current.id)
            .catch(() => undefined);
        return;
      }
      apply(current);
      let failures = 0;
      while (!disposed && IN_PROGRESS.has(current.status)) {
        try {
          current = await api.integrations.wechatQR(orgId, current.id);
          failures = 0;
        } catch (e) {
          if (++failures >= MAX_POLL_FAILURES) {
            if (!disposed) setError(errorText(t, e));
            return;
          }
        }
        if (disposed) return;
        apply(current);
        if (IN_PROGRESS.has(current.status))
          await new Promise((r) => setTimeout(r, POLL_PAUSE_MS));
      }
    })();
    return () => {
      disposed = true;
      stop.current = true;
    };
    // 只在重新生成（attempt）时重新开始
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [attempt]);

  const submitCode = async () => {
    if (!session || !code.trim()) return;
    setVerifying(true);
    try {
      let next = await api.integrations.verifyWeChatQR(
        orgId,
        session.id,
        code.trim(),
      );
      if (stop.current) return;
      apply(next);
      // 验证通过后继续等待确认
      while (!stop.current && IN_PROGRESS.has(next.status)) {
        next = await api.integrations.wechatQR(orgId, next.id);
        if (stop.current) return;
        apply(next);
        if (IN_PROGRESS.has(next.status))
          await new Promise((r) => setTimeout(r, POLL_PAUSE_MS));
      }
      setCode("");
    } catch (e) {
      if (!stop.current) setError(errorText(t, e));
    } finally {
      setVerifying(false);
    }
  };

  const restart = () => setAttempt((n) => n + 1);

  if (error)
    return <Failure message={error} onRestart={restart} onCancel={onCancel} />;
  if (!session)
    return (
      <div
        role="status"
        className="flex min-h-[260px] items-center justify-center gap-2 text-body text-fg-muted"
      >
        <Spinner />
        {t("integrations.wechat.dialog.generating")}
      </div>
    );
  if (session.status === "CONFIRMED") return null;
  if (!IN_PROGRESS.has(session.status) && session.status !== "NEED_VERIFY")
    return (
      <Failure
        message={terminalText(t, session)}
        onRestart={restart}
        onCancel={onCancel}
      />
    );
  return (
    <div className="flex flex-col items-center gap-4">
      <div className="rounded-xl border border-line-default p-3">
        <QRCode
          value={session.qr_content}
          label={t("integrations.wechat.dialog.qrLabel")}
        />
      </div>
      {session.status === "NEED_VERIFY" ? (
        <form
          className="flex w-full max-w-[320px] flex-col gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            void submitCode();
          }}
        >
          <p className="text-center text-body-sm text-fg-muted">
            {t("integrations.wechat.dialog.verifyHint")}
          </p>
          <TextInput
            aria-label={t("integrations.wechat.dialog.verifyLabel")}
            value={code}
            maxLength={16}
            autoComplete="one-time-code"
            onChange={(event) => setCode(event.target.value)}
          />
          <Button type="submit" disabled={!code.trim() || verifying}>
            {verifying
              ? t("integrations.wechat.dialog.verifying")
              : t("integrations.wechat.dialog.verify")}
          </Button>
        </form>
      ) : (
        <>
          <p className="max-w-[360px] text-center text-body-sm text-fg-muted">
            {t("integrations.wechat.dialog.hint")}
          </p>
          <p
            role="status"
            className="flex items-center gap-2 text-caption text-fg-muted"
          >
            <Spinner />
            {session.status === "SCANNED"
              ? t("integrations.wechat.dialog.scanned")
              : t("integrations.wechat.dialog.waiting")}
          </p>
        </>
      )}
      <div className="flex w-full justify-end">
        <Button variant="outline" onClick={onCancel}>
          {t("integrations.wechat.dialog.cancel")}
        </Button>
      </div>
    </div>
  );
}

function Failure({
  message,
  onRestart,
  onCancel,
}: {
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
          {t("integrations.wechat.dialog.cancel")}
        </Button>
        <Button onClick={onRestart}>
          {t("integrations.wechat.dialog.restart")}
        </Button>
      </div>
    </div>
  );
}
