import type { TimelineActivity } from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import { Avatar, Badge } from "@openfox/ui";
import { personName, type Person } from "../queries";
import { formatRelativeTime } from "../primitives/dates";

type SourceActivity = Pick<TimelineActivity, "safe_diff" | "actor_name">;
const text = (v: unknown) => (typeof v === "string" ? v : "");
const integer = (v: unknown) =>
  typeof v === "number" && Number.isSafeInteger(v) && v > 0;
const date = (v: unknown) =>
  typeof v === "string" && Number.isFinite(Date.parse(v)) ? v : "";

export function bitbucketCommentSource(activity: SourceActivity) {
  const d = activity.safe_diff ?? {};
  const repository = text(d.repository);
  const parts = repository.split("/");
  const validRepo =
    parts.length === 2 &&
    repository.length <= 255 &&
    parts.every((p) => /^[A-Za-z0-9_.-]+$/.test(p) && p !== "." && p !== "..");
  const expectedURL =
    validRepo && integer(d.number)
      ? `https://bitbucket.org/${repository}/pull-requests/${d.number}`
      : "";
  const url =
    d.provider === "bitbucket" && expectedURL && text(d.url) === expectedURL
      ? expectedURL
      : "";
  return {
    author: text(d.external_author_name),
    authorID: text(d.external_author_id),
    userID: text(d.author_user_id),
    repository,
    number: integer(d.number) ? String(d.number) : "—",
    commentID: integer(d.comment_id) ? String(d.comment_id) : "—",
    occurred: date(d.occurred_at),
    updated: date(d.updated_at),
    deleted: d.deleted === true,
    url,
  };
}
export function bitbucketCommentText(
  t: Translate,
  activity: SourceActivity,
): string {
  const source = bitbucketCommentSource(activity);
  return t("integrations.bitbucket.commentActivity", {
    author:
      source.author ||
      source.authorID ||
      t("integrations.bitbucket.externalAuthor"),
    number: source.number,
    comment: source.commentID,
  });
}
export function BitbucketCommentActivity({
  activity,
  people,
  locale,
}: {
  activity: TimelineActivity;
  people: Person[];
  locale: string;
}) {
  const t = useT(),
    source = bitbucketCommentSource(activity);
  const person = source.userID
    ? people.find((p) => p.id === source.userID)
    : undefined;
  const author =
    source.author ||
    source.authorID ||
    t("integrations.bitbucket.externalAuthor");
  const updated =
    source.updated &&
    source.occurred &&
    Date.parse(source.updated) > Date.parse(source.occurred);
  return (
    <li className="flex items-start gap-2 text-body-sm text-fg-muted">
      <Avatar name={author} size={16} />
      <div className="grid min-w-0 flex-1 gap-1">
        <span>{bitbucketCommentText(t, activity)}</span>
        <span className="text-caption">{source.repository}</span>
        {source.userID && (
          <Badge tone="info">
            {person
              ? t("integrations.bitbucket.boundAuthor", {
                  name: personName(person),
                })
              : t("integrations.bitbucket.boundIdentity")}
          </Badge>
        )}
        {source.deleted ? (
          <span>{t("integrations.bitbucket.commentDeleted")}</span>
        ) : updated ? (
          <span>{t("integrations.bitbucket.commentUpdated")}</span>
        ) : null}
        {source.updated && (updated || source.deleted) && (
          <time dateTime={source.updated} className="text-caption">
            {t("integrations.bitbucket.sourceUpdated")}{" "}
            {formatRelativeTime(source.updated, locale)}
          </time>
        )}
        {source.url && (
          <a
            className="underline"
            href={source.url}
            target="_blank"
            rel="noopener noreferrer"
          >
            {t("integrations.bitbucket.openSourcePR")}
          </a>
        )}
      </div>
      {source.occurred && (
        <time dateTime={source.occurred} className="shrink-0 text-caption">
          {formatRelativeTime(source.occurred, locale)}
        </time>
      )}
    </li>
  );
}
