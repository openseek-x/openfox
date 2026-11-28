import { ResolutionActions } from "./ResolutionActions";
import { useResolution } from "./resolutionState";
import { useState } from "react";
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import {
  isApiError,
  type WorkspaceCommentMutation,
  type WorkspaceCommentSource,
  type WorkspaceCommentThread,
  type WorkspaceCommentPost,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, cn } from "@openfox/ui";
import { useApi, keys } from "../../context";
import { useMe } from "../../agents/queries";
import {
  addFileComment,
  addBrowserComment,
  readDraft,
  type DraftBrowserContext,
} from "../drafts";
import { workspaceStore } from "../workspaceStorage";
import { useOpenArtifactList, BROWSER_PANEL } from "../artifacts/params";
import { requestFileLocation } from "../artifacts/fileLocation";
import { requestBrowserLocation } from "../browser/BrowserReferences";

const commentKey = (org: string, conversation: string) => [
  ...keys.conversation(org, conversation),
  "comments",
];
const actionClass =
  "text-caption text-fg-secondary hover:text-fg-default disabled:opacity-50";

/** Saving discussion is deliberately distinct from the ordinary chat Send button. */
export function SaveComment({
  orgId,
  conversationId,
  id,
  source,
  screenshot,
  disabled,
  onSaved,
}: {
  orgId: string;
  conversationId: string;
  id: string;
  source: WorkspaceCommentSource;
  screenshot?: File;
  disabled: boolean;
  onSaved: () => void;
}) {
  const api = useApi(),
    client = useQueryClient(),
    t = useT();
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const save = async () => {
    if (busy) return;
    setBusy(true);
    setError("");
    const store = workspaceStore(),
      uploadKey = `comment-upload:${conversationId}:${id}`;
    try {
      let input = structuredClone(source);
      if (screenshot && input.browser) {
        let image = store.read<string>(uploadKey);
        if (!image) {
          image = (
            await api.attachments.uploadToConversation(
              orgId,
              conversationId,
              screenshot,
            )
          ).id;
          store.write(uploadKey, image);
          await store.flush();
        }
        input.browser.comments[0]!.screenshot_attachment_id = image;
      }
      await api.workspaceComments.create(orgId, conversationId, id, input);
      // Only consume the local annotation after the server acknowledged its stable ID.
      if (workspaceStore() === store) onSaved();
      store.write(uploadKey, undefined);
      await client.invalidateQueries({
        queryKey: commentKey(orgId, conversationId),
      });
    } catch (reason) {
      setError(
        t(
          isApiError(reason) && reason.status === 409
            ? "artifacts.comments.conflict"
            : "artifacts.comments.saveFailed",
        ),
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mt-1">
      <button
        type="button"
        className={actionClass}
        disabled={disabled || busy}
        onClick={() => void save()}
      >
        {t(busy ? "artifacts.comments.saving" : "artifacts.comments.save")}
      </button>
      {error && (
        <p role="alert" className="text-caption text-danger">
          {error}
        </p>
      )}
    </div>
  );
}

export function WorkspaceComments({
  orgId,
  conversationId,
  disabled,
  embedded = false,
}: {
  orgId: string;
  conversationId: string;
  disabled: boolean;
  /** 嵌入右侧工作区标签页时占满剩余高度，不限制 60vh。 */
  embedded?: boolean;
}) {
  const api = useApi(),
    t = useT();
  const [filter, setFilter] = useState<"ALL" | "OPEN" | "RESOLVED">("ALL");
  const query = useInfiniteQuery({
    queryKey: commentKey(orgId, conversationId),
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      api.workspaceComments.list(orgId, conversationId, pageParam, signal),
    getNextPageParam: (page) => page.next_cursor || undefined,
    refetchInterval: 15000,
    retry: false,
  });
  const allThreads = query.data?.pages.flatMap((page) => page.threads) ?? [];
  const openCount = allThreads.filter((item) => item.status === "OPEN").length;
  const resolvedCount = allThreads.length - openCount;
  const visibleThreads =
    filter === "ALL"
      ? allThreads
      : allThreads.filter((item) => item.status === filter);
  const filterBar =
    !query.isPending && !query.isError && allThreads.length > 0 ? (
      <div
        role="group"
        aria-label={t("artifacts.comments.saved")}
        className="inline-flex shrink-0 items-center gap-0.5 rounded-lg border border-line-subtle bg-surface-muted p-0.5 text-caption"
      >
        {(
          [
            ["ALL", t("artifacts.comments.all"), allThreads.length],
            ["OPEN", t("artifacts.comments.open"), openCount],
            ["RESOLVED", t("artifacts.comments.resolved"), resolvedCount],
          ] as const
        ).map(([value, label, count]) => (
          <button
            key={value}
            type="button"
            aria-pressed={filter === value}
            onClick={() => setFilter(value)}
            className={cn(
              "rounded-md px-2 py-0.5 transition-colors",
              filter === value
                ? "bg-surface-card font-medium text-fg-default shadow-card"
                : "text-fg-muted hover:text-fg-default",
            )}
          >
            {label} · {count}
          </button>
        ))}
      </div>
    ) : null;
  const content = query.isError ? (
    <div
      role="alert"
      className="flex items-center justify-between gap-2 rounded-xl border border-line-subtle bg-surface-card p-3 text-body-sm shadow-card"
    >
      <span className="text-danger">{t("artifacts.comments.loadFailed")}</span>
      <Button size="28" variant="outline" onClick={() => void query.refetch()}>
        {t("common.retry")}
      </Button>
    </div>
  ) : query.isPending ? (
    <p className="py-6 text-center text-caption text-fg-muted">
      {t("artifacts.review.loading")}
    </p>
  ) : (
    <>
      {allThreads.length === 0 ? (
        <div className="flex flex-col items-center justify-center gap-2 py-10 text-center">
          <span className="flex h-10 w-10 items-center justify-center rounded-xl border border-line-subtle bg-surface-card text-fg-muted shadow-card">
            <Icon name="message-circle" size={16} />
          </span>
          <p className="text-caption text-fg-muted">
            {t("artifacts.comments.empty")}
          </p>
        </div>
      ) : visibleThreads.length === 0 ? (
        <p className="py-8 text-center text-caption text-fg-muted">
          {t(
            filter === "OPEN"
              ? "artifacts.comments.emptyOpen"
              : "artifacts.comments.emptyResolved",
          )}
        </p>
      ) : null}
      {visibleThreads.map((thread) => (
        <Thread
          key={thread.id}
          thread={thread}
          orgId={orgId}
          conversationId={conversationId}
          disabled={disabled}
        />
      ))}
      {query.hasNextPage && (
        <div className="pt-1 text-center">
          <Button
            size="28"
            variant="ghost"
            disabled={query.isFetchingNextPage}
            onClick={() => void query.fetchNextPage()}
          >
            {t("artifacts.comments.more")}
          </Button>
        </div>
      )}
    </>
  );
  if (embedded)
    return (
      <div className="flex min-h-0 flex-1 flex-col bg-surface-canvas/40 text-body-sm">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-line-subtle bg-surface-canvas px-3 py-2">
          <p className="text-caption text-fg-muted">
            {t("artifacts.comments.hint")}
          </p>
          {filterBar}
        </div>
        <div className="min-h-0 flex-1 space-y-2.5 overflow-y-auto p-3">
          {content}
        </div>
      </div>
    );
  return (
    <div className="max-h-[60vh] space-y-2.5 overflow-y-auto text-body-sm">
      <p className="text-caption text-fg-muted">
        {t("artifacts.comments.hint")}
      </p>
      {filterBar}
      {content}
    </div>
  );
}

function Thread({
  thread,
  orgId,
  conversationId,
  disabled,
}: {
  thread: WorkspaceCommentThread;
  orgId: string;
  conversationId: string;
  disabled: boolean;
}) {
  const api = useApi(),
    client = useQueryClient(),
    me = useMe().data,
    t = useT(),
    store = workspaceStore();
  const resolution = useResolution(conversationId, thread);
  const editorKey = `comment-editor:${conversationId}:${thread.id}`,
    pendingKey = `comment-operation:${conversationId}:${thread.id}`;
  const saved = store.read<{ body: string; editId: string }>(editorKey);
  const [body, setBody] = useState(saved?.body ?? ""),
    [editId, setEditId] = useState(saved?.editId ?? "");
  const [pending, setPending] = useState(() =>
    store.read<WorkspaceCommentMutation>(pendingKey),
  );
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const edit = (text: string, id = editId) => {
    setBody(text);
    setEditId(id);
    store.write(editorKey, { body: text, editId: id });
  };
  const openFile = useOpenArtifactList(thread.source.file?.artifact_id),
    openBrowser = useOpenArtifactList(BROWSER_PANEL);
  const refresh = () =>
    client.invalidateQueries({ queryKey: commentKey(orgId, conversationId) });
  const mutate = async (action: WorkspaceCommentMutation["action"]) => {
    if (busy) return;
    setBusy(true);
    setError("");
    const request = pending ?? {
      operation_id: crypto.randomUUID(),
      version: thread.version,
      action,
      ...(action === "reply" || action === "edit"
        ? { body, ...(editId ? { comment_id: editId } : {}) }
        : {}),
    };
    try {
      store.write(pendingKey, request);
      setPending(request);
      await store.flush();
      await api.workspaceComments.update(
        orgId,
        conversationId,
        thread.id,
        request,
      );
      store.write(pendingKey, undefined);
      setPending(undefined);
      if (request.action === "reply" || request.action === "edit") edit("", "");
      await refresh();
    } catch (reason) {
      if (
        isApiError(reason) &&
        reason.status >= 400 &&
        reason.status < 500 &&
        reason.status !== 408 &&
        reason.status !== 429
      ) {
        store.write(pendingKey, undefined);
        setPending(undefined);
        await refresh();
      }
      setError(
        t(
          isApiError(reason) && reason.status === 409
            ? "artifacts.comments.conflict"
            : "artifacts.comments.updateFailed",
        ),
      );
    } finally {
      setBusy(false);
    }
  };
  const queue = async (post: WorkspaceCommentPost) => {
    setError("");
    setBusy(true);
    try {
      if (resolution && resolution.status !== "exact") {
        setError(t("artifacts.resolution.unavailable"));
        return;
      }
      const source = resolution?.target ?? thread.source;
      const ref = {
        ...(resolution && { resolution_id: resolution.id }),
        id: thread.id,
        version: thread.version,
        comment_id: post.id,
      };
      const draft = readDraft(conversationId);
      if (
        [
          ...(draft?.fileContexts ?? []),
          ...(draft?.browserContexts ?? []),
        ].some((c) =>
          c.comments.some(
            (a) =>
              a.discussion?.id === ref.id &&
              a.discussion.version === ref.version &&
              a.discussion.comment_id === ref.comment_id,
          ),
        )
      )
        return;
      let ok = false;
      if (source.file) {
        const { comments, id, ...file } = source.file;
        ok = addFileComment(
          conversationId,
          { ...file, name: thread.source.name },
          {
            thread_id: crypto.randomUUID(),
            body: post.body,
            anchor: structuredClone(comments[0]!.anchor),
            discussion: ref,
          },
        );
      } else if (source.browser) {
        const { id, comments, ...browserSource } = source.browser;
        const original = comments[0]!;
        const image = original.screenshot_attachment_id
          ? await api.workspaceComments.screenshot(
              orgId,
              original.screenshot_attachment_id,
            )
          : undefined;
        if (workspaceStore() !== store) return;
        const screenshot = image
          ? new File(
              [image],
              `comment-${thread.id}.${image.type === "image/jpeg" ? "jpg" : "png"}`,
              { type: image.type },
            )
          : undefined;
        const comment: DraftBrowserContext["comments"][number] = {
          id: crypto.randomUUID(),
          body: post.body,
          anchor: structuredClone(original.anchor),
          discussion: ref,
          ...(screenshot && { screenshot }),
        };
        ok = addBrowserComment(conversationId, browserSource, comment);
      }
      if (!ok) setError(t("artifacts.review.draftLimit"));
    } catch {
      setError(t("artifacts.comments.queueFailed"));
    } finally {
      setBusy(false);
    }
  };
  const isOpen = thread.status === "OPEN";
  return (
    <details
      className="group overflow-hidden rounded-xl border border-line-subtle bg-surface-card shadow-card transition-colors open:border-line-default"
      aria-label={thread.comments[0]!.body}
    >
      <summary className="flex cursor-pointer list-none items-start gap-2.5 px-3 py-2.5 transition-colors hover:bg-surface-hover/40 [&::-webkit-details-marker]:hidden">
        <span
          aria-hidden="true"
          className={cn(
            "mt-1.5 size-2 shrink-0 rounded-full",
            isOpen ? "bg-accent" : "bg-fg-subtle",
          )}
        />
        <div className="min-w-0 flex-1 space-y-0.5">
          <div className="flex items-center gap-1.5 text-caption text-fg-muted">
            <Icon
              name={thread.source.browser ? "globe" : "file-text"}
              size={12}
              className="shrink-0"
            />
            <span className="min-w-0 truncate font-medium text-fg-secondary">
              {thread.source.name}
            </span>
            <span
              className={cn(
                "shrink-0 rounded-full px-1.5 py-px text-[11px]",
                isOpen
                  ? "bg-accent-soft text-accent"
                  : "bg-surface-muted text-fg-muted",
              )}
            >
              {t(
                isOpen
                  ? "artifacts.comments.open"
                  : "artifacts.comments.resolved",
              )}
            </span>
          </div>
          <span className="block truncate text-body-sm text-fg-default">
            {thread.comments[0]!.body}
          </span>
        </div>
        <Icon
          name="chevron-right"
          size={14}
          className="mt-1 shrink-0 text-fg-muted transition-transform group-open:rotate-90"
        />
      </summary>
      <div className="space-y-2 border-t border-line-subtle px-3 py-2.5">
        <blockquote className="max-h-20 overflow-auto rounded-r-md border-l-2 border-line-strong bg-surface-muted/50 py-1 pl-2.5 pr-2 text-caption text-fg-muted">
          {thread.source.file?.comments[0]?.anchor.quote ||
            thread.source.browser?.comments[0]?.anchor.snapshot.quote}
        </blockquote>
        <div className="flex gap-3">
          {(thread.source.file?.artifact_id || thread.source.browser) && (
            <button
              type="button"
              className={actionClass}
              onClick={() => {
                if (thread.source.browser) {
                  requestBrowserLocation(
                    conversationId,
                    thread.source.browser.comments[0]!.anchor.live,
                    openBrowser,
                  );
                } else if (
                  thread.source.file?.artifact_id &&
                  thread.source.file.comments[0]
                ) {
                  requestFileLocation(
                    conversationId,
                    thread.source.file.artifact_id,
                    thread.source.file.sha256,
                    thread.source.file.comments[0].anchor,
                    openFile,
                  );
                } else {
                  openFile();
                }
              }}
            >
              {t("artifacts.comments.locate")}
            </button>
          )}
          <button
            type="button"
            className={actionClass}
            disabled={disabled || busy || Boolean(pending)}
            onClick={() => void mutate(isOpen ? "resolve" : "reopen")}
          >
            {t(
              isOpen
                ? "artifacts.comments.resolve"
                : "artifacts.comments.reopen",
            )}
          </button>
        </div>
        <ResolutionActions
          orgId={orgId}
          conversationId={conversationId}
          thread={thread}
          resolution={resolution}
          disabled={disabled || busy}
        />
        {thread.comments.map((post) => (
          <div key={post.id} className="border-t border-line-subtle pt-2">
            <span className="text-caption text-fg-muted">
              {t(
                post.author_id === me?.id
                  ? "artifacts.comments.you"
                  : "artifacts.comments.member",
              )}
            </span>
            <p className="whitespace-pre-wrap break-words text-body-sm text-fg-default">
              {post.body}
            </p>
            <div className="mt-1 flex gap-3">
              <button
                type="button"
                className={actionClass}
                disabled={
                  disabled ||
                  busy ||
                  Boolean(resolution && resolution.status !== "exact")
                }
                onClick={() => void queue(post)}
              >
                {t("artifacts.comments.agent")}
              </button>
              {post.author_id === me?.id && (
                <button
                  type="button"
                  className={actionClass}
                  disabled={disabled || busy || Boolean(pending)}
                  onClick={() => edit(post.body, post.id)}
                >
                  {t("artifacts.comments.edit")}
                </button>
              )}
            </div>
          </div>
        ))}
        {(isOpen || editId) && (
          <fieldset
            disabled={disabled || busy || Boolean(pending)}
            className="space-y-1.5"
          >
            <textarea
              aria-label={t(
                editId
                  ? "artifacts.comments.editBody"
                  : "artifacts.comments.replyBody",
              )}
              className="min-h-16 w-full resize-y rounded-lg border border-line-default bg-surface-muted/50 p-2 text-body-sm outline-none focus:border-line-strong"
              value={body}
              onChange={(e) => edit(e.target.value)}
              maxLength={8192}
            />
            <div className="flex gap-3">
              <button
                type="button"
                className={actionClass}
                disabled={!body.trim()}
                onClick={() => void mutate(editId ? "edit" : "reply")}
              >
                {t(
                  editId
                    ? "artifacts.comments.saveEdit"
                    : "artifacts.comments.reply",
                )}
              </button>
              {editId && (
                <button
                  type="button"
                  className={actionClass}
                  onClick={() => edit("", "")}
                >
                  {t("common.cancel")}
                </button>
              )}
            </div>
          </fieldset>
        )}
        {pending && (
          <button
            type="button"
            className={actionClass}
            disabled={disabled || busy}
            onClick={() => void mutate(pending.action)}
          >
            {t("artifacts.comments.retryUpdate")}
          </button>
        )}
        {error && (
          <p role="alert" className="text-caption text-danger">
            {error}
          </p>
        )}
      </div>
    </details>
  );
}
