import { useState, type ReactNode } from "react";

import type { DocNode, DocSet } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuSeparator,
  cn,
  type IconName,
} from "@openfox/ui";

import { useDocActions } from "./DocActions";
import type { DocLocation } from "./model";

// 文档库的菜单：快速添加（含上传和导入）、文档行「···」、文档集「···」。

type MenuPlacement = "bottom-start" | "bottom-end" | "right-start";

/** 快速添加：新建文档 / 表格 / 文件夹，以及上传和导入。 */
export function QuickAddMenu({
  location,
  label,
  placement = "bottom-end",
  trigger,
}: {
  location: DocLocation;
  label: string;
  placement?: MenuPlacement;
  trigger: (props: {
    ref: (node: HTMLElement | null) => void;
    open: boolean;
    toggle: () => void;
  }) => ReactNode;
}) {
  const t = useT();
  const actions = useDocActions();
  const [importOpen, setImportOpen] = useState(false);
  return (
    <Menu
      trigger={trigger}
      placement={placement}
      label={label}
      className="w-56"
    >
      <MenuItem
        icon={<Icon name="file-text" />}
        onSelect={() => actions.createNode(location, "doc")}
      >
        {t("docs.create.newDoc")}
      </MenuItem>
      <MenuItem
        icon={<Icon name="file-spreadsheet" />}
        onSelect={() => actions.createNode(location, "sheet")}
      >
        {t("docs.create.newSheet")}
      </MenuItem>
      <MenuItem
        icon={<Icon name="folder" />}
        onSelect={() => actions.createNode(location, "folder")}
      >
        {t("docs.create.newFolder")}
      </MenuItem>
      <MenuSeparator />
      {/* 子菜单在本菜单内展开，避免嵌套浮层与外部点击关闭冲突 */}
      <button
        type="button"
        aria-expanded={importOpen}
        disabled={actions.uploading}
        onClick={() => setImportOpen((value) => !value)}
        className="flex min-h-[34px] w-full items-center gap-2.5 rounded-lg py-1.5 pr-2 pl-2.5 text-left text-body text-fg-default outline-none hover:bg-surface-active focus-visible:bg-surface-active disabled:cursor-not-allowed disabled:opacity-50"
      >
        <Icon name="upload" className="text-fg-secondary" />
        <span className="flex-1">{t("docs.create.uploadImport")}</span>
        <Icon
          name={importOpen ? "chevron-down" : "chevron-right"}
          size={12}
          className="text-fg-muted"
        />
      </button>
      {importOpen && (
        <>
          <UploadItem
            icon="openfox-file-input"
            label={t("docs.create.uploadFile")}
            uploading={actions.uploading}
            onSelect={() => actions.requestUpload(location, "file")}
          />
          <UploadItem
            icon="folder-up"
            label={t("docs.create.uploadFolder")}
            uploading={actions.uploading}
            onSelect={() => actions.requestUpload(location, "folder")}
          />
        </>
      )}
    </Menu>
  );
}

/** 上传项：上传中整项禁用，文字变为「正在上传」。 */
function UploadItem({
  icon,
  label,
  uploading,
  onSelect,
}: {
  icon: IconName;
  label: string;
  uploading: boolean;
  onSelect: () => void;
}) {
  const t = useT();
  return (
    <MenuItem
      icon={<Icon name={icon} />}
      disabled={uploading}
      onSelect={onSelect}
    >
      <span className={cn(uploading && "text-fg-muted")}>
        {uploading ? t("docs.create.uploading") : label}
      </span>
    </MenuItem>
  );
}

/** 文档行「···」：添加到任务 / 重命名 / 复制链接 / 移动到 / 删除。 */
export function DocRowMenu({ node }: { node: DocNode }) {
  const t = useT();
  const actions = useDocActions();
  return (
    <Menu
      placement="bottom-start"
      label={t("docs.row.more")}
      className="w-44"
      trigger={(props) => (
        <IconButton
          ref={props.ref}
          label={t("docs.row.more")}
          icon={<Icon name="ellipsis" />}
          size="28"
          variant="ghost"
          active={props.open}
          onClick={props.toggle}
        />
      )}
    >
      <MenuItem
        icon={<Icon name="square-check" />}
        onSelect={() => actions.addToTask(node)}
      >
        {t("docs.row.addToTask")}
      </MenuItem>
      {node.can_edit && (
        <MenuItem
          icon={<Icon name="pencil" />}
          onSelect={() => actions.rename(node)}
        >
          {t("docs.row.rename")}
        </MenuItem>
      )}
      <MenuItem
        icon={<Icon name="link" />}
        onSelect={() => actions.copyLink(node)}
      >
        {t("docs.row.copyLink")}
      </MenuItem>
      {node.can_edit && (
        <MenuItem
          icon={<Icon name="folder-input" />}
          onSelect={() => actions.move(node)}
        >
          {t("docs.row.move")}
        </MenuItem>
      )}
      {node.can_edit && (
        <>
          <MenuSeparator />
          <MenuItem
            danger
            icon={<Icon name="trash-2" />}
            onSelect={() => actions.remove(node)}
          >
            {t("docs.row.delete")}
          </MenuItem>
        </>
      )}
    </Menu>
  );
}

/** 文档集「···」：只有编辑 / 删除，只对有写权限的成员显示。 */
export function DocSetMenu({ docSet }: { docSet: DocSet }) {
  const t = useT();
  const actions = useDocActions();
  return (
    <Menu
      placement="bottom-start"
      label={t("docs.row.more")}
      className="w-44"
      trigger={(props) => (
        <IconButton
          ref={props.ref}
          label={t("docs.row.more")}
          icon={<Icon name="ellipsis" />}
          size="28"
          variant="ghost"
          active={props.open}
          onClick={props.toggle}
        />
      )}
    >
      <MenuItem
        icon={<Icon name="pencil" />}
        onSelect={() => actions.editSet(docSet)}
      >
        {t("docs.set.edit")}
      </MenuItem>
      <MenuSeparator />
      <MenuItem
        danger
        icon={<Icon name="trash-2" />}
        onSelect={() => actions.deleteSet(docSet)}
      >
        {t("docs.set.delete")}
      </MenuItem>
    </Menu>
  );
}
