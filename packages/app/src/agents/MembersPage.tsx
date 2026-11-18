import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";

import type { ContactItem } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Skeleton,
  Tooltip,
  cn,
} from "@openfox/ui";

import { AgentAvatar, CrewAvatar, PersonAvatar } from "./AgentAvatar";
import { chatPath } from "./AgentsPage";
import { AssignTaskPalette } from "./AssignTaskPalette";
import { CreateAgentDialog } from "./CreateAgentDialog";
import { CreateCrewEntry } from "./CreateCrewDialog";
import { InviteDialog } from "./InviteDialog";
import { parseContact, runtimeLabel } from "./model";
import { useAgentSettingsParam, useCrewSettingsParam } from "./params";
import { SidebarCollapseButton } from "../shell/SidebarCollapseButton";
import {
  rememberSpace,
  useContacts,
  useMe,
  useOrgRole,
  useSpaces,
} from "./queries";

const contactValue = (item: Pick<ContactItem, "kind" | "id">) =>
  `${item.kind}:${item.id}`;

export function MembersNavigation({
  orgId,
  onCollapse,
}: {
  orgId: string;
  onCollapse: () => void;
}) {
  const t = useT();
  const contacts = useContacts(orgId);
  const role = useOrgRole(orgId);
  const [params, setParams] = useSearchParams();
  const agentDrawer = useAgentSettingsParam();
  const crewDrawer = useCrewSettingsParam();
  const [query, setQuery] = useState("");
  // 折叠状态不持久化，默认全部展开
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());
  const [dialog, setDialog] = useState<null | "agent" | "crew" | "invite">(
    null,
  );
  const selected = params.get("contact");

  const needle = query.trim().toLowerCase();
  const groups = (contacts.data?.groups ?? [])
    .map((group) => ({
      ...group,
      key: group.kind === "SPACE" ? `space:${group.space_id}` : group.kind,
      items: group.items.filter(
        (item) => !needle || item.name.toLowerCase().includes(needle),
      ),
    }))
    .filter((group) => group.items.length > 0);

  const select = (item: ContactItem) => {
    if (item.space_id) rememberSpace(item.id, item.space_id);
    setParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set("contact", contactValue(item));
      return next;
    });
  };

  return (
    <nav
      aria-label={t("agents.contacts.title")}
      className="group/panel flex min-w-0 flex-1 flex-col overflow-hidden px-1.5 pt-1.5 pb-3"
    >
      <div className="flex h-11 shrink-0 items-center gap-1 pr-1 pl-2.5">
        <h2 className="min-w-0 flex-1 truncate text-[15px] font-bold">
          {t("agents.contacts.title")}
        </h2>
        <SidebarCollapseButton onClick={onCollapse} />
        <Menu
          label={t("agents.contacts.newMenu")}
          placement="bottom-end"
          trigger={({ ref, toggle, open }) => (
            <IconButton
              ref={ref}
              label={t("agents.contacts.newMenu")}
              icon={<Icon name="circle-plus" />}
              size="28"
              active={open}
              aria-expanded={open}
              onClick={toggle}
            />
          )}
        >
          <MenuItem
            icon={<Icon name="bot" />}
            onSelect={() => setDialog("agent")}
          >
            {t("agents.contacts.newAgent")}
          </MenuItem>
          <MenuItem
            icon={<Icon name="users" />}
            onSelect={() => setDialog("crew")}
          >
            {t("agents.contacts.newCrew")}
          </MenuItem>
          {(role === "OWNER" || role === "ADMIN") && (
            <MenuItem
              icon={<Icon name="user-plus" />}
              onSelect={() => setDialog("invite")}
            >
              {t("agents.contacts.invite")}
            </MenuItem>
          )}
        </Menu>
      </div>
      <label className="mx-1 mb-2 flex h-8 items-center gap-2 rounded-lg bg-surface-muted px-2.5 text-body">
        <Icon name="search" className="text-fg-muted" />
        <input
          type="search"
          aria-label={t("agents.contacts.search")}
          placeholder={t("agents.contacts.search")}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          className="min-w-0 flex-1 bg-transparent outline-none placeholder:text-fg-subtle focus-visible:shadow-none"
        />
      </label>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {contacts.isLoading ? (
          <div className="flex flex-col gap-2 px-1">
            {Array.from({ length: 5 }, (_, i) => (
              <Skeleton key={i} className="h-11" />
            ))}
          </div>
        ) : contacts.isError ? (
          <div className="flex flex-col items-center gap-2 px-2 py-6 text-center text-body-sm text-fg-muted">
            {t("agents.contacts.loadFailed")}
            <Button
              variant="outline"
              size="28"
              onClick={() => void contacts.refetch()}
            >
              {t("common.retry")}
            </Button>
          </div>
        ) : groups.length === 0 ? (
          <p className="px-2.5 py-4 text-body-sm text-fg-muted">
            {t("agents.contacts.empty")}
          </p>
        ) : (
          groups.map((group) => {
            const label =
              group.kind === "HUMANS"
                ? t("agents.contacts.humans")
                : group.kind === "UNASSIGNED"
                  ? t("agents.directory.unassigned")
                  : (group.space_name ?? "");
            const folded = collapsed.has(group.key);
            return (
              <section key={group.key} aria-label={label}>
                <button
                  type="button"
                  aria-expanded={!folded}
                  onClick={() =>
                    setCollapsed((prev) => {
                      const next = new Set(prev);
                      if (next.has(group.key)) next.delete(group.key);
                      else next.add(group.key);
                      return next;
                    })
                  }
                  className="flex h-7 items-center gap-1 px-2 text-[13px] font-medium text-fg-muted hover:text-fg-default"
                >
                  {label}
                  <Icon
                    name="caret-right"
                    size={10}
                    className={cn(
                      "transition-transform",
                      !folded && "rotate-90",
                    )}
                  />
                </button>
                {!folded &&
                  group.items.map((item) => (
                    <ContactRow
                      key={contactValue(item)}
                      item={item}
                      selected={selected === contactValue(item)}
                      onSelect={() => select(item)}
                    />
                  ))}
              </section>
            );
          })
        )}
        {contacts.data?.truncated && (
          <p className="px-2.5 py-2 text-caption text-fg-muted">
            {t("agents.contacts.truncated")}
          </p>
        )}
      </div>
      {dialog === "agent" && (
        <CreateAgentDialog
          orgId={orgId}
          onClose={() => setDialog(null)}
          onCreated={(agent) => {
            setDialog(null);
            agentDrawer.open(agent.id, agent.space_id);
          }}
        />
      )}
      {dialog === "crew" && (
        <CreateCrewEntry
          orgId={orgId}
          onClose={() => setDialog(null)}
          onCreateAgent={() => setDialog("agent")}
          onCreated={(crew) => {
            setDialog(null);
            crewDrawer.open(crew.id, crew.space_id);
          }}
        />
      )}
      {dialog === "invite" && (
        <InviteDialog orgId={orgId} onClose={() => setDialog(null)} />
      )}
    </nav>
  );
}

function ContactAvatar({ item, size }: { item: ContactItem; size: 28 | 64 }) {
  if (item.kind === "user")
    return <PersonAvatar name={item.name} size={size} />;
  if (item.kind === "crew")
    return <CrewAvatar name={item.name} members={[]} size={size} />;
  return (
    <AgentAvatar
      name={item.name}
      avatar={item.avatar}
      size={size}
      offline={item.online === false}
    />
  );
}

/** 联系人行：头像 28、名字 + 「- 职位」；小队、真人只显示名字。 */
function ContactRow({
  item,
  selected,
  onSelect,
}: {
  item: ContactItem;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      aria-current={selected || undefined}
      onClick={onSelect}
      className={cn(
        "flex h-11 w-full items-center gap-2.5 rounded-md px-2 text-left hover:bg-surface-hover",
        selected && "bg-surface-active",
      )}
    >
      <ContactAvatar item={item} size={28} />
      <span className="truncate text-[13px] font-medium text-fg-default/70">
        {item.name}
      </span>
      {item.kind === "agent" && item.title && (
        <span className="truncate text-[13px] text-fg-default/40">
          - {item.title}
        </span>
      )}
    </button>
  );
}

/** 第一个 Agent（没有 ?contact= 时自动选中） */
export function firstAgent(groups: ReadonlyArray<{ items: ContactItem[] }>) {
  for (const group of groups)
    for (const item of group.items) if (item.kind === "agent") return item;
  return undefined;
}

/** 成员页签主区：?contact=agent|crew|user:<id> 决定详情；没有参数时自动选中第一个 Agent。 */
export function MembersPage() {
  const t = useT();
  const { orgId = "" } = useParams();
  const contacts = useContacts(orgId);
  const [params, setParams] = useSearchParams();
  const contact = parseContact(params.get("contact"));

  // 没有 ?contact=，或记住的联系人已不存在（删除 / 无权访问）时，兜底选中第一个 Agent；
  // 一个 Agent 也没有时去掉失效参数
  useEffect(() => {
    if (!contacts.data) return;
    const current = parseContact(params.get("contact"));
    const exists =
      current &&
      contacts.data.groups.some((group) =>
        group.items.some(
          (entry) => entry.kind === current.kind && entry.id === current.id,
        ),
      );
    if (exists) return;
    // 目录被截断时联系人可能只是不在前 N 条里，不能据此改地址
    if (current && contacts.data.truncated) return;
    const first = firstAgent(contacts.data.groups);
    if (!first && !current) return;
    if (first?.space_id) rememberSpace(first.id, first.space_id);
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        if (first) next.set("contact", contactValue(first));
        else next.delete("contact");
        return next;
      },
      { replace: true },
    );
  }, [contacts.data, params, setParams]);

  const item = useMemo(() => {
    if (!contact) return undefined;
    for (const group of contacts.data?.groups ?? [])
      for (const entry of group.items)
        if (entry.kind === contact.kind && entry.id === contact.id)
          return entry;
    return undefined;
  }, [contacts.data, contact]);

  return (
    <div className="flex h-full items-center justify-center p-8">
      {contacts.isLoading ? (
        <Skeleton className="size-16 rounded-full" />
      ) : item ? (
        <ContactDetail orgId={orgId} item={item} />
      ) : (
        <div className="flex flex-col items-center gap-2 text-center">
          <Icon name="users" size={28} className="text-fg-muted" />
          <p className="text-body font-medium">
            {t("agents.contacts.pickTitle")}
          </p>
          <p className="text-body-sm text-fg-muted">
            {t("agents.contacts.pickDesc")}
          </p>
        </div>
      )}
    </div>
  );
}

/** 禁用但仍能显示 Tooltip 的按钮（aria-disabled，不用 disabled 属性以保留悬停事件）。 */
function GatedButton({
  blocked,
  reason,
  children,
  ...rest
}: {
  blocked: boolean;
  reason: string;
  children: ReactNode;
} & React.ComponentProps<typeof Button>) {
  if (!blocked) return <Button {...rest}>{children}</Button>;
  return (
    <Tooltip content={reason}>
      <Button
        {...rest}
        aria-disabled="true"
        onClick={(event) => event.preventDefault()}
        className={cn(rest.className, "cursor-not-allowed opacity-50")}
      >
        {children}
      </Button>
    </Tooltip>
  );
}

function ContactDetail({ orgId, item }: { orgId: string; item: ContactItem }) {
  const t = useT();
  const navigate = useNavigate();
  const me = useMe();
  const spaces = useSpaces(orgId);
  const agentDrawer = useAgentSettingsParam();
  const crewDrawer = useCrewSettingsParam();
  const [assign, setAssign] = useState(false);
  const spaceId = item.space_id ?? undefined;
  const space = spaces.data?.find((s) => s.id === item.space_id);
  const owner = item.owner
    ? item.owner.id === me.data?.id
      ? t("agents.you")
      : item.owner.email
    : null;

  const subtitle = item.kind === "user" ? item.email : item.description;
  const meta = [
    item.kind === "agent" && item.runtime ? (
      <span key="runtime" className="flex items-center gap-1">
        <Icon name="terminal" size={14} />
        {runtimeLabel(item.runtime)}
      </span>
    ) : null,
    space ? (
      <span key="space" className="flex items-center gap-1">
        <Icon name="orbit" size={14} />
        {space.name}
      </span>
    ) : null,
    owner ? (
      <span key="owner" className="flex items-center gap-1">
        <PersonAvatar name={item.owner!.email} size={16} />
        {owner}
      </span>
    ) : null,
  ].filter(Boolean);

  return (
    <div className="flex max-w-[520px] flex-col items-center gap-4 text-center">
      <ContactAvatar item={item} size={64} />
      <h1 className="text-title font-bold">
        {item.name}
        {item.kind === "agent" && item.title && (
          <>
            {" - "}
            <span className="font-medium text-fg-muted">{item.title}</span>
          </>
        )}
      </h1>
      {subtitle && <p className="text-body text-fg-muted">{subtitle}</p>}
      {meta.length > 0 && (
        <div className="flex items-center gap-2 text-body-sm text-fg-secondary">
          {meta.map((node, index) => (
            <span key={index} className="flex items-center gap-2">
              {index > 0 && <span className="text-fg-muted">·</span>}
              {node}
            </span>
          ))}
        </div>
      )}
      {item.kind === "user" ? (
        <GatedButton
          blocked={false}
          reason=""
          onClick={() =>
            navigate(`/o/${orgId}/chat?user=${encodeURIComponent(item.id)}`)
          }
          leading={<Icon name="message" />}
        >
          {t("agents.contacts.chat")}
        </GatedButton>
      ) : (
        <div className="flex items-center gap-2">
          <GatedButton
            blocked={item.kind === "agent" && item.allow_assign === false}
            reason={t("agents.contacts.cannotAssign")}
            className="min-w-40"
            leading={<Icon name="plus" />}
            onClick={() => setAssign(true)}
          >
            {t("agents.contacts.assign")}
          </GatedButton>
          <GatedButton
            blocked={item.kind === "agent" && item.allow_direct_chat === false}
            reason={t("agents.contacts.cannotChat")}
            variant="outline"
            className="min-w-40"
            leading={<Icon name="message" />}
            onClick={() =>
              navigate(
                chatPath(orgId, {
                  type: item.kind === "crew" ? "crew" : "agent",
                  id: item.id,
                }),
              )
            }
          >
            {t("agents.contacts.chat")}
          </GatedButton>
          <IconButton
            variant={item.kind === "crew" ? "secondary" : "outline"}
            label={t("agents.contacts.settings")}
            icon={<Icon name="sliders-horizontal" />}
            onClick={() =>
              item.kind === "agent"
                ? agentDrawer.open(item.id, spaceId)
                : crewDrawer.open(item.id, spaceId)
            }
          />
        </div>
      )}
      {assign && spaceId && item.kind !== "user" && (
        <AssignTaskPalette
          orgId={orgId}
          target={{ kind: item.kind, id: item.id, name: item.name, spaceId }}
          onClose={() => setAssign(false)}
        />
      )}
    </div>
  );
}
