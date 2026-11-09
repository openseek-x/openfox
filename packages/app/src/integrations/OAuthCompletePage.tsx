import { useEffect } from "react";
import { Link, useSearchParams } from "react-router-dom";

import { useT } from "@openfox/i18n";
import { Icon } from "@openfox/ui";

import { localKeys, readLocal } from "../storage";
import { gitBrandText } from "./git-provider";
import { brandText } from "./im-provider";
import { oauthErrorKey } from "./model";

// OAuth 授权完成页：GitHub 回调后服务端把弹窗重定向到这里。
// 有同源 opener 时把结果 postMessage 回去并关闭弹窗；否则直接显示结果。

export function OAuthCompletePage() {
  const t = useT();
  const [params] = useSearchParams();
  const provider = params.get("provider") ?? "";
  const session = params.get("session") ?? "";
  const status = params.get("status") ?? "";
  const reason = params.get("reason") ?? "";
  const ok = status === "ok";

  useEffect(() => {
    const opener = window.opener as Window | null;
    if (!opener || opener === window) return;
    try {
      opener.postMessage(
        { type: "openfox:oauth", provider, session, status, reason },
        window.location.origin,
      );
      window.close();
    } catch {
      // 跨源 opener 访问会抛错：保持页面，显示结果文案
    }
  }, [provider, session, status, reason]);

  const lastOrg = readLocal(localKeys.lastOrg);
  const back = lastOrg
    ? `/o/${lastOrg}/settings/integrations${provider ? `/${encodeURIComponent(provider)}` : ""}`
    : "/";
  return (
    <main className="flex min-h-screen items-center justify-center bg-surface-app p-6">
      <div className="flex w-full max-w-[420px] flex-col items-center gap-3 rounded-xl border border-line-default bg-surface-card p-8 text-center">
        <Icon name={ok ? "check" : "circle-alert"} size={24} />
        <h1 className="text-title font-medium">
          {ok
            ? brandText(
                provider ?? "",
                t(
                  provider === "feishu" ||
                    provider === "lark" ||
                    provider === "dingtalk"
                    ? "integrations.feishu.oauthOkTitle"
                    : "integrations.oauth.okTitle",
                ),
              )
            : t("integrations.oauth.failedTitle")}
        </h1>
        <p className="text-body text-fg-muted">
          {ok
            ? t("integrations.oauth.okDesc")
            : gitBrandText(provider, t(oauthErrorKey(reason)))}
        </p>
        <Link to={back} className="text-body-sm font-medium underline">
          {lastOrg
            ? t("integrations.oauth.backToIntegrations")
            : t("integrations.oauth.backHome")}
        </Link>
      </div>
    </main>
  );
}
