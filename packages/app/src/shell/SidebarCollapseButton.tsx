import { useT } from "@openfox/i18n";
import { Icon, IconButton } from "@openfox/ui";

import { isMacPlatform } from "../me/shortcuts";

export function SidebarCollapseButton({ onClick }: { onClick: () => void }) {
  const t = useT();
  const shortcut = isMacPlatform() ? "⌘\\" : "Ctrl+\\";
  return (
    <IconButton
      label={t("shell.collapse")}
      title={`${t("shell.collapse")} (${shortcut})`}
      icon={<Icon name="chevrons-left" />}
      size="28"
      className="shrink-0 text-fg-muted transition-[opacity,color,background-color] duration-150 hover:text-fg-default pointer-fine:opacity-60 pointer-fine:group-hover/panel:opacity-100 pointer-fine:focus-visible:opacity-100 motion-reduce:transition-none"
      onClick={onClick}
    />
  );
}
