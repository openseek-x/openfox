import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import {
  isApiError,
  type Device,
  type DeviceRegistrationScope,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Dialog,
  Field,
  Icon,
  IconButton,
  Radio,
  Spinner,
  TextInput,
  cn,
  useToast,
} from "@openfox/ui";

import { useOrgRole } from "../agents/queries";
import { keys, useApi } from "../context";
import { desktopBridge, desktopLocalStatusKey } from "../desktop";
import { formatCountdown } from "./model";

/** 等待新电脑上线时刷新电脑列表的间隔 */
export const CONNECT_POLL_MS = 3000;
/** 连接成功后自动关闭弹窗的延迟 */
export const CONNECT_CLOSE_MS = 1500;
const NAME_MAX = 100;

/** 签发结果：只放在组件状态里，不进 react-query 缓存、localStorage 或日志 */
interface Issued {
  installToken: string;
  command: string;
  expiresAt: number;
}

/** 每秒刷新一次的当前时间（仅 enabled 时计时） */
function useNow(enabled: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!enabled) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [enabled]);
  return now;
}

/**
 * 连接新电脑（设置 · 电脑页、侧栏电脑弹层的弹窗与首登第 2 步共用）：
 * 选择范围 → 签发 → 显示可复制命令与倒计时 → 过期后重新生成 → 新电脑上线后 onConnected。
 * 桌面端额外提供「在这台电脑上连接」（桌面 IPC 执行 openfox computer register）。
 */
export function ConnectComputerPanel({
  orgId,
  onConnected,
  inline = false,
}: {
  orgId: string;
  onConnected?: (device: Device) => void;
  /** 首登向导内嵌：成功后不关闭，只显示成功提示 */
  inline?: boolean;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const role = useOrgRole(orgId);
  const canOrganization = role === "OWNER" || role === "ADMIN";
  const guest = role === "GUEST";
  const bridge = desktopBridge();
  const canLocal = Boolean(bridge?.registerComputer);

  const [scope, setScope] = useState<DeviceRegistrationScope>("PERSONAL");
  const [name, setName] = useState(() => t("connect.defaultName"));
  const [issued, setIssued] = useState<Issued | null>(null);
  const [issuing, setIssuing] = useState(false);
  /** 首次签发时已存在的电脑；之后出现且在线的新电脑视为本次连接 */
  const [watching, setWatching] = useState<Set<string> | null>(null);
  const [connected, setConnected] = useState<Device | null>(null);
  const [registering, setRegistering] = useState(false);
  const onConnectedRef = useRef(onConnected);
  onConnectedRef.current = onConnected;

  const now = useNow(Boolean(issued));
  const remaining = issued ? issued.expiresAt - now : 0;
  const expired = Boolean(issued) && remaining <= 0;

  // 过期后立即丢弃令牌
  useEffect(() => {
    if (expired)
      setIssued((value) =>
        value ? { ...value, installToken: "", command: "" } : value,
      );
  }, [expired]);

  // 签发后持续刷新电脑列表，直到出现新的在线电脑
  const devices = useQuery({
    queryKey: keys.devices(orgId),
    queryFn: () => api.devices.list(orgId),
    refetchInterval: watching && !connected ? CONNECT_POLL_MS : false,
    enabled: Boolean(watching),
  });
  useEffect(() => {
    if (!watching || connected || !devices.data) return;
    const found = devices.data.find(
      (device) => !watching.has(device.id) && device.online,
    );
    if (!found) return;
    setConnected(found);
    setIssued(null);
    onConnectedRef.current?.(found);
  }, [devices.data, watching, connected]);

  const issue = async () => {
    const trimmed = name.trim();
    if (!trimmed) return;
    setIssuing(true);
    try {
      // 先拿到签发前的电脑列表作为基线，避免把已有电脑误认为新电脑
      const before = await queryClient.fetchQuery({
        queryKey: keys.devices(orgId),
        queryFn: () => api.devices.list(orgId),
        staleTime: 0,
      });
      const result = await api.devices.issueRegistrationToken(orgId, {
        name: trimmed,
        scope,
      });
      const known = new Set(before.map((device) => device.id));
      setIssued({
        installToken: result.install_token,
        command: result.command,
        expiresAt: new Date(result.expires_at).getTime(),
      });
      // 多次签发时保留最早的基线：上一条命令连上的电脑也算本次连接
      setWatching((value) => value ?? known);
    } catch (error) {
      toast(
        isApiError(error) && error.status === 403
          ? t("connect.forbidden")
          : t("connect.issueFailed"),
        { tone: "error" },
      );
    } finally {
      setIssuing(false);
    }
  };

  const copy = async () => {
    if (!issued?.command) return;
    try {
      await navigator.clipboard.writeText(issued.command);
      toast(t("connect.copied"));
    } catch {
      toast(t("connect.copyFailed"), { tone: "error" });
    }
  };

  const registerLocal = async () => {
    const register = bridge?.registerComputer;
    if (!register || !issued?.installToken) return;
    setRegistering(true);
    try {
      const result = await register(issued.installToken);
      if (result.ok) {
        toast(t("connect.localDone"));
        void queryClient.invalidateQueries({ queryKey: desktopLocalStatusKey });
      } else {
        toast(t(`connect.localErrors.${result.reason}`), { tone: "error" });
      }
    } catch {
      toast(t("connect.localErrors.failed"), { tone: "error" });
    } finally {
      setRegistering(false);
    }
  };

  if (connected)
    return (
      <Callout tone="success" title={t("connect.connectedTitle")}>
        {inline
          ? t("connect.connectedInline", { name: connected.name })
          : t("connect.connectedDesc", { name: connected.name })}
      </Callout>
    );

  if (guest) return <Callout tone="info">{t("connect.guest")}</Callout>;

  const scopeOptions: Array<{
    value: DeviceRegistrationScope;
    label: string;
    desc: string;
    disabled: boolean;
  }> = [
    {
      value: "PERSONAL",
      label: t("connect.scopePersonal"),
      desc: t("connect.scopePersonalDesc"),
      disabled: false,
    },
    {
      value: "ORGANIZATION",
      label: t("connect.scopeOrganization"),
      desc: canOrganization
        ? t("connect.scopeOrganizationDesc")
        : t("connect.scopeOrganizationDisabled"),
      disabled: !canOrganization,
    },
  ];
  const ready = Boolean(issued?.command) && !expired;

  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)] gap-4">
        <Field label={t("connect.name")} required>
          <TextInput
            value={name}
            maxLength={NAME_MAX}
            placeholder={t("connect.namePlaceholder")}
            invalid={!name.trim()}
            onChange={(event) => {
              setName(event.target.value);
              setIssued(null);
            }}
          />
        </Field>
        <fieldset className="flex min-w-0 flex-col gap-1.5">
          <legend className="mb-1.5 text-body-sm font-medium text-fg-default">
            {t("connect.scope")}
          </legend>
          {scopeOptions.map((option) => (
            <label
              key={option.value}
              className={cn(
                "flex items-start gap-2",
                option.disabled ? "cursor-not-allowed" : "cursor-pointer",
              )}
            >
              <Radio
                name={`connect-scope-${orgId}`}
                value={option.value}
                checked={scope === option.value}
                disabled={option.disabled || role === undefined}
                onChange={() => {
                  setScope(option.value);
                  setIssued(null);
                }}
                className="mt-0.5"
              />
              <span className="flex min-w-0 flex-col">
                <span
                  className={cn(
                    "text-body",
                    option.disabled ? "text-fg-muted" : "text-fg-default",
                  )}
                >
                  {option.label}
                </span>
                <span className="text-caption text-fg-muted">
                  {option.desc}
                </span>
              </span>
            </label>
          ))}
        </fieldset>
      </div>

      {!issued ? (
        <div className="flex flex-col gap-2 rounded-xl border border-line-default p-4">
          <p className="text-body-sm text-fg-muted">
            {t("connect.prerequisite")}
          </p>
          <Button
            className="self-start"
            leading={<Icon name="terminal" />}
            loading={issuing}
            disabled={!name.trim() || role === undefined}
            onClick={() => void issue()}
          >
            {t("connect.generate")}
          </Button>
        </div>
      ) : (
        <section
          aria-label={t("connect.command")}
          className="flex flex-col gap-3 rounded-xl border border-line-default p-4"
        >
          <div className="flex items-start justify-between gap-3">
            <div className="flex min-w-0 flex-col gap-0.5">
              <h3 className="text-body font-medium">{t("connect.command")}</h3>
              {ready && (
                <span
                  className="text-caption text-fg-muted"
                  data-testid="connect-countdown"
                >
                  {t("connect.expiresIn", { time: formatCountdown(remaining) })}
                  {" · "}
                  {t("connect.commandHint")}
                </span>
              )}
            </div>
            <Button
              variant="outline"
              size="28"
              loading={issuing}
              onClick={() => void issue()}
            >
              {t("connect.regenerate")}
            </Button>
          </div>
          {ready ? (
            <>
              <div className="flex items-start gap-2 rounded-lg bg-[#1f1f23] px-3 py-2.5 text-white">
                <code
                  className="min-w-0 flex-1 font-mono text-code break-all select-all"
                  data-testid="connect-command"
                >
                  {issued.command}
                </code>
                <IconButton
                  label={t("connect.copy")}
                  icon={<Icon name="copy" />}
                  size="28"
                  className="text-white hover:bg-white/10"
                  onClick={() => void copy()}
                />
              </div>
              <ol className="flex flex-col gap-1.5 text-body-sm text-fg-secondary">
                {(["paste", "wait", "keep"] as const).map((step, index) => (
                  <li key={step} className="flex items-center gap-2">
                    <span className="flex size-5 shrink-0 items-center justify-center rounded-md bg-surface-muted text-caption">
                      {index + 1}
                    </span>
                    {t(`connect.steps.${step}`)}
                  </li>
                ))}
              </ol>
              {canLocal && (
                <div className="flex items-center gap-3 rounded-lg border border-line-default px-3 py-2.5">
                  <Icon name="laptop" className="text-fg-secondary" />
                  <span className="min-w-0 flex-1 text-body-sm text-fg-muted">
                    {t("connect.localDesc")}
                  </span>
                  <Button
                    size="28"
                    loading={registering}
                    onClick={() => void registerLocal()}
                  >
                    {t("connect.local")}
                  </Button>
                </div>
              )}
            </>
          ) : (
            <Callout tone="warning" title={t("connect.expiredTitle")}>
              {t("connect.expiredDesc")}
            </Callout>
          )}
        </section>
      )}

      {watching && (
        <p
          aria-live="polite"
          className="flex items-center gap-2 text-body-sm text-fg-muted"
        >
          <Spinner size={14} />
          {t("connect.waiting")}
        </p>
      )}
    </div>
  );
}

/** 连接新电脑弹窗：新电脑上线后显示成功并自动关闭。 */
export function ConnectComputerDialog({
  orgId,
  open,
  onClose,
}: {
  orgId: string;
  open: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const [done, setDone] = useState(false);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  useEffect(() => {
    if (!done) return;
    const timer = setTimeout(() => onCloseRef.current(), CONNECT_CLOSE_MS);
    return () => clearTimeout(timer);
  }, [done]);
  useEffect(() => {
    if (!open) setDone(false);
  }, [open]);
  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={672}
      title={t("connect.title")}
      description={t("connect.intro")}
      footer={<Button onClick={onClose}>{t("common.close")}</Button>}
    >
      {/* 关闭即卸载：令牌只活在面板状态里 */}
      {open && (
        <ConnectComputerPanel orgId={orgId} onConnected={() => setDone(true)} />
      )}
    </Dialog>
  );
}
