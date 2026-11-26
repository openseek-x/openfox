import { useCallback, useEffect, useRef, useState } from "react";
import {
  isApiError,
  type Api,
  type ConversationMessage,
  type ConversationSummary,
  type Me,
  type Organization,
  type SpaceDetail,
} from "@openfox/api";

import { describeApiError } from "../lib/client";
import { readCurrentPage } from "../lib/page";
import { buildMessageBody } from "../lib/pageRef";
import { mergeMessages } from "../lib/poll";
import { newPendingSend, submitPending, type PendingSend } from "../lib/send";
import { usePolling } from "./usePolling";

type AgentItem = Awaited<ReturnType<Api["spaceAgents"]["list"]>>[number];

export function useChat(api: Api) {
  const [me, setMe] = useState<Me | null>(null);
  const [orgs, setOrgs] = useState<Organization[]>([]);
  const [orgId, setOrgId] = useState<string | null>(null);
  const [spaces, setSpaces] = useState<SpaceDetail[]>([]);
  const [spaceId, setSpaceId] = useState<string | null>(null);
  const [agents, setAgents] = useState<AgentItem[]>([]);
  const [agentId, setAgentId] = useState<string | null>(null);
  const [conversations, setConversations] = useState<ConversationSummary[]>([]);
  const [listQuery, setListQuery] = useState<{ cursor?: string }>({});
  const [listFailed, setListFailed] = useState(false);
  const [nextPage, setNextPage] = useState<string | null>(null);
  const [active, setActive] = useState<ConversationSummary | null>(null);
  const [messages, setMessages] = useState<ConversationMessage[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [historyReady, setHistoryReady] = useState(false);
  const [before, setBefore] = useState(0);
  const cursorRef = useRef(0);
  const historyRequest = useRef(0);
  const pendingRef = useRef<PendingSend | null>(null);
  const draftId = useRef<string | null>(null);
  const sendLock = useRef(false);

  const fail = useCallback(
    (caught: unknown) => setError(describeApiError(caught)),
    [],
  );

  useEffect(() => {
    let live = true;
    Promise.all([api.me.get(), api.me.organizations()])
      .then(([user, list]) => {
        if (!live) return;
        setMe(user);
        setOrgs(list);
        setOrgId(list[0]?.id ?? null);
      })
      .catch((caught: unknown) => live && fail(caught));
    return () => {
      live = false;
    };
  }, [api, fail]);

  useEffect(() => {
    if (!orgId) return;
    let live = true;
    api.spaces
      .list(orgId)
      .then((list) => {
        if (!live) return;
        setSpaces(list);
        setSpaceId(list[0]?.id ?? null);
      })
      .catch((caught: unknown) => live && fail(caught));
    return () => {
      live = false;
    };
  }, [api, orgId, fail]);

  useEffect(() => {
    if (!orgId) return;
    let live = true;
    setListFailed(false);
    api.conversations
      .page(orgId, { ...listQuery, archived: "exclude" })
      .then((page) => {
        if (!live) return;
        // 首次查询或下一页可能与本地刚发送的会话重叠，保留靠前的最新摘要。
        setConversations((current) => {
          const ids = new Set(current.map((item) => item.id));
          return [
            ...current,
            ...page.conversations.filter((item) => !ids.has(item.id)),
          ];
        });
        setNextPage(page.next_cursor);
      })
      .catch((caught: unknown) => {
        if (live) {
          setListFailed(true);
          fail(caught);
        }
      });
    return () => {
      live = false;
    };
  }, [api, orgId, listQuery, fail]);

  useEffect(() => {
    if (!orgId || !spaceId) return;
    let live = true;
    api.spaceAgents
      .list(orgId, spaceId)
      .then((list) => {
        if (!live) return;
        const available = list.filter(
          (agent) => agent.allow_direct_chat && !agent.archived_at,
        );
        setAgents(available);
        setAgentId(available[0]?.id ?? null);
      })
      .catch((caught: unknown) => live && fail(caught));
    return () => {
      live = false;
    };
  }, [api, orgId, spaceId, fail]);

  usePolling({
    api,
    orgId,
    conversationId: active && historyReady ? active.id : null,
    cursorRef,
    onMessages: (incoming) =>
      setMessages((current) => mergeMessages(current, incoming)),
    onError: (caught) => {
      if (isApiError(caught) && [401, 403, 404].includes(caught.status))
        fail(caught);
    },
  });

  function resetConversation(next: ConversationSummary | null = null) {
    historyRequest.current++;
    pendingRef.current = null;
    draftId.current = null;
    cursorRef.current = 0;
    setBefore(0);
    setHistoryReady(false);
    setMessages([]);
    setActive(next);
    setError(null);
  }

  async function loadHistory(summary: ConversationSummary, previous?: number) {
    if (!orgId || sendLock.current) return;
    if (!previous) resetConversation(summary);
    const request = ++historyRequest.current;
    try {
      const page = await api.conversations.messagesBefore(
        orgId,
        summary.id,
        previous,
      );
      if (request !== historyRequest.current) return;
      if (!previous) {
        cursorRef.current = page.messages.at(-1)?.seq ?? 0;
        setHistoryReady(true);
      }
      setMessages((current) => mergeMessages(current, page.messages));
      setBefore(page.next_cursor);
    } catch (caught) {
      if (request === historyRequest.current) fail(caught);
    }
  }

  async function createConversation(org: string, space: string, agent: string) {
    const id = (draftId.current ??= crypto.randomUUID());
    try {
      await api.conversations.create(org, {
        id,
        space_id: space,
        agent_id: agent,
        visibility: "PRIVATE",
      });
    } catch (caught) {
      if (!isApiError(caught, "IDEMPOTENCY_CONFLICT")) throw caught;
    }
    const summary = await api.conversations.get(org, id);
    draftId.current = null;
    setActive(summary);
    setHistoryReady(true);
    setConversations((items) => [
      summary,
      ...items.filter((item) => item.id !== id),
    ]);
    return summary;
  }

  async function send(text: string, withPage: boolean) {
    if (
      !orgId ||
      sendLock.current ||
      !text.trim() ||
      (active && (!historyReady || active.lifecycle !== "OPEN"))
    )
      return false;
    if (!active && (!spaceId || !agentId)) return false;
    // 从取页面引用起锁定目标与草稿，回执到达前禁止重复提交或切换收件人。
    sendLock.current = true;
    setSending(true);
    setError(null);
    try {
      if (!pendingRef.current) {
        const reference = withPage ? await readCurrentPage() : null;
        if (withPage && !reference) {
          setError(
            "无法读取当前网页。请在该网页点击扩展图标授权，或取消引用后再发送。",
          );
          return false;
        }
        pendingRef.current = newPendingSend(buildMessageBody(text, reference));
      }
      const conversation =
        active ?? (await createConversation(orgId, spaceId!, agentId!));
      const message = await submitPending(
        api,
        orgId,
        conversation.id,
        pendingRef.current,
      );
      pendingRef.current = null;
      setMessages((current) => mergeMessages(current, [message]));
      const latest = {
        ...conversation,
        last_message: {
          seq: message.seq,
          created_at: message.created_at,
          author_kind: message.author_kind,
          preview: message.body.trim().replace(/\s+/g, " ").slice(0, 120),
        },
      };
      setConversations((items) => [
        latest,
        ...items.filter((item) => item.id !== latest.id),
      ]);
      return true;
    } catch (caught) {
      if (isApiError(caught, "CONVERSATION_CLOSED")) {
        pendingRef.current = null;
        setActive(
          (current) => current && { ...current, lifecycle: "COMPLETED" },
        );
      }
      fail(caught);
      return false;
    } finally {
      sendLock.current = false;
      setSending(false);
    }
  }

  return {
    me,
    orgs,
    orgId,
    spaces,
    spaceId,
    agents,
    agentId,
    active,
    messages,
    error,
    sending,
    historyReady,
    before,
    nextPage,
    listFailed,
    conversations: conversations.filter((item) => item.space_id === spaceId),
    loadNextPage: () => {
      setError(null);
      setListQuery(
        listFailed ? { ...listQuery } : { cursor: nextPage ?? undefined },
      );
    },
    selectOrg: (id: string) => {
      if (id === orgId || sendLock.current) return;
      resetConversation();
      setSpaces([]);
      setSpaceId(null);
      setAgents([]);
      setAgentId(null);
      setConversations([]);
      setNextPage(null);
      setListQuery({});
      setOrgId(id);
    },
    selectSpace: (id: string) => {
      if (id === spaceId || sendLock.current) return;
      resetConversation();
      setAgents([]);
      setAgentId(null);
      setSpaceId(id);
    },
    selectAgent: (id: string) => {
      if (sendLock.current || id === agentId) return;
      resetConversation();
      setAgentId(id);
    },
    startNew: () => {
      if (!sendLock.current) resetConversation();
    },
    loadHistory,
    send,
    discardPending: () => {
      if (!sendLock.current) pendingRef.current = null;
    },
  };
}
