import { useNavigate } from "react-router-dom";

import { useT, type MessageKey } from "@openfox/i18n";
import {
  Icon,
  Menu,
  MenuItem,
  MenuLabel,
  cn,
  type IconName,
} from "@openfox/ui";

import { spaceSettingsPath } from "../spaces/SpacesNav";

// 侧栏空间子项末尾的「更多」：
// 「空间设置」分组下的三个入口，分别进入空间设置的标签、任务类型（含工作流状态）与常规设置。

const ITEMS: ReadonlyArray<{
  section: string;
  icon: IconName;
  label: MessageKey;
}> = [
  { section: "labels", icon: "tag", label: "shell.space.moreLabels" },
  {
    section: "task-types",
    icon: "circle-dot",
    label: "shell.space.moreTaskTypes",
  },
  {
    section: "general",
    icon: "hexagon-cog",
    label: "shell.space.moreSettings",
  },
];

export function SpaceMoreMenu({
  orgId,
  spaceId,
}: {
  orgId: string;
  spaceId: string;
}) {
  const t = useT();
  const navigate = useNavigate();
  return (
    <Menu
      label={t("shell.space.moreMenu")}
      placement="top-start"
      className="w-[220px]"
      trigger={(props) => (
        <button
          ref={props.ref}
          type="button"
          aria-haspopup="menu"
          aria-expanded={props.open}
          onClick={props.toggle}
          className={cn(
            "flex h-[30px] items-center gap-2 rounded-lg pr-1.5 pl-2 text-left text-body text-fg-default hover:bg-surface-hover",
            props.open && "bg-surface-active",
          )}
        >
          <Icon name="more" className="text-fg-secondary" />
          <span className="min-w-0 flex-1 truncate">
            {t("shell.space.more")}
          </span>
        </button>
      )}
    >
      <MenuLabel>{t("shell.space.moreGroup")}</MenuLabel>
      {ITEMS.map((item) => (
        <MenuItem
          key={item.section}
          icon={<Icon name={item.icon} />}
          onSelect={() =>
            navigate(spaceSettingsPath(orgId, spaceId, item.section))
          }
        >
          {t(item.label)}
        </MenuItem>
      ))}
    </Menu>
  );
}
