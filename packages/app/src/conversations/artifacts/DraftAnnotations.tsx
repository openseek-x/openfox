import { SaveComment } from "../comments/WorkspaceComments";
import { useEffect, useState, type ReactNode } from "react";
import { useT } from "@openfox/i18n";
import { Button, Icon, IconButton } from "@openfox/ui";
import {
  type DraftBrowserContext,
  type DraftFileContext,
  removeBrowserComment,
  removeFileComment,
  updateBrowserComment,
  updateFileComment,
} from "../drafts";
import { requestFileLocation } from "./fileLocation";
import { BROWSER_PANEL, useOpenArtifactList } from "./params";
import { requestBrowserLocation } from "../browser/BrowserReferences";

/** 输入区上方待发送批注列表：支持就地编辑、原位定位、截图缩略图与保存为独立评论。 */
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
      className="mb-2 rounded-xl border border-line-subtle bg-surface-muted px-3 py-2"
    >
      <div className="mb-1.5 flex items-center gap-1.5 text-caption font-medium text-fg-muted">
        <Icon name="message-circle" size={13} />
        {t("artifacts.review.pending", { count })}
      </div>
      <div className="max-h-48 space-y-1.5 overflow-y-auto">
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
          <div
            key={context.id}
            aria-label={t("artifacts.browser.annotations")}
            className="space-y-1.5"
          >
            {context.comments.map((comment) => (
              <Reference
                key={comment.id}
                name={context.title || context.source_url}
                body={comment.body}
                quote={
                  comment.anchor.snapshot.quote || t("artifacts.browser.region")
                }
                screenshot={comment.screenshot}
                disabled={disabled}
                onEdit={(nextBody) =>
                  updateBrowserComment(conversationId, comment.id, nextBody)
                }
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
      className="space-y-1.5"
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
          onEdit={(nextBody) =>
            updateFileComment(
              conversationId,
              file.id,
              comment.thread_id,
              nextBody,
            )
          }
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
          onOpen={
            file.artifact_id
              ? () =>
                  requestFileLocation(
                    conversationId,
                    file.artifact_id!,
                    file.sha256,
                    comment.anchor,
                    open,
                  )
              : undefined
          }
          openLabel={t("artifacts.list.open", { name: file.name })}
        />
      ))}
    </div>
  );
}

function RegionThumbnail({ file }: { file: File }) {
  const t = useT();
  const [url, setUrl] = useState("");
  useEffect(() => {
    const objectUrl = URL.createObjectURL(file);
    setUrl(objectUrl);
    return () => {
      URL.revokeObjectURL(objectUrl);
      setUrl("");
    };
  }, [file]);
  if (!url) return null;
  return (
    <img
      src={url}
      alt={t("artifacts.review.regionPreview")}
      className="mt-1.5 max-h-20 max-w-36 rounded-md border border-line-subtle bg-surface-muted object-contain"
    />
  );
}

function Reference({
  name,
  body,
  quote,
  screenshot,
  disabled,
  onEdit,
  onRemove,
  onOpen,
  openLabel,
  footer,
}: {
  name: string;
  body: string;
  quote: string;
  screenshot?: File;
  disabled: boolean;
  onEdit: (nextBody: string) => boolean;
  onRemove: () => void;
  onOpen?: () => void;
  openLabel: string;
  footer?: ReactNode;
}) {
  const t = useT();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(body);
  const [editError, setEditError] = useState(false);

  const startEdit = () => {
    setDraft(body);
    setEditError(false);
    setEditing(true);
  };
  const saveEdit = () => {
    if (!draft.trim()) return;
    if (onEdit(draft)) {
      setEditing(false);
      setEditError(false);
    } else {
      setEditError(true);
    }
  };

  return (
    <div className="flex items-start gap-1 rounded-lg border border-line-subtle bg-surface-card px-2 py-1.5">
      {editing ? (
        <div className="min-w-0 flex-1 space-y-1.5">
          <span className="block truncate text-caption text-fg-muted">
            {name}
          </span>
          <textarea
            aria-label={t("artifacts.review.editComment")}
            rows={2}
            value={draft}
            disabled={disabled}
            onChange={(event) => {
              setDraft(event.target.value);
              setEditError(false);
            }}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                event.preventDefault();
                setEditing(false);
              }
              if (
                event.key === "Enter" &&
                (event.metaKey || event.ctrlKey) &&
                !event.nativeEvent.isComposing
              ) {
                event.preventDefault();
                saveEdit();
              }
            }}
            className="w-full resize-y rounded-md border border-line-default bg-surface-muted px-2 py-1 text-body-sm outline-none focus:border-line-strong"
          />
          {editError && (
            <p role="alert" className="text-caption text-danger">
              {t("artifacts.review.draftLimit")}
            </p>
          )}
          <div className="flex items-center gap-1.5">
            <Button
              size="28"
              variant="primary"
              className="h-6 px-2 text-caption"
              disabled={disabled || !draft.trim()}
              onClick={saveEdit}
            >
              {t("artifacts.review.saveComment")}
            </Button>
            <Button
              size="28"
              variant="ghost"
              className="h-6 px-2 text-caption"
              onClick={() => setEditing(false)}
            >
              {t("common.cancel")}
            </Button>
          </div>
        </div>
      ) : (
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
          {screenshot && <RegionThumbnail file={screenshot} />}
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
      )}
      {!editing && (
        <IconButton
          label={t("artifacts.review.editComment")}
          icon={<Icon name="pencil" size={13} />}
          size="28"
          disabled={disabled}
          onClick={startEdit}
        />
      )}
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
