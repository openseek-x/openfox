import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { SlackApp, SlackChannel, SlackDelivery } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Callout,
  Checkbox,
  Dialog,
  Field,
  TextInput,
} from "@openfox/ui";
import { keys, useApi } from "../context";
import { useSlackWrite } from "./slack-hooks";
import {
  slackDeliveryState,
  slackError,
  slackID,
  slackMessageTimestamp,
  slackReason,
} from "./slack-model";
export function SlackChannelsDialog({
  orgId,
  app,
  onClose,
}: {
  orgId: string;
  app: SlackApp;
  onClose: () => void;
}) {
  const api = useApi(),
    t = useT(),
    cache = useQueryClient(),
    [cursor, setCursor] = useState(""),
    [id, setID] = useState(""),
    [selected, setSelected] = useState<SlackChannel>(),
    [consent, setConsent] = useState(false);
  const queryKey = [
    ...keys.integrations(orgId),
    "slack-channels",
    app.id,
    app.version,
  ];
  const query = useQuery({
    queryKey: [...queryKey, cursor],
    queryFn: ({ signal }) =>
      api.slack.channels(orgId, app.bot_id, cursor, signal),
    retry: false,
  });
  const write = useSlackWrite(() => {
    void cache.invalidateQueries({ queryKey });
  });
  const action = selected?.enabled ? "disable" : "enable",
    valid =
      slackID(selected?.channel_id || id.trim()) &&
      (action === "disable" || consent);
  function submit() {
    if (!valid) return;
    void write.run(
      (signal) =>
        api.slack.channel(
          orgId,
          app.bot_id,
          selected?.channel_id || id.trim(),
          {
            version: selected?.version ?? 0,
            enabled: action === "enable",
            public_reply_consent: action === "enable" && consent,
          },
          signal,
        ),
      () => {
        setID("");
        setSelected(undefined);
        setConsent(false);
      },
    );
  }
  return (
    <Dialog
      open
      title={t("integrations.slack.channels")}
      onClose={onClose}
      width={680}
      footer={
        <Button variant="outline" onClick={onClose}>
          {t("common.close")}
        </Button>
      }
    >
      <div className="flex flex-col gap-4">
        <p>{t("integrations.slack.channelsHelp")}</p>
        {query.isError && (
          <Callout tone="danger">{t("integrations.slack.loadFailed")}</Callout>
        )}
        {query.data?.channels.map((row) => (
          <div
            key={row.id}
            className="flex items-center justify-between gap-3 border-b border-line-default py-2"
          >
            <div>
              <p className="text-body font-medium">
                {row.name || row.channel_id}
              </p>
              <p className="text-caption text-fg-muted">{row.channel_id}</p>
              <Badge tone={row.enabled ? "success" : "muted"}>
                {t(
                  row.enabled
                    ? "integrations.slack.enabled"
                    : "integrations.slack.disabled",
                )}
              </Badge>
            </div>
            <Button
              variant="outline"
              disabled={write.busy || write.blocked}
              onClick={() => {
                setSelected(row);
                setID("");
                setConsent(false);
              }}
            >
              {t(
                row.enabled
                  ? "integrations.slack.disableChannel"
                  : "integrations.slack.enableChannel",
              )}
            </Button>
          </div>
        ))}
        {query.data?.channels.length === 0 && (
          <p>{t("integrations.slack.noChannels")}</p>
        )}
        <div className="flex gap-2">
          {cursor && (
            <Button variant="outline" onClick={() => setCursor("")}>
              {t("integrations.slack.first")}
            </Button>
          )}
          {query.data?.next_cursor && (
            <Button
              variant="outline"
              onClick={() => setCursor(query.data!.next_cursor)}
            >
              {t("integrations.slack.next")}
            </Button>
          )}
        </div>
        <Field label={t("integrations.slack.channelID")}>
          <TextInput
            value={selected?.channel_id ?? id}
            disabled={!!selected}
            onChange={(e) => {
              setID(e.target.value);
              setConsent(false);
            }}
            maxLength={128}
          />
        </Field>
        {selected && (
          <Button
            variant="outline"
            onClick={() => {
              setSelected(undefined);
              setConsent(false);
            }}
          >
            {t("common.cancel")}
          </Button>
        )}
        {action === "enable" && (
          <label className="flex items-start gap-2 text-body-sm">
            <Checkbox
              checked={consent}
              onChange={(e) => setConsent(e.target.checked)}
            />
            <span>{t("integrations.slack.channelConsent")}</span>
          </label>
        )}
        <Button
          disabled={
            !valid ||
            write.busy ||
            write.blocked ||
            query.isFetching ||
            query.isError
          }
          onClick={submit}
        >
          {t(
            action === "disable"
              ? "integrations.slack.disableChannel"
              : "integrations.slack.enableChannel",
          )}
        </Button>
        {!!write.error && (
          <Callout tone="danger">{slackError(t, write.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}
export function SlackDeliveriesDialog({
  orgId,
  app,
  onClose,
}: {
  orgId: string;
  app: SlackApp;
  onClose: () => void;
}) {
  const api = useApi(),
    t = useT(),
    [cursor, setCursor] = useState(""),
    [resolution, setResolution] = useState<{
      delivery: SlackDelivery;
      action: "verify" | "skip";
    }>(),
    [notice, setNotice] = useState<
      "verificationUnknown" | "verificationDone" | "skipRecorded"
    >();
  const query = useQuery({
    queryKey: [
      ...keys.integrations(orgId),
      "slack-deliveries",
      app.id,
      app.version,
      cursor,
    ],
    queryFn: ({ signal }) =>
      api.slack.deliveries(orgId, app.id, cursor, signal),
    retry: false,
  });
  return (
    <Dialog
      open
      title={t("integrations.slack.deliveries")}
      width={680}
      onClose={onClose}
      footer={
        <Button variant="outline" onClick={onClose}>
          {t("common.close")}
        </Button>
      }
    >
      <div className="flex flex-col gap-3">
        <Callout tone="warning">{t("integrations.slack.deliveryHelp")}</Callout>
        {notice && (
          <Callout tone={notice === "verificationUnknown" ? "warning" : "info"}>
            {t(`integrations.slack.${notice}`)}
          </Callout>
        )}
        {query.isError && (
          <Callout tone="danger">{t("integrations.slack.loadFailed")}</Callout>
        )}
        {query.data?.deliveries.length === 0 && (
          <p>{t("integrations.slack.noDeliveries")}</p>
        )}
        {query.data?.deliveries.map((d) => (
          <article
            key={`${d.id}:${d.version}`}
            className="flex flex-col gap-1 rounded-lg border border-line-default p-3"
          >
            <p>
              {slackDeliveryState(t, d.state)} · {t("integrations.slack.part")}{" "}
              {d.part}
            </p>
            <p className="break-all text-caption text-fg-muted">
              {d.channel_id} · {d.thread_ts} · {d.remote_ts}
            </p>
            {d.status_reason && <p>{slackReason(t, d.status_reason)}</p>}
            {d.state === "UNKNOWN" &&
              (d.source_kind === "REPLY" ||
                d.source_kind === "NOTIFICATION") && (
                <div className="flex flex-wrap gap-2">
                  <Button
                    variant="outline"
                    disabled={query.isFetching}
                    onClick={() =>
                      setResolution({ delivery: d, action: "verify" })
                    }
                  >
                    {t("integrations.slack.verifyDelivery")}
                  </Button>
                  <Button
                    variant="outline"
                    disabled={query.isFetching}
                    onClick={() =>
                      setResolution({ delivery: d, action: "skip" })
                    }
                  >
                    {t("integrations.slack.skipDelivery")}
                  </Button>
                </div>
              )}
            <time className="text-caption text-fg-muted">
              {new Date(d.created_at).toLocaleString()}
            </time>
          </article>
        ))}
        <div className="flex gap-2">
          <Button
            variant="outline"
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            {t("integrations.slack.reload")}
          </Button>
          {cursor && (
            <Button variant="outline" onClick={() => setCursor("")}>
              {t("integrations.slack.first")}
            </Button>
          )}
          {query.data?.next_cursor && (
            <Button
              variant="outline"
              onClick={() => setCursor(query.data!.next_cursor)}
            >
              {t("integrations.slack.next")}
            </Button>
          )}
        </div>
        {resolution && (
          <SlackResolveDialog
            key={`${resolution.delivery.id}:${resolution.delivery.version}:${resolution.action}`}
            orgId={orgId}
            app={app}
            delivery={resolution.delivery}
            action={resolution.action}
            onClose={() => setResolution(undefined)}
            onDone={(result) => {
              setNotice(
                resolution.action === "skip"
                  ? "skipRecorded"
                  : result.state === "DONE"
                    ? "verificationDone"
                    : "verificationUnknown",
              );
              setResolution(undefined);
            }}
            refresh={() => {
              void query.refetch();
            }}
          />
        )}
      </div>
    </Dialog>
  );
}

function SlackResolveDialog({
  orgId,
  app,
  delivery,
  action,
  onClose,
  onDone,
  refresh,
}: {
  orgId: string;
  app: SlackApp;
  delivery: SlackDelivery;
  action: "verify" | "skip";
  onClose: () => void;
  onDone: (result: SlackDelivery) => void;
  refresh: () => void;
}) {
  const api = useApi(),
    t = useT(),
    write = useSlackWrite(refresh),
    [remoteTS, setRemoteTS] = useState("");
  const verify = action === "verify",
    valid =
      delivery.state === "UNKNOWN" &&
      (!verify || slackMessageTimestamp(remoteTS));
  return (
    <Dialog
      open
      title={t(
        verify
          ? "integrations.slack.verifyDelivery"
          : "integrations.slack.skipDelivery",
      )}
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            variant={verify ? "primary" : "danger"}
            disabled={!valid || write.busy || write.blocked}
            onClick={() =>
              void write.run(
                (signal) =>
                  api.slack.resolveDelivery(
                    orgId,
                    app.id,
                    delivery.id,
                    {
                      version: delivery.version,
                      action,
                      remote_ts: verify ? remoteTS : "",
                    },
                    signal,
                  ),
                onDone,
              )
            }
          >
            {t(
              verify
                ? "integrations.slack.verifyDelivery"
                : "integrations.slack.skipDelivery",
            )}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <Callout tone="warning">
          {t(
            verify
              ? "integrations.slack.verifyDeliveryHelp"
              : "integrations.slack.skipDeliveryHelp",
          )}
        </Callout>
        <p className="text-caption text-fg-muted">
          {delivery.channel_id} · {delivery.thread_ts} ·{" "}
          {t("integrations.slack.part")} {delivery.part}
        </p>
        {verify && (
          <Field
            label={t("integrations.slack.remoteTimestamp")}
            helper={t("integrations.slack.remoteTimestampHelp")}
          >
            <TextInput
              value={remoteTS}
              onChange={(event) => setRemoteTS(event.target.value.trim())}
              autoComplete="off"
              maxLength={32}
              disabled={write.busy || write.blocked}
            />
          </Field>
        )}
        {!!write.error && (
          <Callout tone="danger">{slackError(t, write.error)}</Callout>
        )}
      </div>
    </Dialog>
  );
}
