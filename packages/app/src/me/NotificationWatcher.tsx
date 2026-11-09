import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";

import type {
  InboxGroup,
  InboxNotificationKind,
  NotificationChannel,
  NotificationEvent,
} from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";

import { desktopBridge } from "../desktop";
import { useApi } from "../context";
import { useInboxStats } from "../inbox/queries";
import { useNotificationChannels } from "./queries";

// 新通知弹出：收件箱计数刷新（inbox.changed 会失效它）后拉取一次未读，对本页面会话里新到的通知弹出原生横幅。

/** 通知事件的展示顺序（设置页与弹出判断共用）。 */
export const NOTIFICATION_EVENTS: readonly NotificationEvent[] = [
  "assign",
  "status",
  "comment",
  "mention",
  "agent",
  "automation",
];

/** 收件箱通知类型对应的通知事件；status / automation 目前没有对应的收件箱类型。 */
export function eventForKind(
  kind: Exclude<InboxNotificationKind, "DIRECT_MESSAGE">,
): NotificationEvent {
  switch (kind) {
    case "TASK_ASSIGNED":
    case "TRIAGE_NEW":
      return "assign";
    case "TASK_UPDATED":
      return "status";
    case "TASK_REACTION":
    case "TASK_COMMENT":
    case "REPLY":
      return "comment";
    case "MENTION":
      return "mention";
    case "RUN_BLOCKED":
    case "RUN_COMPLETED":
    case "RUN_FAILED":
    case "HANDOFF":
    case "HANDOFF_REVIEW":
      return "agent";
    case "AUTOMATION_RESULT":
      return "automation";
  }
}

/** 浏览器通知权限；不支持 Notification 的环境按被拦截处理。 */
export function browserPermission(): NotificationPermission {
  return typeof Notification === "undefined"
    ? "denied"
    : Notification.permission;
}

/** 当前环境使用的渠道：桌面端用 desktop，其余用 browser。 */
export function alertChannel(
  channels: readonly NotificationChannel[] | undefined,
): NotificationChannel | undefined {
  const name = desktopBridge() ? "desktop" : "browser";
  return channels?.find((item) => item.channel === name);
}

/**
 * 取出本页面会话里第一次见到、且创建于页面打开之后的未读通知。
 * 见过的通知 id 记入 seen（无论是否弹出），之后不会再次作为「新通知」出现。
 */
export function pickNewNotifications(
  groups: readonly InboxGroup[],
  seen: Set<string>,
  openedAt: number,
): InboxGroup[] {
  return groups.filter((group) => {
    if (!group.unread || seen.has(group.notification_id)) return false;
    seen.add(group.notification_id);
    return Date.parse(group.latest_at) > openedAt;
  });
}

export interface AlertEnvironment {
  channel: NotificationChannel | undefined;
  permission: NotificationPermission;
  /** 页面是否在前台（document.hasFocus） */
  focused: boolean;
}

/** 渠道开启、事件类型未关闭、浏览器已授权、页面不在前台时才弹出。 */
export function shouldAlert(group: InboxGroup, env: AlertEnvironment): boolean {
  const { channel, permission, focused } = env;
  if (!channel?.enabled || permission !== "granted" || focused) return false;
  if (group.kind === "DIRECT_MESSAGE") return channel.direct_messages ?? true;
  return !channel.disabled_events.includes(eventForKind(group.kind));
}

/** 挂在组织外壳里，不渲染任何内容。 */
export function NotificationWatcher({ orgId }: { orgId: string }) {
  const t = useT();
  const api = useApi();
  const navigate = useNavigate();
  const stats = useInboxStats(orgId);
  const channels = useNotificationChannels();
  const [openedAt] = useState(() => Date.now());
  const [seen] = useState(() => new Set<string>());
  // 异步回调里读取最新的渠道与翻译，避免为它们重新拉取列表
  const latest = useRef({ channels: channels.data, navigate, t });
  useEffect(() => {
    latest.current = { channels: channels.data, navigate, t };
  });

  const updatedAt = stats.dataUpdatedAt;
  useEffect(() => {
    if (updatedAt === 0) return;
    let cancelled = false;
    void api.inbox
      .list(orgId, { section: "inbox", delivery: "banner", limit: 20 })
      .then((page) => {
        if (cancelled) return;
        const current = latest.current;
        const env: AlertEnvironment = {
          channel: alertChannel(current.channels),
          permission: browserPermission(),
          focused: document.hasFocus(),
        };
        for (const group of pickNewNotifications(page.groups, seen, openedAt))
          if (shouldAlert(group, env))
            showNotification(group, orgId, current.t, current.navigate);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [api, orgId, updatedAt, openedAt, seen]);

  return null;
}

/** 弹出一条原生通知；点击时聚焦窗口并跳到任务（没有任务时跳到收件箱）。 */
function showNotification(
  group: InboxGroup,
  orgId: string,
  t: Translate,
  navigate: (to: string) => void,
): void {
  const title =
    (group.kind === "DIRECT_MESSAGE"
      ? group.actor_name + " · " + t("me.notifications.inbox.groups.human_chat")
      : group.safe_summary) ||
    group.task_title ||
    t("me.notifications.alert.fallbackTitle");
  const body =
    group.task_title && group.task_title !== title
      ? group.task_title
      : undefined;
  const notification = new Notification(title, {
    body,
    tag: group.notification_id,
  });
  notification.onclick = () => {
    window.focus();
    navigate(
      group.direct_chat_id
        ? `/o/${orgId}/chat/direct/${group.direct_chat_id}?message=${group.direct_message_id ?? ""}`
        : group.space_id && group.task_display_id
          ? `/o/${orgId}/spaces/${group.space_id}/tasks?task=${encodeURIComponent(group.task_display_id.toUpperCase())}`
          : `/o/${orgId}/inbox`,
    );
    notification.close();
  };
}
