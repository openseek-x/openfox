import { useQuery } from "@tanstack/react-query";

import type { TaskGitLink } from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import { Badge, Icon } from "@openfox/ui";

import { keys, useApi } from "../../context";
import { Section } from "./Sections";

// 任务详情的「GitHub 关联」区：Issue（含同步条目）、PR 与 Commit 一起列出，没有关联时整区不显示。

const STATE_KEY: Record<TaskGitLink["state"], MessageKey> = {
  OPEN: "integrations.github.links.open",
  DRAFT: "integrations.github.links.draft",
  MERGED: "integrations.github.links.merged",
  CLOSED: "integrations.github.links.closed",
  COMMITTED: "integrations.github.links.commit",
};

const STATE_CLASS: Partial<Record<TaskGitLink["state"], string>> = {
  MERGED: "bg-[#ede9fe] text-[#6d28d9]",
};

const ISSUE_CLOSED_CLASS = "bg-[#ede9fe] text-[#6d28d9]";

// 同一任务里 Issue 在最前，然后是 PR，最后是 Commit
const KIND_RANK: Record<TaskGitLink["kind"], number> = {
  ISSUE: 0,
  PULL_REQUEST: 1,
  COMMIT: 2,
};

function stateTone(state: TaskGitLink["state"]) {
  if (state === "OPEN") return "success" as const;
  if (state === "CLOSED") return "danger" as const; // Issue 的 Closed 用 ISSUE_CLOSED_CLASS 覆盖为紫灰
  return "muted" as const;
}

/** repo#number（Issue、PR）或 repo@短 sha。 */
export function gitLinkRef(link: TaskGitLink): string {
  return link.kind !== "COMMIT" && link.number != null
    ? `${link.repository}#${link.number}`
    : `${link.repository}@${link.sha.slice(0, 7)}`;
}

// Git 关联的服务商展示（图标与名称）；未知服务商按 GitHub 处理。
const PROVIDER_VIEW: Record<
  string,
  { icon: "github" | "brand-gitlab" | "brand-gitee"; name: string }
> = {
  github: { icon: "github", name: "GitHub" },
  gitlab: { icon: "brand-gitlab", name: "GitLab" },
  gitlab_cn: { icon: "brand-gitlab", name: "极狐 GitLab.cn" },
  gitee: { icon: "brand-gitee", name: "Gitee" },
};
const providerView = (provider: string) =>
  PROVIDER_VIEW[provider] ?? PROVIDER_VIEW.github!;

export function GitLinksSection({
  orgId,
  spaceId,
  taskId,
  people,
}: {
  orgId: string;
  spaceId: string;
  taskId: string;
  people: { id: string; email: string }[];
}) {
  const t = useT();
  const api = useApi();
  const links = useQuery({
    queryKey: keys.taskSub(orgId, taskId, "git-links"),
    queryFn: () => api.tasks.gitLinks(orgId, spaceId, taskId),
  });
  // sort 稳定：同类保持服务端顺序
  const items = [...(links.data ?? [])].sort(
    (a, b) => KIND_RANK[a.kind] - KIND_RANK[b.kind],
  );
  if (items.length === 0) return null;
  // 标题：全是同一家 → 「GitLab / Gitee 关联」（GitHub 沿用「GitHub 关联」）；GitHub 与 GitLab、Gitee 混合 → 「Git 关联」
  const isLab = (provider: string) => provider.startsWith("gitlab");
  const kinds = new Set(
    items.map((link) =>
      isLab(link.provider)
        ? "gitlab"
        : link.provider === "gitee"
          ? "gitee"
          : "github",
    ),
  );
  const titleKey =
    kinds.size > 1
      ? "integrations.gitlab.links.mixedTitle"
      : kinds.has("gitlab")
        ? "integrations.gitlab.links.title"
        : kinds.has("gitee")
          ? "integrations.gitee.links.title"
          : "integrations.github.links.title";
  return (
    <Section title={t(titleKey)} count={String(items.length)}>
      <ul className="flex flex-col">
        {items.map((link) => {
          const author = link.author_user_id
            ? people.find((p) => p.id === link.author_user_id)?.email
            : undefined;
          return (
            <li
              key={link.id}
              className="flex min-h-9 items-center gap-2 rounded-lg px-1 py-1 text-body-sm"
            >
              {link.kind === "ISSUE" && (
                <Icon
                  name={providerView(link.provider).icon}
                  size={14}
                  aria-label={`${providerView(link.provider).name} Issue`}
                  className="shrink-0 text-fg-muted"
                />
              )}
              <Badge
                tone={stateTone(link.state)}
                className={
                  link.kind === "ISSUE" && link.state === "CLOSED"
                    ? ISSUE_CLOSED_CLASS
                    : STATE_CLASS[link.state]
                }
              >
                {t(STATE_KEY[link.state])}
              </Badge>
              <span className="shrink-0 text-fg-muted">{gitLinkRef(link)}</span>
              <a
                href={link.url}
                target="_blank"
                rel="noopener noreferrer"
                className="flex min-w-0 flex-1 items-center gap-1 truncate text-fg-default hover:underline"
              >
                <span className="truncate">{link.title}</span>
                <Icon
                  name="external-link"
                  size={12}
                  className="shrink-0 text-fg-muted"
                />
              </a>
              <span className="shrink-0 text-fg-muted">
                {author ?? link.author_login}
              </span>
              <time
                dateTime={link.created_at}
                className="shrink-0 text-fg-subtle"
              >
                {new Date(link.created_at).toLocaleDateString()}
              </time>
            </li>
          );
        })}
      </ul>
    </Section>
  );
}
