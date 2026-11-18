import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  telegramApi,
  type TelegramBindingSession,
  type TelegramPersonalTarget,
  type TelegramIdentity,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog } from "@openfox/ui";
import { useApi } from "../context";
import { SettingsCard, SettingsSection } from "../spaces/settings/parts";
import { useTelegramAction } from "./TelegramDialogs";
import {
  telegramBindingURL,
  telegramError,
  telegramState,
} from "./telegram-model";

export function TelegramPersonalSection({ orgId }: { orgId: string }) {
  const api = useApi(),
    t = useT();
  const targets = useQuery({
    queryKey: ["telegram", orgId, "targets"],
    queryFn: ({ signal }) => telegramApi(api.http).targets(orgId, signal),
  });
  const identities = useQuery({
    queryKey: ["telegram", orgId, "personal"],
    queryFn: ({ signal }) => telegramApi(api.http).personal(orgId, signal),
  });
  const [target, setTarget] = useState<TelegramPersonalTarget | null>(null),
    [removing, setRemoving] = useState<TelegramIdentity | null>(null);
  const refresh = () => {
    void targets.refetch();
    void identities.refetch();
  };
  return (
    <SettingsSection
      title={t("integrations.telegram.personal")}
      description={t("integrations.telegram.personalHelp")}
    >
      <Button variant="outline" size="28" onClick={refresh}>
        {t("integrations.telegram.refresh")}
      </Button>
      {(targets.isPending || identities.isPending) && (
        <p>{t("common.loading")}</p>
      )}
      {(targets.isError || identities.isError) && (
        <p role="alert">
          {telegramError(t, targets.error ?? identities.error)}
        </p>
      )}
      {targets.data?.length === 0 && (
        <p>{t("integrations.telegram.noTargets")}</p>
      )}
      {targets.data?.map((row) => (
        <SettingsCard key={row.bot_id}>
          <div className="flex items-center justify-between gap-3 p-4">
            <div>
              <p>
                {row.name} · @{row.username}
              </p>
              <p className="text-body-sm text-fg-muted">
                {row.kind === "TASK_ASSISTANT"
                  ? row.space.name
                  : row.agent.name}
              </p>
            </div>
            <Button
              variant="outline"
              size="28"
              disabled={
                !row.can_bind ||
                identities.data?.some(
                  (i) => i.bot_id === row.bot_id && i.active,
                )
              }
              onClick={() => setTarget(row)}
            >
              {t("integrations.telegram.bind")}
            </Button>
          </div>
        </SettingsCard>
      ))}
      {identities.data?.map((row) => (
        <SettingsCard key={row.id}>
          <div className="flex items-center justify-between gap-3 p-4">
            <div>
              <p>
                {row.display_name} · <code>{row.remote_user_id}</code>
              </p>
              <p>
                {targets.data?.find((x) => x.bot_id === row.bot_id)?.name ??
                  row.bot_id}
              </p>
              <p className="text-body-sm text-fg-muted">
                {t(
                  row.active
                    ? "integrations.telegram.identityActive"
                    : "integrations.telegram.identityInactive",
                )}
              </p>
            </div>
            {row.active && (
              <Button
                variant="outline"
                size="28"
                onClick={() => setRemoving(row)}
              >
                {t("integrations.telegram.unbind")}
              </Button>
            )}
          </div>
        </SettingsCard>
      ))}
      {target && (
        <BindingDialog
          key={`${orgId}:${target.bot_id}`}
          orgId={orgId}
          target={target}
          onClose={() => setTarget(null)}
          onSaved={refresh}
        />
      )}
      {removing && (
        <UnbindDialog
          key={removing.id}
          orgId={orgId}
          identity={removing}
          onClose={() => setRemoving(null)}
          onSaved={refresh}
        />
      )}
    </SettingsSection>
  );
}
function BindingDialog({
  orgId,
  target,
  onClose,
  onSaved,
}: {
  orgId: string;
  target: TelegramPersonalTarget;
  onClose: () => void;
  onSaved: () => void;
}) {
  const t = useT(),
    api = useApi(),
    action = useTelegramAction(),
    [session, setSession] = useState<TelegramBindingSession | null>(null),
    [link, setLink] = useState<string | null>(null),
    [confirmed, setConfirmed] = useState(false);
  const validCandidate =
    session?.status === "CANDIDATE" &&
    !!session.candidate_user_id &&
    Date.parse(session.expires_at) > Date.now();
  return (
    <Dialog
      open
      onClose={onClose}
      title={t("integrations.telegram.personal")}
      description={target.name}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.close")}
          </Button>
          {session && (
            <Button
              variant="outline"
              loading={action.busy}
              onClick={() =>
                void action.run(async (signal) => {
                  const next = await telegramApi(api.http).session(
                    orgId,
                    session.id,
                    signal,
                  );
                  if (!signal.aborted) {
                    setSession(next);
                    setConfirmed(false);
                    if (next.status !== "WAITING") setLink(null);
                  }
                })
              }
            >
              {t("integrations.telegram.refresh")}
            </Button>
          )}
          {validCandidate && (
            <Button
              disabled={!confirmed}
              loading={action.busy}
              onClick={() =>
                void action.run(async (signal) => {
                  await telegramApi(api.http).confirmIdentity(
                    orgId,
                    session.id,
                    { version: session.version },
                    signal,
                  );
                  if (!signal.aborted) {
                    setLink(null);
                    onSaved();
                    onClose();
                  }
                })
              }
            >
              {t("common.confirm")}
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-col gap-3">
        {action.error && <p role="alert">{action.error}</p>}
        {!session && (
          <Button
            loading={action.busy}
            onClick={() =>
              void action.run(async (signal) => {
                const next = await telegramApi(api.http).issue(
                  orgId,
                  target.bot_id,
                  signal,
                );
                if (!signal.aborted) {
                  const { start_url, ...safe } = next;
                  setSession(safe);
                  setLink(
                    start_url
                      ? telegramBindingURL(start_url, target.username)
                      : null,
                  );
                }
              })
            }
          >
            {t("integrations.telegram.issue")}
          </Button>
        )}
        {link && (
          <>
            <Callout tone="warning">
              {t("integrations.telegram.secretLink")}
            </Callout>
            <a
              className="text-body underline"
              href={link}
              target="_blank"
              rel="noopener noreferrer"
              referrerPolicy="no-referrer"
            >
              {t("integrations.telegram.openTelegram")}
            </a>
            <Button variant="outline" size="28" onClick={() => setLink(null)}>
              {t("integrations.telegram.hideLink")}
            </Button>
          </>
        )}
        {session && (
          <>
            <p>{telegramState(t, session.status)}</p>
            <p>
              {t("integrations.telegram.expires", {
                time: new Date(session.expires_at).toLocaleString(),
              })}
            </p>
            {session.status === "WAITING" && (
              <p>{t("integrations.telegram.waiting")}</p>
            )}
          </>
        )}
        {validCandidate && (
          <>
            <h3>{t("integrations.telegram.candidate")}</h3>
            <p>{session.candidate_name}</p>
            <p>
              {t("integrations.telegram.candidateID")}:{" "}
              <code>{session.candidate_user_id}</code>
            </p>
            <label className="flex gap-2">
              <input
                type="checkbox"
                checked={confirmed}
                onChange={(e) => setConfirmed(e.target.checked)}
              />
              {t("integrations.telegram.confirmIdentity")}
            </label>
          </>
        )}
      </div>
    </Dialog>
  );
}
function UnbindDialog({
  orgId,
  identity,
  onClose,
  onSaved,
}: {
  orgId: string;
  identity: TelegramIdentity;
  onClose: () => void;
  onSaved: () => void;
}) {
  const api = useApi(),
    t = useT(),
    action = useTelegramAction();
  return (
    <Dialog
      open
      onClose={onClose}
      title={t("integrations.telegram.unbind")}
      description={t("integrations.telegram.unbindHelp")}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={action.busy}
            onClick={() =>
              void action.run(async (signal) => {
                await telegramApi(api.http).unbind(
                  orgId,
                  identity.id,
                  { version: identity.version },
                  signal,
                );
                if (!signal.aborted) {
                  onSaved();
                  onClose();
                }
              })
            }
          >
            {t("common.confirm")}
          </Button>
        </>
      }
    >
      <p>
        {identity.display_name} · {identity.remote_user_id}
      </p>
      {action.error && <p role="alert">{action.error}</p>}
    </Dialog>
  );
}
