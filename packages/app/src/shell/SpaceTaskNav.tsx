import { useState, type ReactElement } from "react";
import { Link, NavLink, useLocation } from "react-router-dom";

import type { SpaceFeatures } from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import { Icon, IconButton, Menu, cn } from "@openfox/ui";

import { localKeys, readLocal, writeLocal } from "../storage";
import { TriageIcon } from "../triage/TriageState";
import { PinnedViews } from "../views/SpaceViewsNav";

// 侧栏空间里「任务」的下一级：迭代 / 分拣台 / 视图 / 专项。
// 只显示已固定的几项（默认迭代、视图），其余从「更多…」菜单进入，菜单里可以固定 / 取消固定。
// 固定状态按空间存在本机（localStorage），只影响本机显示。
// 菜单列出四个目的地；功能的启用状态由对应页面展示。

type TaskNavId = "iterations" | "triage" | "views" | "projects";

const ITEMS: ReadonlyArray<{
  id: TaskNavId;
  path: string;
  icon: ReactElement;
  label: MessageKey;
}> = [
  {
    id: "iterations",
    path: "iterations",
    icon: <Icon name="repeat" />,
    label: "shell.space.iterations",
  },
  {
    id: "triage",
    path: "triage",
    icon: <TriageIcon />,
    label: "shell.space.triage",
  },
  {
    id: "views",
    path: "views",
    icon: <Icon name="layers" />,
    label: "shell.space.views",
  },
  {
    id: "projects",
    path: "projects",
    icon: <Icon name="target" />,
    label: "shell.space.projects",
  },
];

const DEFAULT_PINNED: readonly TaskNavId[] = ["iterations", "views"];

function readPinned(spaceId: string): TaskNavId[] {
  try {
    const raw = readLocal(localKeys.taskNavPinned(spaceId));
    if (raw === null) return [...DEFAULT_PINNED];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [...DEFAULT_PINNED];
    return ITEMS.map((item) => item.id).filter((id) => parsed.includes(id));
  } catch {
    return [...DEFAULT_PINNED];
  }
}

/** 「任务」下的子项行：左侧引导线，只有文字。 */
function SubRow({
  to,
  label,
  end,
}: {
  to: string;
  label: string;
  end?: boolean;
}) {
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) =>
        cn(
          "flex h-[var(--density-subnav-row,30px)] items-center rounded-lg px-2 text-body hover:bg-surface-hover",
          isActive ? "bg-surface-active font-medium" : "text-fg-default",
        )
      }
    >
      <span className="min-w-0 flex-1 truncate">{label}</span>
    </NavLink>
  );
}

export function SpaceTaskNav({
  orgId,
  spaceId,
  features,
}: {
  orgId: string;
  spaceId: string;
  features: SpaceFeatures;
}) {
  const t = useT();
  const { pathname } = useLocation();
  const base = `/o/${orgId}/spaces/${spaceId}`;
  const [pinned, setPinned] = useState<TaskNavId[]>(() => readPinned(spaceId));

  const togglePin = (id: TaskNavId) => {
    const next = pinned.includes(id)
      ? pinned.filter((item) => item !== id)
      : ITEMS.map((item) => item.id).filter(
          (item) => item === id || pinned.includes(item),
        );
    setPinned(next);
    writeLocal(localKeys.taskNavPinned(spaceId), JSON.stringify(next));
  };

  // 当前打开的是未固定的子页时，临时显示这一项，让用户知道自己在哪
  const current = ITEMS.find(
    (item) =>
      pathname === `${base}/${item.path}` ||
      pathname.startsWith(`${base}/${item.path}/`),
  );
  const shown = ITEMS.filter(
    (item) => pinned.includes(item.id) || item.id === current?.id,
  );

  return (
    <div className="ml-[15px] flex flex-col gap-0.5 border-l border-line-default pl-2">
      {shown.map((item) => (
        <div key={item.id} className="flex flex-col gap-0.5">
          <SubRow
            to={`${base}/${item.path}`}
            label={t(item.label)}
            end={item.id === "views"}
          />
          {item.id === "views" && features.views && (
            <PinnedViews orgId={orgId} spaceId={spaceId} />
          )}
        </div>
      ))}
      <Menu
        label={t("shell.space.taskNavMenu")}
        placement="right-start"
        className="w-56"
        trigger={(props) => (
          <button
            ref={props.ref}
            type="button"
            aria-haspopup="menu"
            aria-expanded={props.open}
            onClick={props.toggle}
            className={cn(
              "flex h-[30px] items-center rounded-lg px-2 text-left text-body text-fg-default hover:bg-surface-hover",
              props.open && "bg-surface-active",
            )}
          >
            {t("shell.space.taskNavMore")}
          </button>
        )}
      >
        {ITEMS.map((item) => {
          const isPinned = pinned.includes(item.id);
          return (
            <div
              key={item.id}
              role="none"
              className="flex items-center gap-1 rounded-lg pr-1 hover:bg-surface-active"
            >
              <Link
                to={`${base}/${item.path}`}
                role="menuitem"
                className="flex h-[var(--density-nav-row,32px)] min-w-0 flex-1 items-center gap-2.5 rounded-lg pl-2.5 text-left text-body text-fg-default outline-none focus-visible:bg-surface-active focus-visible:shadow-none [&>svg]:shrink-0 [&>svg]:text-fg-muted"
              >
                {item.icon}
                <span className="truncate">{t(item.label)}</span>
              </Link>
              <IconButton
                size="28"
                aria-pressed={isPinned}
                label={t(isPinned ? "shell.space.unpin" : "shell.space.pin", {
                  name: t(item.label),
                })}
                icon={<Icon name={isPinned ? "pin-off" : "pin"} size={14} />}
                onClick={() => togglePin(item.id)}
              />
            </div>
          );
        })}
      </Menu>
    </div>
  );
}
