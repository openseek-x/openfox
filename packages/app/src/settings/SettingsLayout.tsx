import { desktopBridge } from "../desktop";
import { ProxyDialog } from "./ProxyDialog";
import { Fragment, useState } from "react";
import { NavLink, Outlet } from "react-router-dom";

import { useT, type MessageKey } from "@openfox/i18n";
import { Icon, cn, type IconName } from "@openfox/ui";

import { GetStartedNavCount } from "../me/GetStartedPage";
import { useBetaTier } from "../me/queries";
import { MySpacesNavGroup } from "../spaces/SpacesNav";
import { SidebarCollapseButton } from "../shell/SidebarCollapseButton";

interface SettingsItem {
  /** 相对 /o/:orgId/settings 的路径 */
  path: string;
  icon: IconName;
  label: MessageKey;
  /** 只有内测账号可见 */
  beta?: boolean;
}

interface SettingsGroup {
  label: MessageKey;
  items: SettingsItem[];
}

/** 设置导航分组。 */
export const settingsGroups: SettingsGroup[] = [
  {
    label: "settings.groups.manage",
    items: [
      {
        path: "organization",
        icon: "building",
        label: "settings.items.organization",
      },
      { path: "spaces", icon: "folder-kanban", label: "settings.items.spaces" },
      { path: "members", icon: "users", label: "settings.items.members" },
      {
        path: "integrations",
        icon: "plug",
        label: "settings.items.integrations",
      },
      {
        path: "import-export",
        icon: "arrow-left-right",
        label: "settings.items.importExport",
      },
    ],
  },
  {
    label: "settings.groups.agents",
    items: [
      { path: "agents", icon: "briefcase", label: "settings.items.agents" },
      { path: "crews", icon: "users-round", label: "settings.items.squads" },
      { path: "computers", icon: "monitor", label: "settings.items.computers" },
    ],
  },
  {
    label: "settings.groups.global",
    items: [
      { path: "git", icon: "folder-git-2", label: "gitSettings.title" },
      { path: "labels", icon: "tag", label: "settings.items.labels" },
      {
        path: "statuses",
        icon: "circle-dot",
        label: "settings.items.statuses",
      },
      {
        path: "relation-types",
        icon: "link",
        label: "settings.items.relationTypes",
      },
      {
        path: "custom-fields",
        icon: "sliders-horizontal",
        label: "settings.items.customFields",
      },
    ],
  },
  {
    label: "settings.groups.mine",
    items: [
      { path: "profile", icon: "user", label: "settings.items.profile" },
      {
        path: "preferences",
        icon: "settings-2",
        label: "settings.items.preferences",
      },
      // 开始使用（偏好设置之后、快捷键之前）
      {
        path: "get-started",
        icon: "graduation-cap",
        label: "settings.items.getStarted",
      },
      {
        path: "shortcuts",
        icon: "keyboard",
        label: "settings.items.shortcuts",
      },
      {
        path: "notifications",
        icon: "bell",
        label: "settings.items.notifications",
      },
      { path: "tokens", icon: "key-round", label: "settings.items.tokens" },
      // 第三方账号绑定（访问令牌之后）
      {
        path: "account-binding",
        icon: "link",
        label: "settings.items.accountBinding",
      },
      // 内测功能开关（我的设置分组最后，仅内测账号可见）
      {
        path: "beta-features",
        icon: "flask-conical",
        label: "settings.items.betaFeatures",
        beta: true,
      },
    ],
  },
];

/**
 * 设置导航：进入设置后替换外壳侧栏里的主导航面板，
 * 宽度与主导航一致；设置子页面直接渲染在主区域。
 */
export function SettingsNavigation({
  orgId,
  onCollapse,
}: {
  orgId: string;
  onCollapse: () => void;
}) {
  const t = useT();
  const [proxyOpen, setProxyOpen] = useState(false);
  const bridge = desktopBridge();
  const base = `/o/${orgId}/settings`;
  const beta = useBetaTier() === true;
  return (
    <nav
      aria-label={t("settings.a11y.nav")}
      className="group/panel flex min-w-0 flex-1 flex-col gap-3 overflow-y-auto px-1.5 pt-1.5 pb-3"
    >
      <div className="flex h-11 shrink-0 items-center gap-1 pr-1 pl-2.5">
        <h2 className="min-w-0 flex-1 truncate text-title font-bold">
          {t("settings.title")}
        </h2>
        <SidebarCollapseButton onClick={onCollapse} />
      </div>
      {settingsGroups.map((group) => (
        <Fragment key={group.label}>
          {/* 「我的空间」分组插在「全局配置」之前 */}
          {group.label === "settings.groups.global" && (
            <MySpacesNavGroup orgId={orgId} />
          )}
          <div className="flex flex-col gap-0.5">
            <div className="px-2.5 pb-1 text-caption text-fg-muted">
              {t(group.label)}
            </div>
            {group.items
              .filter((item) => !item.beta || beta)
              .map((item) => (
                <NavLink
                  key={item.path}
                  to={`${base}/${item.path}`}
                  // 空间设置页（/settings/spaces/:id）由「我的空间」分组高亮，这里只匹配列表本身
                  end={item.path === "spaces"}
                  className={({ isActive }) =>
                    cn(
                      "flex h-[var(--density-nav-row,30px)] items-center gap-2 rounded-lg px-2 text-body hover:bg-surface-hover",
                      isActive
                        ? "bg-surface-active font-medium"
                        : "text-fg-default",
                    )
                  }
                >
                  <Icon name={item.icon} className="text-fg-secondary" />
                  <span className="min-w-0 flex-1 truncate">
                    {t(item.label)}
                  </span>
                  {item.path === "get-started" && (
                    <GetStartedNavCount orgId={orgId} />
                  )}
                </NavLink>
              ))}
            {group.label === "settings.groups.mine" && bridge && (
              <>
                <NavLink
                  to={base + "/daemon"}
                  className={({ isActive }) =>
                    cn(
                      "flex h-[var(--density-nav-row,30px)] items-center gap-2 rounded-lg px-2 text-body hover:bg-surface-hover",
                      isActive && "bg-surface-active font-medium",
                    )
                  }
                >
                  <Icon name="monitor" />
                  {t("shell.localService")}
                </NavLink>
                {bridge.getProxy && (
                  <button
                    onClick={() => setProxyOpen(true)}
                    className="flex h-[var(--density-nav-row,30px)] items-center gap-2 rounded-lg px-2 text-body hover:bg-surface-hover"
                  >
                    <Icon name="globe" />
                    {t("shell.proxyTitle")}
                  </button>
                )}
              </>
            )}
          </div>
        </Fragment>
      ))}
      {proxyOpen && <ProxyDialog onClose={() => setProxyOpen(false)} />}
    </nav>
  );
}

/** 设置页路由外壳：导航由 OrgLayout 侧栏渲染（SettingsNavigation），这里只承载子页面。 */
export function SettingsLayout() {
  return <Outlet />;
}
