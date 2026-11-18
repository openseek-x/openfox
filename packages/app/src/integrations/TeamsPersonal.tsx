import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  teamsApi,
  type TeamsBindingChallenge,
  type TeamsPersonalBinding,
  type TeamsPersonalTarget,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog } from "@openfox/ui";
import { useApi } from "../context";
import { SettingsCard, SettingsSection } from "../spaces/settings/parts";
import { teamsError, useTeamsAction } from "./teams-model";
export function TeamsPersonalSection({ orgId }: { orgId: string }) {
  const api = useApi(),
    t = useT();
  const targets = useQuery({
    queryKey: ["teams", orgId, "targets"],
    queryFn: ({ signal }) => teamsApi(api.http).targets(orgId, signal),
  });
  const identities = useQuery({
    queryKey: ["teams", orgId, "personal"],
    queryFn: ({ signal }) => teamsApi(api.http).personal(orgId, signal),
  });
  const [target, setTarget] = useState<TeamsPersonalTarget | null>(null),
    [removing, setRemoving] = useState<TeamsPersonalBinding | null>(null);
  const refresh = () => {
    void targets.refetch();
    void identities.refetch();
  };
  return (
    <SettingsSection
      title={t("integrations.teams.personal")}
      description={t("integrations.teams.personalHelp")}
    >
      <Button variant="outline" onClick={refresh}>
        {t("integrations.teams.refresh")}
      </Button>
      {(targets.isPending || identities.isPending) && (
        <p>{t("common.loading")}</p>
      )}
      {(targets.isError || identities.isError) && (
        <p role="alert">{teamsError(t, targets.error ?? identities.error)}</p>
      )}
      {targets.data?.length === 0 && <p>{t("integrations.teams.noTargets")}</p>}
      {targets.data?.map((row) => (
        <SettingsCard key={row.bot_id}>
          <div className="flex items-center justify-between gap-3 p-4">
            <div>
              <p>{row.name}</p>
              <p>{row.space?.name ?? row.agent.name}</p>
            </div>
            <Button
              variant="outline"
              disabled={
                !row.can_bind ||
                identities.data?.some(
                  (v) => v.bot_id === row.bot_id && v.active,
                )
              }
              onClick={() => setTarget(row)}
            >
              {t("integrations.teams.bind")}
            </Button>
          </div>
        </SettingsCard>
      ))}
      {identities.data?.map((row) => (
        <SettingsCard key={row.id}>
          <div className="flex items-center justify-between gap-3 p-4">
            <div>
              <p>
                {row.name} · <code>{row.aad_object_id}</code>
              </p>
              <p>
                {t(
                  row.active
                    ? "integrations.teams.identityActive"
                    : "integrations.teams.identityInactive",
                )}
              </p>
            </div>
            {row.active && (
              <Button variant="outline" onClick={() => setRemoving(row)}>
                {t("integrations.teams.unbind")}
              </Button>
            )}
          </div>
        </SettingsCard>
      ))}
      {target && (
        <TeamsBindingDialog
          key={`${orgId}:${target.bot_id}`}
          orgId={orgId}
          target={target}
          onClose={() => setTarget(null)}
          onSaved={refresh}
        />
      )}
      {removing && (
        <TeamsUnbindDialog
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
export function TeamsBindingDialog({
  orgId,
  target,
  onClose,
  onSaved,
}: {
  orgId: string;
  target: TeamsPersonalTarget;
  onClose: () => void;
  onSaved: () => void;
}) {
  const api = useApi(),
    t = useT(),
    action = useTeamsAction();
  const [challenge, setChallenge] = useState<TeamsBindingChallenge | null>(
      null,
    ),
    [expired, setExpired] = useState(false),
    [bound, setBound] = useState(false);
  useEffect(() => {
    if (!challenge) return;
    const ms = Date.parse(challenge.expires_at) - Date.now();
    if (ms <= 0) {
      setChallenge(null);
      setExpired(true);
      return;
    }
    const timer = setTimeout(
      () => {
        setChallenge(null);
        setExpired(true);
      },
      Math.min(ms, 2_147_483_647),
    );
    return () => clearTimeout(timer);
  }, [challenge]);
  const issue = () =>
    action.run(async (signal) => {
      const c = await teamsApi(api.http).issue(orgId, target.bot_id, signal);
      if (!signal.aborted) {
        setChallenge(c);
        setExpired(false);
        setBound(false);
      }
    });
  const refresh = () =>
    action.run(async (signal) => {
      const all = await teamsApi(api.http).personal(orgId, signal);
      if (!signal.aborted) {
        if (all.some((v) => v.bot_id === target.bot_id && v.active)) {
          setChallenge(null);
          setBound(true);
        }
        onSaved();
      }
    });
  return (
    <Dialog
      open
      onClose={onClose}
      title={t("integrations.teams.personal")}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.close")}
          </Button>
          <Button disabled={action.busy} onClick={() => void refresh()}>
            {t("integrations.teams.refresh")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <p>{target.name}</p>
        <p>{t("integrations.teams.personalHelp")}</p>
        {action.error && <p role="alert">{action.error}</p>}
        {bound ? (
          <Callout tone="info">
            {t("integrations.teams.identityActive")}
          </Callout>
        ) : (
          <Button
            variant="outline"
            loading={action.busy}
            onClick={() => void issue()}
          >
            {t("integrations.teams.issue")}
          </Button>
        )}
        {expired && <p>{t("integrations.teams.expired")}</p>}
        {challenge && (
          <>
            <Callout tone="warning">
              {t("integrations.teams.commandHelp")}
            </Callout>
            <code className="break-all select-all">{challenge.command}</code>
            <p>
              {t("integrations.teams.expires", {
                time: new Date(challenge.expires_at).toLocaleString(),
              })}
            </p>
            <Button variant="outline" onClick={() => setChallenge(null)}>
              {t("integrations.teams.hide")}
            </Button>
          </>
        )}
      </div>
    </Dialog>
  );
}
function TeamsUnbindDialog({
  orgId,
  identity,
  onClose,
  onSaved,
}: {
  orgId: string;
  identity: TeamsPersonalBinding;
  onClose: () => void;
  onSaved: () => void;
}) {
  const api = useApi(),
    t = useT(),
    action = useTeamsAction();
  return (
    <Dialog
      open
      onClose={onClose}
      title={t("integrations.teams.unbind")}
      description={t("integrations.teams.unbindHelp")}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.close")}
          </Button>
          <Button
            loading={action.busy}
            onClick={() =>
              void action.run(async (signal) => {
                await teamsApi(api.http).unbind(
                  orgId,
                  identity.bot_id,
                  identity.version,
                  signal,
                );
                if (!signal.aborted) {
                  onSaved();
                  onClose();
                }
              })
            }
          >
            {t("integrations.teams.unbind")}
          </Button>
        </>
      }
    >
      {action.error && <p role="alert">{action.error}</p>}
      <p>{identity.name}</p>
    </Dialog>
  );
}
