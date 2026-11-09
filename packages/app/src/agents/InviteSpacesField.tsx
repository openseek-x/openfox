import { useT } from "@openfox/i18n";
import { Badge, Icon, cn } from "@openfox/ui";

import { useSpaceList } from "../spaces/queries";
import { SpaceIcon } from "../spaces/SpaceIcon";
import { Picker } from "../tasks/pickers";

/**
 * 邀请弹窗的「加入空间」多选：只列当前用户能读到的活跃空间，按名称排序；
 * 已选在触发器里显示为空间图标 + 标识符小胶囊。
 */
export function InviteSpacesField({
  orgId,
  value,
  onChange,
  disabled = false,
}: {
  orgId: string;
  value: string[];
  onChange: (ids: string[]) => void;
  disabled?: boolean;
}) {
  const t = useT();
  const spaces = (useSpaceList(orgId).data ?? [])
    .filter((space) => space.status === "active")
    .sort((a, b) => a.name.localeCompare(b.name));
  const selected = spaces.filter((space) => value.includes(space.id));
  return (
    <Picker
      label={t("agents.invite.spaces")}
      placeholder={t("organization.invite.searchSpaces")}
      emptyText={t("organization.invite.noSpaces")}
      multiple
      selected={value}
      groups={[
        {
          options: spaces.map((space) => ({
            value: space.id,
            label: space.name,
            keywords: space.prefix,
            icon: <SpaceIcon icon={space.icon} size={18} />,
            description: (
              <span className="flex items-center gap-1.5">
                <Badge variant="outline">
                  {space.visibility === "private"
                    ? t("spaces.access.private")
                    : t("spaces.access.public")}
                </Badge>
                <span className="font-mono">{space.prefix}</span>
              </span>
            ),
          })),
        },
      ]}
      onSelect={(id) =>
        onChange(
          value.includes(id) ? value.filter((v) => v !== id) : [...value, id],
        )
      }
      trigger={({ ref, open, toggle }) => (
        <button
          ref={ref}
          type="button"
          disabled={disabled}
          aria-label={t("agents.invite.spaces")}
          aria-haspopup="listbox"
          aria-expanded={open}
          onClick={toggle}
          className={cn(
            "flex min-h-8 w-full items-center gap-1.5 rounded-md border border-line-default px-2.5 py-1 text-left text-body",
            "disabled:cursor-not-allowed disabled:opacity-50",
          )}
        >
          <span className="flex min-w-0 flex-1 flex-wrap gap-1">
            {selected.length === 0 ? (
              <span className="text-fg-subtle">
                {t("organization.invite.spacesPlaceholder")}
              </span>
            ) : (
              selected.map((space) => (
                <span
                  key={space.id}
                  className="flex h-5 items-center gap-1 rounded bg-surface-muted px-1.5 font-mono text-caption"
                >
                  <SpaceIcon icon={space.icon} size={14} />
                  {space.prefix}
                </span>
              ))
            )}
          </span>
          <Icon name="chevron-down" className="shrink-0 text-fg-muted" />
        </button>
      )}
    />
  );
}

/** 默认勾选第一个公开空间。 */
export function useDefaultInviteSpace(orgId: string): string | null {
  const spaces = useSpaceList(orgId).data ?? [];
  return (
    spaces
      .filter(
        (space) => space.status === "active" && space.visibility === "shared",
      )
      .sort((a, b) => a.name.localeCompare(b.name))[0]?.id ?? null
  );
}
