import { useEffect, useRef, useState } from "react";

import type { Api, ConversationMessage, Me } from "@openfox/api";
import {
  Button,
  Callout,
  Icon,
  Menu,
  MenuItem,
  MenuLabel,
  Select,
} from "@openfox/ui";

import { splitPageReference } from "../lib/pageRef";
import { useChat } from "./useChat";

/** 对话页：选空间与 Agent、历史会话、消息列表和输入框，可选附带当前页面引用。 */
export function ChatView({ api, me }: { api: Api; me: Me }) {
  const chat = useChat(api);
  const [text, setText] = useState("");
  const [withPage, setWithPage] = useState(false);
  const inputRef = useRef<HTMLTextAreaElement>(null);
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
    return message.author_kind === "USER" && message.author_user_id === me.id;
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

  const agentName =
    chat.active?.agent.name ??
    chat.agents.find((agent) => agent.id === chat.agentId)?.name;
  return (
    <div className="chat-page">
      <header className="chat-head">
        <div className="chat-top">
          <div className="account" title={me.email}>
            <img src="./openfox.png" alt="" width="32" height="32" />
            <span className="account-copy">
              <strong>OpenFox</strong>
              <small>{me.full_name || me.email}</small>
            </span>
            <span
              className="connected-indicator"
              role="img"
              aria-label="当前浏览器已连接"
              title="当前浏览器已连接"
            />
          </div>
          <div className="chat-actions">
            <Menu
              label="历史会话"
              placement="bottom-end"
              focusFirstOnOpen
              className="history-menu"
              trigger={({ ref, open, toggle }) => (
                <button
                  type="button"
                  className="tool-button"
                  ref={ref}
                  onClick={toggle}
                  disabled={chat.sending}
                  aria-label="历史会话"
                  title="历史会话"
                  aria-haspopup="menu"
                  aria-expanded={open}
                >
                  <Icon name="history" size={18} />
                </button>
              )}
            >
              <MenuLabel>历史会话</MenuLabel>
              {chat.conversations.map((item) => (
                <MenuItem
                  key={item.id}
                  selected={item.id === chat.active?.id}
                  onSelect={() => {
                    if (item.id !== chat.active?.id)
                      changeTarget(() => void chat.loadHistory(item));
                  }}
                >
                  {item.title || item.last_message?.preview || "未命名会话"}
                </MenuItem>
              ))}
              {!chat.conversations.length && !chat.listFailed && (
                <p className="no-history">当前空间暂无会话</p>
              )}
              {(chat.nextPage || chat.listFailed) && (
                <MenuItem onSelect={chat.loadNextPage}>
                  {chat.listFailed ? "重试读取会话列表" : "加载更多会话"}
                </MenuItem>
              )}
            </Menu>
            <button
              type="button"
              className="new-chat"
              disabled={chat.sending}
              onClick={() => {
                changeTarget(chat.startNew);
                inputRef.current?.focus();
              }}
            >
              <Icon name="plus" size={15} />
              新会话
            </button>
          </div>
        </div>
        <div className="chat-context">
          {chat.orgs.length > 1 && (
            <Select
              aria-label="组织"
              className="org-select context-select"
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
            className="context-select"
            leading={<Icon name="layers" size={14} />}
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
          {chat.active ? (
            <div className="active-agent" title={agentName}>
              <Icon name="bot" size={15} />
              <span>{agentName}</span>
            </div>
          ) : (
            <Select
              aria-label="Agent"
              className="context-select"
              leading={<Icon name="bot" size={15} />}
              placeholder="选择 Agent"
              value={chat.agentId}
              disabled={chat.sending}
              onChange={(id) => {
                if (id !== chat.agentId)
                  changeTarget(() => chat.selectAgent(id));
              }}
              options={chat.agents.map((agent) => ({
                value: agent.id,
                label: agent.name,
              }))}
            />
          )}
        </div>
      </header>
      <main className="thread" aria-label="对话消息">
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
        {!textMessages.length &&
          !(chat.active && !chat.historyReady && chat.error) && (
            <div className="empty-chat">
              <div className="empty-icon">
                <Icon name="message-circle" size={26} />
              </div>
              <h1>
                {chat.active
                  ? chat.historyReady
                    ? "暂无文字消息"
                    : "正在读取消息…"
                  : agentName
                    ? "开始新对话"
                    : "选择对话 Agent"}
              </h1>
              {!chat.active && (
                <p>
                  {agentName
                    ? "给 " + agentName + " 发消息，也可以引用正在浏览的网页。"
                    : "请选择一个有可对话 Agent 的空间。"}
                </p>
              )}
            </div>
          )}
        {textMessages.map((message) => {
          const mine = isMine(message);
          const { text: prose, reference } =
            message.author_kind === "USER"
              ? splitPageReference(message.body)
              : { text: message.body, reference: null };
          return (
            <article
              key={message.id}
              className={"message " + (mine ? "outgoing" : "incoming")}
            >
              <div className="message-author">
                {!mine && (
                  <Icon
                    name={
                      message.author_kind === "AGENT" ? "bot" : "message-circle"
                    }
                    size={15}
                  />
                )}
                {authorLabel(message)}
              </div>
              <div className="bubble">
                <p>{prose}</p>
                {reference && (
                  <details className="citation">
                    <summary title="展开或收起网页引用">
                      <span className="source-icon">
                        <Icon name="globe" size={15} />
                      </span>
                      <span className="source-label">
                        <strong>{reference.title || "网页引用"}</strong>
                        <small>{new URL(reference.url).host}</small>
                      </span>
                      <Icon name="chevron-down" size={14} />
                    </summary>
                    <div className="source-detail">
                      {reference.selection && (
                        <blockquote>{reference.selection}</blockquote>
                      )}
                      <a href={reference.url} target="_blank" rel="noreferrer">
                        {reference.url}
                        <Icon name="external-link" size={12} />
                      </a>
                    </div>
                  </details>
                )}
              </div>
            </article>
          );
        })}
        <div ref={bottomRef} />
      </main>
      <footer className="composer-wrap">
        {chat.error && <Callout tone="danger">{chat.error}</Callout>}
        {closed && (
          <Callout tone="warning">
            这个会话已经关闭。点击「新会话」可继续与 Agent 对话。
          </Callout>
        )}
        <div className="composer">
          <textarea
            ref={inputRef}
            aria-label="消息"
            rows={2}
            value={text}
            disabled={closed || chat.sending}
            placeholder={closed ? "此会话已关闭" : "输入消息…"}
            onChange={(event) => {
              setText(event.target.value);
              chat.discardPending();
            }}
            onKeyDown={(event) => {
              if (
                event.key === "Enter" &&
                !event.nativeEvent.isComposing &&
                (event.metaKey || event.ctrlKey)
              ) {
                event.preventDefault();
                void submit();
              }
            }}
          />
          <div className="compose-tools">
            <label className="quote-toggle">
              <input
                type="checkbox"
                checked={withPage}
                disabled={closed || chat.sending}
                onChange={(event) => {
                  setWithPage(event.target.checked);
                  chat.discardPending();
                }}
              />
              <Icon name={withPage ? "check" : "quote"} size={14} />
              引用当前页面
            </label>
            <button
              type="button"
              className="accent-button send"
              disabled={chat.sending || !canSend || !text.trim()}
              title="发送 (Ctrl / ⌘ + Enter)"
              onClick={() => void submit()}
            >
              {chat.sending ? "发送中…" : "发送"}
              <Icon name="arrow-up" size={15} />
            </button>
          </div>
        </div>
      </footer>
    </div>
  );
}
