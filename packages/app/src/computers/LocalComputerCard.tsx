import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";

import { useT } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Switch,
  cn,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../context";

import {
  desktopBridge,
  desktopLocalStatusKey,
  type DesktopLocalStatus,
} from "../desktop";
import {
  useLocalRegister,
  useLocalRegisterState,
  useLocalRegistrationView,
} from "./localRegistration";
import { useLocalStatus } from "./queries";
import { OsBadge, RuntimeIcons, StatusDot } from "./parts";
import { runtimeName } from "./model";

type LocalState = "available" | "stopped" | "starting" | "unreachable";
type DaemonAction = "start" | "stop" | "restart";

/** CONNECTING 持续超过这个时间（按两次状态读取的时间差算）才当作连不上服务器 */
export const CONNECTING_GRACE_MS = 15_000;

/**
 * 守护进程状态 → 卡片状态；启动 / 重启请求进行中也算「启动中」。
 * 服务在运行但与服务器断开（RECONNECTING / DISCONNECTED，或 CONNECTING 持续不变）时为 unreachable，
 * 不再显示「可用」；旧版桌面端没有 remote_state，按可用处理。
 */
export function localState(
  status: DesktopLocalStatus | undefined,
  pending: DaemonAction | null,
  connectingStuck = false,
): LocalState {
  if (pending === "start" || pending === "restart") return "starting";
  const state = status?.daemon.state;
  if (state === "RUNNING") {
    const remote = status?.daemon.remote_state;
    if (remote === "RECONNECTING" || remote === "DISCONNECTED")
      return "unreachable";
    if (remote === "CONNECTING" && connectingStuck) return "unreachable";
    return "available";
  }
  if (state === "STARTING") return "starting";
  return "stopped";
}

/** 本机操作系统：桥接不返回系统信息，按浏览器平台推断 */
export function localOs(): string {
  const platform = typeof navigator === "undefined" ? "" : navigator.platform;
  if (/Mac/i.test(platform)) return "darwin";
  if (/Win/i.test(platform)) return "windows";
  return "linux";
}

/** CONNECTING 是否已持续超过宽限期：用状态读取时间判断，状态刷新（每 5 秒）时重新计算。 */
export function useConnectingStuck(
  status: DesktopLocalStatus | undefined,
  updatedAt: number,
): boolean {
  const since = useRef<number | null>(null);
  if (status?.daemon.remote_state !== "CONNECTING") {
    since.current = null;
    return false;
  }
  since.current ??= updatedAt;
  return updatedAt - since.current >= CONNECTING_GRACE_MS;
}

/** 本机注册状态行：自动连接进行中 / 失败可重试 / 已连到其他组织 / 已被服务端删除可重新连接。 */
function LocalRegistrationRow({ orgId }: { orgId: string }) {
  const t = useT();
  const api = useApi();
  const local = useLocalStatus();
  const view = useLocalRegistrationView(orgId);
  const register = useLocalRegister(orgId);
  const state = useLocalRegisterState(orgId);
  const organizations = useQuery({
    queryKey: keys.organizations,
    queryFn: api.me.organizations,
    enabled: view.kind === "other_org",
  });
  if (!desktopBridge()?.registerComputer) return null;
  const cliState = local.data?.cli?.state;

  let message: string | null = null;
  let action: { label: string; force: boolean } | null = null;
  let tone: "muted" | "danger" = "muted";
  if (state.status === "pending") {
    message = t("connect.auto.registering");
  } else if (view.kind === "other_org") {
    const name =
      organizations.data?.find((item) => item.id === view.organizationId)
        ?.name ?? t("connect.auto.otherOrgUnknown");
    message = t("connect.auto.otherOrg", { name });
  } else if (view.kind === "this_org" && view.staleCenterUrl) {
    message = t("connect.auto.centerChanged", { url: view.staleCenterUrl });
    action = { label: t("connect.auto.reconnect"), force: true };
    tone = "danger";
  } else if (view.kind === "this_org" && view.stale) {
    message = t("connect.auto.stale");
    action = { label: t("connect.auto.reconnect"), force: true };
  } else if (view.kind === "not_registered") {
    if (state.status === "error" && state.reason) {
      message = t(`connect.auto.errors.${state.reason}`);
      tone = "danger";
      action = { label: t("connect.auto.retry"), force: false };
    } else if (cliState === "missing" || cliState === "failed") {
      message = t("connect.auto.cliMissing");
    } else {
      message = t("connect.auto.notRegistered");
      action = { label: t("connect.auto.connect"), force: false };
    }
  }
  if (!message) return null;
  return (
    <div className="flex items-center gap-3 border-t border-line-default px-4 py-2.5">
      <span
        role={tone === "danger" ? "alert" : "status"}
        className={cn(
          "min-w-0 flex-1 text-body-sm",
          tone === "danger" ? "text-danger" : "text-fg-secondary",
        )}
      >
        {message}
      </span>
      {state.status === "pending" ? (
        <Button variant="outline" size="28" loading>
          {t("connect.auto.registering")}
        </Button>
      ) : (
        action && (
          <Button
            variant="outline"
            size="28"
            onClick={() => register.mutate({ force: action.force })}
          >
            {action.label}
          </Button>
        )
      )}
    </div>
  );
}

/** 本机卡片：仅桌面端，通过桌面桥接读写本机守护进程。 */
export function LocalComputerCard({ orgId }: { orgId?: string }) {
  const t = useT();
  const toast = useToast();
  const queryClient = useQueryClient();
  const local = useLocalStatus();
  const [confirmStop, setConfirmStop] = useState(false);
  const action = useMutation({
    mutationFn: (next: DaemonAction) => desktopBridge()!.daemonAction(next),
    onSuccess: (status) => {
      queryClient.setQueryData(desktopLocalStatusKey, status);
      setConfirmStop(false);
    },
    onError: () => {
      toast(t("computers.local.actionFailed"), { tone: "error" });
      void local.refetch();
    },
  });
  const pending = action.isPending ? action.variables : null;
  const bridge = desktopBridge();
  const sleep = useMutation({
    mutationFn: (enabled: boolean) => bridge!.setPreventSleep!(enabled),
    onSuccess: (prevent_sleep) => {
      queryClient.setQueryData<DesktopLocalStatus>(
        desktopLocalStatusKey,
        (status) => (status ? { ...status, prevent_sleep } : status),
      );
    },
    onError: () => toast(t("connect.sleep.failed"), { tone: "error" }),
  });
  const preventSleep = local.data?.prevent_sleep;
  const sleepEnabled = sleep.isPending
    ? sleep.variables
    : (preventSleep?.enabled ?? false);
  const view = useLocalRegistrationView(orgId);
  const connectingStuck = useConnectingStuck(local.data, local.dataUpdatedAt);
  const rawState = localState(local.data, pending ?? null, connectingStuck);
  // 注册的服务器地址已过期：本机服务看起来在跑，其实一直连着旧地址，不能显示「可用」
  const state: LocalState =
    rawState === "available" && view.kind === "this_org" && view.staleCenterUrl
      ? "unreachable"
      : rawState;
  const runtime = local.data?.runtime;
  const runnable = runtime?.runnable ? 1 : 0;
  const os = localOs();

  const description =
    state === "available"
      ? t("computers.local.runningDesc", { count: runnable })
      : state === "unreachable"
        ? t("computers.local.unreachableDesc")
        : state === "starting"
          ? t("computers.local.startingDesc")
          : t("computers.local.stoppedDesc");
  const redetect = () => void local.refetch();

  return (
    <section
      aria-label={t("computers.local.name")}
      aria-busy={local.isLoading || undefined}
      className="flex flex-col rounded-xl border border-line-default"
    >
      <div className="flex items-center gap-3 px-4 py-3">
        <OsBadge os={os} size={32} />
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
            <span className="text-body font-medium text-fg-default">
              {t("computers.local.name")}
            </span>
            <span className="text-body text-fg-muted">
              {t("computers.local.suffix")}
            </span>
            <span className="inline-flex items-center gap-1.5 text-body-sm text-fg-default">
              <StatusDot
                status={
                  state === "available"
                    ? "online"
                    : state === "starting" || state === "unreachable"
                      ? "noRuntime"
                      : "offline"
                }
              />
              {t(`computers.local.${state}`)}
            </span>
            {state === "available" && runtime && (
              <RuntimeIcons
                runtimes={[
                  {
                    provider: runtime.runtime,
                    version: runtime.version ?? null,
                    status: runtime.runnable ? "READY" : "UNAVAILABLE",
                    runnable: runtime.runnable,
                    reason: runtime.reason ?? null,
                    login_status: runtime.login_status ?? null,
                    models: [],
                    approval_modes: ["FOLLOW_RUNTIME"],
                  },
                ]}
              />
            )}
          </div>
          <span className="truncate text-caption text-fg-muted">
            {description}
            {state === "available" && runtime && !runtime.runnable && (
              <>
                {" "}
                {t("computers.local.runtimeUnavailable", {
                  name: runtimeName(runtime.runtime),
                })}
              </>
            )}
          </span>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {bridge?.setPreventSleep && preventSleep && (
            <div
              className="mr-1 flex items-center gap-2 text-body-sm text-fg-secondary"
              title={t("connect.sleep.desc")}
            >
              <Switch
                checked={sleepEnabled}
                disabled={sleep.isPending}
                label={t("connect.sleep.label")}
                onCheckedChange={(next) => sleep.mutate(next)}
              />
              <span>
                {preventSleep.active
                  ? t("connect.sleep.active")
                  : t("connect.sleep.label")}
              </span>
            </div>
          )}
          {(state === "available" || state === "unreachable") && (
            <>
              <IconButton
                label={t("computers.local.redetect")}
                icon={<Icon name="refresh" />}
                variant="outline"
                size="28"
                onClick={redetect}
              />
              <Button
                variant="outline"
                size="28"
                leading={<Icon name="rotate-cw" />}
                onClick={() => action.mutate("restart")}
              >
                {t("computers.local.restart")}
              </Button>
            </>
          )}
          {state === "stopped" && (
            <Button
              variant="outline"
              size="28"
              leading={<Icon name="play" />}
              loading={action.isPending}
              onClick={() => action.mutate("start")}
            >
              {t("computers.local.start")}
            </Button>
          )}
          {state === "starting" && (
            <Button variant="outline" size="28" loading>
              {t("computers.local.startingButton")}
            </Button>
          )}
          {state !== "stopped" && (
            <Button
              variant="danger-outline"
              size="28"
              leading={<Icon name="square" />}
              disabled={pending === "stop"}
              onClick={() => setConfirmStop(true)}
            >
              {t("computers.local.stop")}
            </Button>
          )}
          <Menu
            label={t("computers.local.more")}
            placement="bottom-end"
            trigger={({ ref, toggle, open }) => (
              <IconButton
                ref={ref}
                label={t("computers.local.more")}
                icon={<Icon name="ellipsis" />}
                variant="outline"
                size="28"
                aria-expanded={open}
                onClick={toggle}
              />
            )}
          >
            <MenuItem icon={<Icon name="refresh" />} onSelect={redetect}>
              {t("computers.local.redetect")}
            </MenuItem>
          </Menu>
        </div>
      </div>
      {orgId && <LocalRegistrationRow orgId={orgId} />}
      <ConfirmDialog
        open={confirmStop}
        danger
        title={t("computers.local.stopTitle")}
        description={t("computers.local.stopDesc")}
        confirmLabel={t("computers.local.stop")}
        loading={pending === "stop"}
        onCancel={() => setConfirmStop(false)}
        onConfirm={() => action.mutate("stop")}
      />
    </section>
  );
}
