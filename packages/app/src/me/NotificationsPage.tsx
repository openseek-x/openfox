import { useState, type ReactNode } from "react";
import { useNavigate, useParams } from "react-router-dom";

import type {
  NotificationChannel,
  NotificationChannelName,
  NotificationEvent,
} from "@openfox/api";
import { useT, type MessageKey, type Translate } from "@openfox/i18n";
import {
  Badge,
  Button,
  Icon,
  Switch,
  useToast,
  cn,
  type IconName,
} from "@openfox/ui";

import {
  SettingRow,
  SettingsCard,
  SettingsColumn,
  SettingsSection,
} from "../spaces/settings/parts";
import { useFeishuApp, useWeComPersonal } from "../integrations/queries";
import {
  useNotificationChannels,
  useNotificationSaving,
  useResetChannel,
  useSaveChannel,
} from "./queries";
import {
  InboxNotificationSettings,
  useInboxPreferences,
} from "./InboxNotificationSettings";
import { NOTIFICATION_EVENTS, browserPermission } from "./NotificationWatcher";

// 通知：渠道列表与渠道详情两态，地址不变，由组件状态切换。

/** 列表里的行：桌面端、浏览器和邮件可设置；Slack 等未连接；移动端即将推出。 */
type RowKey =
  | "inbox"
  | "desktop"
  | "browser"
  | "slack"
  | "feishu"
  | "wecom"
  | "lark"
  | "dingtalk"
  | "mobile"
  | "email";

const ROWS: Array<{ key: RowKey; icon: IconName }> = [
  { key: "desktop", icon: "monitor" },
  { key: "browser", icon: "globe" },
  { key: "slack", icon: "brand-slack" },
  { key: "feishu", icon: "brand-feishu" },
  { key: "wecom", icon: "message-circle" },
  { key: "lark", icon: "brand-lark" },
  { key: "dingtalk", icon: "brand-dingtalk" },
  { key: "inbox", icon: "inbox" },
  { key: "mobile", icon: "smartphone" },
  { key: "email", icon: "mail" },
];

const CHANNEL_ROWS: readonly RowKey[] = ["desktop", "browser", "email"];
const INTEGRATION_ROWS: readonly RowKey[] = [
  "slack",
  "feishu",
  "lark",
  "dingtalk",
];

const EVENT_TEXT: Record<
  NotificationEvent,
  { title: MessageKey; help: MessageKey }
> = {
  assign: {
    title: "me.notifications.events.assign",
    help: "me.notifications.events.assignHelp",
  },
  status: {
    title: "me.notifications.events.status",
    help: "me.notifications.events.statusHelp",
  },
  comment: {
    title: "me.notifications.events.comment",
    help: "me.notifications.events.commentHelp",
  },
  mention: {
    title: "me.notifications.events.mention",
    help: "me.notifications.events.mentionHelp",
  },
  agent: {
    title: "me.notifications.events.agent",
    help: "me.notifications.events.agentHelp",
  },
  automation: {
    title: "me.notifications.events.automation",
    help: "me.notifications.events.automationHelp",
  },
};

const toggleTitle: Record<NotificationChannelName, MessageKey> = {
  desktop: "me.notifications.detail.desktopTitle",
  browser: "me.notifications.detail.browserTitle",
  feishu: "me.notifications.detail.feishuTitle",
  wecom: "me.notifications.detail.wecomTitle",
  email: "me.notifications.detail.emailTitle",
};
const toggleHelp: Record<NotificationChannelName, MessageKey> = {
  desktop: "me.notifications.detail.desktopHelp",
  browser: "me.notifications.detail.browserHelp",
  feishu: "me.notifications.detail.feishuHelp",
  wecom: "me.notifications.detail.wecomHelp",
  email: "me.notifications.detail.emailHelp",
};

const channelName = (t: Translate, key: RowKey): string =>
  t(`me.notifications.channels.${key}` as MessageKey);

/** 渠道列表里的状态文案。 */
function channelStatus(t: Translate, channel: NotificationChannel): string {
  if (channel.available === false)
    return t("me.notifications.status.unavailable");
  if (!channel.enabled) return t("me.notifications.status.off");
  const enabled = NOTIFICATION_EVENTS.filter(
    (event) => !channel.disabled_events.includes(event),
  ).map((event) => t(EVENT_TEXT[event].title));
  if (
    channel.direct_messages ??
    !["feishu", "wecom", "email"].includes(channel.channel)
  )
    enabled.splice(4, 0, t("me.notifications.inbox.groups.human_chat"));
  if (enabled.length === NOTIFICATION_EVENTS.length + 1)
    return t("me.notifications.status.allOn");
  if (!enabled.length) return t("me.notifications.status.allOff");
  const names = enabled
    .slice(0, 2)
    .join(t("me.notifications.status.separator"));
  return enabled.length <= 2
    ? t("me.notifications.status.named", { names })
    : t("me.notifications.status.some", { names, count: enabled.length - 2 });
}

export function NotificationsPage() {
  const { orgId = "" } = useParams();
  const channels = useNotificationChannels();
  const feishu = useFeishuApp(orgId);
  const wecom = useWeComPersonal(orgId);
  const [detail, setDetail] = useState<RowKey | null>(null);
  if (!channels.data) return null;
  const list = channels.data;
  // 本人在当前组织绑定了飞书账号，飞书行才是真实渠道
  const feishuPersonal = feishu.data?.personal ?? null;
  const feishuChannel = list.find((item) => item.channel === "feishu");
  const wecomTarget = wecom.data?.bindings.find(
    (binding) => binding.notification_target,
  );
  const wecomChannel = list.find((item) => item.channel === "wecom");
  return (
    <SettingsColumn>
      {detail === null ? (
        <ChannelList
          channels={list}
          feishuBound={feishuPersonal !== null}
          wecomTarget={wecomTarget !== undefined}
          wecomHasBindings={Boolean(wecom.data?.bindings.length)}
          onOpen={setDetail}
        />
      ) : detail === "inbox" ? (
        <InboxNotificationSettings onBack={() => setDetail(null)} />
      ) : detail === "feishu" && feishuPersonal && feishuChannel ? (
        <ChannelDetail
          channelKey="feishu"
          channel={feishuChannel}
          boundName={feishuPersonal.display_name}
          onBack={() => setDetail(null)}
        />
      ) : detail === "wecom" && wecomTarget && wecomChannel ? (
        <ChannelDetail
          channelKey="wecom"
          channel={wecomChannel}
          boundName={wecomTarget.bot_name}
          onBack={() => setDetail(null)}
        />
      ) : CHANNEL_ROWS.includes(detail) ? (
        <ChannelDetail
          channelKey={detail as NotificationChannelName}
          channel={list.find((item) => item.channel === detail)}
          onBack={() => setDetail(null)}
        />
      ) : (
        <IntegrationDetail
          rowKey={detail}
          orgHasApp={feishu.data?.app != null}
          wecomHasBindings={Boolean(wecom.data?.bindings.length)}
          onBack={() => setDetail(null)}
        />
      )}
    </SettingsColumn>
  );
}

function ChannelList({
  channels,
  feishuBound,
  wecomTarget,
  wecomHasBindings,
  onOpen,
}: {
  channels: NotificationChannel[];
  feishuBound: boolean;
  wecomTarget: boolean;
  wecomHasBindings: boolean;
  onOpen: (key: RowKey) => void;
}) {
  const t = useT();
  const prefs = useInboxPreferences();
  const groups = prefs.data?.filter((e) => !e.group) ?? [];
  const active = groups.filter(
    (g) =>
      g.level !== "off" &&
      prefs.data?.some(
        (event) => event.group === g.key && event.level !== "off",
      ),
  );
  return (
    <SettingsSection
      title={t("me.notifications.title")}
      description={t("me.notifications.description")}
    >
      <SettingsCard>
        {ROWS.map(({ key, icon }) => {
          if (key === "inbox") {
            const status = !prefs.data
              ? t(
                  prefs.isError
                    ? "me.notifications.detail.saveFailed"
                    : "common.loading",
                )
              : active.length === groups.length
                ? t("me.notifications.status.allOn")
                : active.length === 0
                  ? t("me.notifications.status.allOff")
                  : t(
                      active.length > 2
                        ? "me.notifications.status.some"
                        : "me.notifications.status.named",
                      {
                        names: active
                          .slice(0, 2)
                          .map((g) =>
                            t(
                              `me.notifications.inbox.groups.${g.key}` as MessageKey,
                            ),
                          )
                          .join(t("me.notifications.status.separator")),
                        count: active.length - 2,
                      },
                    );
            return (
              <ListRow
                key={key}
                icon={icon}
                name={t("me.notifications.inbox.title")}
                status={status}
                on={active.length > 0}
                onClick={() => onOpen(key)}
              />
            );
          }
          const name = channelName(t, key);
          if (CHANNEL_ROWS.includes(key)) {
            const channel = channels.find((item) => item.channel === key);
            if (!channel) return null;
            return (
              <ListRow
                key={key}
                icon={icon}
                name={name}
                status={channelStatus(t, channel)}
                on={channel.enabled}
                onClick={() => onOpen(key)}
              />
            );
          }
          if (key === "feishu") {
            const channel = channels.find((item) => item.channel === "feishu");
            const bound = feishuBound && channel !== undefined;
            return (
              <ListRow
                key={key}
                icon={icon}
                name={name}
                status={
                  bound
                    ? channelStatus(t, channel)
                    : t("me.notifications.status.unbound")
                }
                on={bound && channel.enabled}
                onClick={() => onOpen(key)}
              />
            );
          }
          if (key === "wecom") {
            const channel = channels.find((item) => item.channel === "wecom");
            const bound = wecomTarget && channel !== undefined;
            return (
              <ListRow
                key={key}
                icon={icon}
                name={name}
                status={
                  bound
                    ? channelStatus(t, channel)
                    : t(
                        wecomHasBindings
                          ? "me.notifications.status.chooseBot"
                          : "me.notifications.status.unbound",
                      )
                }
                on={bound && channel.enabled}
                onClick={() => onOpen(key)}
              />
            );
          }
          if (INTEGRATION_ROWS.includes(key))
            return (
              <ListRow
                key={key}
                icon={icon}
                name={name}
                status={t(
                  `me.notifications.status.connect.${key}` as MessageKey,
                )}
                on={false}
                onClick={() => onOpen(key)}
              />
            );
          // 移动端：即将推出，不可点
          return (
            <div
              key={key}
              className="flex min-h-[68px] items-center gap-3 p-4 opacity-60"
            >
              <IconBox icon={icon} />
              <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="text-body font-medium text-fg-default">
                  {name}
                </span>
                <span className="text-caption text-fg-muted">
                  {t("me.notifications.status.soon")}
                </span>
              </span>
            </div>
          );
        })}
      </SettingsCard>
    </SettingsSection>
  );
}

function IconBox({ icon }: { icon: IconName }) {
  return (
    <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-surface-muted text-fg-secondary">
      <Icon name={icon} />
    </span>
  );
}

function ListRow({
  icon,
  name,
  status,
  on,
  onClick,
}: {
  icon: IconName;
  name: string;
  status: string;
  on: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex min-h-[68px] w-full items-center gap-3 p-4 text-left hover:bg-surface-hover focus-visible:shadow-focus focus-visible:outline-none"
    >
      <IconBox icon={icon} />
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="text-body font-medium text-fg-default">{name}</span>
        <span className="flex items-center gap-1.5 text-caption text-fg-muted">
          <span
            aria-hidden
            className={cn(
              "size-1.5 shrink-0 rounded-full",
              on ? "bg-success" : "bg-fg-subtle",
            )}
          />
          {status}
        </span>
      </span>
      <Icon name="chevron-right" className="shrink-0 text-fg-muted" />
    </button>
  );
}

/** 渠道详情的顶部：返回条 + 标题 +（可选）右侧操作。 */
function DetailHeader({
  title,
  onBack,
  action,
}: {
  title: string;
  onBack: () => void;
  action?: ReactNode;
}) {
  const t = useT();
  return (
    <div className="flex flex-col gap-2">
      <button
        type="button"
        onClick={onBack}
        className="inline-flex h-7 w-fit items-center gap-1 rounded-lg px-1.5 text-body-sm text-fg-secondary hover:bg-surface-hover"
      >
        <Icon name="chevron-left" size={14} />
        <span>{t("me.notifications.back")}</span>
      </button>
      <header className="flex items-center gap-4">
        <h1 className="min-w-0 flex-1 text-[24px] leading-8 font-medium text-fg-default">
          {title}
        </h1>
        {action}
      </header>
    </div>
  );
}

function ChannelDetail({
  channelKey,
  channel,
  boundName,
  onBack,
}: {
  channelKey: NotificationChannelName;
  channel: NotificationChannel | undefined;
  /** 飞书渠道：已绑定的飞书姓名 */
  boundName?: string;
  onBack: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const saving = useNotificationSaving();
  const save = useSaveChannel();
  const reset = useResetChannel();
  if (!channel) return null;

  const title = channelName(t, channelKey);
  const saveFailed = () =>
    toast(t("me.notifications.detail.saveFailed"), { tone: "error" });
  const update = (patch: {
    enabled?: boolean;
    disabled_events?: NotificationEvent[];
    direct_messages?: boolean;
  }) =>
    save.mutate(
      {
        channel: channelKey,
        enabled: patch.enabled ?? channel.enabled,
        direct_messages: patch.direct_messages ?? channel.direct_messages,
        disabled_events: patch.disabled_events ?? channel.disabled_events,
      },
      { onError: saveFailed },
    );

  return (
    <div className="flex flex-col gap-6">
      <DetailHeader
        title={title}
        onBack={onBack}
        action={
          <Button
            variant="ghost"
            size="28"
            className={cn(reset.isPending && "opacity-50")}
            disabled={reset.isPending}
            onClick={() =>
              reset.mutate(channelKey, {
                onSuccess: () => toast(t("me.notifications.detail.resetDone")),
                onError: () =>
                  toast(t("me.notifications.detail.resetFailed"), {
                    tone: "error",
                  }),
              })
            }
          >
            {t("me.notifications.detail.reset")}
          </Button>
        }
      />
      {channelKey === "feishu" && boundName !== undefined && (
        <p className="px-1 text-body-sm text-fg-muted">
          {t("me.notifications.detail.feishuVia", { name: boundName })}
        </p>
      )}
      {channelKey === "wecom" && boundName !== undefined && (
        <p className="px-1 text-body-sm text-fg-muted">
          {t("me.notifications.detail.wecomVia", { name: boundName })}
        </p>
      )}
      {channelKey === "email" && channel.available === false && (
        <p className="px-1 text-body-sm text-fg-muted">
          {t("me.notifications.detail.emailUnavailable")}
        </p>
      )}
      <SettingsCard>
        <SettingRow
          title={t(toggleTitle[channelKey])}
          description={t(toggleHelp[channelKey])}
          control={
            <Switch
              label={t(toggleTitle[channelKey])}
              checked={channel.enabled}
              disabled={
                saving || (channel.available === false && !channel.enabled)
              }
              onCheckedChange={(enabled) => update({ enabled })}
            />
          }
        />
        {channelKey === "browser" && <BrowserPermissionRow />}
      </SettingsCard>
      <EventsCard
        enabled={channel.enabled && channel.available !== false}
        direct={
          channel.direct_messages ??
          !["feishu", "wecom", "email"].includes(channelKey)
        }
        onDirect={(direct_messages) => update({ direct_messages })}
        disabledEvents={channel.disabled_events}
        saving={saving}
        onChange={(disabled_events) => update({ disabled_events })}
      />
    </div>
  );
}

/** 浏览器权限三态：未授权显示「启用」，已授权显示徽标，被拦截显示说明。 */
function BrowserPermissionRow() {
  const t = useT();
  const [permission, setPermission] = useState(browserPermission);
  const enable = async () => {
    if (typeof Notification === "undefined") return;
    setPermission(await Notification.requestPermission());
  };
  if (permission === "granted")
    return (
      <SettingRow
        title={t("me.notifications.detail.permission.title")}
        description={t("me.notifications.detail.permission.enabledHelp")}
        control={
          <Badge tone="success">
            {t("me.notifications.detail.permission.enabled")}
          </Badge>
        }
      />
    );
  if (permission === "denied")
    return (
      <SettingRow
        title={t("me.notifications.detail.permission.title")}
        description={
          <span className="text-danger">
            {t("me.notifications.detail.permission.blocked")}
          </span>
        }
      />
    );
  return (
    <SettingRow
      title={t("me.notifications.detail.permission.title")}
      description={t("me.notifications.detail.permission.help")}
      control={
        <Button variant="outline" size="28" onClick={() => void enable()}>
          {t("me.notifications.detail.permission.enable")}
        </Button>
      }
    />
  );
}

/** 事件类型：总开关关闭时整张卡片禁用。 */
function EventsCard({
  enabled,
  disabledEvents,
  direct = false,
  onDirect,
  saving,
  onChange,
}: {
  enabled: boolean;
  disabledEvents: NotificationEvent[];
  direct?: boolean;
  onDirect?: (enabled: boolean) => void;
  saving: boolean;
  onChange: (disabled: NotificationEvent[]) => void;
}) {
  const t = useT();
  return (
    <SettingsSection
      title={t("me.notifications.events.title")}
      description={t("me.notifications.events.description")}
    >
      <div className={cn(!enabled && "pointer-events-none opacity-50")}>
        <SettingsCard>
          {NOTIFICATION_EVENTS.map((event) => {
            const on = !disabledEvents.includes(event);
            return (
              <SettingRow
                key={event}
                title={t(EVENT_TEXT[event].title)}
                description={t(EVENT_TEXT[event].help)}
                control={
                  <Switch
                    label={t(EVENT_TEXT[event].title)}
                    checked={on}
                    disabled={!enabled || saving}
                    onCheckedChange={(next) =>
                      onChange(
                        next
                          ? disabledEvents.filter((item) => item !== event)
                          : [...disabledEvents, event],
                      )
                    }
                  />
                }
              />
            );
          })}
          <SettingRow
            title={t("me.notifications.inbox.groups.human_chat")}
            description={t(
              "me.notifications.inbox.events.chat_message_received_recipient",
            )}
            control={
              <Switch
                label={t("me.notifications.inbox.groups.human_chat")}
                checked={direct}
                disabled={!enabled || saving || !onDirect}
                onCheckedChange={(value) => onDirect?.(value)}
              />
            }
          />
        </SettingsCard>
      </div>
    </SettingsSection>
  );
}

/** 集成通知渠道的详情与配置入口。 */
function IntegrationDetail({
  rowKey,
  orgHasApp,
  wecomHasBindings,
  onBack,
}: {
  rowKey: RowKey;
  /** 飞书行：组织是否已绑定飞书应用 */
  orgHasApp: boolean;
  wecomHasBindings: boolean;
  onBack: () => void;
}) {
  const t = useT();
  const navigate = useNavigate();
  const { orgId = "" } = useParams();
  const name = channelName(t, rowKey);
  const feishu = rowKey === "feishu";
  const wecom = rowKey === "wecom";
  return (
    <div className="flex flex-col gap-6">
      <DetailHeader title={name} onBack={onBack} />
      <div className="flex flex-col gap-3 rounded-xl border border-line-default bg-surface-card p-4">
        <span className="text-body font-medium text-fg-default">
          {t(
            feishu || wecom
              ? "me.notifications.status.unbound"
              : "me.notifications.integration.title",
            { name },
          )}
        </span>
        <p className="text-body-sm text-fg-muted">
          {t(
            wecom
              ? wecomHasBindings
                ? "me.notifications.integration.wecomSelectDesc"
                : "me.notifications.integration.wecomBindDesc"
              : !feishu
                ? "me.notifications.integration.description"
                : orgHasApp
                  ? "me.notifications.integration.feishuBindDesc"
                  : "me.notifications.integration.feishuNeedAdmin",
          )}
        </p>
        <div>
          <Button
            variant="outline"
            size="28"
            onClick={() =>
              navigate(
                `/o/${orgId}/settings/integrations${feishu ? "/feishu" : wecom ? "/wecom" : ""}`,
              )
            }
          >
            {t(
              wecom
                ? "me.notifications.integration.bindWecom"
                : feishu
                  ? "me.notifications.integration.bindFeishu"
                  : "me.notifications.integration.open",
            )}
          </Button>
        </div>
      </div>
      <SettingsCard>
        <SettingRow
          title={t("me.notifications.integration.toggle", { name })}
          description={t("me.notifications.integration.toggleHelp", { name })}
          control={
            <Switch
              label={t("me.notifications.integration.toggle", { name })}
              checked={false}
              disabled
              onCheckedChange={() => undefined}
            />
          }
        />
      </SettingsCard>
      <EventsCard
        enabled={false}
        disabledEvents={[]}
        saving={false}
        onChange={() => undefined}
      />
    </div>
  );
}
