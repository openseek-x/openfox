import { useState } from "react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import {
  telegramApi,
  type TelegramBot,
  type TelegramGroupDiscovery,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog } from "@openfox/ui";
import { useApi } from "../context";
import { useTelegramAction } from "./TelegramDialogs";
import { telegramError, telegramReason, telegramState } from "./telegram-model";

export function TelegramGroupsDialog({
  orgId,
  bot,
  onClose,
}: {
  orgId: string;
  bot: TelegramBot;
  onClose: () => void;
}) {
  const t = useT(),
    api = useApi(),
    action = useTelegramAction();
  const groups = useInfiniteQuery({
    queryKey: ["telegram", orgId, bot.id, "groups"],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      telegramApi(api.http).groups(orgId, bot.id, pageParam, signal),
    getNextPageParam: (page) =>
      page.length === 50 ? page[page.length - 1]?.id : undefined,
  });
  const discoveries = useQuery({
    queryKey: ["telegram", orgId, bot.id, "discoveries"],
    queryFn: ({ signal }) =>
      telegramApi(api.http).discoveries(orgId, bot.id, signal),
  });
  const [selected, setSelected] = useState<TelegramGroupDiscovery | null>(null),
    [publicConsent, setPublic] = useState(false),
    [background, setBackground] = useState(false);
  const rows = groups.data?.pages.flat() ?? [];
  const refresh = () => {
    void groups.refetch();
    void discoveries.refetch();
    setSelected(null);
    setPublic(false);
    setBackground(false);
  };
  return (
    <Dialog
      open
      onClose={onClose}
      title={t("integrations.telegram.groups")}
      width={620}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.close")}
          </Button>
          <Button variant="outline" onClick={refresh}>
            {t("integrations.telegram.refresh")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Callout tone="info">
          {t("integrations.telegram.groupHelp", { username: bot.username })}
        </Callout>
        <p>{t("integrations.telegram.groupEpoch")}</p>
        <p>{t("integrations.telegram.privacyHelp")}</p>
        {(groups.isError || discoveries.isError) && (
          <p role="alert">
            {telegramError(t, groups.error ?? discoveries.error)}
          </p>
        )}
        {action.error && <p role="alert">{action.error}</p>}
        {rows.map((row) => (
          <div
            key={row.id}
            className="flex items-center justify-between gap-3 rounded-lg border border-line-default p-3"
          >
            <div>
              <p>
                {row.title} · <code>{row.chat_id}</code>
              </p>
              <p>
                {t(
                  row.enabled
                    ? "integrations.telegram.groupEnabled"
                    : "integrations.telegram.groupDisabled",
                )}
              </p>
              {row.background_enabled && (
                <p>{t("integrations.telegram.background")}</p>
              )}
            </div>
            {row.enabled && (
              <Button
                variant="outline"
                size="28"
                loading={action.busy}
                onClick={() =>
                  void action.run(async (signal) => {
                    await telegramApi(api.http).saveGroup(
                      orgId,
                      bot.id,
                      {
                        epoch_id: row.id,
                        connection_version: bot.version,
                        expected_version: row.version,
                        enabled: false,
                        public_reply_consent: false,
                        background_enabled: false,
                      },
                      signal,
                    );
                    if (!signal.aborted) refresh();
                  })
                }
              >
                {t("integrations.telegram.disableGroup")}
              </Button>
            )}
          </div>
        ))}
        {groups.hasNextPage && (
          <Button
            variant="outline"
            size="28"
            loading={groups.isFetchingNextPage}
            onClick={() => void groups.fetchNextPage()}
          >
            {t("integrations.telegram.loadMore")}
          </Button>
        )}
        {discoveries.data?.length === 0 && (
          <p>{t("integrations.telegram.noDiscoveries")}</p>
        )}
        {discoveries.data?.map((row) => (
          <label key={row.id} className="flex items-start gap-2">
            <input
              type="radio"
              name="telegram-group"
              checked={selected?.id === row.id}
              disabled={
                bot.state !== "ACTIVE" ||
                Date.parse(row.expires_at) <= Date.now()
              }
              onChange={() => {
                setSelected(row);
                setPublic(false);
                setBackground(false);
              }}
            />
            <span>
              {row.title} · <code>{row.chat_id}</code>
            </span>
          </label>
        ))}
        {selected && (
          <>
            <label className="flex items-start gap-2">
              <input
                type="checkbox"
                checked={publicConsent}
                onChange={(e) => setPublic(e.target.checked)}
              />
              {t("integrations.telegram.publicConsent")}
            </label>
            {bot.kind === "TASK_ASSISTANT" && (
              <>
                <label className="flex items-start gap-2">
                  <input
                    type="checkbox"
                    checked={background}
                    onChange={(e) => setBackground(e.target.checked)}
                  />
                  {t("integrations.telegram.background")}
                </label>
                <p className="text-body-sm text-fg-muted">
                  {t("integrations.telegram.backgroundHelp")}
                </p>
              </>
            )}
            <Button
              disabled={
                !publicConsent || Date.parse(selected.expires_at) <= Date.now()
              }
              loading={action.busy}
              onClick={() =>
                void action.run(async (signal) => {
                  await telegramApi(api.http).saveGroup(
                    orgId,
                    bot.id,
                    {
                      discovery_id: selected.id,
                      connection_version: bot.version,
                      expected_version:
                        rows.find(
                          (r) => r.chat_id === selected.chat_id && r.enabled,
                        )?.version ?? 0,
                      enabled: true,
                      public_reply_consent: publicConsent,
                      background_enabled:
                        bot.kind === "TASK_ASSISTANT" && background,
                    },
                    signal,
                  );
                  if (!signal.aborted) refresh();
                })
              }
            >
              {t("integrations.telegram.enableGroup")}
            </Button>
          </>
        )}
      </div>
    </Dialog>
  );
}
export function TelegramDeliveriesDialog({
  orgId,
  bot,
  onClose,
}: {
  orgId: string;
  bot: TelegramBot;
  onClose: () => void;
}) {
  const api = useApi(),
    t = useT();
  const query = useInfiniteQuery({
    queryKey: ["telegram", orgId, bot.id, "deliveries"],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      telegramApi(api.http).deliveries(orgId, bot.id, pageParam, signal),
    getNextPageParam: (page) =>
      page.length === 50 ? page[page.length - 1]?.id : undefined,
  });
  const rows = query.data?.pages.flat() ?? [];
  return (
    <Dialog
      open
      onClose={onClose}
      title={t("integrations.telegram.diagnostics")}
      width={600}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.close")}
          </Button>
          <Button variant="outline" onClick={() => void query.refetch()}>
            {t("integrations.telegram.refresh")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        {query.isError && <p role="alert">{telegramError(t, query.error)}</p>}
        {query.isSuccess && rows.length === 0 && (
          <p>{t("integrations.telegram.noDeliveries")}</p>
        )}
        {rows.map((row) => (
          <div
            key={row.id}
            className="rounded-lg border border-line-default p-3"
          >
            <p>{telegramState(t, row.state)}</p>
            <p>
              <code>{row.chat_id}</code> ·{" "}
              <code>{row.reply_to_message_id}</code>
            </p>
            <p className="text-body-sm text-fg-muted">
              {new Date(row.created_at).toLocaleString()}
            </p>
            {row.last_error === "delivery_unknown" ? (
              <Callout tone="warning">
                {t("integrations.telegram.unknownDelivery")}
              </Callout>
            ) : row.last_error ? (
              <p>{telegramReason(t, row.last_error)}</p>
            ) : null}
          </div>
        ))}
        {query.hasNextPage && (
          <Button
            variant="outline"
            loading={query.isFetchingNextPage}
            onClick={() => void query.fetchNextPage()}
          >
            {t("integrations.telegram.loadMore")}
          </Button>
        )}
      </div>
    </Dialog>
  );
}
