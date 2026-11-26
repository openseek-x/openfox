import { useEffect, useRef, useState } from "react";

import type { Api, ConversationMessage } from "@openfox/api";
import { Button, Callout, Select, Textarea } from "@openfox/ui";

import { useChat } from "./useChat";

/** 对话页：选空间与 Agent、历史会话、消息列表和输入框，可选附带当前页面引用。 */
export function ChatView({
  api,
  onDisconnect,
}: {
  api: Api;
  onDisconnect: () => void | Promise<void>;
}) {
  const chat = useChat(api);
  const [text, setText] = useState("");
  const [withPage, setWithPage] = useState(false);
  const bottomRef = useRef<HTMLDivElement | null>(null);
  const closed = chat.active !== null && chat.active.lifecycle !== "OPEN";

  const lastId = chat.messages.at(-1)?.id;
  const canSend = chat.active ? chat.historyReady && !closed : !!chat.agentId;

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [lastId]);

  async function submit() {
    if (!text.trim() || chat.sending || closed) return;
    if (await chat.send(text, withPage)) {
      setText("");
      setWithPage(false);
    }
  }

  function changeTarget(action: () => void) {
    if (chat.sending) return;
    setText("");
    setWithPage(false);
    action();
  }

  function isMine(message: ConversationMessage) {
    return (
      !!chat.me &&
      message.author_kind === "USER" &&
      message.author_user_id === chat.me.id
    );
  }

  function authorLabel(message: ConversationMessage): string {
    if (message.author_kind === "USER")
      return isMine(message) ? "我" : "其他成员";
    if (message.author_kind === "SYSTEM") return "系统";
    return (
      chat.agents.find((agent) => agent.id === message.author_agent_id)?.name ??
      chat.active?.agent.name ??
      "Agent"
    );
  }

  const textMessages = chat.messages.filter(
    (message) => message.kind === "TEXT",
  );

  return (
    <div className="flex h-screen flex-col">
      <header className="flex flex-col gap-2 border-b border-line-default bg-surface-card p-3">
        <div className="flex items-center justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2">
            <img src="./openfox.png" alt="" width="28" height="28" />
            <div className="min-w-0">
              <h1 className="font-semibold">OpenFox</h1>
              {chat.me && (
                <div className="truncate text-caption text-fg-muted">
                  {chat.me.full_name || chat.me.email}
                </div>
              )}
            </div>
          </div>
          <Button
            variant="ghost-muted"
            size="28"
            disabled={chat.sending}
            onClick={() => void onDisconnect()}
          >
            断开连接
          </Button>
        </div>
        {chat.orgs.length > 1 && (
          <Select
            aria-label="组织"
            value={chat.orgId}
            disabled={chat.sending}
            onChange={(id) => {
              if (id !== chat.orgId) changeTarget(() => chat.selectOrg(id));
            }}
            options={chat.orgs.map((org) => ({
              value: org.id,
              label: org.name,
            }))}
          />
        )}
        <Select
          aria-label="空间"
          placeholder="选择空间"
          value={chat.spaceId}
          disabled={chat.sending}
          onChange={(id) => {
            if (id !== chat.spaceId) changeTarget(() => chat.selectSpace(id));
          }}
          options={chat.spaces.map((space) => ({
            value: space.id,
            label: space.name,
          }))}
        />
        {!chat.active && (
          <Select
            aria-label="Agent"
            placeholder="选择 Agent"
            value={chat.agentId}
            disabled={chat.sending}
            onChange={(id) => {
              if (id !== chat.agentId) changeTarget(() => chat.selectAgent(id));
            }}
            options={chat.agents.map((agent) => ({
              value: agent.id,
              label: agent.name,
            }))}
          />
        )}
        <div className="flex items-center gap-2">
          <Select
            aria-label="历史会话"
            disabled={chat.sending}
            placeholder="历史会话"
            value={chat.active?.id ?? null}
            onChange={(id) => {
              const target = chat.conversations.find((item) => item.id === id);
              if (target && target.id !== chat.active?.id)
                changeTarget(() => void chat.loadHistory(target));
            }}
            options={chat.conversations.map((item) => ({
              value: item.id,
              label: item.title ?? item.last_message?.preview ?? "未命名会话",
            }))}
            className="min-w-0 flex-1"
          />
          <Button
            variant="outline"
            disabled={chat.sending}
            onClick={() => changeTarget(chat.startNew)}
          >
            新会话
          </Button>
        </div>
        {(chat.nextPage || chat.listFailed) && (
          <Button variant="ghost-muted" onClick={chat.loadNextPage}>
            {chat.listFailed ? "重试读取会话列表" : "加载更多会话"}
          </Button>
        )}
      </header>

      <main className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-3">
        {chat.before > 0 && chat.active && (
          <Button
            variant="ghost-muted"
            onClick={() =>
              chat.active && void chat.loadHistory(chat.active, chat.before)
            }
          >
            查看更早消息
          </Button>
        )}
        {chat.active && !chat.historyReady && chat.error && (
          <Button
            variant="outline"
            onClick={() => chat.active && void chat.loadHistory(chat.active)}
          >
            重新加载会话
          </Button>
        )}
        {textMessages.length === 0 && (
          <p className="m-auto text-fg-muted">
            {chat.active
              ? chat.historyReady
                ? "暂无文字消息"
                : chat.error
                  ? ""
                  : "正在读取消息…"
              : chat.agentId
                ? "发送第一条消息，开始与 Agent 对话"
                : "请在有可对话 Agent 的空间开始新会话"}
          </p>
        )}
        {textMessages.map((message) => (
          <div
            key={message.id}
            className={`flex flex-col gap-1 ${
              isMine(message) ? "items-end" : "items-start"
            }`}
          >
            <span className="text-body-sm text-fg-muted">
              {authorLabel(message)}
            </span>
            <div
              className={`max-w-full rounded-lg px-3 py-2 break-words whitespace-pre-wrap ${
                isMine(message)
                  ? "bg-primary text-fg-inverse"
                  : "bg-surface-card text-fg-default"
              }`}
            >
              {message.body}
            </div>
          </div>
        ))}
        <div ref={bottomRef} />
      </main>

      <footer className="flex flex-col gap-2 border-t border-line-default bg-surface-card p-3">
        {chat.error && <Callout tone="danger">{chat.error}</Callout>}
        {closed && (
          <Callout tone="warning">
            这个会话已经关闭，无法继续发送。请点击“新会话”重新开始。
          </Callout>
        )}
        <Textarea
          aria-label="消息"
          rows={3}
          value={text}
          disabled={closed || chat.sending}
          placeholder="输入消息，Ctrl/⌘ + Enter 发送"
          onChange={(event) => {
            setText(event.target.value);
            chat.discardPending();
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
              event.preventDefault();
              void submit();
            }
          }}
        />
        <div className="flex items-center justify-between gap-2">
          <label className="flex items-center gap-1.5 text-body-sm text-fg-secondary">
            <input
              type="checkbox"
              checked={withPage}
              disabled={closed || chat.sending}
              onChange={(event) => {
                setWithPage(event.target.checked);
                chat.discardPending();
              }}
            />
            引用当前页面
          </label>
          <Button
            loading={chat.sending}
            disabled={!canSend || !text.trim()}
            onClick={() => void submit()}
          >
            发送
          </Button>
        </div>
      </footer>
    </div>
  );
}
