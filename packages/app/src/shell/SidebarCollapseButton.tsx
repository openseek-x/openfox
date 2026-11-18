import { useT } from "@openfox/i18n";
import { Icon, IconButton } from "@openfox/ui";

export function SidebarCollapseButton({ onClick }: { onClick: () => void }) {
  const t = useT();
  return (
    <IconButton
      label={t("shell.collapse")}
      icon={<Icon name="chevrons-left" />}
      size="28"
      className="shrink-0 opacity-0 group-hover/panel:opacity-100 focus-visible:opacity-100"
      onClick={onClick}
    />
  );
}
