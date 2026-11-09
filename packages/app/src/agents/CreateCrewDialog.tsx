import { useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";

import type { AgentVisibility, Crew } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Dialog,
  Field,
  Icon,
  SearchMenu,
  Select,
  TextInput,
  useToast,
} from "@openfox/ui";

import { useApi } from "../context";
import { usePeople, useSpaceAgents } from "../tasks/queries";
import { AgentAvatar, PersonAvatar } from "./AgentAvatar";
import {
  agentErrorMessage,
  storeCrew,
  useDirectory,
  useSpaces,
} from "./queries";

/** 小队总人数上限（含队长与真人成员） */
export const CREW_LIMIT = 32;

/** 组织里还没有 Agent 时代替创建小队弹窗。 */
export function NeedAgentDialog({
  onClose,
  onCreateAgent,
}: {
  onClose: () => void;
  onCreateAgent: () => void;
}) {
  const t = useT();
  return (
    <Dialog
      open
      onClose={onClose}
      title={t("agents.crewCreate.needAgentTitle")}
      description={t("agents.crewCreate.needAgentDesc")}
      footer={
        <>
          <Button variant="outline" onClick={onClose} data-autofocus>
            {t("common.cancel")}
          </Button>
          <Button onClick={onCreateAgent}>
            {t("agents.crewCreate.needAgentAction")}
          </Button>
        </>
      }
    />
  );
}

/**
 * 创建小队入口：目录里没有任何 Agent 时显示「请先创建 Agent」，否则显示创建弹窗。
 */
export function CreateCrewEntry({
  orgId,
  defaultVisibility = "PRIVATE",
  defaultSpaceId,
  onClose,
  onCreated,
  onCreateAgent,
}: {
  orgId: string;
  defaultVisibility?: AgentVisibility;
  /** 默认选中的空间（空间 Agents 页传当前空间）；不传时沿用原来的第一个空间 */
  defaultSpaceId?: string;
  onClose: () => void;
  onCreated: (crew: Crew) => void;
  onCreateAgent: () => void;
}) {
  const directory = useDirectory(orgId);
  if (!directory.data) return null;
  const hasAgent = directory.data.entries.some(
    (entry) => entry.type === "agent",
  );
  return hasAgent ? (
    <CreateCrewDialog
      orgId={orgId}
      defaultVisibility={defaultVisibility}
      defaultSpaceId={defaultSpaceId}
      onClose={onClose}
      onCreated={onCreated}
    />
  ) : (
    <NeedAgentDialog onClose={onClose} onCreateAgent={onCreateAgent} />
  );
}

/** 创建小队弹窗：名称 + 队长 + 空间必填，成员最多 32 人（含队长）。 */
export function CreateCrewDialog({
  orgId,
  defaultVisibility,
  defaultSpaceId,
  onClose,
  onCreated,
}: {
  orgId: string;
  defaultVisibility: AgentVisibility;
  defaultSpaceId?: string;
  onClose: () => void;
  onCreated: (crew: Crew) => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const spaces = useSpaces(orgId);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [spaceId, setSpaceId] = useState<string | null>(defaultSpaceId ?? null);
  const [visibility, setVisibility] =
    useState<AgentVisibility>(defaultVisibility);
  const [leaderId, setLeaderId] = useState<string | null>(null);
  const [members, setMembers] = useState<string[]>([]);
  const [membersOpen, setMembersOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [showErrors, setShowErrors] = useState(false);
  const membersAnchor = useRef<HTMLButtonElement | null>(null);

  const space =
    spaces.data?.find((item) => item.id === spaceId) ?? spaces.data?.[0];
  const agents = (useSpaceAgents(orgId, space?.id).data ?? []).filter(
    (agent) => !agent.archived_at && !agent.builtin_kind,
  );
  const people = usePeople(orgId, space);
  const leader = agents.find((agent) => agent.id === leaderId);
  const selectedAgents = members.filter(
    (value) => value.startsWith("agent:") && value !== `agent:${leaderId}`,
  );
  const selectedPeople = members.filter((value) => value.startsWith("user:"));
  const total =
    (leader ? 1 : 0) + selectedAgents.length + selectedPeople.length;

  const changeSpace = (next: string) => {
    setSpaceId(next);
    setLeaderId(null);
    setMembers([]);
  };

  const toggleMember = (value: string) =>
    setMembers((list) => {
      if (list.includes(value)) return list.filter((item) => item !== value);
      if (total >= CREW_LIMIT) {
        toast(t("agents.crewCreate.limit"), { tone: "error" });
        return list;
      }
      return [...list, value];
    });

  const memberNames = [
    ...selectedAgents.map(
      (value) => agents.find((a) => `agent:${a.id}` === value)?.name,
    ),
    ...selectedPeople.map(
      (value) => people.find((p) => `user:${p.id}` === value)?.email,
    ),
  ].filter(Boolean);

  const submit = async () => {
    setShowErrors(true);
    if (!name.trim() || !leader || !space) return;
    setBusy(true);
    try {
      const crew = await api.crews.create(orgId, space.id, {
        name: name.trim(),
        description: description.trim(),
        visibility,
        leader_agent_id: leader.id,
        agent_ids: [
          leader.id,
          ...selectedAgents.map((value) => value.slice("agent:".length)),
        ],
        user_ids: selectedPeople.map((value) => value.slice("user:".length)),
      });
      storeCrew(queryClient, orgId, crew);
      toast(t("agents.toast.crewCreated"));
      onCreated(crew);
    } catch (error) {
      toast(agentErrorMessage(t, error), { tone: "error" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={busy ? undefined : onClose}
      title={t("agents.crewCreate.title")}
      width={600}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={busy}
            disabled={!name.trim() || !leader || !space}
            onClick={() => void submit()}
          >
            {t("agents.crewCreate.submit")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4 px-0.5 pb-0.5">
        <span className="flex size-16 items-center justify-center self-center rounded-xl bg-surface-muted text-fg-secondary">
          <Icon name="users" size={24} />
        </span>
        <Field
          label={t("agents.crewCreate.name")}
          required
          error={
            showErrors && !name.trim()
              ? t("agents.create.nameRequired")
              : undefined
          }
        >
          <TextInput
            variant="labeled"
            data-autofocus
            value={name}
            maxLength={255}
            placeholder={t("agents.crewCreate.namePlaceholder")}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label={t("agents.crewCreate.description")}>
          <TextInput
            variant="labeled"
            value={description}
            maxLength={10000}
            placeholder={t("agents.crewCreate.descriptionPlaceholder")}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
        <div className="grid grid-cols-2 gap-4">
          <Field label={t("agents.crewCreate.space")} required>
            <Select
              variant="labeled"
              value={space?.id ?? null}
              onChange={changeSpace}
              leading={<Icon name="layout-grid" />}
              options={(spaces.data ?? []).map((item) => ({
                value: item.id,
                label: item.name,
              }))}
            />
          </Field>
          <Field label={t("agents.crewCreate.visibility")}>
            <Select
              variant="labeled"
              value={visibility}
              onChange={setVisibility}
              options={(["SHARED", "PRIVATE"] as const).map((value) => ({
                value,
                label: t(`agents.visibility.${value}`),
                icon: <Icon name={value === "SHARED" ? "users" : "lock"} />,
              }))}
            />
          </Field>
        </div>
        <Field
          label={t("agents.crewCreate.leader")}
          required
          error={
            showErrors && !leader
              ? t("agents.crewCreate.leaderRequired")
              : undefined
          }
        >
          <Select
            variant="labeled"
            value={leader?.id ?? null}
            onChange={(id) => {
              setLeaderId(id);
              setMembers((list) => list.filter((v) => v !== `agent:${id}`));
            }}
            placeholder={t("agents.crewCreate.leaderPlaceholder")}
            options={agents.map((agent) => ({
              value: agent.id,
              label: agent.title
                ? `${agent.name} - ${agent.title}`
                : agent.name,
              icon: (
                <AgentAvatar
                  name={agent.name}
                  avatar={agent.avatar}
                  size={20}
                />
              ),
            }))}
          />
        </Field>
        <Field
          label={t("agents.crewCreate.members")}
          helper={t("agents.crewCreate.membersSelected", { count: total })}
        >
          <button
            ref={membersAnchor}
            type="button"
            aria-expanded={membersOpen}
            onClick={() => setMembersOpen((v) => !v)}
            className="flex h-9 w-full items-center gap-2 rounded-lg border border-line-default bg-surface-card px-3 text-left text-body"
          >
            <Icon name="users" className="text-fg-muted" />
            <span
              className={
                memberNames.length
                  ? "min-w-0 flex-1 truncate"
                  : "min-w-0 flex-1 truncate text-fg-subtle"
              }
            >
              {memberNames.length
                ? memberNames.join("、")
                : t("agents.crewCreate.membersPlaceholder")}
            </span>
            <Icon name="chevron-down" className="text-fg-muted" />
          </button>
        </Field>
        <SearchMenu
          open={membersOpen}
          onOpenChange={setMembersOpen}
          anchorRef={membersAnchor}
          multiple
          label={t("agents.crewCreate.members")}
          placeholder={t("agents.crewCreate.membersSearch")}
          selected={members}
          onSelect={toggleMember}
          className="w-[400px]"
          groups={[
            {
              id: "agents",
              label: t("agents.crewCreate.agentsGroup"),
              options: agents
                .filter((agent) => agent.id !== leaderId)
                .map((agent) => ({
                  value: `agent:${agent.id}`,
                  label: agent.name,
                  keywords: agent.title,
                  icon: (
                    <AgentAvatar
                      name={agent.name}
                      avatar={agent.avatar}
                      size={20}
                    />
                  ),
                })),
            },
            {
              id: "people",
              label: t("agents.crewCreate.peopleGroup"),
              options: people.map((person) => ({
                value: `user:${person.id}`,
                label: person.email,
                icon: <PersonAvatar name={person.email} size={20} />,
              })),
            },
          ]}
        />
      </div>
    </Dialog>
  );
}
