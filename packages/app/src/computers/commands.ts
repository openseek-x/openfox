import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import type {
  CreateDeviceCommandInput,
  DeviceCommand,
  DeviceCommandKind,
} from "@openfox/api";

import { keys, useApi } from "../context";

/** 远程命令轮询间隔（规格：每 1 秒 GET 一次直到终态或过期） */
export const COMMAND_POLL_MS = 1000;
/** 兜底上限：最长的 storage_cleanup 执行超时 5 分钟，再加领取的 2 分钟 */
export const COMMAND_POLL_LIMIT_MS = 8 * 60 * 1000;

export function isCommandSettled(command: Pick<DeviceCommand, "status">) {
  return (
    command.status === "SUCCEEDED" ||
    command.status === "FAILED" ||
    command.status === "EXPIRED"
  );
}

type Params = CreateDeviceCommandInput["params"];

/**
 * 发起一种远程命令并轮询结果：组件卸载后 react-query 自动停止轮询；
 * 同一命令进行中 running = true（按钮显示加载并禁用）；到达终态时调用一次 onSettled。
 * 读取失败（例如 404：电脑已不可见）也停止轮询，并以 onError 通知。
 */
export function useDeviceCommand(
  orgId: string,
  deviceId: string,
  kind: DeviceCommandKind,
  handlers: {
    onSettled?: (command: DeviceCommand) => void;
    onError?: (error: unknown) => void;
  } = {},
) {
  const api = useApi();
  const queryClient = useQueryClient();
  const [commandId, setCommandId] = useState<string | null>(null);
  const startedAt = useRef(0);
  const settledId = useRef<string | null>(null);
  const handlersRef = useRef(handlers);
  handlersRef.current = handlers;

  const create = useMutation({
    mutationFn: (params: Params) =>
      api.devices.createCommand(orgId, deviceId, {
        kind,
        ...(params ? { params } : {}),
      }),
    onSuccess: (command) => {
      queryClient.setQueryData(
        keys.deviceCommand(orgId, deviceId, command.id),
        command,
      );
      startedAt.current = Date.now();
      settledId.current = null;
      setCommandId(command.id);
    },
    onError: (error) => handlersRef.current.onError?.(error),
  });

  const query = useQuery({
    queryKey: keys.deviceCommand(orgId, deviceId, commandId ?? ""),
    queryFn: () => api.devices.getCommand(orgId, deviceId, commandId!),
    enabled: Boolean(commandId),
    retry: false,
    refetchOnWindowFocus: false,
    refetchInterval: (q) => {
      const data = q.state.data;
      if (q.state.status === "error") return false;
      if (data && isCommandSettled(data)) return false;
      if (Date.now() - startedAt.current > COMMAND_POLL_LIMIT_MS) return false;
      return COMMAND_POLL_MS;
    },
  });

  const command = commandId ? query.data : undefined;
  useEffect(() => {
    if (!command || !isCommandSettled(command)) return;
    if (settledId.current === command.id) return;
    settledId.current = command.id;
    handlersRef.current.onSettled?.(command);
  }, [command]);

  // 超过兜底上限仍未终态：停止等待（轮询已由 refetchInterval 停止）
  const [gaveUp, setGaveUp] = useState<string | null>(null);
  useEffect(() => {
    if (!commandId) return;
    const timer = setTimeout(() => setGaveUp(commandId), COMMAND_POLL_LIMIT_MS);
    return () => clearTimeout(timer);
  }, [commandId]);

  const failed = Boolean(commandId) && query.isError;
  const error = query.error;
  useEffect(() => {
    // 只在进入错误状态时通知一次
    if (failed) handlersRef.current.onError?.(error);
  }, [failed, error]);

  const running =
    create.isPending ||
    (Boolean(commandId) &&
      !failed &&
      gaveUp !== commandId &&
      (!command || !isCommandSettled(command)));

  return {
    start: (params?: Params) => {
      if (running) return;
      create.mutate(params);
    },
    running,
    /** 最近一次命令（终态后保留，用于显示结果） */
    command,
  };
}
