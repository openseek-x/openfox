import { useMemo, useRef, useState, type RefObject } from "react";
import { useNavigate } from "react-router-dom";

import type { AgentDirectoryEntry } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Button, Icon, Popover, Spinner, cn } from "@openfox/ui";

import { AgentAvatar, PersonAvatar } from "../agents/AgentAvatar";
import { CreateAgentDialog } from "../agents/CreateAgentDialog";
import { useAgentSettingsParam } from "../agents/params";
import { useDirectory, useContacts, useMe } from "../agents/queries";

/** 新会话地址：?agent= / ?crew= 预选 */
export const newChatPath = (
  orgId: string,
  target: { type: "agent" | "crew"; id: string },
) => `/o/${orgId}/chat?${target.type}=${encodeURIComponent(target.id)}`;

/**
 * 新建对话浮层：搜索 Agent 或小队，按空间分组；
 * 第一项是小助手；没有任何 Agent 时显示空态并引导创建。
 */
export function NewChatPopover({
  orgId,
  open,
  onOpenChange,
  anchorRef,
}: {
  orgId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  anchorRef: RefObject<HTMLElement | null>;
}) {
  const t = useT();
  const navigate = useNavigate();
  const directory = useDirectory(orgId, open);
  const contacts = useContacts(orgId);
  const me = useMe();
  const people =
    contacts.data?.groups
      .flatMap((g) => g.items)
      .filter((c) => c.kind === "user" && c.id !== me.data?.id) ?? [];
  const settings = useAgentSettingsParam();
  const [query, setQuery] = useState("");
  const [creating, setCreating] = useState(false);
  const matchingPeople = people.filter((c) =>
    c.name.toLowerCase().includes(query.trim().toLowerCase()),
  );

  const entries = useMemo(
    () => (directory.data?.entries ?? []).filter((entry) => !entry.archived_at),
    [directory.data],
  );
  const groups = useMemo(() => {
    const q = query.trim().toLowerCase();
    const matched = entries.filter(
      (entry) =>
        !q ||
        entry.name.toLowerCase().includes(q) ||
        entry.title.toLowerCase().includes(q),
    );
    const bySpace = new Map<
      string,
      { name: string; items: AgentDirectoryEntry[] }
    >();
    for (const entry of matched) {
      const group = bySpace.get(entry.space_id) ?? {
        name: entry.space_name,
        items: [],
      };
      group.items.push(entry);
      bySpace.set(entry.space_id, group);
    }
    return [...bySpace.entries()].map(([id, group]) => ({ id, ...group }));
  }, [entries, query]);

  const pick = (entry: AgentDirectoryEntry) => {
    onOpenChange(false);
    navigate(newChatPath(orgId, entry));
  };

  return (
    <>
      <Popover
        open={open}
        onOpenChange={onOpenChange}
        anchorRef={anchorRef}
        placement="bottom-start"
        aria-label={t("chat.newChat")}
        className="w-64 p-0"
      >
        {directory.isLoading ? (
          <div className="flex justify-center py-6">
            <Spinner />
          </div>
        ) : entries.length === 0 && people.length === 0 ? (
          <div className="flex flex-col items-center gap-2 px-4 py-5 text-center">
            <Icon name="bot-face" size={20} />
            <p className="text-body font-semibold">
              {t("chat.newChatPopover.noAgents")}
            </p>
            <p className="text-body-sm text-fg-muted">
              {t("chat.newChatPopover.noAgentsDesc")}
            </p>
            <Button
              variant="outline"
              size="28"
              onClick={() => {
                onOpenChange(false);
                setCreating(true);
              }}
            >
              {t("chat.newChatPopover.createAgent")}
            </Button>
          </div>
        ) : (
          <div className="flex flex-col">
            <input
              data-autofocus
              autoFocus
              aria-label={t("chat.newChatPopover.search")}
              placeholder={t("chat.newChatPopover.search")}
              value={query}
              maxLength={100}
              onChange={(event) => setQuery(event.target.value)}
              className="h-10 border-b border-line-default bg-transparent px-3 text-body outline-none placeholder:text-fg-subtle"
            />
            <div
              role="listbox"
              aria-label={t("chat.newChat")}
              className="max-h-[360px] overflow-y-auto p-1"
            >
              {!query.trim() && (
                <PickRow
                  icon={
                    <span className="flex size-5 items-center justify-center rounded-full bg-surface-muted">
                      <Icon name="bot-face" size={12} />
                    </span>
                  }
                  name={t("chat.newChatPopover.assistant")}
                  onPick={() => {
                    onOpenChange(false);
                    navigate(`/o/${orgId}/assistant`);
                  }}
                />
              )}
              {matchingPeople.map((person) => (
                <PickRow
                  key={person.id}
                  name={person.name}
                  icon={<PersonAvatar name={person.name} size={20} />}
                  onPick={() => {
                    onOpenChange(false);
                    navigate(
                      `/o/${orgId}/chat?user=${encodeURIComponent(person.id)}`,
                    );
                  }}
                />
              ))}
              {groups.length === 0 && matchingPeople.length === 0 && (
                <p className="px-2.5 py-3 text-center text-body-sm text-fg-muted">
                  {t("chat.newChatPopover.noMatch")}
                </p>
              )}
              {groups.map((group, index) => (
                <div key={group.id}>
                  {(index > 0 || groups.length > 1) && (
                    <div className="px-2.5 pt-2 pb-1 text-caption text-fg-muted">
                      {group.name}
                    </div>
                  )}
                  {group.items.map((entry) => {
                    const disabled =
                      entry.type === "agent" &&
                      entry.allow_direct_chat === false;
                    return (
                      <PickRow
                        key={`${entry.type}:${entry.id}`}
                        icon={
                          entry.type === "crew" ? (
                            <SmallCrewIcon />
                          ) : (
                            <AgentAvatar
                              name={entry.name}
                              avatar={entry.avatar}
                              size={20}
                              offline={!entry.online}
                            />
                          )
                        }
                        name={entry.name}
                        hint={entry.title}
                        trailing={
                          entry.type === "crew" ? (
                            <Badge small tone="muted">
                              {t("chat.newChatPopover.crew")}
                            </Badge>
                          ) : entry.visibility === "PRIVATE" ? (
                            <Icon name="lock" size={12} />
                          ) : undefined
                        }
                        disabled={disabled}
                        disabledReason={t("chat.newChatPopover.directDisabled")}
                        onPick={() => pick(entry)}
                      />
                    );
                  })}
                </div>
              ))}
            </div>
          </div>
        )}
      </Popover>
      {creating && (
        <CreateAgentDialog
          orgId={orgId}
          onClose={() => setCreating(false)}
          onCreated={(agent) => {
            setCreating(false);
            settings.open(agent.id, agent.space_id);
          }}
        />
      )}
    </>
  );
}

/** 小队的小号图标（CrewAvatar 最小 28，列表行用 20 的圆形图标） */
export function SmallCrewIcon() {
  return (
    <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-surface-muted text-fg-secondary">
      <Icon name="users" size={12} />
    </span>
  );
}

function PickRow({
  icon,
  name,
  hint,
  trailing,
  disabled,
  disabledReason,
  onPick,
}: {
  icon: React.ReactNode;
  name: string;
  hint?: string;
  trailing?: React.ReactNode;
  disabled?: boolean;
  disabledReason?: string;
  onPick: () => void;
}) {
  return (
    <button
      type="button"
      role="option"
      aria-selected={false}
      aria-disabled={disabled || undefined}
      title={disabled ? disabledReason : undefined}
      onClick={() => {
        if (!disabled) onPick();
      }}
      className={cn(
        "flex h-9 w-full items-center gap-2 rounded-lg px-2.5 text-left text-body hover:bg-surface-active",
        disabled && "cursor-not-allowed opacity-50 hover:bg-transparent",
      )}
    >
      {icon}
      <span className="truncate">{name}</span>
      {hint && (
        <span className="min-w-0 flex-1 truncate text-caption text-fg-muted">
          {hint}
        </span>
      )}
      {!hint && <span className="flex-1" />}
      {trailing && <span className="flex text-fg-muted">{trailing}</span>}
    </button>
  );
}

/** 「新建对话」按钮 + 浮层 */
export function NewChatButton({
  orgId,
  className,
}: {
  orgId: string;
  className?: string;
}) {
  const t = useT();
  const ref = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        ref={ref}
        type="button"
        aria-label={t("chat.newChat")}
        title={t("chat.newChat")}
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className={cn(
          "flex size-7 items-center justify-center rounded-lg text-fg-secondary hover:bg-surface-muted",
          className,
        )}
      >
        <Icon name="circle-plus" size={18} />
      </button>
      <NewChatPopover
        orgId={orgId}
        open={open}
        onOpenChange={setOpen}
        anchorRef={ref}
      />
    </>
  );
}
