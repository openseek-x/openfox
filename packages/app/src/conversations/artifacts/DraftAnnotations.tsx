import { SaveComment } from "../comments/WorkspaceComments";
import type { ReactNode } from "react";
import { useT } from "@openfox/i18n";
import { Icon, IconButton } from "@openfox/ui";
import {
  type DraftBrowserContext,
  type DraftFileContext,
  removeBrowserComment,
  removeFileComment,
} from "../drafts";
import { BROWSER_PANEL, useOpenArtifactList } from "./params";
import { requestBrowserLocation } from "../browser/BrowserReferences";

/** 左侧待发送引用；可独立保存评论，发送仍由 Composer 负责。 */
export function DraftAnnotations({
  orgId,
  conversationId,
  files,
  browsers,
  disabled,
}: {
  orgId?: string;
  conversationId: string;
  files: DraftFileContext[];
  browsers: DraftBrowserContext[];
  disabled: boolean;
}) {
  const t = useT();
  const openBrowser = useOpenArtifactList(BROWSER_PANEL);
  const count = [...files, ...browsers].reduce(
    (n, file) => n + file.comments.length,
    0,
  );
  if (!count) return null;
  return (
    <section
      aria-label={t("artifacts.review.chatComments")}
      className="mb-2 rounded-xl border border-line-default bg-surface-muted px-3 py-2"
    >
      <div className="mb-1 flex items-center gap-1.5 text-caption text-fg-muted">
        <Icon name="message-circle" size={13} />
        {t("artifacts.review.pending", { count })}
      </div>
      <div className="max-h-48 space-y-1 overflow-y-auto">
        {files.map((file) => (
          <FileDraft
            key={file.id}
            file={file}
            orgId={orgId}
            conversationId={conversationId}
            disabled={disabled}
          />
        ))}
        {browsers.map((context) => (
          <div key={context.id} aria-label={t("artifacts.browser.annotations")}>
            {context.comments.map((comment) => (
              <Reference
                key={comment.id}
                name={context.title || context.source_url}
                body={comment.body}
                quote={
                  comment.anchor.snapshot.quote || t("artifacts.browser.region")
                }
                disabled={disabled}
                onRemove={() =>
                  removeBrowserComment(conversationId, comment.id)
                }
                footer={
                  orgId &&
                  !comment.discussion && (
                    <SaveComment
                      orgId={orgId}
                      conversationId={conversationId}
                      id={comment.id}
                      source={{
                        name: context.title || context.source_url,
                        browser: {
                          ...context,
                          comments: [
                            (({ screenshot, ...saved }) => saved)(comment),
                          ],
                        },
                      }}
                      screenshot={comment.screenshot}
                      disabled={disabled}
                      onSaved={() =>
                        removeBrowserComment(conversationId, comment.id)
                      }
                    />
                  )
                }

                openLabel={t("artifacts.browser.locate")}
                onOpen={() => {
                  requestBrowserLocation(
                    conversationId,
                    comment.anchor.live,
                    openBrowser,
                  );
                }}
              />
            ))}
          </div>
        ))}
      </div>
    </section>
  );
}
function FileDraft({
  orgId,
  file,
  conversationId,
  disabled,
}: {
  orgId?: string;
  file: DraftFileContext;
  conversationId: string;
  disabled: boolean;
}) {
  const t = useT();
  const open = useOpenArtifactList(file.artifact_id);
  return (
    <div
      aria-label={t("artifacts.review.contextChip", {
        name: file.name,
        count: file.comments.length,
      })}
    >
      {file.comments.map((comment) => (
        <Reference
          key={comment.thread_id}
          name={file.name}
          body={comment.body}
          quote={
            comment.anchor.quote ||
            t("artifacts.review.regionLabel", {
              page: comment.anchor.page ?? 1,
            })
          }
          disabled={disabled}
          onRemove={() =>
            removeFileComment(conversationId, file.id, comment.thread_id)
          }
          footer={
            orgId &&
            !comment.discussion && (
              <SaveComment
                orgId={orgId}
                conversationId={conversationId}
                id={comment.thread_id}
                source={{
                  name: file.name,
                  file: {
                    ...(({ name, ...saved }) => saved)(file),
                    comments: [comment],
                  },
                }}
                disabled={disabled}
                onSaved={() =>
                  removeFileComment(conversationId, file.id, comment.thread_id)
                }
              />
            )
          }
          onOpen={file.artifact_id ? open : undefined}
          openLabel={t("artifacts.list.open", { name: file.name })}
        />
      ))}
    </div>
  );
}
function Reference({
  name,
  body,
  quote,
  disabled,
  onRemove,
  onOpen,
  openLabel,
  footer,
}: {
  name: string;
  body: string;
  quote: string;
  disabled: boolean;
  onRemove: () => void;
  onOpen?: () => void;
  openLabel: string;
  footer?: ReactNode;
}) {
  const t = useT();
  return (
    <div className="flex items-start gap-1 py-1">
      <details className="min-w-0 flex-1">
        <summary
          className="cursor-pointer list-none text-body-sm [&::-webkit-details-marker]:hidden"
          title={body}
        >
          <span className="block truncate text-caption text-fg-muted">
            {name}
          </span>
          <span className="line-clamp-2 whitespace-pre-wrap break-words leading-5">
            {body}
          </span>
        </summary>
        <blockquote className="mt-1 max-h-20 overflow-auto whitespace-pre-wrap break-words border-l-2 border-line-default pl-2 text-caption text-fg-muted">
          {quote}
        </blockquote>
        {onOpen && (
          <button
            type="button"
            className="mt-1 text-caption text-fg-secondary hover:text-fg-default"
            onClick={onOpen}
          >
            {openLabel}
          </button>
        )}
        {footer}
      </details>
      <IconButton
        label={t("artifacts.review.removeComment")}
        icon={<Icon name="x" size={13} />}
        size="28"
        disabled={disabled}
        onClick={onRemove}
      />
    </div>
  );
}
