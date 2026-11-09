import { useState } from "react";
import { useNavigate } from "react-router-dom";

import {
  type DocContainer,
  type DocNode,
  type DocSet,
  type Space,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Icon, cn, useToast } from "@openfox/ui";

import { joinedSpaces, isRetired, useSpaceList } from "../spaces/queries";
import {
  docsPath,
  isMoveTargetBlocked,
  PERSONAL_CONTAINER,
  setContainer,
} from "./model";
import {
  docErrorMessage,
  useDocChildren,
  useDocSets,
  useMoveDocNode,
} from "./queries";

// 移动到：按空间分组，当前空间排第一；可选「我的文档」与可写的文档集，
// 下面是逐层按需加载的文件夹。移动对象自身及其子孙禁用并标「不能移入自身」。

interface Target {
  spaceId: string;
  spaceName: string;
  container: DocContainer;
  parentId: string | null;
  /** 文件夹路径（不含空间名），例如「我的文档 / 需求 / 2026」 */
  label: string;
}

const FOLDER_INDENT = 28;
const DEPTH_INDENT = 14;

export function MoveDocDialog({
  orgId,
  spaceId,
  node,
  onClose,
}: {
  orgId: string;
  spaceId: string;
  node: DocNode;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const spaces = useSpaceList(orgId);
  const move = useMoveDocNode(orgId, spaceId);
  const [target, setTarget] = useState<Target | null>(null);

  // 当前空间排第一，其余只列已加入且未停用的空间
  const current = (spaces.data ?? []).find((space) => space.id === spaceId);
  const others = joinedSpaces(spaces.data).filter(
    (space) => space.id !== spaceId && !isRetired(space),
  );
  const ordered = [...(current ? [current] : []), ...others];

  const submit = async () => {
    if (!target || move.isPending) return;
    try {
      await move.mutateAsync({
        node,
        target: {
          target_space_id: target.spaceId,
          container: target.container,
          parent_id: target.parentId,
          before_id: null,
        },
      });
      onClose();
      if (target.spaceId !== spaceId)
        toast(t("docs.move.moved", { target: target.spaceName }), {
          action: {
            label: t("docs.move.view"),
            onClick: () => navigate(docsPath(orgId, target.spaceId, node.id)),
          },
          duration: 5000,
        });
    } catch (error) {
      toast(docErrorMessage(t, error, "docs.move.failed"), { tone: "error" });
    }
  };

  return (
    <Dialog
      open
      onClose={move.isPending ? undefined : onClose}
      title={t("docs.move.title")}
      description={t("docs.move.desc", {
        name: node.title || t("docs.untitled"),
      })}
      width={440}
      footer={
        <>
          <span className="mr-auto min-w-0 truncate text-body-sm text-fg-muted">
            {target
              ? t("docs.move.target", {
                  target: `${target.spaceName} · ${target.label}`,
                })
              : t("docs.move.selectHint")}
          </span>
          <Button variant="outline" onClick={onClose} disabled={move.isPending}>
            {t("docs.move.cancel")}
          </Button>
          <Button
            variant="primary"
            onClick={() => void submit()}
            disabled={!target || move.isPending}
            loading={move.isPending}
          >
            {t("docs.move.submit")}
          </Button>
        </>
      }
    >
      <div className="flex max-h-[360px] flex-col gap-3 overflow-y-auto">
        {ordered.map((space) => (
          <SpaceGroup
            key={space.id}
            orgId={orgId}
            space={space}
            isCurrent={space.id === spaceId}
            movingNode={node}
            movingSpaceId={spaceId}
            selected={target}
            onSelect={setTarget}
          />
        ))}
      </div>
    </Dialog>
  );
}

function SpaceGroup({
  orgId,
  space,
  isCurrent,
  movingNode,
  movingSpaceId,
  selected,
  onSelect,
}: {
  orgId: string;
  space: Space;
  isCurrent: boolean;
  movingNode: DocNode;
  movingSpaceId: string;
  selected: Target | null;
  onSelect: (target: Target) => void;
}) {
  const t = useT();
  const sets = useDocSets(orgId, space.id);
  // 只有源空间里的移动会检查「移入自身」；跨空间移动的目标里不可能有被移动对象
  const movingId = space.id === movingSpaceId ? movingNode.id : "";
  const common = {
    orgId,
    spaceId: space.id,
    spaceName: space.name,
    movingId,
    selected,
    onSelect,
  };
  return (
    <section className="flex flex-col gap-1">
      <div className="flex h-7 items-center gap-2 px-1 text-caption text-fg-muted">
        <span className="truncate">{space.name}</span>
        {isCurrent && (
          <span className="shrink-0 rounded bg-surface-muted px-1.5 text-fg-secondary">
            {t("docs.move.currentSpace")}
          </span>
        )}
      </div>
      <ContainerOption
        {...common}
        container={PERSONAL_CONTAINER}
        label={t("docs.move.personal")}
        writable
      />
      {(sets.data ?? []).map((set: DocSet) => (
        <ContainerOption
          key={set.id}
          {...common}
          container={setContainer(set.id)}
          label={set.name}
          writable={set.can_edit}
        />
      ))}
    </section>
  );
}

interface OptionContext {
  orgId: string;
  spaceId: string;
  spaceName: string;
  movingId: string;
  selected: Target | null;
  onSelect: (target: Target) => void;
}

function ContainerOption({
  container,
  label,
  writable,
  ...context
}: OptionContext & {
  container: DocContainer;
  label: string;
  writable: boolean;
}) {
  const [open, setOpen] = useState(false);
  const isSelected =
    context.selected?.spaceId === context.spaceId &&
    context.selected.container.kind === container.kind &&
    context.selected.container.set_id === container.set_id &&
    context.selected.parentId === null;
  return (
    <>
      <OptionRow
        depth={0}
        label={label}
        icon="library-big"
        expandable
        open={open}
        disabled={!writable}
        selected={isSelected}
        onToggle={() => setOpen((value) => !value)}
        onSelect={() =>
          context.onSelect({
            spaceId: context.spaceId,
            spaceName: context.spaceName,
            container,
            parentId: null,
            label,
          })
        }
      />
      {open && (
        <FolderList
          {...context}
          container={container}
          parentId={null}
          depth={1}
          path={[]}
          pathNames={[label]}
          writable={writable}
        />
      )}
    </>
  );
}

function FolderList({
  orgId,
  spaceId,
  container,
  parentId,
  depth,
  path,
  pathNames,
  writable,
  ...context
}: OptionContext & {
  container: DocContainer;
  parentId: string | null;
  depth: number;
  /** 祖先文件夹 id（不含当前层的父级之外的内容），用于判断「不能移入自身」 */
  path: readonly string[];
  pathNames: readonly string[];
  writable: boolean;
}) {
  const nodes = useDocChildren(orgId, spaceId, container, parentId);
  const folders = (nodes.data ?? []).filter((node) => node.kind === "folder");
  return folders.map((folder) => (
    <FolderItem
      key={folder.id}
      orgId={orgId}
      spaceId={spaceId}
      container={container}
      folder={folder}
      depth={depth}
      path={path}
      pathNames={pathNames}
      writable={writable}
      {...context}
    />
  ));
}

function FolderItem({
  folder,
  depth,
  path,
  pathNames,
  writable,
  orgId,
  spaceId,
  container,
  ...context
}: OptionContext & {
  orgId: string;
  container: DocContainer;
  folder: DocNode;
  depth: number;
  path: readonly string[];
  pathNames: readonly string[];
  writable: boolean;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const blocked = isMoveTargetBlocked(context.movingId, folder.id, path);
  const childPath = [...path, folder.id];
  const childNames = [...pathNames, folder.title || t("docs.untitled")];
  const isSelected =
    context.selected?.spaceId === spaceId &&
    context.selected.parentId === folder.id;
  return (
    <>
      <OptionRow
        depth={depth}
        label={folder.title}
        icon="folder"
        expandable={folder.has_children}
        open={open}
        disabled={!writable || blocked}
        blockedReason={blocked ? "docs.move.blocked" : undefined}
        selected={isSelected}
        onToggle={() => setOpen((value) => !value)}
        onSelect={() =>
          context.onSelect({
            spaceId,
            spaceName: context.spaceName,
            container,
            parentId: folder.id,
            label: childNames.join(" / "),
          })
        }
      />
      {open && folder.has_children && (
        <FolderList
          {...context}
          orgId={orgId}
          spaceId={spaceId}
          container={container}
          parentId={folder.id}
          depth={depth + 1}
          path={childPath}
          pathNames={childNames}
          writable={writable && !blocked}
        />
      )}
    </>
  );
}

function OptionRow({
  depth,
  label,
  icon,
  expandable,
  open,
  disabled,
  blockedReason,
  selected,
  onToggle,
  onSelect,
}: {
  depth: number;
  label: string;
  icon: "library-big" | "folder";
  expandable: boolean;
  open: boolean;
  disabled: boolean;
  blockedReason?: "docs.move.blocked";
  selected: boolean;
  onToggle: () => void;
  onSelect: () => void;
}) {
  const t = useT();
  // 文件夹缩进：28 + 14 × 深度（文档集与「我的文档」本身为 0 层）
  const indent = depth === 0 ? 8 : FOLDER_INDENT + DEPTH_INDENT * (depth - 1);
  return (
    <div
      className={cn(
        "group flex min-h-[30px] items-center gap-1 rounded-lg pr-2",
        selected && "bg-surface-active",
        disabled && "opacity-50",
      )}
      style={{ paddingLeft: indent }}
    >
      <button
        type="button"
        aria-label={
          open ? t("docs.nav.collapseNode") : t("docs.nav.expandNode")
        }
        disabled={!expandable}
        onClick={onToggle}
        className={cn(
          "flex size-5 shrink-0 items-center justify-center rounded text-fg-muted",
          !expandable && "invisible",
        )}
      >
        <Icon name={open ? "chevron-down" : "chevron-right"} size={12} />
      </button>
      <button
        type="button"
        aria-pressed={selected}
        disabled={disabled}
        title={blockedReason ? t(blockedReason) : undefined}
        onClick={onSelect}
        className={cn(
          "flex min-w-0 flex-1 items-center gap-2 py-1 text-left text-body",
          disabled && "cursor-not-allowed",
        )}
      >
        <Icon name={icon} size={16} className="shrink-0 text-fg-secondary" />
        <span className="truncate">{label}</span>
        {blockedReason && (
          <span className="shrink-0 text-caption text-fg-muted">
            {t(blockedReason)}
          </span>
        )}
      </button>
    </div>
  );
}
