import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { DiscordBot, DiscordIdentity } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Button, Callout, Dialog } from "@openfox/ui";
import { useApi } from "../context";
import { SettingsSection } from "../spaces/settings/parts";
import {
  discordError,
  discordInstallURL,
  discordKey,
  useDiscordRefresh,
  useDiscordWrite,
} from "./discord-hooks";
import {
  DiscordConnectDialog,
  type DiscordTarget,
} from "./DiscordConnectDialog";
import { DiscordBindingDialog } from "./DiscordBindingDialog";
import {
  DiscordChannelsDialog,
  DiscordDeliveriesDialog,
} from "./DiscordChannelsDialog";
export function DiscordSections({
  orgId,
  personalOnly = false,
  target,
}: {
  orgId: string;
  personalOnly?: boolean;
  target?: DiscordTarget;
}) {
  const api = useApi(),
    t = useT(),
    refresh = useDiscordRefresh(orgId),
    [creating, setCreating] = useState<DiscordTarget>();
  const q = useQuery({
    queryKey: [
      ...discordKey(orgId),
      personalOnly ? "personal-targets" : "bots",
    ],
    queryFn: ({ signal }) =>
      personalOnly
        ? api.discord.personalTargets(orgId, signal)
        : api.discord.bots(orgId, signal),
    refetchInterval: 10_000,
  });
  const bots = (q.data?.bots ?? []).filter(
    (b) =>
      !target ||
      (b.kind === target.kind &&
        (!target.agentId || b.agent.id === target.agentId) &&
        (!target.spaceId || b.space?.id === target.spaceId)),
  );
  return (
    <div className="flex flex-col gap-4">
      <Callout tone="info">{t("integrations.discord.scope")}</Callout>
      {q.isError ? (
        <Callout tone="danger">{discordError(t, q.error)}</Callout>
      ) : q.isPending ? (
        <p>{t("common.loading")}</p>
      ) : (
        <>
          {!personalOnly && (
            <SettingsSection title={t("integrations.discord.title")}>
              <div className="flex flex-wrap gap-2">
                {(target
                  ? [target]
                  : ([
                      { kind: "AGENT" },
                      { kind: "TASK_ASSISTANT" },
                    ] as DiscordTarget[])
                ).map((v) => (
                  <Button
                    key={v.kind}
                    variant="outline"
                    onClick={() => setCreating(v)}
                  >
                    {t(
                      v.kind === "AGENT"
                        ? "integrations.discord.createAgent"
                        : "integrations.discord.createAssistant",
                    )}
                  </Button>
                ))}
              </div>
              {!bots.length && <p>{t("integrations.discord.empty")}</p>}
              {bots.map((b) => (
                <DiscordBotCard
                  key={`${b.id}:${b.version}`}
                  orgId={orgId}
                  bot={b}
                  refresh={refresh}
                />
              ))}
            </SettingsSection>
          )}
          <DiscordPersonal orgId={orgId} bots={bots} refresh={refresh} />
        </>
      )}
      <Button variant="outline" onClick={refresh}>
        {t("integrations.discord.reload")}
      </Button>
      {creating && (
        <DiscordConnectDialog
          orgId={orgId}
          target={creating}
          refresh={refresh}
          onClose={() => setCreating(undefined)}
        />
      )}
    </div>
  );
}
function DiscordBotCard({
  orgId,
  bot,
  refresh,
}: {
  orgId: string;
  bot: DiscordBot;
  refresh: () => void;
}) {
  const t = useT(),
    [dialog, setDialog] = useState<
      "rotate" | "disconnect" | "channels" | "deliveries"
    >(),
    install = discordInstallURL(bot.installation_url, bot.application_id);
  const api = useApi();
  const details = useQuery({
    queryKey: [...discordKey(orgId), "bot", bot.id],
    enabled: dialog === "rotate",
    queryFn: ({ signal }) => api.discord.bot(orgId, bot.id, signal),
    retry: false,
  });
  return (
    <article
      aria-label={bot.name || bot.username}
      className="my-2 flex flex-col gap-3 rounded-xl border border-line-default bg-surface-card p-4"
    >
      <div className="flex flex-wrap justify-between gap-2">
        <h3 className="font-medium">
          {bot.name || bot.username} · {bot.space?.name ?? bot.agent.name}
        </h3>
        <Badge
          variant="status"
          tone={bot.state === "ACTIVE" ? "success" : "muted"}
        >
          {t(
            bot.state === "ACTIVE"
              ? "integrations.discord.active"
              : bot.state === "DISCONNECTED"
                ? "integrations.discord.disconnected"
                : "integrations.discord.attention",
          )}
        </Badge>
      </div>
      <p>
        {t("integrations.discord.botID")}: {bot.remote_bot_id} ·{" "}
        {t("integrations.discord.application")}: {bot.application_id}
      </p>
      <p>
        {t(
          bot.state === "DISCONNECTED"
            ? "integrations.discord.disconnected"
            : bot.gateway_state === "CONNECTED"
              ? "integrations.discord.online"
              : "integrations.discord.pending",
        )}
      </p>
      {bot.gap_count > 0 && (
        <Callout tone="warning">
          {t("integrations.discord.gap", { count: bot.gap_count })}
        </Callout>
      )}
      {bot.kind === "TASK_ASSISTANT" && !bot.message_content_enabled && (
        <Callout tone="warning">
          {t("integrations.discord.contentMissing")}
        </Callout>
      )}
      {bot.state === "ACTIVE" && install && (
        <>
          <p>{t("integrations.discord.installHelp")}</p>
          <a
            href={install}
            target="_blank"
            rel="noopener noreferrer"
            className="underline"
          >
            {t("integrations.discord.install")}
          </a>
        </>
      )}
      {bot.can_manage && (
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" onClick={() => setDialog("rotate")}>
            {t(
              bot.state === "DISCONNECTED"
                ? "integrations.discord.reconnect"
                : "integrations.discord.rotate",
            )}
          </Button>
          {bot.state !== "DISCONNECTED" && (
            <>
              <Button variant="outline" onClick={() => setDialog("disconnect")}>
                {t("integrations.discord.disconnect")}
              </Button>
            </>
          )}
          {bot.state === "ACTIVE" && (
            <Button variant="outline" onClick={() => setDialog("channels")}>
              {t("integrations.discord.channels")}
            </Button>
          )}
          <Button variant="outline" onClick={() => setDialog("deliveries")}>
            {t("integrations.discord.deliveries")}
          </Button>
        </div>
      )}
      {dialog === "rotate" && details.isError && (
        <Callout tone="danger">{discordError(t, details.error)}</Callout>
      )}
      {dialog === "rotate" && details.data && (
        <DiscordConnectDialog
          orgId={orgId}
          target={{ kind: bot.kind }}
          current={details.data}
          refresh={refresh}
          onClose={() => setDialog(undefined)}
        />
      )}
      {dialog === "disconnect" && (
        <DiscordStopDialog
          orgId={orgId}
          bot={bot}
          refresh={refresh}
          onClose={() => setDialog(undefined)}
        />
      )}
      {dialog === "channels" && (
        <DiscordChannelsDialog
          orgId={orgId}
          bot={bot}
          onClose={() => setDialog(undefined)}
        />
      )}
      {dialog === "deliveries" && (
        <DiscordDeliveriesDialog
          orgId={orgId}
          botId={bot.id}
          onClose={() => setDialog(undefined)}
        />
      )}
    </article>
  );
}
function DiscordPersonal({
  orgId,
  bots,
  refresh,
}: {
  orgId: string;
  bots: DiscordBot[];
  refresh: () => void;
}) {
  const api = useApi(),
    t = useT(),
    [binding, setBinding] = useState<string>(),
    [removing, setRemoving] = useState<DiscordIdentity>();
  const q = useQuery({
    queryKey: [...discordKey(orgId), "personal"],
    queryFn: ({ signal }) => api.discord.personal(orgId, signal),
  });
  return (
    <SettingsSection title={t("integrations.discord.personal")}>
      {q.isPending ? (
        <p>{t("common.loading")}</p>
      ) : q.isError ? (
        <Callout tone="danger">{discordError(t, q.error)}</Callout>
      ) : (
        <>
          {!bots.length && <p>{t("integrations.discord.empty")}</p>}
          {bots.map((b) => {
            const identity = q.data.find((i) => i.bot_id === b.id && i.active);
            return (
              <article
                key={b.id}
                className="flex flex-wrap items-center justify-between gap-3 rounded border border-line-default p-3"
              >
                <div>
                  <p>
                    {b.name || b.username} · {b.remote_bot_id}
                  </p>
                  {identity && (
                    <p>
                      {t("integrations.discord.bound")}: {identity.display_name}{" "}
                      · {identity.remote_user_id}
                    </p>
                  )}
                </div>
                {identity ? (
                  <Button
                    variant="outline"
                    onClick={() => setRemoving(identity)}
                  >
                    {t("integrations.discord.unbind")}
                  </Button>
                ) : (
                  <Button
                    variant="outline"
                    disabled={b.state !== "ACTIVE"}
                    onClick={() => setBinding(b.id)}
                  >
                    {t("integrations.discord.bind")}
                  </Button>
                )}
              </article>
            );
          })}
        </>
      )}
      {binding && (
        <DiscordBindingDialog
          orgId={orgId}
          botId={binding}
          refresh={refresh}
          onClose={() => setBinding(undefined)}
        />
      )}
      {removing && (
        <DiscordStopDialog
          orgId={orgId}
          identity={removing}
          refresh={refresh}
          onClose={() => setRemoving(undefined)}
        />
      )}
    </SettingsSection>
  );
}
function DiscordStopDialog({
  orgId,
  bot,
  identity,
  refresh,
  onClose,
}: {
  orgId: string;
  bot?: DiscordBot;
  identity?: DiscordIdentity;
  refresh: () => void;
  onClose: () => void;
}) {
  const api = useApi(),
    t = useT(),
    write = useDiscordWrite(refresh);
  return (
    <Dialog
      open
      title={t(
        bot ? "integrations.discord.disconnect" : "integrations.discord.unbind",
      )}
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="danger"
            disabled={write.busy || write.blocked}
            onClick={() =>
              void write.run(
                (signal) =>
                  bot
                    ? api.discord.disconnect(orgId, bot.id, bot.version, signal)
                    : api.discord.unbind(
                        orgId,
                        identity!.id,
                        identity!.version,
                        signal,
                      ),
                onClose,
              )
            }
          >
            {t(
              bot
                ? "integrations.discord.disconnect"
                : "integrations.discord.unbind",
            )}
          </Button>
        </>
      }
    >
      <p>
        {t(
          bot
            ? "integrations.discord.disconnectHelp"
            : "integrations.discord.unbindHelp",
        )}
      </p>
      {!!write.error && (
        <Callout tone="danger">{discordError(t, write.error)}</Callout>
      )}
    </Dialog>
  );
}
