import { DeviceSettingsEditor } from "./DeviceSettingsEditor";
import { RuntimeTestButton } from "../agents/settings/RuntimeTest";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactElement, type ReactNode } from "react";
import { useNavigate, useParams } from "react-router-dom";

import { isApiError, type Device, type DeviceRuntime } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Callout,
  ConfirmDialog,
  Dialog,
  Icon,
  IconButton,
  Tooltip,
  cn,
  useToast,
  type BadgeTone,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import { desktopBridge } from "../desktop";
import { formatRelativeTime } from "../tasks/primitives/dates";
import {
  RUNTIME_INSTALL,
  RUNTIME_PROVIDERS,
  computerStatus,
  osIcon,
  runtimeIcon,
  runtimeName,
  statusDotClass,
  statusLabel,
  visibilityText,
  type ComputerStatus,
} from "./model";
import { useLocalRegister } from "./localRegistration";
import { useDevices, useLocalStatus, useMe } from "./queries";

/** 未开放的入口：禁用按钮外包一层，禁用元素收不到鼠标事件，提示挂在外层。 */
export function ComingSoon({
  children,
  label,
}: {
  children: ReactElement;
  label?: string;
}) {
  const t = useT();
  return (
    <Tooltip content={label ?? t("computers.comingSoon")}>
      <span tabIndex={0} className="inline-flex rounded-lg outline-none">
        {children}
      </span>
    </Tooltip>
  );
}

export function StatusDot({
  status,
  className,
}: {
  status: ComputerStatus;
  className?: string;
}) {
  return (
    <span
      aria-hidden
      className={cn(
        "inline-block size-1.5 shrink-0 rounded-full",
        statusDotClass[status],
        className,
      )}
    />
  );
}

const statusText: Record<ComputerStatus, string> = {
  online: "text-accent",
  noRuntime: "text-warning",
  offline: "text-fg-muted",
};

/** 状态点 + 文案（在线 / Runtime 未检测到 / 离线） */
export function ComputerStatusLabel({
  device,
  availableLabel = false,
}: {
  device: Pick<Device, "online" | "runtimes">;
  /** 侧栏与抽屉把「在线」叫作「可用」 */
  availableLabel?: boolean;
}) {
  const t = useT();
  const status = computerStatus(device);
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 text-body-sm",
        statusText[status],
      )}
    >
      <StatusDot status={status} />
      {status === "online" && availableLabel
        ? t("computers.status.available")
        : t(statusLabel[status])}
    </span>
  );
}

/** 操作系统图标底座：列表行 28、详情头部 48。 */
export function OsBadge({
  os,
  size = 28,
}: {
  os: string;
  size?: 28 | 32 | 48;
}) {
  return (
    <span
      aria-hidden
      style={{ width: size, height: size }}
      className={cn(
        "flex shrink-0 items-center justify-center bg-surface-muted text-fg-secondary",
        size === 48 ? "rounded-full" : "rounded-lg",
      )}
    >
      <Icon name={osIcon(os)} size={size === 48 ? 22 : 16} />
    </span>
  );
}

/** Runtime 图标组；没有可用 Runtime 时显示「Runtime 未检测到」 */
export function RuntimeIcons({
  runtimes,
  max = 5,
}: {
  runtimes: readonly DeviceRuntime[];
  max?: number;
}) {
  const t = useT();
  if (runtimes.length === 0)
    return (
      <span className="inline-flex items-center gap-1 text-body-sm text-warning">
        <Icon name="circle-alert" size={14} />
        {t("computers.status.noRuntime")}
      </span>
    );
  const shown = runtimes.slice(0, max);
  return (
    <span className="inline-flex items-center gap-1">
      {shown.map((runtime) => (
        <Icon
          key={runtime.provider}
          name={runtimeIcon(runtime.provider)}
          size={14}
          label={runtimeName(runtime.provider)}
          className={runtime.runnable ? undefined : "opacity-40"}
        />
      ))}
      {runtimes.length > max && (
        <span className="text-caption text-fg-muted">
          +{runtimes.length - max}
        </span>
      )}
    </span>
  );
}

export function CommandChip({ children }: { children: string }) {
  return (
    <code className="rounded-md border border-line-default bg-surface-card px-1.5 py-0.5 font-mono text-code text-fg-default">
      {children}
    </code>
  );
}

/**
 * 「修复并重连」：当前查看的就是本机（本机注册的 device_id 与电脑 ID 一致）且桌面端支持注册时可用，
 * 执行与「这台电脑」卡片相同的强制重新注册；其他电脑或浏览器里禁用并提示。
 */
function RepairButton({
  orgId,
  deviceId,
}: {
  orgId: string;
  deviceId: string;
}) {
  const t = useT();
  const local = useLocalStatus();
  const register = useLocalRegister(orgId);
  const registration = local.data?.registration;
  const isLocal =
    Boolean(desktopBridge()?.registerComputer) &&
    registration?.registered === true &&
    registration.device_id === deviceId;
  if (!isLocal)
    return (
      <Tooltip content={t("computers.detail.repairOnlyLocal")}>
        <span tabIndex={0} className="inline-flex rounded-lg outline-none">
          <Button variant="outline" size="28" disabled>
            {t("computers.detail.repair")}
          </Button>
        </span>
      </Tooltip>
    );
  return (
    <Button
      variant="outline"
      size="28"
      loading={register.isPending}
      onClick={() => register.mutate({ force: true })}
    >
      {t("computers.detail.repair")}
    </Button>
  );
}

/**
 * 「迁移到这台电脑」：重新连接会产生新的电脑记录，旧记录上的 Agent 还指向旧记录。
 * 当前查看的是自己名下的离线旧记录，且本机已注册为本组织里的另一条记录时，把 Agent 迁到本机记录。
 */
function MigrateButton({ orgId, device }: { orgId: string; device: Device }) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const local = useLocalStatus();
  const me = useMe();
  const [open, setOpen] = useState(false);
  const registration = local.data?.registration;
  const localId =
    Boolean(desktopBridge()?.registerComputer) &&
    registration?.registered === true &&
    registration.organization_id === orgId &&
    registration.device_id !== device.id
      ? registration.device_id
      : undefined;
  const mine = me.data?.id !== undefined && device.owner_user_id === me.data.id;
  const devices = useDevices(orgId, Boolean(localId) && mine && !device.online);
  const migrate = useMutation({
    mutationFn: () => api.devices.replace(orgId, localId!, device.id),
    onSuccess: (result) => {
      setOpen(false);
      toast(t("computers.detail.migrated", { count: result.moved_agents }));
      for (const queryKey of [
        keys.devices(orgId),
        keys.agents(orgId),
        keys.contacts(orgId),
        keys.assistant(orgId),
        keys.agentDirectory(orgId),
        ["org", orgId, "space-agents"],
      ])
        void queryClient.invalidateQueries({ queryKey });
      queryClient.removeQueries({ queryKey: keys.device(orgId, device.id) });
      navigate(
        `/o/${orgId}/settings/computers?computer=${encodeURIComponent(result.device_id)}`,
      );
    },
    onError: (error) => {
      setOpen(false);
      toast(
        isApiError(error, "DEVICE_ONLINE")
          ? t("computers.detail.migrateErrors.online")
          : isApiError(error, "GRANT_SCOPE_MISMATCH")
            ? t("computers.detail.migrateErrors.scopeMismatch")
            : t("settings.errors.generic"),
        { tone: "error" },
      );
    },
  });
  if (
    !localId ||
    !mine ||
    device.online ||
    !devices.data?.some((item) => item.id === localId)
  )
    return null;
  return (
    <>
      <Button
        variant="outline"
        size="28"
        loading={migrate.isPending}
        onClick={() => setOpen(true)}
      >
        {t("computers.detail.migrate")}
      </Button>
      <ConfirmDialog
        open={open}
        title={t("computers.detail.migrateTitle")}
        description={t("computers.detail.migrateDesc", {
          name: device.name,
          count: device.agent_count,
        })}
        confirmLabel={t("computers.detail.migrateConfirm")}
        loading={migrate.isPending}
        onCancel={() => setOpen(false)}
        onConfirm={() => migrate.mutate()}
      />
    </>
  );
}

/** 在线：CLI 管理说明；离线：离线提示 + 「修复并重连」与（旧记录时）「迁移到这台电脑」。 */
export function CliNotice({
  device,
  orgId,
}: {
  device: Device;
  orgId: string;
}) {
  const t = useT();
  if (!device.online)
    return (
      <Callout
        tone="info"
        title={t("computers.detail.offlineTitle")}
        action={
          <span className="flex flex-wrap items-center gap-2">
            <MigrateButton orgId={orgId} device={device} />
            <RepairButton orgId={orgId} deviceId={device.id} />
          </span>
        }
      >
        <p className="flex flex-wrap items-center gap-1">
          {t("computers.detail.offlineIntro")}
          <CommandChip>openfox daemon start</CommandChip>
          {t("computers.detail.period")}
        </p>
        <p className="mt-1 text-body-sm text-fg-muted">
          {t("computers.detail.offlineHint")}
        </p>
      </Callout>
    );
  return (
    <Callout tone="success">
      <p>{t("computers.detail.cliIntro")}</p>
      <p className="mt-1.5 flex flex-wrap items-center gap-1.5">
        <CommandChip>openfox daemon start</CommandChip>
        <CommandChip>openfox daemon stop</CommandChip>
        <CommandChip>openfox daemon restart</CommandChip>
        <span>{t("computers.detail.orRun")}</span>
        <CommandChip>openfox daemon --help</CommandChip>
        <span>{t("computers.detail.forHelp")}</span>
      </p>
    </Callout>
  );
}

export function SectionTitle({
  children,
  action,
}: {
  children: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="flex min-h-8 items-center justify-between gap-2">
      <h2 className="text-body-sm font-medium text-fg-muted">{children}</h2>
      {action}
    </div>
  );
}

/** 概览指标卡 */
export function Metric({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-2 px-4 py-3">
      <dt className="text-caption text-fg-muted">{label}</dt>
      <dd className="truncate text-body font-medium text-fg-default">
        {value}
      </dd>
    </div>
  );
}

/** 概览：Runtimes 可用 x/y、Agents 在线 x/y、并发、可见性；离线时加最后活跃。 */
export function Overview({
  device,
  showAgents = true,
}: {
  device: Device;
  showAgents?: boolean;
}) {
  const { t, locale } = useI18n();
  const runnable = device.online
    ? device.runtimes.filter((runtime) => runtime.runnable).length
    : 0;
  const items: Array<{ label: string; value: ReactNode }> = [
    {
      label: t("computers.detail.runtimes"),
      value: t("computers.detail.available", {
        count: runnable,
        total: device.runtimes.length,
      }),
    },
  ];
  if (showAgents)
    items.push({
      label: t("computers.detail.agents"),
      value: t("computers.detail.agentsOnline", {
        count: device.online ? device.agent_count : 0,
        total: device.agent_count,
      }),
    });
  items.push({
    label: t("computers.detail.concurrency"),
    value: `${device.reserved_slots} / ${device.max_slots}`,
  });
  if (!device.online)
    items.push({
      label: t("computers.detail.lastSeen"),
      value: device.last_seen_at
        ? formatRelativeTime(device.last_seen_at, locale)
        : t("computers.detail.never"),
    });
  items.push({
    label: t("computers.detail.visibility"),
    value: visibilityText(device, t),
  });
  return (
    <dl
      className="grid divide-x divide-line-default rounded-xl border border-line-default"
      style={{ gridTemplateColumns: `repeat(${items.length}, minmax(0, 1fr))` }}
    >
      {items.map((item) => (
        <Metric key={item.label} label={item.label} value={item.value} />
      ))}
    </dl>
  );
}

function runtimeBadge(
  runtime: DeviceRuntime,
  online: boolean,
): { tone: BadgeTone; label: "ready" | "login" | "unavailable" | "offline" } {
  if (!online) return { tone: "muted", label: "offline" };
  if (runtime.status === "READY" && runtime.runnable)
    return { tone: "success", label: "ready" };
  if (runtime.status === "NEEDS_LOGIN")
    return { tone: "warning", label: "login" };
  return { tone: "warning", label: "unavailable" };
}

const runtimeBadgeKey = {
  ready: "computers.detail.runtimeReady",
  login: "computers.detail.runtimeNeedsLogin",
  unavailable: "computers.detail.runtimeUnavailable",
  offline: "computers.detail.runtimeOffline",
} as const;

function RuntimeCard({
  icon,
  title,
  badge,
  description,
  action,
}: {
  icon: ReactNode;
  title: string;
  badge: ReactNode;
  description: ReactNode;
  action?: ReactNode;
}) {
  return (
    <li className="flex items-center gap-3 rounded-xl border border-line-default px-4 py-3">
      {icon}
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex items-center gap-2">
          <span className="text-body font-medium text-fg-default">{title}</span>
          {badge}
        </div>
        <span className="truncate text-caption text-fg-muted">
          {description}
        </span>
      </div>
      {action}
    </li>
  );
}

/**
 * Runtime 卡片：来自心跳上报（codex、claude-code、opencode、pi）。
 * 支持的 Runtime 里没有上报的显示「未检测到」与各自的安装指南。
 */
export function RuntimeList({ device }: { device: Device }) {
  const t = useT();
  const { orgId = "" } = useParams();
  const me = useMe();
  const local = useLocalStatus();
  const [expanded, setExpanded] = useState<string | null>(null);
  const registration = local.data?.registration;
  const sameDevice =
    me.data?.id === device.owner_user_id &&
    registration?.registered &&
    registration.device_id === device.id &&
    registration.organization_id === orgId;
  const [guide, setGuide] = useState<string | null>(null);
  const reported = new Set(
    device.runtimes.map((runtime) => runtime.provider.toLowerCase()),
  );
  const missing = RUNTIME_PROVIDERS.filter(
    (provider) => !reported.has(provider),
  );
  return (
    <>
      <ul className="flex flex-col gap-2">
        {device.runtimes.map((runtime) => {
          const badge = runtimeBadge(runtime, device.online);
          const version = runtime.version
            ? t("computers.detail.runtimeVersion", { version: runtime.version })
            : "";
          const reason =
            badge.label === "login"
              ? t("computers.detail.runtimeNeedsLoginDesc")
              : badge.label === "unavailable"
                ? (runtime.reason ?? "")
                : "";
          return (
            <li
              key={runtime.provider}
              className="rounded-xl border border-line-default"
            >
              <button
                type="button"
                className="flex w-full items-center gap-3 px-4 py-3 text-left hover:bg-surface-hover"
                aria-expanded={expanded === runtime.provider}
                aria-label={
                  runtimeName(runtime.provider) +
                  " · " +
                  t("shell.runtimeDiagnostics")
                }
                onClick={() =>
                  setExpanded(
                    expanded === runtime.provider ? null : runtime.provider,
                  )
                }
              >
                <Icon name={runtimeIcon(runtime.provider)} size={20} />
                <span className="flex flex-1 flex-col gap-1">
                  <span className="flex items-center gap-2 text-body font-medium">
                    {runtimeName(runtime.provider)}{" "}
                    <Badge tone={badge.tone} small>
                      {t(runtimeBadgeKey[badge.label])}
                    </Badge>
                  </span>
                  <span className="text-caption text-fg-muted">
                    {[version, reason].filter(Boolean).join(" · ")}
                  </span>
                </span>
                <Icon
                  name={
                    expanded === runtime.provider
                      ? "chevron-down"
                      : "chevron-right"
                  }
                />
              </button>
              {expanded === runtime.provider && (
                <div className="flex flex-col gap-2 border-t border-line-soft p-4">
                  <RuntimeTestButton
                    orgId={orgId}
                    device={device}
                    meId={me.data?.id}
                    model=""
                    provider={runtime.provider}
                  />
                  {runtime.provider === "codex" &&
                    me.data?.id === device.owner_user_id && (
                      <div className="flex gap-2">
                        {(["cli", "args"] as const).map((section) => (
                          <DeviceSettingsEditor
                            key={section}
                            section={section}
                            orgId={orgId}
                            deviceId={device.id}
                            disabled={!device.online}
                          />
                        ))}
                      </div>
                    )}
                  <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-body-sm">
                    <dt>{t("shell.runtimeLogin")}</dt>
                    <dd>{runtime.login_status ?? "—"}</dd>
                    {sameDevice &&
                      local.data?.runtime.runtime === runtime.provider &&
                      local.data.runtime.executable && (
                        <>
                          <dt>CLI</dt>
                          <dd className="break-all">
                            {local.data.runtime.executable}
                          </dd>
                        </>
                      )}
                    {!!runtime.models.length && (
                      <>
                        <dt>{t("shell.runtimeModels")}</dt>
                        <dd>{runtime.models.map((m) => m.id).join(", ")}</dd>
                      </>
                    )}
                  </dl>
                </div>
              )}
            </li>
          );
        })}
        {missing.map((provider) => (
          <RuntimeCard
            key={provider}
            icon={<Icon name={runtimeIcon(provider)} size={20} />}
            title={runtimeName(provider)}
            badge={
              <Badge tone="muted" small>
                {t("computers.detail.runtimeMissing")}
              </Badge>
            }
            description={t("computers.detail.runtimeMissingDesc")}
            action={
              <Button
                variant="outline"
                size="28"
                aria-label={`${t("computers.detail.installGuide")} · ${runtimeName(provider)}`}
                onClick={() => setGuide(provider)}
              >
                {t("computers.detail.installGuide")}
              </Button>
            }
          />
        ))}
      </ul>
      <InstallGuideDialog
        open={guide !== null}
        onClose={() => setGuide(null)}
        provider={guide ?? RUNTIME_PROVIDERS[0]}
      />
    </>
  );
}

/** 安装指南弹窗（页 18 C8）：各 Runtime 的 npm 安装命令与官方文档。 */
export function InstallGuideDialog({
  open,
  onClose,
  provider,
}: {
  open: boolean;
  onClose: () => void;
  provider: string;
}) {
  const t = useT();
  const toast = useToast();
  const install = RUNTIME_INSTALL[provider] ?? RUNTIME_INSTALL.codex!;
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(install.command);
      toast(t("computers.guide.copied"));
    } catch {
      toast(t("computers.guide.copyFailed"), { tone: "error" });
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={520}
      title={t("computers.guide.title")}
      footer={
        <>
          <a
            href={install.docs}
            target="_blank"
            rel="noreferrer noopener"
            className="mr-auto inline-flex h-8 items-center rounded-lg bg-surface-muted px-3 text-body font-medium hover:bg-surface-active"
          >
            {t("computers.guide.docs")}
          </a>
          <Button onClick={onClose}>{t("common.close")}</Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <p className="flex items-start gap-2 text-body text-fg-secondary">
          <Icon name={runtimeIcon(provider)} size={18} className="mt-0.5" />
          <span>
            {t("computers.guide.desc", { name: runtimeName(provider) })}
          </span>
        </p>
        <section className="flex flex-col gap-2 rounded-xl border border-line-strong p-3">
          <div className="flex items-center gap-2">
            <span className="text-body font-medium">
              {t("computers.guide.npm")}
            </span>
            <Badge tone="success" small>
              {t("computers.guide.recommended")}
            </Badge>
          </div>
          <div className="flex items-center gap-2 rounded-lg bg-surface-muted px-3 py-2">
            <code className="min-w-0 flex-1 truncate font-mono text-code">
              {install.command}
            </code>
            <IconButton
              label={t("computers.guide.copy")}
              icon={<Icon name="copy" />}
              size="28"
              onClick={() => void copy()}
            />
          </div>
        </section>
        <p className="text-caption text-fg-muted">
          {t("computers.guide.note")}
        </p>
      </div>
    </Dialog>
  );
}

/** 电脑信息表的一行：左标签（可带说明），右值 */
export function InfoRow({
  label,
  description,
  children,
}: {
  label: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <div className="flex min-h-12 items-center gap-4 border-b border-line-soft px-4 py-2.5 last:border-b-0">
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="text-body text-fg-default">{label}</span>
        {description && (
          <span className="text-caption text-fg-muted">{description}</span>
        )}
      </div>
      <div className="flex min-w-0 shrink-0 items-center justify-end gap-2 text-body text-fg-secondary">
        {children}
      </div>
    </div>
  );
}

export function InfoCard({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-xl border border-line-default">{children}</div>
  );
}

/** 创建时间：本地化日期时间 */
export function formatDateTime(value: string, locale: string): string {
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "long",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value));
}
