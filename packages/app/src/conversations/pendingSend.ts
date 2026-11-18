import type {
  Api,
  ConversationWorkspaceSelection,
  MessageCreateInput,
} from "@openfox/api";
import type { ComposerSubmit } from "./Composer";
import { notifyDraft, type ComposerDraft } from "./drafts";
import { workspaceStore, type WorkspaceStore } from "./workspaceStorage";

export type PendingSend = {
  clientId: string;
  conversationId: string;
  created: boolean;
  selection?: ConversationWorkspaceSelection;
  input: ComposerSubmit;
  source: ComposerDraft;
  uploaded: string[];
  request?: MessageCreateInput;
};
export const pendingKey = (key: string) => `send:${key}`;

/** Consume only the frozen input; edits made while the request was in flight survive. */
export function consumeSentDraft(
  store: WorkspaceStore,
  key: string,
  source: ComposerDraft,
  notify = true,
) {
  const latest = store.read<ComposerDraft>(`draft:${key}`);
  if (!latest) return;
  const files = (latest.fileContexts ?? []).flatMap((c) => {
    const sent = source.fileContexts?.find((s) => s.id === c.id);
    const comments = c.comments.filter(
      (a) =>
        !sent?.comments.some(
          (s) => s.thread_id === a.thread_id && s.body === a.body,
        ),
    );
    return comments.length
      ? [{ ...c, id: sent ? crypto.randomUUID() : c.id, comments }]
      : [];
  });
  const browsers = (latest.browserContexts ?? []).flatMap((c) => {
    const sent = source.browserContexts?.find((s) => s.id === c.id);
    const comments = c.comments.filter(
      (a) => !sent?.comments.some((s) => s.id === a.id && s.body === a.body),
    );
    return comments.length
      ? [{ ...c, id: sent ? crypto.randomUUID() : c.id, comments }]
      : [];
  });
  const sameBody =
    latest.bodyVersion === source.bodyVersion && latest.body === source.body;
  store.write(`draft:${key}`, {
    ...latest,
    body: sameBody ? "" : latest.body,
    picked: sameBody ? [] : latest.picked,
    files: latest.filesVersion === source.filesVersion ? [] : latest.files,
    reply: latest.reply?.id === source.reply?.id ? undefined : latest.reply,
    assistantAction:
      latest.assistantAction === source.assistantAction
        ? undefined
        : latest.assistantAction,
    fileContexts: files,
    browserContexts: browsers,
  } satisfies ComposerDraft);
  if (notify && workspaceStore() === store) notifyDraft(key);
}

export async function prepareRequest(
  api: Api,
  orgId: string,
  store: WorkspaceStore,
  key: string,
  pending: PendingSend,
) {
  if (pending.request) return pending;
  const input = pending.input;
  const images = (input.browserContexts ?? []).flatMap((c) =>
    c.comments.flatMap((a) => (a.screenshot ? [a.screenshot] : [])),
  );
  const files = [...input.files, ...images];
  if (files.length > 10) throw new Error("ATTACHMENT_LIMIT");
  const uploaded = [...pending.uploaded];
  for (let i = 0; i < files.length; i++) {
    if (uploaded[i]) continue;
    const file = files[i]!;
    try {
      uploaded[i] = (
        await api.attachments.uploadToConversation(
          orgId,
          pending.conversationId,
          file,
        )
      ).id;
    } catch (reason) {
      throw new AttachmentUploadError(file.name, reason);
    }
    pending = { ...pending, uploaded: [...uploaded] };
    store.write(pendingKey(key), pending);
    await store.flush();
  }
  let imageIndex = input.files.length;
  const request: MessageCreateInput = {
    body: input.body,
    client_message_id: pending.clientId,
    ...(input.mentions.length && { mentions: input.mentions }),
    ...(uploaded.length && { attachment_ids: uploaded }),
    ...(input.replyToId && { reply_to_id: input.replyToId }),
    ...(input.assistantAction && { assistant_action: input.assistantAction }),
    ...(input.fileContexts?.length && {
      file_contexts: input.fileContexts.map(({ name, ...context }) => context),
    }),
    ...(input.browserContexts?.length && {
      browser_contexts: input.browserContexts.map((c) => ({
        ...c,
        comments: c.comments.map(({ screenshot, ...comment }) => ({
          ...comment,
          ...(screenshot && {
            screenshot_attachment_id: uploaded[imageIndex++]!,
          }),
        })),
      })),
    }),
  };
  pending = { ...pending, request };
  store.write(pendingKey(key), pending);
  await store.flush();
  return pending;
}

export class AttachmentUploadError extends Error {
  constructor(
    readonly fileName: string,
    readonly reason: unknown,
  ) {
    super(`attachment upload failed: ${fileName}`);
  }
}

// A second Enter or a remounted view must not prepare competing uploads for one request.
const flights = new WeakMap<WorkspaceStore, Set<string>>();
export async function withSendLock<T>(
  store: WorkspaceStore,
  key: string,
  action: () => Promise<T>,
) {
  let keys = flights.get(store);
  if (!keys) {
    keys = new Set();
    flights.set(store, keys);
  }
  if (keys.has(key)) return;
  keys.add(key);
  try {
    return await action();
  } finally {
    keys.delete(key);
  }
}
