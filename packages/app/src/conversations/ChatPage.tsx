import { DirectChatPage } from "./DirectChat";
import { useQueryClient } from "@tanstack/react-query";
import { useRef } from "react";
import {
  Navigate,
  useLocation,
  useParams,
  useSearchParams,
} from "react-router-dom";

import {
  isApiError,
  type ConversationSummary,
  type ConversationWorkspaceSelection,
  type SpaceAgent,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, Skeleton } from "@openfox/ui";

import { AgentAvatar } from "../agents/AgentAvatar";
import { useAgentSettingsParam } from "../agents/params";
import { useAgent, useCrew, useDirectory } from "../agents/queries";
import type { ComposerAgent } from "./Composer";
import { conversationPath } from "./ChatPanel";
import { conversationDestination, useChatNavigate } from "./chatNavigation";
import { ConversationHeader } from "./ConversationHeader";
import { ConversationView } from "./ConversationView";
import { conversationTitle } from "./model";
import {
  sameWorkspaceSelection,
  invalidateConversation,
  useConversation,
} from "./queries";
import { newDefaultsOf } from "./useOverrides";
import { useApi } from "../context";
import { useRejectNavLocation } from "../shell/navMemory";

/** 页面级状态：图标 + 标题 + 说明 + 可选操作 */
export function PageState({
  icon,
  title,
  desc,
  action,
}: {
  icon: React.ReactNode;
  title: string;
  desc?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-10 text-center">
      {icon}
      <p className="text-body font-medium">{title}</p>
      {desc && <p className="max-w-sm text-body-sm text-fg-muted">{desc}</p>}
      {action}
    </div>
  );
}

export function PageSkeleton() {
  return (
    <div
      aria-busy="true"
      className="flex h-full flex-col items-center justify-center gap-2"
    >
      <Skeleton className="size-9 rounded-lg" />
      <Skeleton className="h-3 w-28" />
      <Skeleton className="h-3 w-20" />
    </div>
  );
}

/**
 * 对话页：/o/:org/chat（新会话，?agent= / ?crew= 预选）与 /o/:org/chat/:conversationId。
 */
export function ChatPage() {
  const t = useT();
  const { orgId = "", conversationId } = useParams();
  const [params] = useSearchParams();
  if (params.get("user")) return <DirectChatPage />;
  if (conversationId)
    return (
      <ExistingConversation
        key={conversationId}
        orgId={orgId}
        conversationId={conversationId}
      />
    );
  const crewId = params.get("crew");
  if (crewId)
    return <NewCrewConversation key={crewId} orgId={orgId} crewId={crewId} />;
  const agentId = params.get("agent");
  if (agentId)
    return <NewConversation key={agentId} orgId={orgId} agentId={agentId} />;
  return (
    <PageState
      icon={
        <Icon name="openfox-chat-bubble" size={28} className="text-fg-muted" />
      }
      title={t("chat.page.pick")}
    />
  );
}

/** 会话所在 Agent 的配置（Runtime / 模型 chip） */
export function useAgentConfig(
  orgId: string,
  spaceId: string | undefined,
  agentId: string,
) {
  const agent = useAgent(orgId, spaceId, agentId);
  const config = agent.data?.config;
  return {
    detail: agent.data,
    runtime: config?.runtime ?? null,
    model: config?.model ?? null,
    /** 新对话在建会话前选择 Runtime / 模型 / 权限用的 Agent 默认值；读不到配置时为 null */
    newDefaults: config ? newDefaultsOf(config) : null,
  };
}

function ExistingConversation({
  orgId,
  conversationId,
}: {
  orgId: string;
  conversationId: string;
}) {
  const t = useT();
  const query = useConversation(orgId, conversationId);
  const location = useLocation();
  const [params] = useSearchParams();
  // 会话不存在（404）时退回所属导航模块，避免污染另一页签的记忆。
  const missing = isApiError(query.error) && query.error.status === 404;
  useRejectNavLocation(
    missing,
    `/o/${orgId}/${params.get("rail") === "home" ? "inbox" : "chat"}`,
  );
  const conversation = query.data;
  const settings = useAgentSettingsParam();
  const crewState = useCrewMembers(
    orgId,
    conversation?.space_id,
    conversation?.crew_id ?? null,
  );
  const config = useAgentConfig(
    orgId,
    conversation?.agent.space_id ?? conversation?.space_id,
    conversation?.agent_id ?? "",
  );
  if (query.isLoading || (conversation?.crew_id && crewState.query.isLoading))
    return <PageSkeleton />;
  if (!conversation)
    return (
      <PageState
        icon={
          <Icon name="openfox-alert-circle" size={28} className="text-danger" />
        }
        title={
          isApiError(query.error) && query.error.status === 404
            ? t("chat.page.notFound")
            : t("chat.page.loadFailed")
        }
        action={
          !(isApiError(query.error) && query.error.status === 404) && (
            <Button
              variant="outline"
              size="28"
              onClick={() => void query.refetch()}
            >
              {t("common.retry")}
            </Button>
          )
        }
      />
    );
  // 兼容历史会话链接，并在普通对话转为 / 绑定任务后进入任务上下文。
  if (conversation.task?.display_id) {
    const target = new URL(
      conversationDestination(
        orgId,
        conversation,
        params.get("rail") === "home" ? "home" : "chat",
      ),
      "http://nav.invalid",
    );
    const search = new URLSearchParams(params);
    search.delete("task");
    target.searchParams.forEach((value, key) => search.set(key, value));
    return (
      <Navigate
        to={`${target.pathname}?${search}`}
        replace
        state={location.state}
      />
    );
  }
  return (
    <ConversationLayout
      orgId={orgId}
      conversation={conversation}
      agent={conversation.agent}
      spaceId={conversation.space_id}
      config={config}
      onOpenSettings={() =>
        settings.open(
          conversation.agent_id,
          conversation.agent.space_id ?? conversation.space_id,
        )
      }
      crew={crewState.crew}
    />
  );
}

/** 小队对话需要的成员：队长排第一，只含 Agent 成员 */
export function useCrewMembers(
  orgId: string,
  spaceId: string | undefined,
  crewId: string | null,
) {
  const query = useCrew(orgId, spaceId, crewId);
  const detail = query.data;
  if (!crewId || !detail) return { crew: undefined, query };
  const members: ComposerAgent[] = detail.members
    .filter((member) => member.kind === "AGENT")
    .map((member) => ({ id: member.id, name: member.name }));
  const leaderId = detail.leader?.id;
  members.sort((a, b) => Number(b.id === leaderId) - Number(a.id === leaderId));
  return {
    crew: {
      id: detail.id,
      name: detail.name,
      members,
      leaderName: detail.leader?.name ?? members[0]?.name ?? "",
    },
    query,
  };
}

/** 新的小队对话：会话在发出第一条消息时创建，Agent 由服务端取队长 */
function NewCrewConversation({
  orgId,
  crewId,
}: {
  orgId: string;
  crewId: string;
}) {
  const t = useT();
  const api = useApi();
  const navigate = useChatNavigate();
  const queryClient = useQueryClient();
  const directory = useDirectory(orgId);
  const entry = directory.data?.entries.find(
    (item) => item.type === "crew" && item.id === crewId && !item.archived_at,
  );
  const state = useCrewMembers(orgId, entry?.space_id, entry ? crewId : null);
  const pendingId = useRef<string>(crypto.randomUUID());
  const created = useRef(false);

  if (directory.isLoading || (entry && state.query.isLoading))
    return <PageSkeleton />;
  if (!entry || !state.crew)
    return (
      <PageState
        icon={<Icon name="users" size={28} className="text-fg-muted" />}
        title={
          directory.isError || state.query.isError
            ? t("chat.page.loadFailed")
            : t("chat.page.crewUnavailable")
        }
      />
    );
  const crew = state.crew;
  const leader = crew.members[0];
  if (!leader)
    return (
      <PageState
        icon={<Icon name="users" size={28} className="text-fg-muted" />}
        title={t("chat.page.crewNoMembers")}
      />
    );

  const ensureConversation = async () => {
    const id = pendingId.current;
    if (!created.current) {
      try {
        await api.conversations.create(orgId, {
          id,
          space_id: entry.space_id,
          crew_id: crewId,
          visibility: "PRIVATE",
        });
      } catch (error) {
        // 上一次创建可能已生效（超时或重复提交）：确认会话存在后继续发消息
        const exists = await api.conversations
          .get(orgId, id)
          .then(() => true)
          .catch(() => false);
        if (!exists) throw error;
      }
      created.current = true;
    }
    return id;
  };

  return (
    <div className="flex h-full flex-col">
      <ConversationHeader
        orgId={orgId}
        agent={{ ...leader, avatar: null, online: true }}
        spaceId={entry.space_id}
        crew={{ id: crewId, name: crew.name }}
      />
      <ConversationView
        orgId={orgId}
        agent={leader}
        crew={crew}
        conversation={undefined}
        ensureConversation={ensureConversation}
        onConversationCreated={(id) => {
          invalidateConversation(queryClient, orgId, id);
          navigate(conversationPath(orgId, id), { replace: true });
        }}
        spaceId={entry.space_id}
        empty={
          <div className="flex flex-col items-center gap-3 text-center">
            <Icon name="users" size={32} className="text-fg-muted" />
            <p className="text-body text-fg-secondary">
              {t("chat.page.crewStartWith", { name: crew.name })}
            </p>
          </div>
        }
      />
    </div>
  );
}

function NewConversation({
  orgId,
  agentId,
}: {
  orgId: string;
  agentId: string;
}) {
  const t = useT();
  const api = useApi();
  const navigate = useChatNavigate();
  const queryClient = useQueryClient();
  const directory = useDirectory(orgId);
  const settings = useAgentSettingsParam();
  const entry = directory.data?.entries.find(
    (item) => item.type === "agent" && item.id === agentId && !item.archived_at,
  );
  const config = useAgentConfig(orgId, entry?.space_id, agentId);
  // 会话 id 由前端生成：第一条消息重试时不会重复创建会话
  const pendingId = useRef<string>(crypto.randomUUID());
  const created = useRef(false);

  if (directory.isLoading) return <PageSkeleton />;
  if (!entry)
    return (
      <PageState
        icon={<Icon name="bot" size={28} className="text-fg-muted" />}
        title={
          directory.isError
            ? t("chat.page.loadFailed")
            : t("chat.page.agentUnavailable")
        }
      />
    );

  const ensureConversation = async (
    workspace?: ConversationWorkspaceSelection,
  ) => {
    const id = pendingId.current;
    if (!created.current) {
      try {
        await api.conversations.create(orgId, {
          id,
          space_id: entry.space_id,
          agent_id: entry.id,
          visibility: "PRIVATE",
          ...(workspace ? { workspace_selection: workspace } : {}),
        });
      } catch (error) {
        // 上一次创建可能已生效（超时或重复提交）：确认会话存在后继续发消息
        const exists = await api.conversations
          .get(orgId, id)
          .then((existing) =>
            sameWorkspaceSelection(workspace, existing.workspace_selection),
          )
          .catch(() => false);
        if (!exists) throw error;
      }
      created.current = true;
    }
    return id;
  };

  return (
    <div className="flex h-full flex-col">
      <ConversationHeader
        orgId={orgId}
        agent={entry}
        spaceId={entry.space_id}
      />
      <ConversationView
        orgId={orgId}
        agent={{ ...entry, avatar: entry.avatar, ...config }}
        agentDetail={config.detail}
        newDefaults={config.newDefaults}
        conversation={undefined}
        ensureConversation={ensureConversation}
        onConversationCreated={(id) => {
          // 第一条消息发出后切到会话地址（消息已按 id 写入缓存）
          invalidateConversation(queryClient, orgId, id);
          navigate(conversationPath(orgId, id), { replace: true });
        }}
        spaceId={entry.space_id}
        onOpenSettings={() => settings.open(entry.id, entry.space_id)}
        onOpenDirectorySettings={() =>
          settings.open(entry.id, entry.space_id, "run-settings")
        }
        empty={
          <div className="flex flex-col items-center gap-3 text-center">
            <AgentAvatar name={entry.name} avatar={entry.avatar} size={40} />
            <p className="text-body text-fg-secondary">
              {t("chat.page.startWith", { name: entry.name })}
            </p>
          </div>
        }
      />
    </div>
  );
}

export function ConversationLayout({
  orgId,
  conversation,
  agent,
  spaceId,
  config,
  onOpenSettings,
  crew,
}: {
  orgId: string;
  conversation: ConversationSummary;
  agent: ConversationSummary["agent"];
  spaceId: string;
  config: { runtime: string | null; model: string | null; detail?: SpaceAgent };
  onOpenSettings: () => void;
  /** 小队对话：成员与队长 */
  crew?: {
    id: string;
    name: string;
    members: ComposerAgent[];
    leaderName: string;
  };
}) {
  const t = useT();
  return (
    <div className="flex h-full flex-col">
      <ConversationHeader
        orgId={orgId}
        agent={agent}
        spaceId={spaceId}
        conversation={conversation}
        crew={crew && { id: crew.id, name: crew.name }}
      />
      <ConversationView
        orgId={orgId}
        agent={{ ...agent, ...config }}
        agentDetail={config.detail}
        crew={crew}
        conversation={conversation}
        spaceId={spaceId}
        onOpenSettings={onOpenSettings}
        empty={
          <div className="flex flex-col items-center gap-3 text-center">
            <Icon name="openfox-bot" size={32} className="text-fg-muted" />
            <p className="text-body text-fg-secondary">
              {t("chat.page.readyEmpty", {
                title: conversationTitle(conversation) ?? t("chat.untitled"),
              })}
            </p>
          </div>
        }
      />
    </div>
  );
}
