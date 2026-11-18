import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { Link, useLocation } from "react-router-dom";

import { isApiError, type ShareSnapshotItem } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";

import { useApi } from "../context";
import { safeHref } from "../conversations/Markdown";

/** 地址片段里的令牌；没有时返回空串。片段不会发给服务器，也不会进入 Referer。 */
function tokenFromHash(hash: string): string {
  return hash.replace(/^#/, "").trim();
}

/**
 * 公开分享快照查看页：路由在 RequireAuth 之外。
 * 只做只读渲染：正文作为纯文本节点，链接只放行 http(s)/mailto 且带 noopener noreferrer nofollow，
 * 不执行快照里的任何脚本，不加载登录态相关资源。
 */
export function SharedSnapshotPage() {
  const t = useT();
  const { formatDate } = useI18n();
  const api = useApi();
  // Subscribe to same-page fragment navigation; reading window.location alone
  // does not rerender a retained route element when another share is opened.
  const token = tokenFromHash(useLocation().hash);
  // 页面本身不被索引，且不向外链泄露地址（令牌在片段里，本就不会进 Referer，这里再加一层）
  useEffect(() => {
    const added = ["robots:noindex, nofollow", "referrer:no-referrer"].map(
      (pair) => {
        const [name, content] = pair.split(":") as [string, string];
        const meta = document.createElement("meta");
        meta.name = name;
        meta.content = content;
        document.head.appendChild(meta);
        return meta;
      },
    );
    return () => added.forEach((meta) => meta.remove());
  }, []);
  const view = useQuery({
    queryKey: ["share-view", token],
    queryFn: () => api.shareSnapshots.view(token),
    enabled: token !== "",
    retry: false,
    // 不缓存：撤销后重新打开必须立即失效
    gcTime: 0,
    staleTime: 0,
  });
  const date = (value: string) =>
    formatDate(new Date(value), { dateStyle: "medium", timeStyle: "short" });

  if (token === "" || view.isError) {
    // 404 与任何失败统一表现，不区分原因；限组织的内容给登录入口
    const unauthorizedHint =
      !isApiError(view.error) || view.error.status !== 429;
    return (
      <main className="mx-auto flex min-h-screen max-w-xl flex-col items-center justify-center gap-3 px-6 text-center">
        <h1 className="text-title font-medium">
          {t("share.view.notAvailableTitle")}
        </h1>
        <p className="text-body text-fg-muted">
          {t("share.view.notAvailableHelp")}
        </p>
        {unauthorizedHint && (
          <Link
            className="text-body text-accent underline"
            to="/login"
            state={{ next: `/share#${token}` }}
          >
            {t("share.view.signIn")}
          </Link>
        )}
      </main>
    );
  }
  if (!view.data)
    return (
      <main
        aria-busy="true"
        className="flex min-h-screen items-center justify-center text-body text-fg-muted"
      >
        {t("share.view.loading")}
      </main>
    );
  const data = view.data;
  return (
    <main className="mx-auto flex min-h-screen max-w-3xl flex-col gap-4 px-6 py-8">
      <header className="flex flex-col gap-1">
        <h1 className="text-title font-medium break-words">{data.title}</h1>
        <p className="text-body-sm text-fg-muted">
          {t("share.view.meta", {
            name: data.creator_name,
            org: data.organization_name,
          })}
        </p>
        <p className="text-caption text-fg-muted">
          {t("share.view.createdAt", { date: date(data.created_at) })}
          {data.expires_at &&
            ` · ${t("share.view.expiresAt", { date: date(data.expires_at) })}`}
        </p>
        <p className="text-caption text-fg-subtle">
          {t("share.view.snapshotNote")}
        </p>
      </header>
      <ol className="flex flex-col gap-3">
        {data.items.map((item, index) => (
          <SnapshotItem key={index} index={index + 1} item={item} />
        ))}
      </ol>
    </main>
  );
}

function SnapshotItem({
  index,
  item,
}: {
  index: number;
  item: ShareSnapshotItem;
}) {
  const t = useT();
  const { formatDate } = useI18n();
  return (
    <li
      id={`item-${index}`}
      className="flex flex-col gap-1 rounded-lg border border-line-soft bg-surface-card px-4 py-3"
    >
      <div className="flex items-center gap-2 text-body-sm">
        <span className="font-medium">{item.author}</span>
        {item.author_kind === "AGENT" && (
          <span className="rounded bg-surface-muted px-1.5 text-caption text-fg-muted">
            {t("share.view.agent")}
          </span>
        )}
        <time className="text-caption text-fg-muted" dateTime={item.at}>
          {formatDate(new Date(item.at), {
            dateStyle: "medium",
            timeStyle: "short",
          })}
        </time>
        {item.reply_to ? (
          <span className="text-caption text-fg-muted">
            {t("share.view.replyTo", { n: item.reply_to })}
          </span>
        ) : null}
      </div>
      {/* 只产生文本与受限链接，没有 dangerouslySetInnerHTML */}
      <p className="text-body break-words whitespace-pre-wrap">
        {linkify(item.body)}
      </p>
    </li>
  );
}

const BARE_URL = /(https?:\/\/[^\s<>()]+[^\s<>().,;:!?'"])/g;

/** 只把裸 https/http 地址变成链接，其余一律是文本节点；外链带 noopener noreferrer nofollow。 */
export function linkify(text: string) {
  return text.split(BARE_URL).map((part, index) => {
    const href = index % 2 === 1 ? safeHref(part) : null;
    return href ? (
      <a
        key={index}
        href={href}
        target="_blank"
        rel="noopener noreferrer nofollow"
        className="text-info underline underline-offset-2"
      >
        {part}
      </a>
    ) : (
      part
    );
  });
}
