import { Link, useParams } from "react-router-dom";

import { useT } from "@openfox/i18n";
import { Button, Icon, type IconName } from "@openfox/ui";

import { useRejectNavLocation } from "../shell/navMemory";

function StatePage({
  icon,
  title,
  desc,
  action,
}: {
  icon: IconName;
  title: string;
  desc: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 p-10 text-center">
      <span className="flex size-12 items-center justify-center rounded-xl bg-surface-muted text-fg-muted ring-1 ring-inset ring-line-floating">
        <Icon name={icon} size={24} />
      </span>
      <h1 className="text-title font-semibold tracking-[-0.01em] text-fg-default">
        {title}
      </h1>
      <p className="max-w-sm text-body-sm text-fg-muted">{desc}</p>
      {action}
    </div>
  );
}

/** 未匹配路由的兜底页：页面不存在或功能暂未开放，不显示假数据。 */
export function UnavailablePage() {
  const t = useT();
  // 不存在或未接入的页面不记入主导航记忆，避免「首页 / 全部设置」回到这里
  useRejectNavLocation(true);
  return (
    <StatePage
      icon="clock"
      title={t("shell.unavailableTitle")}
      desc={t("shell.unavailableDesc")}
    />
  );
}

export function NotFoundPage() {
  const t = useT();
  const { orgId } = useParams();
  useRejectNavLocation(Boolean(orgId));
  return (
    <StatePage
      icon="help-circle"
      title={t("shell.notFoundTitle")}
      desc={t("shell.notFoundDesc")}
      action={
        <Link to={orgId ? `/o/${orgId}` : "/"}>
          <Button variant="outline">{t("shell.backHome")}</Button>
        </Link>
      }
    />
  );
}
