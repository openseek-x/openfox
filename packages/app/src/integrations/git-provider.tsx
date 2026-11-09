import { createContext, useCallback, useContext } from "react";

import { useT } from "@openfox/i18n";
import type { MessageKey, Translate } from "@openfox/i18n";

// Git 服务商家族（GitHub、GitLab 自部署版、极狐 GitLab.cn、Gitee）共用同一套详情页分区：
// 服务商经 Context 传入，文案里的品牌词按服务商替换，GitLab 系有差异的文案走覆盖表。
// 默认 GitHub，所以未包裹 Provider 的旧用法（含全部 GitHub 测试）行为不变。

export interface GitProviderInfo {
  id: string;
  name: string;
}

export const GitProviderContext = createContext<GitProviderInfo>({
  id: "github",
  name: "GitHub",
});

export function useGitProvider(): GitProviderInfo {
  return useContext(GitProviderContext);
}

export const isGitLab = (id: string) => id === "gitlab" || id === "gitlab_cn";
export const isGitee = (id: string) => id === "gitee";
export const isGitProvider = (id: string) =>
  id === "github" || isGitLab(id) || isGitee(id);

/** 授权完成页不知道目录里的展示名，用品牌名。 */
const BRAND: Record<string, string> = {
  gitlab: "GitLab",
  gitlab_cn: "极狐 GitLab.cn",
  gitee: "Gitee",
};

export function gitBrandText(providerId: string, text: string): string {
  const brand = BRAND[providerId];
  return brand ? text.replaceAll("GitHub", brand) : text;
}

/** GitLab 系与 GitHub 文案不同的键（键为 GitHub 版）。 */
const GITLAB_OVERRIDES: Partial<Record<MessageKey, MessageKey>> = {
  "integrations.github.bindOrg": "integrations.gitlab.bindInstance",
  "integrations.github.selectTitle": "integrations.gitlab.selectTitle",
  "integrations.github.selectDesc": "integrations.gitlab.selectDesc",
  "integrations.github.kindOrg": "integrations.gitlab.kindOrg",
  "integrations.github.disconnectBody": "integrations.gitlab.disconnectBody",
  "integrations.github.personalDesc": "integrations.gitlab.personalDesc",
  "integrations.github.linkAccount": "integrations.gitlab.linkAccount",
  "integrations.github.webhook.step1": "integrations.gitlab.webhook.step1",
  "integrations.github.webhook.step2": "integrations.gitlab.webhook.step2",
  "integrations.github.webhook.payloadUrl":
    "integrations.gitlab.webhook.payloadUrl",
  "integrations.github.webhook.secret": "integrations.gitlab.webhook.secret",
  "integrations.github.webhook.rotateBody":
    "integrations.gitlab.webhook.rotateBody",
  "integrations.github.pr.needWebhook": "integrations.gitlab.pr.needWebhook",
  "integrations.github.pr.linkPullRequests":
    "integrations.gitlab.pr.linkPullRequests",
  "integrations.github.pr.linkPullRequestsDesc":
    "integrations.gitlab.pr.linkPullRequestsDesc",
  "integrations.github.pr.backlink": "integrations.gitlab.pr.backlink",
  "integrations.github.pr.backlinkDesc": "integrations.gitlab.pr.backlinkDesc",
};

/** Gitee 与 GitHub 文案不同的键：只有 Webhook 配置步骤和重新生成提示（Gitee 没有实例地址，其余沿用 GitHub 的结构）。 */
const GITEE_OVERRIDES: Partial<Record<MessageKey, MessageKey>> = {
  "integrations.github.webhook.step1": "integrations.gitee.webhook.step1",
  "integrations.github.webhook.step2": "integrations.gitee.webhook.step2",
  "integrations.github.webhook.payloadUrl":
    "integrations.gitee.webhook.payloadUrl",
  "integrations.github.webhook.secret": "integrations.gitee.webhook.secret",
  "integrations.github.webhook.rotateBody":
    "integrations.gitee.webhook.rotateBody",
};

/** 与 useT 相同；GitLab 系、Gitee 会换成覆盖文案并把品牌词 GitHub 换成当前服务商名称。 */
export function useGitT(): Translate {
  const t = useT();
  const { id, name } = useGitProvider();
  return useCallback<Translate>(
    (key, params) => {
      if (isGitee(id)) {
        const text = t(GITEE_OVERRIDES[key] ?? key, { name, ...params });
        return text.replaceAll("GitHub", name);
      }
      if (!isGitLab(id)) return t(key, params);
      const text = t(GITLAB_OVERRIDES[key] ?? key, { name, ...params });
      return text.replaceAll("GitHub", name);
    },
    [t, id, name],
  );
}
