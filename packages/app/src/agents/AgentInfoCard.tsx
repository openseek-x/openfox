import "./agent-info-card.css";
import { useMemo, useState, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { useI18n, useT } from "@openfox/i18n";
import { Icon, IconButton, Popover, Spinner, type IconName } from "@openfox/ui";
import { useOrgPeople } from "../tasks/queries";
import { effectiveMaxSlots, formatBytes } from "../computers/model";
import { AgentAvatar } from "./AgentAvatar";
import { AgentInfoContext, type AgentInfoTarget } from "./AgentInfoTrigger";
import { AssignTaskPalette, type AssignTarget } from "./AssignTaskPalette";
import { chatPath } from "./AgentsPage";
import { runtimeLabel } from "./model";
import { useAgentSettingsParam } from "./params";
import { useAgent, useDevices, useEntitySpace, useSpaces } from "./queries";

export function AgentInfoProvider({
  orgId,
  children,
}: {
  orgId: string;
  children: ReactNode;
}) {
  const [target, setTarget] = useState<AgentInfoTarget | null>(null);
  const [assign, setAssign] = useState<AssignTarget | null>(null);
  const controller = useMemo(
    () => ({
      anchor: target?.anchor ?? null,
      toggle: (next: AgentInfoTarget) =>
        setTarget((prev) => (prev?.anchor === next.anchor ? null : next)),
      dismiss: (anchor: HTMLElement) =>
        setTarget((prev) => (prev?.anchor === anchor ? null : prev)),
    }),
    [target],
  );
  return (
    <AgentInfoContext.Provider value={controller}>
      {children}
      {target && (
        <AgentInfoCard
          key={target.id}
          orgId={orgId}
          target={target}
          onClose={() => setTarget(null)}
          onAssign={setAssign}
        />
      )}
      {assign && (
        <AssignTaskPalette
          orgId={orgId}
          target={assign}
          onClose={() => setAssign(null)}
        />
      )}
    </AgentInfoContext.Provider>
  );
}

function Detail({
  icon,
  label,
  children,
}: {
  icon: IconName;
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <dt className="flex items-center gap-1.5 text-[11px] leading-4 text-fg-muted">
        <Icon name={icon} size={14} />
        {label}
      </dt>
      <dd className="min-w-0 text-[13px] leading-5 font-medium text-fg-default [overflow-wrap:anywhere]">
        {children}
      </dd>
    </div>
  );
}

function AgentInfoCard({
  orgId,
  target,
  onClose,
  onAssign,
}: {
  orgId: string;
  target: AgentInfoTarget;
  onClose: () => void;
  onAssign: (target: AssignTarget) => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const navigate = useNavigate();
  const settings = useAgentSettingsParam();
  const anchorRef = useMemo(
    () => ({ current: target.anchor }),
    [target.anchor],
  );
  const location = useEntitySpace(
    orgId,
    "agent",
    target.spaceId ? null : target.id,
  );
  const query = useAgent(orgId, target.spaceId ?? location.spaceId, target.id);
  const agent = query.data;
  const spaces = useSpaces(orgId);
  const people = useOrgPeople(orgId);
  const devices = useDevices(
    orgId,
    !!agent?.config?.computer_id && !query.isError,
  );
  const device = devices.data?.find(
    (item) =>
      item.id === agent?.config?.computer_id &&
      item.grant_id === agent.config?.grant_id,
  );
  const owner = people.data?.find(
    (person) => person.id === agent?.owner_user_id,
  );
  const space = spaces.data?.find((item) => item.id === agent?.space_id);
  const title = t("agents.info.label", { name: agent?.name ?? target.name });
  return (
    <Popover
      open
      anchorRef={anchorRef}
      onOpenChange={onClose}
      fitHeight
      role="dialog"
      aria-label={title}
      className="of-agent-profile w-[352px] max-w-[calc(100vw-16px)] overflow-x-hidden overflow-y-auto"
    >
      {(!target.spaceId && location.notFound) || query.isError ? (
        <p role="status" className="p-4 text-body-sm text-fg-muted">
          {t("agents.drawer.notFound")}
        </p>
      ) : !agent ? (
        <div className="flex justify-center p-5">
          <Spinner />
        </div>
      ) : (
        <>
          <div className="of-agent-cover" aria-hidden="true" />
          <div className="relative px-5">
            <div className="flex items-end justify-between gap-3">
              <span className="-mt-8 inline-flex shrink-0 rounded-full bg-surface-card p-1 shadow-sm">
                <AgentAvatar
                  name={agent.name}
                  avatar={agent.avatar}
                  size={64}
                />
              </span>
              <div className="flex items-center gap-0.5 pb-1">
                <IconButton
                  size="28"
                  icon={<Icon name="plus" />}
                  label={t("agents.contacts.assign")}
                  disabled={!!agent.archived_at || !agent.allow_assign}
                  onClick={() => {
                    onClose();
                    onAssign({
                      kind: "agent",
                      id: agent.id,
                      name: agent.name,
                      spaceId: agent.space_id,
                    });
                  }}
                />
                <IconButton
                  size="28"
                  icon={<Icon name="message" />}
                  label={t("agents.contacts.chat")}
                  disabled={!!agent.archived_at || !agent.allow_direct_chat}
                  onClick={() => {
                    onClose();
                    navigate(
                      agent.builtin_kind === "ASSISTANT"
                        ? `/o/${orgId}/assistant`
                        : chatPath(orgId, { type: "agent", id: agent.id }),
                    );
                  }}
                />
                <IconButton
                  size="28"
                  icon={<Icon name="sliders-horizontal" />}
                  label={t("agents.contacts.settings")}
                  onClick={() => {
                    onClose();
                    settings.open(agent.id, agent.space_id);
                  }}
                />
              </div>
            </div>
            <div className="mt-2 min-w-0">
              <h3 className="break-words text-[21px] leading-7 font-semibold tracking-tight">
                {agent.name}
              </h3>
              {agent.title && (
                <p className="break-words text-body-sm text-fg-muted">
                  {agent.title}
                </p>
              )}
            </div>
            {agent.description && (
              <p className="mt-2 whitespace-pre-wrap break-words text-[13px] leading-5 text-fg-secondary">
                {agent.description}
              </p>
            )}
          </div>
          <dl className="mx-5 mt-4 grid grid-cols-2 gap-x-5 gap-y-3 border-t border-line-soft py-4">
            <Detail icon="orbit" label={t("agents.info.space")}>
              {space?.name ?? t("agents.unknownSpace")} ·{" "}
              {t(`agents.visibility.${agent.visibility}`)}
            </Detail>
            <Detail icon="user" label={t("agents.info.owner")}>
              {owner?.name || owner?.email || agent.owner_user_id}
            </Detail>
            {agent.config?.runtime && (
              <Detail icon="terminal" label={t("agents.info.runtime")}>
                {runtimeLabel(agent.config.runtime)}
              </Detail>
            )}
            {agent.computer_name && (
              <Detail icon="monitor" label={t("agents.info.computer")}>
                {agent.computer_name}
              </Detail>
            )}
          </dl>
          {device && (
            <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1.5 border-t border-line-soft bg-surface-muted/60 px-5 py-2.5 text-[12px] tabular-nums text-fg-secondary">
              <span className="inline-flex items-center gap-2">
                <span
                  aria-hidden
                  className="h-1.5 w-9 overflow-hidden rounded-full bg-line-default"
                >
                  <span
                    className="block h-full rounded-full bg-accent"
                    style={{
                      width: `${Math.min(100, (device.reserved_slots / Math.max(1, effectiveMaxSlots(device))) * 100)}%`,
                    }}
                  />
                </span>
                {t("agents.info.slots", {
                  used: device.reserved_slots,
                  total: effectiveMaxSlots(device),
                })}
              </span>
              {device.storage && (
                <span className="inline-flex items-center gap-1.5">
                  <Icon name="hard-drive" size={13} />
                  {t("agents.info.storage", {
                    size: formatBytes(device.storage.total_bytes, locale),
                  })}
                </span>
              )}
            </div>
          )}
        </>
      )}
    </Popover>
  );
}
