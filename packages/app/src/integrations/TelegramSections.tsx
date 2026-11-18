import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  telegramApi,
  type IntegrationDetail,
  type TelegramBot,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Button, Callout } from "@openfox/ui";
import { keys, useApi } from "../context";
import { useOrgRole } from "../agents/queries";
import { SettingsCard, SettingsSection } from "../spaces/settings/parts";
import {
  TelegramConnectDialog,
  TelegramDisconnectDialog,
  useTelegramAction,
} from "./TelegramDialogs";
import { TelegramPersonalSection } from "./TelegramPersonal";
import {
  TelegramGroupsDialog,
  TelegramDeliveriesDialog,
} from "./TelegramGroups";
import { telegramError, telegramReason, telegramState } from "./telegram-model";

export function TelegramSections({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const t = useT();
  return (
    <>
      <Callout tone="info">{t("integrations.telegram.scope")}</Callout>
      {!detail.provider.configured && (
        <Callout tone="warning">
          {t("integrations.telegram.configured")}
        </Callout>
      )}
      <TelegramManager
        key={orgId}
        orgId={orgId}
        configured={detail.provider.configured}
      />
      <TelegramPersonalSection key={`personal:${orgId}`} orgId={orgId} />
    </>
  );
}
function TelegramManager({
  orgId,
  configured,
}: {
  orgId: string;
  configured: boolean;
}) {
  const api = useApi(),
    t = useT(),
    cache = useQueryClient(),
    role = useOrgRole(orgId),
    [adding, setAdding] = useState(false);
  const bots = useQuery({
    queryKey: ["telegram", orgId, "bots"],
    queryFn: ({ signal }) => telegramApi(api.http).bots(orgId, signal),
    refetchInterval: 5000,
  });
  const refresh = () => {
    void bots.refetch();
    void cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
    void cache.invalidateQueries({ queryKey: ["telegram", orgId, "targets"] });
  };
  return (
    <SettingsSection title={t("integrations.telegram.bots")}>
      <div className="flex gap-2">
        <Button
          variant="outline"
          disabled={!configured || !role || role === "GUEST"}
          onClick={() => setAdding(true)}
        >
          {t("integrations.telegram.create")}
        </Button>
        <Button variant="outline" onClick={refresh}>
          {t("integrations.telegram.refresh")}
        </Button>
      </div>
      {bots.isError && <p role="alert">{telegramError(t, bots.error)}</p>}
      {bots.isPending && <p>{t("common.loading")}</p>}
      {bots.data?.bots.length === 0 && (
        <p>{t("integrations.telegram.noBots")}</p>
      )}
      {bots.data?.bots.map((bot) => (
        <BotRow
          key={`${bot.id}:${bot.version}`}
          orgId={orgId}
          bot={bot}
          onSaved={refresh}
        />
      ))}
      {adding && (
        <TelegramConnectDialog
          orgId={orgId}
          bots={bots.data?.bots ?? []}
          onClose={() => setAdding(false)}
          onSaved={refresh}
        />
      )}
    </SettingsSection>
  );
}
function BotRow({
  orgId,
  bot,
  onSaved,
}: {
  orgId: string;
  bot: TelegramBot;
  onSaved: () => void;
}) {
  const t = useT(),
    api = useApi(),
    action = useTelegramAction();
  const [edit, setEdit] = useState<{
      bot: TelegramBot;
      recover: boolean;
    } | null>(null),
    [disconnect, setDisconnect] = useState(false),
    [groups, setGroups] = useState(false),
    [deliveries, setDeliveries] = useState(false);
  const openEdit = (recover: boolean) =>
    action.run(async (signal) => {
      const current = await telegramApi(api.http).bot(orgId, bot.id, signal);
      if (!signal.aborted) setEdit({ bot: current, recover });
    });
  return (
    <SettingsCard>
      <div className="flex flex-col gap-3 p-4">
        <div className="flex items-center gap-2">
          <h3 className="font-medium">
            {bot.name} · @{bot.username}
          </h3>
          <Badge
            small
            variant="status"
            tone={bot.state === "ACTIVE" ? "success" : "muted"}
          >
            {telegramState(t, bot.state)}
          </Badge>
        </div>
        <p>
          {bot.space?.name ?? bot.agent.name} · <code>{bot.remote_bot_id}</code>
        </p>
        <p>{telegramState(t, bot.control_status)}</p>
        <p>
          {t("integrations.telegram.ignoredTopics", {
            count: bot.unsupported_topic_count,
          })}
        </p>
        {bot.configured_at && (
          <p>
            {t("integrations.telegram.configuredAt", {
              time: new Date(bot.configured_at).toLocaleString(),
            })}
          </p>
        )}
        <p>
          {bot.first_update_at
            ? t("integrations.telegram.firstUpdate", {
                time: new Date(bot.first_update_at).toLocaleString(),
              })
            : t("integrations.telegram.noUpdate")}
        </p>
        {action.error && <p role="alert">{action.error}</p>}
        {bot.last_error && (
          <Callout tone="warning">{telegramReason(t, bot.last_error)}</Callout>
        )}
        {bot.can_manage && (
          <div className="flex flex-wrap gap-2">
            <Button
              size="28"
              variant="outline"
              disabled={
                action.busy ||
                bot.control_status === "SENDING" ||
                bot.control_status === "UNKNOWN"
              }
              onClick={() => void openEdit(false)}
            >
              {t("integrations.telegram.rotate")}
            </Button>
            {(bot.control_status === "UNKNOWN" ||
              bot.control_status === "SENDING") && (
              <Button
                size="28"
                variant="outline"
                disabled={action.busy}
                onClick={() => void openEdit(true)}
              >
                {t("integrations.telegram.recover")}
              </Button>
            )}
            {bot.control_status === "UNKNOWN" &&
              bot.state !== "DISCONNECTED" && (
                <Button
                  size="28"
                  variant="outline"
                  loading={action.busy}
                  onClick={() =>
                    void action.run(async (signal) => {
                      await telegramApi(api.http).reconcile(
                        orgId,
                        bot.id,
                        { version: bot.version },
                        signal,
                      );
                      if (!signal.aborted) onSaved();
                    })
                  }
                >
                  {t("integrations.telegram.reconcile")}
                </Button>
              )}
            {bot.state !== "DISCONNECTED" && (
              <Button
                size="28"
                variant="outline"
                onClick={() => setDisconnect(true)}
              >
                {t("integrations.telegram.disconnect")}
              </Button>
            )}
            <Button size="28" variant="outline" onClick={() => setGroups(true)}>
              {t("integrations.telegram.groups")}
            </Button>
            <Button
              size="28"
              variant="outline"
              onClick={() => setDeliveries(true)}
            >
              {t("integrations.telegram.diagnostics")}
            </Button>
          </div>
        )}
        {edit && (
          <TelegramConnectDialog
            orgId={orgId}
            bots={[]}
            bot={edit.bot}
            recover={edit.recover}
            onClose={() => setEdit(null)}
            onSaved={onSaved}
          />
        )}
        {disconnect && (
          <TelegramDisconnectDialog
            orgId={orgId}
            bot={bot}
            onClose={() => setDisconnect(false)}
            onSaved={onSaved}
          />
        )}
        {groups && (
          <TelegramGroupsDialog
            orgId={orgId}
            bot={bot}
            onClose={() => setGroups(false)}
          />
        )}
        {deliveries && (
          <TelegramDeliveriesDialog
            orgId={orgId}
            bot={bot}
            onClose={() => setDeliveries(false)}
          />
        )}
      </div>
    </SettingsCard>
  );
}
