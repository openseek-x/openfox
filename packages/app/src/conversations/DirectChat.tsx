import {
  useMessageTarget,
  useLocateMessage,
  useMessagePages,
} from "./messageWindow";
import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Link, useMatch, useParams, useSearchParams } from "react-router-dom";
import { isApiError, type DirectChat, type DirectChatFile } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Button, Icon, IconButton, cn } from "@openfox/ui";
import { PersonAvatar } from "../agents/AgentAvatar";
import { useMe } from "../agents/queries";
import {
  attachmentErrorMessage,
  formatBytes,
  precheckFile,
} from "../attachments/model";
import { useApi } from "../context";
import { useRejectNavLocation } from "../shell/navMemory";
import { PageSkeleton, PageState } from "./ChatPage";
import { OfferCard } from "./OfferCard";
import { Markdown } from "./Markdown";
import { withChatRail } from "./chatNavigation";

const key = (org: string) => ["org", org, "direct-chats"] as const;
export const directChatPath = (org: string, id: string) =>
  `/o/${org}/chat/direct/${encodeURIComponent(id)}`;

export function DirectChatList({
  orgId,
  compact = false,
  rail = null,
}: {
  orgId: string;
  compact?: boolean;
  rail?: "home" | null;
}) {
  const api = useApi();
  const t = useT();
  const directChatId = useMatch("/o/:orgId/chat/direct/:directChatId")?.params
    .directChatId;
  const [params] = useSearchParams();
  const query = useInfiniteQuery({
    queryKey: [...key(orgId), "list"],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      api.directChats.list(orgId, pageParam || undefined),
    getNextPageParam: (page) => page.next_before || undefined,
  });
  const rows = [
    ...new Map(
      query.data?.pages.flatMap((p) => p.chats).map((c) => [c.id, c]) ?? [],
    ).values(),
  ];
  if (!rows.length && !query.isError) return null;
  return (
    <section
      aria-label={t("chat.human.title")}
      className="mb-2 border-b border-line-default pb-2"
    >
      <h3 className="px-2.5 py-2 text-caption text-fg-muted">
        {t("chat.human.title")}
      </h3>
      {query.isError && (
        <Button
          aria-label={t("chat.human.retryList")}
          variant="ghost"
          size="28"
          onClick={() => void query.refetch()}
        >
          {t("common.retry")}
        </Button>
      )}
      {(compact ? rows.slice(0, 5) : rows).map((c) => (
        <Link
          key={c.id}
          to={withChatRail(directChatPath(orgId, c.id), rail)}
          aria-current={directChatId === c.id ? "page" : undefined}
          className={cn(
            "flex items-center gap-2 rounded-lg px-2.5 py-[var(--density-chat-padding,8px)] hover:bg-surface-muted",
            (directChatId === c.id || params.get("user") === c.peer.id) &&
              "bg-surface-muted",
          )}
        >
          <PersonAvatar name={c.peer.name} size={28} />
          <span className="min-w-0 flex-1">
            <span className="block truncate text-body-sm">{c.peer.name}</span>
            <span className="block truncate text-caption text-fg-muted">
              {c.last_offer_kind
                ? t(
                    c.last_offer_kind === "INVITE"
                      ? "tasks.detail.previewInvite"
                      : "tasks.detail.previewTransfer",
                  )
                : c.last_body || t("attachments.title")}
            </span>
          </span>
          {Number(c.unread_count) > 0 && (
            <span
              aria-label={t("chat.list.unread")}
              className="rounded-full bg-brand px-1.5 text-caption text-white"
            >
              {c.unread_count}
            </span>
          )}
        </Link>
      ))}
      {!compact && query.hasNextPage && (
        <Button
          size="28"
          variant="ghost"
          disabled={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          {t("chat.human.older")}
        </Button>
      )}
    </section>
  );
}

export function DirectChatPage() {
  const { orgId = "", directChatId } = useParams();
  const [params] = useSearchParams();
  const user = params.get("user") ?? "";
  return (
    <DirectChatLoader
      key={directChatId || user}
      orgId={orgId}
      id={directChatId}
      user={user}
    />
  );
}
function DirectChatLoader({
  orgId,
  id,
  user,
}: {
  orgId: string;
  id?: string;
  user: string;
}) {
  const api = useApi();
  const t = useT();
  const me = useMe();
  const [params] = useSearchParams();
  const self = !id && user === me.data?.id;
  const query = useQuery({
    enabled: !self && !me.isLoading,
    queryKey: [...key(orgId), id ? "detail" : "peer", id || user],
    queryFn: () =>
      id ? api.directChats.get(orgId, id) : api.directChats.ensure(orgId, user),
    retry: false,
  });
  const missing = isApiError(query.error) && query.error.status === 404;
  useRejectNavLocation(
    missing,
    `/o/${orgId}/${params.get("rail") === "home" ? "inbox" : "chat"}`,
  );
  if (me.isLoading || query.isLoading) return <PageSkeleton />;
  if (self)
    return (
      <DirectChatView
        orgId={orgId}
        chat={{
          id: "",
          peer: { id: user, name: me.data?.email ?? user },
          last_seq: 0,
          read_seq: 0,
          unread_count: 0,
          last_body: "",
          updated_at: "",
        }}
      />
    );
  if (!query.data)
    return (
      <PageState
        icon={<Icon name="message" />}
        title={
          isApiError(query.error) && query.error.code === "DIRECT_CHAT_SELF"
            ? t("chat.human.self")
            : t(missing ? "chat.page.notFound" : "chat.page.loadFailed")
        }
        action={
          <Button variant="outline" onClick={() => void query.refetch()}>
            {t("common.retry")}
          </Button>
        }
      />
    );
  return <DirectChatView key={query.data.id} orgId={orgId} chat={query.data} />;
}
export function DirectChatView({
  orgId,
  chat,
}: {
  orgId: string;
  chat: DirectChat;
}) {
  const api = useApi();
  const cache = useQueryClient();
  const t = useT();
  const { locale } = useI18n();
  const me = useMe();
  const [body, setBody] = useState("");
  const [files, setFiles] = useState<DirectChatFile[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const clientID = useRef(crypto.randomUUID());
  const gate = useRef(false);
  const input = useRef<HTMLInputElement>(null);
  const scroll = useRef<HTMLDivElement>(null);
  const nearBottom = useRef(true);
  const initial = useRef(true);
  const prepend = useRef<{ height: number; top: number } | null>(null);
  const [atBottom, setAtBottom] = useState(true);
  const [foreground, setForeground] = useState(
    () => document.visibilityState !== "hidden" && document.hasFocus(),
  );
  useEffect(() => {
    const change = () =>
      setForeground(
        document.visibilityState !== "hidden" && document.hasFocus(),
      );
    window.addEventListener("focus", change);
    window.addEventListener("blur", change);
    document.addEventListener("visibilitychange", change);
    return () => {
      window.removeEventListener("focus", change);
      window.removeEventListener("blur", change);
      document.removeEventListener("visibilitychange", change);
    };
  }, []);
  const { target, leave: leaveTarget } = useMessageTarget();
  const query = useMessagePages(
    [...key(orgId), "messages", chat.id],
    Boolean(chat.id),
    target,
    async (cursor) => {
      const page = await api.directChats.messages(
        orgId,
        chat.id,
        cursor || undefined,
      );
      return {
        messages: page.messages,
        next_cursor: Number(
          (typeof cursor === "object" && "after" in cursor
            ? page.next_after
            : page.next_before) || 0,
        ),
      };
    },
  );
  const messages = [
    ...new Map(
      query.data?.pages.flatMap((p) => p.messages).map((m) => [m.id, m]) ?? [],
    ).values(),
  ].sort((a, b) => Number(a.seq) - Number(b.seq));
  const latest = Number(messages.at(-1)?.seq ?? 0);
  useLayoutEffect(() => {
    const el = scroll.current;
    if (!el) return;
    if (prepend.current) {
      el.scrollTop =
        prepend.current.top + el.scrollHeight - prepend.current.height;
      prepend.current = null;
    } else if (!target && (initial.current || nearBottom.current)) {
      el.scrollTop = el.scrollHeight;
      initial.current = false;
    }
  }, [query.data, target]);
  useLocateMessage(target, scroll, query.data, () => {
    initial.current = false;
    nearBottom.current = false;
    setAtBottom(false);
  });
  const read = useRef(Number(chat.read_seq));
  useEffect(() => {
    if (target || !foreground || !atBottom || latest <= read.current) return;
    let mounted = true;
    void api.directChats
      .read(orgId, chat.id, latest)
      .then(() => {
        if (mounted) {
          read.current = latest;
          void cache.invalidateQueries({ queryKey: [...key(orgId), "list"] });
        }
      })
      .catch(() => {});
    return () => {
      mounted = false;
    };
  }, [
    target,
    foreground,
    atBottom,
    latest,
    api,
    orgId,
    chat.id,
    cache,
    query.dataUpdatedAt,
  ]);
  const invalidate = () => cache.invalidateQueries({ queryKey: key(orgId) });
  const changeBody = (value: string) => {
    setBody(value);
    clientID.current = crypto.randomUUID();
  };
  const send = async () => {
    if (gate.current || (!body.trim() && !files.length)) return;
    gate.current = true;
    setBusy(true);
    setError("");
    try {
      if (!chat.id) await api.directChats.ensure(orgId, chat.peer.id);
      await api.directChats.send(orgId, chat.id, {
        body: body.trim(),
        client_message_id: clientID.current,
        attachment_ids: files.map((f) => f.id),
      });
      setBody("");
      setFiles([]);
      clientID.current = crypto.randomUUID();
      if (target) leaveTarget();
      nearBottom.current = true;
      setAtBottom(true);
      await invalidate();
    } catch (e) {
      if (isApiError(e) && e.code === "DIRECT_CHAT_SELF") setBody("");
      setError(
        isApiError(e) && e.code === "DIRECT_CHAT_SELF"
          ? t("chat.human.self")
          : t("chat.human.sendFailed"),
      );
    } finally {
      gate.current = false;
      setBusy(false);
    }
  };
  const upload = async (values: File[]) => {
    if (gate.current || !values.length) return;
    if (values.length + files.length > 10) {
      setError(t("chat.human.fileLimit"));
      return;
    }
    gate.current = true;
    setBusy(true);
    setError("");
    try {
      for (const f of values) {
        if (precheckFile(f)) {
          setError(t("attachments.errors.tooLarge", { name: f.name }));
          break;
        }
        try {
          if (!chat.id) await api.directChats.ensure(orgId, chat.peer.id);
          const uploaded = await api.directChats.upload(orgId, chat.id, f);
          setFiles((old) => [...old, uploaded]);
          clientID.current = crypto.randomUUID();
        } catch (e) {
          setError(
            isApiError(e) && e.code === "DIRECT_CHAT_SELF"
              ? t("chat.human.self")
              : attachmentErrorMessage(t, e, f.name),
          );
          break;
        }
      }
    } finally {
      gate.current = false;
      setBusy(false);
    }
  };
  const remove = async (file: DirectChatFile) => {
    if (gate.current) return;
    gate.current = true;
    setBusy(true);
    try {
      await api.directChats.removeFile(orgId, chat.id, file.id);
      setFiles((old) => old.filter((f) => f.id !== file.id));
      clientID.current = crypto.randomUUID();
    } catch (e) {
      setError(attachmentErrorMessage(t, e, file.name));
    } finally {
      gate.current = false;
      setBusy(false);
    }
  };
  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex h-14 shrink-0 items-center gap-3 border-b border-line-default px-6">
        <PersonAvatar name={chat.peer.name} size={28} />
        <h1 className="text-title font-semibold">{chat.peer.name}</h1>
      </header>
      <div
        ref={scroll}
        role="log"
        aria-label={t("chat.human.messages")}
        className="min-h-0 flex-1 overflow-y-auto px-6 py-5"
        onScroll={() => {
          const el = scroll.current!;
          nearBottom.current =
            el.scrollHeight - el.scrollTop - el.clientHeight < 48;
          setAtBottom(nearBottom.current);
          if (
            target &&
            nearBottom.current &&
            query.hasPreviousPage &&
            !query.isFetching
          )
            void query.fetchPreviousPage();
        }}
      >
        {query.hasNextPage && (
          <div className="text-center">
            <Button
              variant="ghost"
              size="28"
              disabled={query.isFetchingNextPage}
              onClick={async () => {
                const el = scroll.current!;
                prepend.current = {
                  height: el.scrollHeight,
                  top: el.scrollTop,
                };
                await query.fetchNextPage();
              }}
            >
              {t("chat.human.older")}
            </Button>
          </div>
        )}
        {query.isError && (
          <div role="alert">
            {t(
              target && isApiError(query.error) && query.error.status === 404
                ? "chat.messages.targetUnavailable"
                : "chat.page.loadFailed",
            )}
            <Button variant="ghost" onClick={() => void query.refetch()}>
              {t("common.retry")}
            </Button>
          </div>
        )}
        {!query.isLoading && !messages.length && !query.isError && (
          <p className="mt-16 text-center text-body text-fg-muted">
            {t("chat.human.empty")}
          </p>
        )}
        <div className="mx-auto flex max-w-3xl flex-col gap-5">
          {messages.map((m) => {
            const own = m.author_id === me.data?.id;
            return (
              <article
                key={m.id}
                data-message-id={m.id}
                className={cn(
                  m.id === target && "message-search-hit",
                  "flex max-w-[85%] flex-col gap-1",
                  own ? "self-end items-end" : "self-start items-start",
                )}
              >
                <p className="text-caption text-fg-muted">
                  {own ? t("agents.you") : chat.peer.name} ·{" "}
                  {new Date(m.created_at).toLocaleString(locale)}
                </p>
                {m.offer ? (
                  <OfferCard orgId={orgId} offer={m.offer} own={own} />
                ) : (
                  m.body && (
                    <div
                      className={cn(
                        "max-w-full break-words rounded-xl px-4 py-2",
                        own ? "bg-surface-muted" : "bg-surface-card",
                      )}
                    >
                      <Markdown source={m.body} />
                    </div>
                  )
                )}
                {m.attachments.map((f) => (
                  <a
                    key={f.id}
                    href={api.directChats.fileUrl(orgId, chat.id, f.id)}
                    className="flex max-w-full items-center gap-2 rounded-lg border border-line-default p-3 text-body-sm"
                    download={f.name}
                  >
                    {f.previewable ? (
                      <img
                        src={api.directChats.fileUrl(
                          orgId,
                          chat.id,
                          f.id,
                          true,
                        )}
                        alt={f.name}
                        className="max-h-40 max-w-48 rounded"
                      />
                    ) : (
                      <Icon name="paperclip" />
                    )}
                    <span className="break-all">
                      {f.name} · {formatBytes(Number(f.size_bytes))}
                    </span>
                    <Icon name="download" />
                  </a>
                ))}
              </article>
            );
          })}
          {target && query.hasPreviousPage && (
            <Button
              variant="ghost"
              size="28"
              disabled={query.isFetchingPreviousPage}
              onClick={() => void query.fetchPreviousPage()}
            >
              {t("chat.messages.loadNewer")}
            </Button>
          )}
        </div>
      </div>
      {target && (
        <Button
          className="self-center"
          variant="ghost"
          size="28"
          onClick={() => {
            initial.current = true;
            nearBottom.current = true;
            setAtBottom(true);
            leaveTarget();
          }}
        >
          {t("chat.messages.scrollBottom")}
        </Button>
      )}
      <form
        className="mx-auto w-full max-w-4xl shrink-0 px-6 pb-5 pt-2"
        onSubmit={(e) => {
          e.preventDefault();
          void send();
        }}
      >
        {error && (
          <p role="alert" className="mb-2 text-body-sm text-danger">
            {error}
          </p>
        )}
        <div
          className="rounded-2xl border border-line-default bg-surface-card p-3"
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault();
            void upload(Array.from(e.dataTransfer.files));
          }}
        >
          {files.length > 0 && (
            <ul
              aria-label={t("attachments.title")}
              className="mb-2 flex flex-wrap gap-2"
            >
              {files.map((f) => (
                <li
                  key={f.id}
                  className="flex items-center gap-1 rounded-md bg-surface-muted p-1 text-caption"
                >
                  {f.name}
                  <IconButton
                    label={t("chat.human.removeFile", { name: f.name })}
                    icon={<Icon name="x" size={12} />}
                    size="28"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => void remove(f)}
                  />
                </li>
              ))}
            </ul>
          )}
          <textarea
            aria-label={t("chat.human.input")}
            placeholder={t("chat.human.input")}
            value={body}
            disabled={busy}
            maxLength={65536}
            rows={3}
            className="w-full resize-none bg-transparent text-body outline-none"
            onChange={(e) => changeBody(e.target.value)}
            onPaste={(e) => {
              if (e.clipboardData.files.length) {
                e.preventDefault();
                void upload(Array.from(e.clipboardData.files));
              }
            }}
            onKeyDown={(e) => {
              if (
                e.key === "Enter" &&
                !e.shiftKey &&
                !e.nativeEvent.isComposing
              ) {
                e.preventDefault();
                void send();
              }
            }}
          />
          <div className="flex items-center justify-between">
            <input
              ref={input}
              type="file"
              multiple
              className="hidden"
              aria-label={t("chat.human.attach")}
              onChange={(e) => {
                void upload(Array.from(e.target.files ?? []));
                e.target.value = "";
              }}
            />
            <IconButton
              label={t("chat.human.attach")}
              icon={<Icon name="paperclip" />}
              variant="ghost"
              disabled={busy}
              onClick={() => input.current?.click()}
            />
            <Button
              type="submit"
              size="32"
              disabled={busy || (!body.trim() && !files.length)}
            >
              {t("chat.human.send")}
            </Button>
          </div>
        </div>
      </form>
    </div>
  );
}
