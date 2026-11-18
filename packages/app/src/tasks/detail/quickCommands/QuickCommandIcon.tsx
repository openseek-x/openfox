import type { QuickCommand } from "@openfox/api";
import { Icon } from "@openfox/ui";

import { ViewIcon } from "../../../views/ViewIcon";

/** 快捷指令图标：复用视图图标（内置图标 / 表情 / 上传图片）；没有图标时显示闪电。 */
export function QuickCommandIcon({
  command,
  size = 16,
}: {
  command: Pick<QuickCommand, "icon_kind" | "icon" | "icon_url" | "name">;
  size?: number;
}) {
  if (!command.icon_kind)
    return <Icon name="zap" size={size} className="text-fg-muted" />;
  return <ViewIcon view={command} size={size} />;
}
