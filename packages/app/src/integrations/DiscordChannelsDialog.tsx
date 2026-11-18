import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { DiscordBot } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Callout,
  Checkbox,
  Dialog,
  Field,
  Select,
} from "@openfox/ui";
import { useApi } from "../context";
import {
  discordError,
  discordKey,
  useDiscordRefresh,
  useDiscordWrite,
} from "./discord-hooks";
export function DiscordChannelsDialog({
  orgId,
  bot,
  onClose,
}: {
  orgId: string;
  bot: DiscordBot;
  onClose: () => void;
}) {
  const api = useApi(),
    t = useT(),
    refresh = useDiscordRefresh(orgId),
    write = useDiscordWrite(refresh);
  const [after, setAfter] = useState<string>(),
    [guild, setGuild] = useState<string | null>(null),
    [channel, setChannel] = useState<string | null>(null),
    [publicReply, setPublicReply] = useState(false),
    [history, setHistory] = useState(false);
  const list = useQuery({
    queryKey: [...discordKey(orgId), "channels", bot.id],
    queryFn: ({ signal }) => api.discord.channels(orgId, bot.id, signal),
  });
  const identities = useQuery({
    queryKey: [...discordKey(orgId), "personal"],
    queryFn: ({ signal }) => api.discord.personal(orgId, signal),
  });
  const bound =
    identities.data?.some((i) => i.bot_id === bot.id && i.active) ?? false;
  const guilds = useQuery({
    queryKey: [...discordKey(orgId), "guilds", bot.id, after],
    queryFn: ({ signal }) => api.discord.guilds(orgId, bot.id, after, signal),
    enabled: bound,
    retry: false,
  });
  const channels = useQuery({
    queryKey: [...discordKey(orgId), "remote-channels", bot.id, guild],
    queryFn: ({ signal }) =>
      api.discord.remoteChannels(orgId, bot.id, guild!, signal),
    enabled: !!guild && bound,
    retry: false,
  });
  const failure =
    write.error ??
    list.error ??
    identities.error ??
    guilds.error ??
    channels.error;
  return (
    <Dialog
      open
      title={t("integrations.discord.channels")}
      onClose={onClose}
      width={680}
    >
      <div className="flex flex-col gap-4">
        <p>{t("integrations.discord.channelHelp")}</p>
        {list.isPending ? (
          <p>{t("common.loading")}</p>
        ) : !list.data?.length ? (
          <p>{t("integrations.discord.noChannels")}</p>
        ) : (
          list.data.map((c) => (
            <article
              key={c.id}
              className="flex flex-wrap items-center justify-between gap-3 rounded border border-line-default p-3"
            >
              <div>
                <p>
                  {c.channel_name} · {c.channel_id}
                </p>
                <Badge
                  variant="status"
                  tone={c.enabled && !c.paused ? "success" : "muted"}
                >
                  {t(
                    c.enabled
                      ? c.paused
                        ? "integrations.discord.paused"
                        : "integrations.discord.enabled"
                      : "integrations.discord.disabled",
                  )}
                </Badge>
              </div>
              {c.enabled && (
                <Button
                  variant="outline"
                  disabled={write.busy || write.blocked}
                  onClick={() =>
                    void write.run(
                      (signal) =>
                        api.discord.disableChannel(
                          orgId,
                          bot.id,
                          c.id,
                          c.version,
                          signal,
                        ),
                      () => {},
                    )
                  }
                >
                  {t("integrations.discord.disable")}
                </Button>
              )}
            </article>
          ))
        )}
        {!bound ? (
          <Callout tone="info">{t("integrations.discord.bindFirst")}</Callout>
        ) : (
          <>
            <Field label={t("integrations.discord.guild")}>
              <Select
                value={guild}
                onChange={(v) => {
                  setGuild(v);
                  setChannel(null);
                  setPublicReply(false);
                  setHistory(false);
                }}
                options={(guilds.data?.guilds ?? []).map((g) => ({
                  value: g.id,
                  label: `${g.name} · ${g.id}`,
                }))}
              />
            </Field>
            {!!guilds.data?.next && (
              <Button
                variant="outline"
                onClick={() => {
                  setAfter(guilds.data!.next);
                  setGuild(null);
                  setChannel(null);
                }}
              >
                {t("integrations.discord.next")}
              </Button>
            )}
            <Field label={t("integrations.discord.channel")}>
              <Select
                value={channel}
                onChange={(v) => {
                  setChannel(v);
                  setPublicReply(false);
                  setHistory(false);
                }}
                options={(channels.data ?? []).map((c) => ({
                  value: c.id,
                  label: `${c.name} · ${c.id}`,
                }))}
                disabled={!guild}
              />
            </Field>
            <label className="flex items-start gap-2">
              <Checkbox
                checked={publicReply}
                onChange={(e) => setPublicReply(e.target.checked)}
              />
              {t("integrations.discord.publicConsent")}
            </label>
            <label className="flex items-start gap-2">
              <Checkbox
                checked={history}
                onChange={(e) => setHistory(e.target.checked)}
              />
              {t("integrations.discord.historyConsent")}
            </label>
            <Button
              disabled={
                !guild ||
                !channel ||
                !publicReply ||
                !history ||
                write.busy ||
                write.blocked
              }
              onClick={() => {
                if (guild && channel)
                  void write.run(
                    (signal) =>
                      api.discord.enableChannel(
                        orgId,
                        bot.id,
                        {
                          guild_id: guild,
                          channel_id: channel,
                          public_reply_consent: true,
                          history_consent: true,
                        },
                        signal,
                      ),
                    () => {
                      setChannel(null);
                      setPublicReply(false);
                      setHistory(false);
                    },
                  );
              }}
            >
              {t("integrations.discord.enable")}
            </Button>
          </>
        )}
        {failure && <Callout tone="danger">{discordError(t, failure)}</Callout>}
        <Button variant="outline" onClick={refresh}>
          {t("integrations.discord.reload")}
        </Button>
      </div>
    </Dialog>
  );
}
export function DiscordDeliveriesDialog({
  orgId,
  botId,
  onClose,
}: {
  orgId: string;
  botId: string;
  onClose: () => void;
}) {
  const api = useApi(),
    t = useT(),
    [after, setAfter] = useState<string>();
  const q = useQuery({
    queryKey: [...discordKey(orgId), "deliveries", botId, after],
    queryFn: ({ signal }) =>
      api.discord.deliveries(orgId, botId, after, signal),
    retry: false,
  });
  return (
    <Dialog
      open
      title={t("integrations.discord.deliveries")}
      onClose={onClose}
      width={680}
    >
      <div className="flex flex-col gap-3">
        <Callout tone="info">{t("integrations.discord.unknownHelp")}</Callout>
        {q.isPending ? (
          <p>{t("common.loading")}</p>
        ) : q.isError ? (
          <Callout tone="danger">{discordError(t, q.error)}</Callout>
        ) : !q.data.items.length ? (
          <p>{t("integrations.discord.emptyDeliveries")}</p>
        ) : (
          q.data.items.map((d) => (
            <article
              className="rounded border border-line-default p-3"
              key={d.id}
            >
              <p>
                {d.state === "UNKNOWN"
                  ? t("integrations.discord.unknown")
                  : d.state}{" "}
                · {d.channel_id}
              </p>
              <p className="text-caption text-fg-muted">
                {d.reply_to_message_id} ·{" "}
                {new Date(d.created_at).toLocaleString()}
              </p>
            </article>
          ))
        )}
        {!!q.data?.next && (
          <Button variant="outline" onClick={() => setAfter(q.data!.next)}>
            {t("integrations.discord.next")}
          </Button>
        )}
        <Button variant="outline" onClick={() => void q.refetch()}>
          {t("integrations.discord.reload")}
        </Button>
      </div>
    </Dialog>
  );
}
