import { useRef, useState } from "react";

import type { Label, LabelGroup } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  SearchMenu,
  type SearchMenuOption,
} from "@openfox/ui";

const NO_GROUP = "__none__";

function labelDot(color: string | null) {
  return (
    <span
      aria-hidden
      className="size-2 rounded-full"
      style={{ backgroundColor: color ?? "var(--color-line-strong)" }}
    />
  );
}

/** 合并目标选项：未归档的组织级标签，排除来源标签。 */
export function mergeTargetOptions(
  labels: Label[],
  exclude: Set<string>,
): SearchMenuOption[] {
  return labels
    .filter(
      (label) =>
        !exclude.has(label.id) && !label.archived_at && !label.space_id,
    )
    .map((label) => ({
      value: label.id,
      label: label.name,
      icon: labelDot(label.color),
      keywords: label.description,
    }));
}

/** 标签行「···」菜单：编辑名称、查看任务、移入分组（搜索子菜单）、合并、归档、删除。 */
export function LabelRowMenu({
  label,
  canManage,
  groups,
  mergeCandidates,
  onRename,
  onViewTasks,
  onMoveToGroup,
  onMerge,
  onArchive,
  onUnarchive,
  onDelete,
}: {
  label: Label;
  canManage: boolean;
  groups: LabelGroup[];
  /** 未归档的组织级标签 */
  mergeCandidates: Label[];
  onRename: () => void;
  onViewTasks: () => void;
  onMoveToGroup: (groupId: string | null) => void;
  onMerge: (target: Label) => void;
  onArchive: () => void;
  onUnarchive: () => void;
  onDelete: () => void;
}) {
  const t = useT();
  const anchor = useRef<HTMLElement | null>(null);
  const [picker, setPicker] = useState<"group" | "merge" | null>(null);
  const archived = Boolean(label.archived_at);
  return (
    <>
      <Menu
        label={t("settings.labels.menu.label", { name: label.name })}
        placement="bottom-end"
        trigger={({ ref, open, toggle }) => (
          <IconButton
            ref={(node) => {
              ref(node);
              anchor.current = node;
            }}
            label={t("settings.labels.menu.label", { name: label.name })}
            icon={<Icon name="ellipsis" />}
            size="28"
            active={open}
            aria-haspopup="menu"
            aria-expanded={open}
            onClick={toggle}
          />
        )}
      >
        {canManage && (
          <MenuItem icon={<Icon name="pencil" />} onSelect={onRename}>
            {t("settings.labels.menu.rename")}
          </MenuItem>
        )}
        <MenuItem icon={<Icon name="list-checks" />} onSelect={onViewTasks}>
          {t("settings.labels.menu.viewTasks")}
        </MenuItem>
        {canManage && (
          <>
            <MenuItem
              icon={<Icon name="layout-grid" />}
              trailing={<Icon name="chevron-right" />}
              onSelect={() => setPicker("group")}
            >
              {t("settings.labels.menu.addToGroup")}
            </MenuItem>
            {!archived && (
              <MenuItem
                icon={<Icon name="merge" />}
                onSelect={() => setPicker("merge")}
              >
                {t("settings.labels.menu.merge")}
              </MenuItem>
            )}
            <MenuSeparator />
            {archived ? (
              <MenuItem
                icon={<Icon name="archive-restore" />}
                onSelect={onUnarchive}
              >
                {t("settings.labels.menu.unarchive")}
              </MenuItem>
            ) : (
              <MenuItem icon={<Icon name="archive" />} onSelect={onArchive}>
                {t("settings.labels.menu.archive")}
              </MenuItem>
            )}
            <MenuItem icon={<Icon name="trash-2" />} danger onSelect={onDelete}>
              {t("settings.labels.menu.delete")}
            </MenuItem>
          </>
        )}
      </Menu>
      {canManage && (
        <>
          <SearchMenu
            open={picker === "group"}
            onOpenChange={(open) => setPicker(open ? "group" : null)}
            anchorRef={anchor}
            placement="bottom-end"
            label={t("settings.labels.groupPicker.label")}
            placeholder={t("settings.labels.groupPicker.placeholder")}
            emptyText={t("settings.labels.groupPicker.empty")}
            selected={[label.group_id ?? NO_GROUP]}
            groups={[
              {
                options: [
                  {
                    value: NO_GROUP,
                    label: t("settings.labels.groupPicker.none"),
                    icon: <Icon name="circle-slash" />,
                  },
                  ...groups.map((group) => ({
                    value: group.id,
                    label: group.name,
                    icon: (
                      <Icon name="layout-grid" style={{ color: group.color }} />
                    ),
                  })),
                ],
              },
            ]}
            onSelect={(value) => {
              const next = value === NO_GROUP ? null : value;
              if (next !== label.group_id) onMoveToGroup(next);
            }}
          />
          <SearchMenu
            open={picker === "merge"}
            onOpenChange={(open) => setPicker(open ? "merge" : null)}
            anchorRef={anchor}
            placement="bottom-end"
            label={t("settings.labels.mergePicker.label", {
              name: label.name,
            })}
            placeholder={t("settings.labels.mergePicker.placeholder")}
            emptyText={t("settings.labels.mergePicker.empty")}
            selected={[]}
            groups={[
              {
                options: mergeTargetOptions(
                  mergeCandidates,
                  new Set([label.id]),
                ),
              },
            ]}
            onSelect={(value) => {
              const target = mergeCandidates.find((item) => item.id === value);
              if (target) onMerge(target);
            }}
          />
        </>
      )}
    </>
  );
}

/** 分组行「···」菜单：添加标签到分组、编辑分组名称、删除分组。 */
export function GroupRowMenu({
  group,
  onAddLabel,
  onRename,
  onDelete,
}: {
  group: LabelGroup;
  onAddLabel: () => void;
  onRename: () => void;
  onDelete: () => void;
}) {
  const t = useT();
  return (
    <Menu
      label={t("settings.labels.menu.groupLabel", { name: group.name })}
      placement="bottom-end"
      trigger={({ ref, open, toggle }) => (
        <IconButton
          ref={ref}
          label={t("settings.labels.menu.groupLabel", { name: group.name })}
          icon={<Icon name="ellipsis" />}
          size="28"
          active={open}
          aria-haspopup="menu"
          aria-expanded={open}
          onClick={toggle}
        />
      )}
    >
      <MenuItem icon={<Icon name="plus" />} onSelect={onAddLabel}>
        {t("settings.labels.menu.groupAddLabel")}
      </MenuItem>
      <MenuItem icon={<Icon name="pencil" />} onSelect={onRename}>
        {t("settings.labels.menu.groupRename")}
      </MenuItem>
      <MenuSeparator />
      <MenuItem icon={<Icon name="trash-2" />} danger onSelect={onDelete}>
        {t("settings.labels.menu.groupDelete")}
      </MenuItem>
    </Menu>
  );
}

/** 批量操作的「合并到...」按钮 + 搜索面板。 */
export function BulkMergeButton({
  sources,
  candidates,
  onMerge,
  children,
}: {
  sources: Label[];
  candidates: Label[];
  onMerge: (target: Label) => void;
  children: (props: {
    ref: (node: HTMLElement | null) => void;
    toggle: () => void;
    open: boolean;
  }) => React.ReactNode;
}) {
  const t = useT();
  const anchor = useRef<HTMLElement | null>(null);
  const [open, setOpen] = useState(false);
  return (
    <>
      {children({
        ref: (node) => {
          anchor.current = node;
        },
        toggle: () => setOpen((v) => !v),
        open,
      })}
      <SearchMenu
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        placement="top-start"
        label={t("settings.labels.mergePicker.bulkLabel", {
          count: sources.length,
        })}
        placeholder={t("settings.labels.mergePicker.placeholder")}
        emptyText={t("settings.labels.mergePicker.empty")}
        selected={[]}
        groups={[
          {
            options: mergeTargetOptions(
              candidates,
              new Set(sources.map((label) => label.id)),
            ),
          },
        ]}
        onSelect={(value) => {
          const target = candidates.find((item) => item.id === value);
          if (target) onMerge(target);
        }}
      />
    </>
  );
}
