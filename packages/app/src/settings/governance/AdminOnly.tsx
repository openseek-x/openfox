import type { ReactNode } from "react";

import { useT } from "@openfox/i18n";
import { Icon } from "@openfox/ui";

import { useOrgRole } from "../common";

/**
 * 阶段 12e：仅组织所有者与管理员可见的设置页外壳。
 * 角色未加载时不渲染内容（避免先发请求再被拒）；其他角色直接看到无权限页。
 * 这只是界面层的收口，真正的鉴权在服务端。
 */
export function AdminOnly({
  orgId,
  children,
}: {
  orgId: string;
  children: ReactNode;
}) {
  const t = useT();
  const role = useOrgRole(orgId);
  if (role === undefined) return <div aria-busy="true" className="h-full" />;
  if (role !== "OWNER" && role !== "ADMIN")
    return (
      <div
        role="alert"
        className="flex h-full flex-col items-center justify-center gap-3 p-10 text-center"
      >
        <span className="flex size-12 items-center justify-center rounded-xl bg-surface-muted text-fg-muted">
          <Icon name="lock" size={24} />
        </span>
        <h1 className="text-title font-medium">
          {t("governance.forbidden.title")}
        </h1>
        <p className="max-w-sm text-body text-fg-muted">
          {t("governance.forbidden.desc")}
        </p>
      </div>
    );
  return <>{children}</>;
}
