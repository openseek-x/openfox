import { useQuery, useQueryClient } from "@tanstack/react-query";

import { isApiError, type Device, type DeviceDetail } from "@openfox/api";

import { keys, useApi } from "../context";
import { desktopBridge, desktopLocalStatusKey } from "../desktop";

/** 在线状态靠心跳判定、没有实时事件，列表每 30 秒刷新一次 */
export const DEVICE_POLL_MS = 30_000;

/** 电脑列表：设置页、侧栏弹层、首登向导共用同一个查询键。 */
export function useDevices(orgId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: keys.devices(orgId),
    queryFn: () => api.devices.list(orgId),
    refetchInterval: DEVICE_POLL_MS,
    enabled,
  });
}

export function useDevice(orgId: string, deviceId: string | null) {
  const api = useApi();
  return useQuery({
    queryKey: keys.device(orgId, deviceId ?? ""),
    queryFn: () => api.devices.get(orgId, deviceId!),
    enabled: Boolean(deviceId),
    refetchInterval: DEVICE_POLL_MS,
  });
}

/** 服务端当前中心地址（与安装令牌里的一致）；失败不重试，调用方按「没有地址」处理 */
export function useServerCenterUrl(orgId: string, enabled = true) {
  const api = useApi();
  return useQuery({
    queryKey: ["org", orgId, "device-center"],
    queryFn: () => api.devices.center(orgId),
    enabled: enabled && Boolean(orgId),
    retry: false,
  });
}

/** 当前用户（排序「自己的在前」、所有者判定用） */
export function useMe() {
  const api = useApi();
  return useQuery({ queryKey: keys.me, queryFn: api.me.get });
}

export const LOCAL_STATUS_POLL_MS = 5000;

/** 与服务器的连接还没稳定：正在连接 / 重连中 / 已断开 */
export const isRemoteUnsettled = (remote: string | undefined) =>
  remote === "CONNECTING" ||
  remote === "RECONNECTING" ||
  remote === "DISCONNECTED";

/** 本机守护进程与 Runtime 状态；仅桌面端可用。 */
export function useLocalStatus() {
  const bridge = desktopBridge();
  return useQuery({
    queryKey: desktopLocalStatusKey,
    queryFn: () => bridge!.getLocalStatus(),
    enabled: Boolean(bridge),
    // 本机服务连不上服务器时每 5 秒刷新，恢复后卡片能及时变回可用
    refetchInterval: (query) =>
      isRemoteUnsettled(query.state.data?.daemon.remote_state)
        ? LOCAL_STATUS_POLL_MS
        : false,
  });
}

/** 写入后把返回的电脑合并进列表和详情缓存，避免等刷新。 */
export function useApplyDevice(orgId: string) {
  const queryClient = useQueryClient();
  return (device: Device) => {
    queryClient.setQueryData<Device[]>(keys.devices(orgId), (list) =>
      list?.map((item) => (item.id === device.id ? device : item)),
    );
    queryClient.setQueryData<DeviceDetail>(
      keys.device(orgId, device.id),
      (detail) => (detail ? { ...detail, ...device } : detail),
    );
  };
}

export const isNotFound = (error: unknown) =>
  isApiError(error) && error.status === 404;
