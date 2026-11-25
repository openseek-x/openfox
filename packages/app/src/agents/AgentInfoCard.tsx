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

function Detail({ icon, children }: { icon: IconName; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-center gap-2 text-body-sm">
      <Icon name={icon} size={15} className="shrink-0 text-fg-muted" />
      <span className="min-w-0 break-words">{children}</span>
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
      className="w-80 max-w-[calc(100vw-16px)] overflow-y-auto p-3"
    >
      {(!target.spaceId && location.notFound) || query.isError ? (
        <p role="status" className="text-body-sm text-fg-muted">
          {t("agents.drawer.notFound")}
        </p>
      ) : !agent ? (
        <Spinner />
      ) : (
        <>
          <div className="flex items-start gap-2">
            <AgentAvatar name={agent.name} avatar={agent.avatar} size={40} />
            <div className="min-w-0 flex-1">
              <h3 className="break-words text-body font-semibold">
                {agent.name}
              </h3>
              {agent.title && (
                <p className="break-words text-body-sm text-fg-muted">
                  {agent.title}
                </p>
              )}
            </div>
            <div className="flex shrink-0">
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
          {agent.description && (
            <p className="mt-2 whitespace-pre-wrap break-words text-body-sm text-fg-secondary">
              {agent.description}
            </p>
          )}
          <div className="mt-3 flex flex-col gap-1.5 border-t border-line-soft pt-2">
            <Detail icon="orbit">
              {space?.name ?? t("agents.unknownSpace")} ·{" "}
              {t(`agents.visibility.${agent.visibility}`)}
            </Detail>
            <Detail icon="user">
              {owner?.name || owner?.email || agent.owner_user_id}
            </Detail>
            {agent.config?.runtime && (
              <Detail icon="terminal">
                {runtimeLabel(agent.config.runtime)}
              </Detail>
            )}
            {agent.computer_name && (
              <Detail icon="monitor">{agent.computer_name}</Detail>
            )}
            {device && (
              <Detail icon="activity">
                {t("agents.info.slots", {
                  used: device.reserved_slots,
                  total: effectiveMaxSlots(device),
                })}
              </Detail>
            )}
            {device?.storage && (
              <Detail icon="hard-drive">
                {t("agents.info.storage", {
                  size: formatBytes(device.storage.total_bytes, locale),
                })}
              </Detail>
            )}
          </div>
        </>
      )}
    </Popover>
  );
}
