import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router-dom";

import type { Crew, CrewMember, CrewPatch } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  ConfirmDialog,
  Dialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  SearchMenu,
  Skeleton,
  Switch,
  TextInput,
  Textarea,
  Tooltip,
  cn,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { InlineEdit } from "../../settings/InlineEdit";
import { usePeople, useSpaceAgents } from "../../tasks/queries";
import { AgentAvatar, CrewAvatar, PersonAvatar } from "../AgentAvatar";
import { useArchiveEntry, useFavoriteEntry } from "../actions";
import { chatPath } from "../AgentsPage";
import { AssignTaskPalette } from "../AssignTaskPalette";
import { CREW_LIMIT } from "../CreateCrewDialog";
import { useCrewSettingsParam } from "../params";
import {
  agentErrorMessage,
  storeCrew,
  useCrew,
  useDirectory,
  useEntitySpace,
  useOptimisticMutation,
  useSpaces,
} from "../queries";
import { AccessDialog } from "./AccessDialog";
import { DangerCard, SettingsCard, Stats } from "./parts";

/** 全局挂载：地址栏带 ?crewSettings=<id> 时打开小队设置抽屉。 */
export function CrewSettingsHost({ orgId }: { orgId: string }) {
  const param = useCrewSettingsParam();
  if (!param.id) return null;
  return (
    <CrewSettingsDrawer
      key={param.id}
      orgId={orgId}
      crewId={param.id}
      onClose={param.close}
    />
  );
}

/** 小队设置抽屉：宽 720，右侧滑入，非模态。 */
export function CrewSettingsDrawer({
  orgId,
  crewId,
  onClose,
}: {
  orgId: string;
  crewId: string;
  onClose: () => void;
}) {
  const t = useT();
  const location = useEntitySpace(orgId, "crew", crewId);
  const crewQuery = useCrew(orgId, location.spaceId, crewId);
  const crew = crewQuery.data;
  const [entered, setEntered] = useState(false);
  useEffect(() => {
    const frame = requestAnimationFrame(() => setEntered(true));
    return () => cancelAnimationFrame(frame);
  }, []);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return;
      if (
        document.querySelector(
          '[aria-modal="true"],[role="menu"],[role="listbox"]',
        )
      )
        return;
      onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);
  const notFound =
    location.notFound ||
    (crewQuery.isError &&
      (crewQuery.error as { status?: number }).status === 404);

  return createPortal(
    <aside
      role="complementary"
      aria-label={t("agents.crew.drawerLabel", { name: crew?.name ?? "" })}
      className={cn(
        "fixed top-11 right-[9px] bottom-[9px] z-30 flex w-[720px] max-w-[calc(100vw-18px)] flex-col overflow-hidden rounded-xl border border-line-default bg-surface-card shadow-dialog",
        "transition-transform duration-300 ease-drawer",
        entered ? "translate-x-0" : "translate-x-[calc(100%+9px)]",
      )}
    >
      <div className="flex h-11 shrink-0 items-center px-3">
        <IconButton
          label={t("agents.drawer.close")}
          icon={<Icon name="x" />}
          size="28"
          onClick={onClose}
        />
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {notFound ? (
          <p className="p-10 text-center text-body text-fg-muted">
            {t("agents.drawer.notFound")}
          </p>
        ) : !crew ? (
          <div aria-busy="true" className="flex flex-col gap-3 p-6">
            <Skeleton className="size-16 rounded-full" />
            <Skeleton className="h-6 w-1/2" />
          </div>
        ) : (
          <CrewBody orgId={orgId} crew={crew} />
        )}
      </div>
    </aside>,
    document.body,
  );
}

function useCrewPatch(orgId: string, crew: Crew) {
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const t = useT();
  return useOptimisticMutation<Omit<CrewPatch, "version">, Crew, Crew>(orgId, {
    queryKey: keys.crew(orgId, crew.id),
    mutationFn: (changes) => {
      const current =
        queryClient.getQueryData<Crew>(keys.crew(orgId, crew.id)) ?? crew;
      return api.crews.update(orgId, current.space_id, current.id, {
        ...changes,
        version: current.version,
      });
    },
    optimistic: (data, changes) => {
      const next = { ...data, ...changes } as Crew;
      if (changes.leader_agent_id) {
        const leader = data.members.find(
          (m) => m.id === changes.leader_agent_id,
        );
        if (leader)
          next.leader = {
            id: leader.id,
            name: leader.name,
            title: leader.title ?? "",
          };
      }
      return next;
    },
    onSuccess: (saved) => {
      storeCrew(queryClient, orgId, saved);
      toast(t("agents.toast.saved"));
    },
  });
}

function CrewBody({ orgId, crew }: { orgId: string; crew: Crew }) {
  const t = useT();
  const navigate = useNavigate();
  const spaces = useSpaces(orgId);
  const directory = useDirectory(orgId);
  const patch = useCrewPatch(orgId, crew);
  const favorite = useFavoriteEntry(orgId);
  const archive = useArchiveEntry(orgId);
  const [editing, setEditing] = useState<null | "name" | "description">(null);
  const [access, setAccess] = useState(false);
  const [assign, setAssign] = useState(false);
  const [confirmArchive, setConfirmArchive] = useState(false);
  const [open, setOpen] = useState<Set<string>>(() => new Set(["members"]));
  const toggle = (id: string) =>
    setOpen((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const space = spaces.data?.find((s) => s.id === crew.space_id);
  const target = {
    type: "crew" as const,
    id: crew.id,
    space_id: crew.space_id,
    name: crew.name,
    version: crew.version,
  };
  const agentMembers = crew.members.filter((m) => m.kind === "AGENT");
  // 队长所在空间：优先看目录，找不到时就是小队当前空间
  const leaderSpace =
    directory.data?.entries.find(
      (e) => e.type === "agent" && e.id === crew.leader?.id,
    )?.space_id ?? crew.space_id;

  return (
    <div className="flex flex-col gap-4 px-6 pb-8">
      <section className="flex flex-col gap-3 pt-2">
        <div className="flex items-center gap-4">
          <CrewAvatar name={crew.name} members={agentMembers} size={64} />
          <Stats
            items={[
              {
                label: t("agents.crew.stats.members"),
                value: crew.member_count,
              },
              {
                label: t("agents.crew.stats.running"),
                value: crew.running_task_count,
              },
            ]}
          />
        </div>
        <div className="flex items-center gap-2 text-title">
          {editing === "name" ? (
            <InlineEdit
              value={crew.name}
              label={t("agents.drawer.editName")}
              maxLength={255}
              onCommit={(name) => patch.mutate({ name })}
              onDone={() => setEditing(null)}
              className="max-w-[320px]"
            />
          ) : (
            <button
              type="button"
              onClick={() => setEditing("name")}
              className="truncate font-bold hover:underline"
            >
              {crew.name}
            </button>
          )}
          {crew.archived_at && (
            <Badge tone="muted">{t("agents.drawer.archived")}</Badge>
          )}
        </div>
        {editing === "description" ? (
          <InlineEdit
            value={crew.description}
            label={t("agents.drawer.editDescription")}
            maxLength={10000}
            onCommit={(description) => patch.mutate({ description })}
            onDone={() => setEditing(null)}
          />
        ) : (
          <button
            type="button"
            onClick={() => setEditing("description")}
            className="self-start text-left text-body text-fg-muted hover:underline"
          >
            {crew.description || t("agents.drawer.noDescription")}
          </button>
        )}
        <button
          type="button"
          aria-label={t("agents.drawer.access", {
            space: space?.name ?? crew.space_name,
            visibility: t(`agents.visibility.${crew.visibility}`),
            owner: crew.mine ? t("agents.you") : crew.owner_email,
          })}
          onClick={() => setAccess(true)}
          className="flex items-center gap-1.5 self-start rounded-md px-1 py-0.5 text-body hover:bg-surface-hover"
        >
          <Icon name="orbit" className="text-fg-muted" />
          <span>{space?.name ?? crew.space_name}</span>
          <span className="text-fg-muted">·</span>
          <span className="text-fg-muted">
            {t(`agents.visibility.${crew.visibility}`)}
          </span>
          <span className="text-fg-muted">·</span>
          <span>{crew.mine ? t("agents.you") : crew.owner_email}</span>
        </button>
        <div className="flex items-center gap-2 pt-1">
          <Button
            className="flex-1"
            leading={<Icon name="plus" />}
            disabled={Boolean(crew.archived_at)}
            onClick={() => setAssign(true)}
          >
            {t("agents.drawer.assign")}
          </Button>
          <Button
            variant="outline"
            className="flex-1"
            leading={<Icon name="message" />}
            onClick={() =>
              navigate(chatPath(orgId, { type: "crew", id: crew.id }))
            }
          >
            {t("agents.drawer.chat")}
          </Button>
          <IconButton
            variant="outline"
            label={
              crew.favorite
                ? t("agents.card.unfavorite")
                : t("agents.card.favorite")
            }
            active={crew.favorite}
            icon={
              <Icon
                name="star"
                className={
                  crew.favorite ? "fill-current text-warning" : undefined
                }
              />
            }
            onClick={() =>
              favorite.mutate({
                target: { type: "crew", id: crew.id },
                favorite: !crew.favorite,
              })
            }
          />
          <Menu
            label={t("agents.drawer.more")}
            placement="bottom-end"
            trigger={({ ref, toggle: toggleMenu, open: menuOpen }) => (
              <IconButton
                ref={ref}
                variant="outline"
                label={t("agents.drawer.more")}
                icon={<Icon name="ellipsis" />}
                active={menuOpen}
                onClick={toggleMenu}
              />
            )}
          >
            <MenuItem
              danger
              icon={<Icon name="archive" />}
              disabled={Boolean(crew.archived_at)}
              onSelect={() => setConfirmArchive(true)}
            >
              {t("agents.drawer.archive")}
            </MenuItem>
          </Menu>
        </div>
      </section>

      <SettingsCard
        id="crew-section-instructions"
        icon="notebook-pen"
        title={t("agents.crew.instructions.title")}
        desc={t("agents.crew.instructions.desc")}
        summary={
          crew.instructions ? undefined : t("agents.crew.instructions.unset")
        }
        open={open.has("instructions")}
        onToggle={() => toggle("instructions")}
      >
        <CrewInstructions
          crew={crew}
          saving={patch.isPending}
          onSave={(instructions) => patch.mutate({ instructions })}
        />
      </SettingsCard>
      <SettingsCard
        id="crew-section-members"
        icon="users"
        title={t("agents.crew.members.title")}
        desc={t("agents.crew.members.desc")}
        summary={String(crew.member_count)}
        open={open.has("members")}
        onToggle={() => toggle("members")}
      >
        <CrewMembers
          orgId={orgId}
          crew={crew}
          onSetLeader={(id) => patch.mutate({ leader_agent_id: id })}
        />
      </SettingsCard>
      <SettingsCard
        id="crew-section-run"
        icon="list-checks"
        title={t("agents.crew.runSettings.title")}
        desc={t("agents.crew.runSettings.desc")}
        open={open.has("run")}
        onToggle={() => toggle("run")}
      >
        <div className="flex items-center gap-4">
          <div className="flex min-w-0 flex-1 flex-col gap-0.5">
            <span className="text-body font-medium">
              {t("agents.crew.runSettings.sharedDir")}
            </span>
            <span className="text-caption text-fg-muted">
              {t("agents.crew.runSettings.sharedDirDesc")}
            </span>
          </div>
          <Switch
            checked={crew.shared_task_dir}
            label={t("agents.crew.runSettings.sharedDir")}
            onCheckedChange={(shared_task_dir) =>
              patch.mutate({ shared_task_dir })
            }
          />
        </div>
      </SettingsCard>
      <DangerCard
        desc={t("agents.sections.archive.crewDesc")}
        archived={Boolean(crew.archived_at)}
        onArchive={() => setConfirmArchive(true)}
        onRestore={() => archive.mutate({ target, archived: false })}
      />

      <ConfirmDialog
        open={confirmArchive}
        danger
        title={t("agents.archive.crewTitle")}
        description={t("agents.archive.crewDesc", { name: crew.name })}
        confirmLabel={t("agents.archive.confirm")}
        loading={archive.isPending}
        onCancel={() => setConfirmArchive(false)}
        onConfirm={() =>
          archive.mutate(
            { target, archived: true },
            { onSettled: () => setConfirmArchive(false) },
          )
        }
      />
      {access && (
        <AccessDialog
          orgId={orgId}
          ownerEmail={crew.owner_email}
          initial={{
            spaceId: crew.space_id,
            visibility: crew.visibility,
            ownerId: crew.owner_user_id,
          }}
          saving={patch.isPending}
          block={(value) =>
            value.spaceId !== crew.space_id &&
            crew.leader &&
            leaderSpace !== value.spaceId
              ? t("agents.access.leaderMismatch", { name: crew.leader.name })
              : null
          }
          onClose={() => setAccess(false)}
          onSave={(changes) =>
            patch.mutate(
              {
                ...(changes.spaceId ? { space_id: changes.spaceId } : {}),
                ...(changes.visibility
                  ? { visibility: changes.visibility }
                  : {}),
                ...(changes.ownerId ? { owner_user_id: changes.ownerId } : {}),
              },
              { onSuccess: () => setAccess(false) },
            )
          }
        />
      )}
      {assign && (
        <AssignTaskPalette
          orgId={orgId}
          target={{
            kind: "crew",
            id: crew.id,
            name: crew.name,
            spaceId: crew.space_id,
          }}
          onClose={() => setAssign(false)}
        />
      )}
    </div>
  );
}

function CrewInstructions({
  crew,
  saving,
  onSave,
}: {
  crew: Crew;
  saving: boolean;
  onSave: (value: string) => void;
}) {
  const t = useT();
  const [draft, setDraft] = useState(crew.instructions);
  useEffect(() => setDraft(crew.instructions), [crew.instructions]);
  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <span className="text-caption text-fg-muted">
          {t("agents.sections.instructions.chars", { count: draft.length })}
        </span>
        <Button
          size="28"
          disabled={draft === crew.instructions}
          loading={saving}
          onClick={() => onSave(draft)}
        >
          {t("common.save")}
        </Button>
      </div>
      <Textarea
        rows={8}
        aria-label={t("agents.crew.instructions.title")}
        value={draft}
        maxLength={100000}
        placeholder={t("agents.crew.instructions.placeholder")}
        onChange={(event) => setDraft(event.target.value)}
        className="bg-surface-canvas"
      />
    </div>
  );
}

/** 成员卡片：搜索、添加、成员行「···」（设为队长 / 专属 / 移出）。 */
function CrewMembers({
  orgId,
  crew,
  onSetLeader,
}: {
  orgId: string;
  crew: Crew;
  onSetLeader: (agentId: string) => void;
}) {
  const t = useT();
  const api = useApi();
  const queryClient = useQueryClient();
  const [query, setQuery] = useState("");
  const [adding, setAdding] = useState(false);
  const [exclusiveFor, setExclusiveFor] = useState<CrewMember | null>(null);
  const queryKey = keys.crew(orgId, crew.id);

  const exclusive = useOptimisticMutation<
    { member: CrewMember; value: boolean },
    Crew,
    Crew
  >(orgId, {
    queryKey,
    mutationFn: ({ member, value }) =>
      api.crews.setExclusive(orgId, crew.space_id, crew.id, member.id, value),
    optimistic: (data, { member, value }) => ({
      ...data,
      members: data.members.map((m) =>
        m.kind === member.kind && m.id === member.id
          ? { ...m, exclusive: value }
          : m,
      ),
    }),
    onSuccess: (saved) => storeCrew(queryClient, orgId, saved),
  });
  const remove = useOptimisticMutation<CrewMember, void, Crew>(orgId, {
    queryKey,
    mutationFn: (member) =>
      api.crews.removeMember(
        orgId,
        crew.space_id,
        crew.id,
        member.kind === "AGENT" ? "agent" : "human",
        member.id,
      ),
    optimistic: (data, member) => ({
      ...data,
      members: data.members.filter(
        (m) => !(m.kind === member.kind && m.id === member.id),
      ),
      member_count: Math.max(0, data.member_count - 1),
    }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey });
      void queryClient.invalidateQueries({
        queryKey: keys.agentDirectory(orgId),
      });
    },
  });

  const needle = query.trim().toLowerCase();
  const list = crew.members.filter(
    (m) => !needle || m.name.toLowerCase().includes(needle),
  );

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-2">
        <TextInput
          className="flex-1"
          type="search"
          leading={<Icon name="search" />}
          aria-label={t("agents.crew.members.search")}
          placeholder={t("agents.crew.members.search")}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <Button
          leading={<Icon name="plus" />}
          disabled={crew.member_count >= CREW_LIMIT}
          title={
            crew.member_count >= CREW_LIMIT
              ? t("agents.crew.members.limit")
              : undefined
          }
          onClick={() => setAdding(true)}
        >
          {t("agents.crew.members.add")}
        </Button>
      </div>
      {list.length === 0 ? (
        <p className="py-3 text-center text-body-sm text-fg-muted">
          {t("agents.crew.members.noMatch")}
        </p>
      ) : (
        <ul className="flex flex-col">
          {list.map((member) => {
            const isLeader =
              member.kind === "AGENT" && member.id === crew.leader?.id;
            const role = isLeader
              ? t("agents.crew.members.leader")
              : member.kind === "HUMAN"
                ? t("agents.crew.members.memberRole")
                : member.title;
            return (
              <li
                key={`${member.kind}:${member.id}`}
                className="flex h-10 items-center gap-2.5 px-1"
                data-testid={`crew-member-${member.id}`}
              >
                {member.kind === "AGENT" ? (
                  <AgentAvatar
                    name={member.name}
                    size={24}
                    offline={member.online === false}
                  />
                ) : (
                  <PersonAvatar name={member.name} size={24} />
                )}
                <span className="truncate text-body font-medium">
                  {member.name}
                </span>
                {member.exclusive && (
                  <Badge tone="neutral">
                    {t("agents.crew.members.exclusive")}
                  </Badge>
                )}
                {role && (
                  <span className="truncate text-body text-fg-muted">
                    - {role}
                  </span>
                )}
                <span className="flex-1" />
                {member.active_tasks.length > 0 && (
                  <span className="font-mono text-caption text-fg-muted">
                    {member.active_tasks[0]}
                    {member.active_tasks.length > 1 &&
                      ` +${member.active_tasks.length - 1}`}
                  </span>
                )}
                <Menu
                  label={t("agents.crew.members.menu", { name: member.name })}
                  placement="bottom-end"
                  trigger={({ ref, toggle }) => (
                    <IconButton
                      ref={ref}
                      label={t("agents.crew.members.menu", {
                        name: member.name,
                      })}
                      icon={<Icon name="ellipsis" size={14} />}
                      size="28"
                      onClick={toggle}
                    />
                  )}
                >
                  {member.kind === "AGENT" && (
                    <MenuItem
                      icon={<Icon name="crown" />}
                      disabled={isLeader}
                      onSelect={() => onSetLeader(member.id)}
                    >
                      {t("agents.crew.members.setLeader")}
                    </MenuItem>
                  )}
                  {member.kind === "AGENT" && (
                    <MenuItem
                      icon={<Icon name="lock" />}
                      onSelect={() =>
                        member.exclusive
                          ? exclusive.mutate({ member, value: false })
                          : setExclusiveFor(member)
                      }
                    >
                      {member.exclusive
                        ? t("agents.crew.members.unsetExclusive")
                        : t("agents.crew.members.setExclusive")}
                    </MenuItem>
                  )}
                  {member.kind === "AGENT" && <MenuSeparator />}
                  {isLeader ? (
                    <Tooltip
                      content={t("agents.crew.members.leaderCannotRemove")}
                    >
                      <span className="block">
                        <MenuItem
                          danger
                          disabled
                          icon={<Icon name="user-x" />}
                          disabledReason={t(
                            "agents.crew.members.leaderCannotRemove",
                          )}
                        >
                          {t("agents.crew.members.remove")}
                        </MenuItem>
                      </span>
                    </Tooltip>
                  ) : (
                    <MenuItem
                      danger
                      icon={<Icon name="user-x" />}
                      onSelect={() => remove.mutate(member)}
                    >
                      {t("agents.crew.members.remove")}
                    </MenuItem>
                  )}
                </Menu>
              </li>
            );
          })}
        </ul>
      )}
      <ConfirmDialog
        open={exclusiveFor !== null}
        title={t("agents.crew.members.exclusiveTitle")}
        description={t("agents.crew.members.exclusiveDesc")}
        confirmLabel={t("agents.crew.members.exclusiveConfirm")}
        onCancel={() => setExclusiveFor(null)}
        onConfirm={() => {
          if (exclusiveFor)
            exclusive.mutate({ member: exclusiveFor, value: true });
          setExclusiveFor(null);
        }}
      />
      {adding && (
        <AddMembersDialog
          orgId={orgId}
          crew={crew}
          onClose={() => setAdding(false)}
        />
      )}
    </div>
  );
}

/** 添加小队成员：同空间未入队的 Agent 与可读该空间的成员。 */
function AddMembersDialog({
  orgId,
  crew,
  onClose,
}: {
  orgId: string;
  crew: Crew;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const spaces = useSpaces(orgId);
  const space = spaces.data?.find((s) => s.id === crew.space_id);
  const agents = useSpaceAgents(orgId, crew.space_id).data ?? [];
  const people = usePeople(orgId, space);
  const [selected, setSelected] = useState<string[]>([]);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const anchorRef = useRef<HTMLButtonElement | null>(null);
  const existing = new Set(
    crew.members.map((m) => `${m.kind === "AGENT" ? "agent" : "user"}:${m.id}`),
  );
  const room = CREW_LIMIT - crew.member_count;

  const toggle = (value: string) =>
    setSelected((list) => {
      if (list.includes(value)) return list.filter((v) => v !== value);
      if (list.length >= room) {
        toast(t("agents.crew.members.limit"), { tone: "error" });
        return list;
      }
      return [...list, value];
    });

  const names = selected
    .map((value) =>
      value.startsWith("agent:")
        ? agents.find((a) => `agent:${a.id}` === value)?.name
        : people.find((p) => `user:${p.id}` === value)?.email,
    )
    .filter(Boolean);

  const submit = async () => {
    setBusy(true);
    try {
      const saved = await api.crews.addMembers(orgId, crew.space_id, crew.id, {
        agent_ids: selected
          .filter((v) => v.startsWith("agent:"))
          .map((v) => v.slice(6)),
        user_ids: selected
          .filter((v) => v.startsWith("user:"))
          .map((v) => v.slice(5)),
      });
      storeCrew(queryClient, orgId, saved);
      onClose();
    } catch (error) {
      void queryClient.invalidateQueries({
        queryKey: keys.crew(orgId, crew.id),
      });
      toast(agentErrorMessage(t, error), { tone: "error" });
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={busy ? undefined : onClose}
      title={t("agents.crew.members.addTitle")}
      width={480}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={busy}
            disabled={selected.length === 0}
            onClick={() => void submit()}
          >
            {t("agents.crew.members.addSubmit")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-2 px-0.5 pb-0.5">
        <span className="text-body font-medium">
          {t("agents.crew.members.addLabel")}
        </span>
        <button
          ref={anchorRef}
          type="button"
          aria-expanded={pickerOpen}
          onClick={() => setPickerOpen((v) => !v)}
          className="flex h-9 w-full items-center gap-2 rounded-lg border border-line-default px-3 text-left text-body"
        >
          <Icon name="users" className="text-fg-muted" />
          <span
            className={cn(
              "min-w-0 flex-1 truncate",
              !names.length && "text-fg-subtle",
            )}
          >
            {names.length
              ? names.join("、")
              : t("agents.crewCreate.membersPlaceholder")}
          </span>
          <Icon name="chevron-down" className="text-fg-muted" />
        </button>
      </div>
      <SearchMenu
        open={pickerOpen}
        onOpenChange={setPickerOpen}
        anchorRef={anchorRef}
        multiple
        label={t("agents.crew.members.addLabel")}
        placeholder={t("agents.crewCreate.membersSearch")}
        selected={selected}
        onSelect={toggle}
        className="w-[420px]"
        groups={[
          {
            id: "agents",
            label: t("agents.crewCreate.agentsGroup"),
            options: agents
              .filter(
                (a) =>
                  !a.archived_at &&
                  !a.builtin_kind &&
                  !existing.has(`agent:${a.id}`),
              )
              .map((a) => ({
                value: `agent:${a.id}`,
                label: a.name,
                keywords: a.title,
                icon: <AgentAvatar name={a.name} avatar={a.avatar} size={20} />,
              })),
          },
          {
            id: "people",
            label: t("agents.crewCreate.peopleGroup"),
            options: people
              .filter((p) => !existing.has(`user:${p.id}`))
              .map((p) => ({
                value: `user:${p.id}`,
                label: p.email,
                icon: <PersonAvatar name={p.email} size={20} />,
              })),
          },
        ]}
      />
    </Dialog>
  );
}
