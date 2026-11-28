import { useRef, useState, type RefObject } from "react";
import { useNavigate } from "react-router-dom";

import { useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Icon,
  Popover,
  SearchMenu,
  Spinner,
  cn,
  type SearchMenuGroup,
} from "@openfox/ui";

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
 * 新建对话浮层（Figma Popover/NewChat）：搜索 Agent 或小队，按空间分组；
 * 第一项是小助手；没有任何 Agent 时显示空态并引导创建。
 */
export function NewChatPopover({
  orgId,
  open,
  onOpenChange,
  anchorRef,
  matchWidth,
}: {
  orgId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  anchorRef: RefObject<HTMLElement | null>;
  matchWidth?: boolean;
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
  const [creating, setCreating] = useState(false);
  const spaces = new Map<string, SearchMenuGroup>();
  for (const entry of directory.data?.entries ?? []) {
    if (entry.archived_at) continue;
    const group = spaces.get(entry.space_id) ?? {
      id: entry.space_id,
      label: entry.space_name,
      options: [],
    };
    const crew = entry.type === "crew";
    group.options.push({
      value: newChatPath(orgId, entry),
      label: entry.name,
      keywords: entry.title,
      description:
        [
          entry.title,
          !crew &&
            !entry.runtime &&
            !entry.online &&
            !entry.computer_name &&
            t("chat.newChatPopover.noComputer"),
        ]
          .filter(Boolean)
          .join(" · ") || undefined,
      icon: crew ? (
        <SmallCrewIcon />
      ) : (
        <AgentAvatar
          agentId={entry.id}
          spaceId={entry.space_id}
          name={entry.name}
          avatar={entry.avatar}
          size={20}
          offline={!entry.online}
        />
      ),
      trailing: crew ? (
        <Badge small tone="muted">
          {t("chat.newChatPopover.crew")}
        </Badge>
      ) : entry.visibility === "PRIVATE" ? (
        <Icon name="lock" size={12} />
      ) : undefined,
      disabled: !crew && entry.allow_direct_chat === false,
      disabledReason: t("chat.newChatPopover.directDisabled"),
    });
    spaces.set(entry.space_id, group);
  }
  const groups: SearchMenuGroup[] = [
    {
      id: "people",
      options: [
        {
          value: `/o/${orgId}/assistant`,
          label: t("chat.newChatPopover.assistant"),
          icon: <Icon name="bot-face" size={16} />,
        },
        ...people.map((person) => ({
          value: `/o/${orgId}/chat?user=${encodeURIComponent(person.id)}`,
          label: person.name,
          icon: <PersonAvatar name={person.name} size={20} />,
        })),
      ],
    },
    ...Array.from(spaces.values(), (group) =>
      spaces.size === 1 ? { ...group, label: undefined } : group,
    ),
  ];
  const loading = directory.isLoading || contacts.isLoading;
  const empty = spaces.size === 0 && people.length === 0;
  return (
    <>
      {loading || empty ? (
        <Popover
          open={open}
          onOpenChange={onOpenChange}
          anchorRef={anchorRef}
          placement="bottom-start"
          matchWidth={matchWidth}
          fitHeight
          aria-label={t("chat.newChat")}
          className="w-64 p-0"
        >
          {loading ? (
            <div className="flex justify-center py-6">
              <Spinner />
            </div>
          ) : (
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
          )}
        </Popover>
      ) : (
        <SearchMenu
          open={open}
          onOpenChange={onOpenChange}
          anchorRef={anchorRef}
          placement="bottom-start"
          matchWidth={matchWidth}
          fitHeight
          label={t("chat.newChat")}
          placeholder={t("chat.newChatPopover.search")}
          emptyText={t("chat.newChatPopover.noMatch")}
          groups={groups}
          selected={[]}
          onSelect={(to) => navigate(to)}
        />
      )}
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

/** 「新建对话」按钮 + 浮层 */
export function NewChatButton({
  orgId,
  className,
  landing = false,
}: {
  orgId: string;
  className?: string;
  landing?: boolean;
}) {
  const t = useT();
  const ref = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  const label = t(landing ? "chat.page.pick" : "chat.newChat");
  return (
    <>
      <button
        ref={ref}
        type="button"
        aria-label={label}
        aria-haspopup="dialog"
        title={label}
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className={cn(
          landing
            ? "flex min-h-12 w-full items-center gap-3 rounded-xl border border-line-default bg-surface-card px-4 text-left text-body text-fg-secondary shadow-card transition-[border-color,background-color] duration-150 hover:border-line-strong hover:bg-surface-hover motion-reduce:transition-none"
            : "flex size-7 items-center justify-center rounded-lg text-fg-secondary hover:bg-surface-muted",
          className,
        )}
      >
        <Icon name={landing ? "users" : "circle-plus"} size={18} />
        {landing && (
          <>
            <span className="flex-1">{label}</span>
            <Icon name="chevron-down" size={16} />
          </>
        )}
      </button>
      <NewChatPopover
        orgId={orgId}
        open={open}
        onOpenChange={setOpen}
        anchorRef={ref}
        matchWidth={landing}
      />
    </>
  );
}

/** 侧栏空会话态的「新建对话」入口：带文案按钮 + 复用选人浮层 */
export function EmptyAgentConversations({ orgId }: { orgId: string }) {
  const t = useT();
  const ref = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  return (
    <div className="flex flex-col items-start gap-1.5 px-2.5 py-1.5">
      <p className="text-body-sm text-fg-muted">
        {t("chat.list.noAgentConversations")}
      </p>
      <Button
        ref={ref}
        size="28"
        variant="outline"
        leading={<Icon name="hifox-new-chat" size={14} />}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        {t("chat.newChat")}
      </Button>
      <NewChatPopover
        orgId={orgId}
        open={open}
        onOpenChange={setOpen}
        anchorRef={ref}
      />
    </div>
  );
}
