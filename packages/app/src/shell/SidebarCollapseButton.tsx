import { useT } from "@openfox/i18n";
import { Icon, IconButton } from "@openfox/ui";

export function SidebarCollapseButton({ onClick }: { onClick: () => void }) {
  const t = useT();
  return (
    <IconButton
      label={t("shell.collapse")}
      icon={<Icon name="chevrons-left" />}
      size="28"
      className="shrink-0 pointer-fine:opacity-0 pointer-fine:group-hover/panel:opacity-100 pointer-fine:focus-visible:opacity-100"
      onClick={onClick}
    />
  );
}
