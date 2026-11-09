import {
  useMutation,
  useMutationState,
  useQueryClient,
} from "@tanstack/react-query";
import { useEffect, useRef } from "react";

import { isApiError } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useToast } from "@openfox/ui";

import { keys, useApi } from "../context";
import {
  desktopBridge,
  desktopLocalStatusKey,
  type DesktopLocalStatus,
  type DesktopRegisterResult,
} from "../desktop";
import { useOrgRole } from "../spaces/queries";
import { useDevices, useLocalStatus, useServerCenterUrl } from "./queries";

// 桌面端自动注册本机：打开组织时本机还没注册，就用当前登录身份签发个人安装令牌，
// 交给主进程执行 openfox computer register（令牌经 stdin 传给 CLI），并安装后台服务。
// 一个窗口生命周期内每个组织只自动尝试一次；失败后由「这台电脑」卡片上的按钮重试。

type LocalRegisterFailure = Exclude<
  DesktopRegisterResult,
  { ok: true }
>["reason"];

/** 本次注册的失败原因：签发令牌失败单独记为 issue_failed / forbidden */
export type LocalRegisterError =
  LocalRegisterFailure | "issue_failed" | "forbidden";

class RegisterError extends Error {
  constructor(readonly reason: LocalRegisterError) {
    super(reason);
  }
}

const registerKey = (orgId: string) =>
  ["desktop", "local-register", orgId] as const;

const attempted = new Set<string>();

/** 只用于测试：清空「已自动尝试」记录 */
export function resetAutoRegisterAttempts() {
  attempted.clear();
}

/** 本机注册与这个组织的关系 */
export type LocalRegistrationView =
  | { kind: "unknown" }
  | { kind: "not_registered" }
  | {
      kind: "this_org";
      deviceId: string;
      stale: boolean;
      /** 本机注册的中心地址与服务端当前地址不同时，为本机注册的（旧）地址 */
      staleCenterUrl?: string;
    }
  | { kind: "other_org"; organizationId: string };

/** 中心地址的比较键：scheme + host + port；忽略末尾 /、默认端口与大小写。无法解析返回 null。 */
function centerKey(value: string): string | null {
  try {
    const url = new URL(value.trim());
    if (url.protocol !== "http:" && url.protocol !== "https:") return null;
    const port = url.port || (url.protocol === "https:" ? "443" : "80");
    return `${url.protocol}//${url.hostname}:${port}`;
  } catch {
    return null;
  }
}

/** 两个中心地址是否指向同一个服务；任一方缺失或无法解析时不判定为不同。 */
export function sameCenter(
  local: string | undefined,
  server: string | undefined,
): boolean {
  if (!local || !server) return true;
  const a = centerKey(local);
  const b = centerKey(server);
  return a === null || b === null || a === b;
}

export function registrationView(
  status: DesktopLocalStatus | undefined,
  orgId: string,
  deviceIds: string[] | undefined,
  serverCenterUrl?: string,
): LocalRegistrationView {
  const registration = status?.registration;
  if (!registration) return { kind: "unknown" };
  if (!registration.registered)
    return registration.reason === "not_registered"
      ? { kind: "not_registered" }
      : { kind: "unknown" };
  if (registration.organization_id !== orgId)
    return { kind: "other_org", organizationId: registration.organization_id };
  return {
    kind: "this_org",
    deviceId: registration.device_id,
    // 电脑列表已加载、却没有这台电脑：服务端已删除，本机还留着旧配置
    stale: Boolean(deviceIds && !deviceIds.includes(registration.device_id)),
    staleCenterUrl: sameCenter(registration.center_url, serverCenterUrl)
      ? undefined
      : registration.center_url,
  };
}

/** 注册成功后后台服务要几秒才连上中心：在此期间短间隔刷新电脑列表，直到本机上线或超时。 */
export const ONLINE_POLL_MS = 2000;
export const ONLINE_POLL_LIMIT = 30;

/** 重新连接后迁移旧记录上 Agent 的结果；迁移不影响重新连接本身是否成功。 */
type ReplaceOutcome = { moved: number } | { failed: true } | null;

/** 注册本机：签发个人安装令牌 → 主进程执行 CLI 注册；强制重连时再把旧记录上的 Agent 迁到新记录。 */
export function useLocalRegister(orgId: string) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const waitUntilOnline = async () => {
    const bridge = desktopBridge();
    if (!bridge || !mounted.current) return;
    for (let i = 0; i < ONLINE_POLL_LIMIT; i++) {
      await new Promise((resolve) => setTimeout(resolve, ONLINE_POLL_MS));
      if (!mounted.current) return;
      const status = await queryClient.fetchQuery({
        queryKey: desktopLocalStatusKey,
        queryFn: () => bridge.getLocalStatus(),
        staleTime: 0,
      });
      if (!mounted.current) return;
      const registration = status.registration;
      if (!registration?.registered) return;
      const devices = await queryClient.fetchQuery({
        queryKey: keys.devices(orgId),
        queryFn: () => api.devices.list(orgId),
        staleTime: 0,
      });
      if (
        devices.some(
          (device) => device.id === registration.device_id && device.online,
        )
      )
        return;
    }
  };
  return useMutation({
    mutationKey: registerKey(orgId),
    mutationFn: async ({
      force,
    }: {
      force: boolean;
    }): Promise<ReplaceOutcome> => {
      const bridge = desktopBridge();
      if (!bridge?.registerComputer) throw new RegisterError("unavailable");
      const status = queryClient.getQueryData<DesktopLocalStatus>(
        desktopLocalStatusKey,
      );
      let token: string;
      try {
        const issued = await api.devices.issueRegistrationToken(orgId, {
          name: status?.machine_name?.trim() || t("connect.defaultName"),
          scope: "PERSONAL",
        });
        token = issued.install_token;
      } catch (error) {
        const forbidden =
          typeof error === "object" &&
          error !== null &&
          (error as { status?: number }).status === 403;
        throw new RegisterError(forbidden ? "forbidden" : "issue_failed");
      }
      // 强制重连会让服务器新建一条电脑记录：记下重连前属于本组织的旧记录，成功后把 Agent 迁过来
      const previous =
        force &&
        status?.registration?.registered === true &&
        status.registration.organization_id === orgId
          ? status.registration.device_id
          : undefined;
      const result = await bridge.registerComputer(token, { force });
      if (!result.ok) throw new RegisterError(result.reason);
      if (!previous) return null;
      try {
        const fresh = await queryClient.fetchQuery({
          queryKey: desktopLocalStatusKey,
          queryFn: () => bridge.getLocalStatus(),
          staleTime: 0,
        });
        const next = fresh.registration;
        if (
          !next?.registered ||
          next.organization_id !== orgId ||
          next.device_id === previous
        )
          return null;
        const replaced = await api.devices.replace(
          orgId,
          next.device_id,
          previous,
        );
        return { moved: replaced.moved_agents };
      } catch (error) {
        // 404：旧记录不在这个服务器上或已经处理过，静默
        if (isApiError(error) && error.status === 404) return null;
        return { failed: true };
      }
    },
    onSuccess: (outcome) => {
      toast(t("connect.auto.done"));
      if (outcome && "failed" in outcome)
        toast(t("connect.auto.moveFailed"), { tone: "error" });
      else if (outcome && outcome.moved > 0) {
        toast(t("connect.auto.moved", { count: outcome.moved }));
        void queryClient.invalidateQueries({ queryKey: keys.agents(orgId) });
        void queryClient.invalidateQueries({ queryKey: keys.contacts(orgId) });
        void queryClient.invalidateQueries({ queryKey: keys.assistant(orgId) });
        void queryClient.invalidateQueries({
          queryKey: keys.agentDirectory(orgId),
        });
        void queryClient.invalidateQueries({
          queryKey: ["org", orgId, "space-agents"],
        });
      }
      // Registration succeeded. A transient status refresh failure must not become
      // an unhandled rejection; the normal status queries can retry on refocus.
      void waitUntilOnline().catch(() => undefined);
    },
    onError: (error) => {
      const reason = error instanceof RegisterError ? error.reason : "failed";
      toast(t(`connect.auto.errors.${reason}`), { tone: "error" });
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: desktopLocalStatusKey });
      void queryClient.invalidateQueries({ queryKey: keys.devices(orgId) });
    },
  });
}

/** 最近一次注册（任何组件发起的）的状态，供「这台电脑」卡片显示。 */
export function useLocalRegisterState(orgId: string): {
  status: "idle" | "pending" | "success" | "error";
  reason: LocalRegisterError | null;
} {
  const states = useMutationState({
    filters: { mutationKey: registerKey(orgId) },
    select: (mutation) => ({
      status: mutation.state.status,
      error: mutation.state.error,
    }),
  });
  const last = states.at(-1);
  if (!last) return { status: "idle", reason: null };
  return {
    status: last.status,
    reason:
      last.status === "error"
        ? last.error instanceof RegisterError
          ? last.error.reason
          : "failed"
        : null,
  };
}

/** 挂在组织外壳里：桌面端打开组织时，本机未注册就自动注册一次。 */
export function useAutoRegisterLocal(orgId: string) {
  const bridge = desktopBridge();
  const enabled = Boolean(bridge?.registerComputer);
  const local = useLocalStatus();
  const role = useOrgRole(orgId);
  const register = useLocalRegister(orgId);
  const notRegistered =
    local.data?.registration?.registered === false &&
    local.data.registration.reason === "not_registered";
  const cliState = local.data?.cli?.state;
  // 自带 CLI 缺失或安装失败时不自动尝试（卡片上会说明），避免一打开就报错
  const cliUsable = cliState !== "missing" && cliState !== "failed";
  const ready =
    enabled &&
    notRegistered &&
    cliUsable &&
    role !== undefined &&
    role !== "GUEST";
  const mutate = register.mutate;
  useEffect(() => {
    if (!ready || attempted.has(orgId)) return;
    attempted.add(orgId);
    mutate({ force: false });
  }, [ready, orgId, mutate]);
}

/** 卡片用：本机注册与本组织的关系（会顺带加载电脑列表判断是否已被服务端删除、当前中心地址判断地址是否过期）。 */
export function useLocalRegistrationView(orgId: string | undefined) {
  const local = useLocalStatus();
  const registration = local.data?.registration;
  const devices = useDevices(
    orgId ?? "",
    registration?.registered === true && Boolean(orgId),
  );
  const center = useServerCenterUrl(
    orgId ?? "",
    registration?.registered === true &&
      registration.organization_id === orgId &&
      registration.center_url !== undefined,
  );
  // 电脑列表要比本机状态新才可信：刚注册完时列表缓存还没有这台电脑，不能当成已被删除
  const fresh =
    devices.data !== undefined && devices.dataUpdatedAt >= local.dataUpdatedAt;
  return registrationView(
    local.data,
    orgId ?? "",
    fresh ? devices.data!.map((device) => device.id) : undefined,
    // 接口失败时没有数据，不判定为不一致
    center.data?.center_url,
  );
}
