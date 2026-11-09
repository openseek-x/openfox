import {
  DndContext,
  PointerSensor,
  useDraggable,
  useDroppable,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragMoveEvent,
} from "@dnd-kit/core";
import { useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  useContext,
  useState,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from "react";
import { useNavigate } from "react-router-dom";

import type { DocContainer, DocNode, DocSet } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, IconButton, cn, useToast } from "@openfox/ui";

import { keys } from "../context";
import { useDocActions } from "./DocActions";
import { DocRowMenu, DocSetMenu, QuickAddMenu } from "./DocMenus";
import {
  beforeIdFor,
  clampNavWidth,
  DOC_NAV_WIDTH,
  docsPath,
  dropZone,
  PERSONAL_CONTAINER,
  setContainer,
  type DropZone,
} from "./model";
import { DocSetIcon, NodeIcon } from "./parts";
import {
  docErrorMessage,
  useDocChildren,
  useDocSets,
  useMoveDocNode,
} from "./queries";

// 文档库二级导航：搜索行、「我的文档」与「团队文档」两组，
// 树节点按需加载子节点，可以拖拽移动（上下 30% 为前后插入线，中间为放入）。

/** 拖拽的数据：被拖拽的节点 */
interface DragPayload {
  node: DocNode;
}

/** 放置目标：一个节点，或一个容器（我的文档 / 文档集的根） */
type DropData =
  | {
      kind: "node";
      node: DocNode;
      /** 目标的祖先 id（不含目标自身），用于禁止把节点移入自己的子孙 */
      path: readonly string[];
    }
  | { kind: "container"; container: DocContainer; writable: boolean };

interface DropIndicator {
  id: string;
  zone: DropZone;
}

const DropIndicatorContext = createContext<DropIndicator | null>(null);

/** 节点在树里的缩进：4 + 16 × 深度 */
const indentFor = (depth: number) => 4 + 16 * depth;

const setIdOf = (container: DocContainer) =>
  container.kind === "set" ? (container.set_id ?? "") : "";

/** 能否放到目标上：目标要可写，且不能是被拖拽节点自身或它的子孙。 */
function canDrop(dragged: DocNode, data: DropData, zone: DropZone): boolean {
  if (data.kind === "container") return data.writable && zone === "inside";
  if (!data.node.can_edit) return false;
  if (data.node.id === dragged.id) return false;
  return !data.path.includes(dragged.id);
}

/** 指针在目标内的相对位置：拖拽激活时的指针位置加上累计位移。 */
function zoneFor(
  event: DragMoveEvent | DragEndEvent,
  data: DropData,
): DropZone | null {
  const over = event.over;
  if (!over) return null;
  if (data.kind === "container") return "inside";
  const pointer = event.activatorEvent as PointerEvent;
  const clientY = "clientY" in pointer ? pointer.clientY : 0;
  return dropZone(
    clientY + event.delta.y - over.rect.top,
    over.rect.height,
    data.node.kind === "folder",
  );
}

export function DocsNav({
  orgId,
  spaceId,
  selectedId,
  width,
  onWidthChange,
}: {
  orgId: string;
  spaceId: string;
  /** 当前打开的节点，用于选中态 */
  selectedId?: string;
  width: number;
  onWidthChange: (width: number) => void;
}) {
  const t = useT();
  const actions = useDocActions();
  const queryClient = useQueryClient();
  const sets = useDocSets(orgId, spaceId);
  const toast = useToast();
  const move = useMoveDocNode(orgId, spaceId);
  const [indicator, setIndicator] = useState<DropIndicator | null>(null);
  // 只用指针拖拽：键盘传感器会占用树里的回车键
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
  );

  const onDragMove = (event: DragMoveEvent) => {
    const dragged = (event.active.data.current as DragPayload | undefined)
      ?.node;
    const data = event.over?.data.current as DropData | undefined;
    if (!dragged || !data || !event.over) return setIndicator(null);
    const zone = zoneFor(event, data);
    setIndicator(
      zone && canDrop(dragged, data, zone)
        ? { id: String(event.over.id), zone }
        : null,
    );
  };

  const onDragEnd = (event: DragEndEvent) => {
    setIndicator(null);
    const dragged = (event.active.data.current as DragPayload | undefined)
      ?.node;
    const data = event.over?.data.current as DropData | undefined;
    if (!dragged || !data) return;
    const zone = zoneFor(event, data);
    if (!zone || !canDrop(dragged, data, zone)) return;
    const target = dropTarget(queryClient, orgId, spaceId, dragged, data, zone);
    if (!target) return;
    move.mutate(
      { node: dragged, target: { ...target, target_space_id: spaceId } },
      {
        onError: (error) =>
          toast(docErrorMessage(t, error, "docs.move.failed"), {
            tone: "error",
          }),
      },
    );
  };

  return (
    <aside
      aria-label={t("docs.title")}
      style={{ width }}
      className="relative flex h-full shrink-0 flex-col border-r border-line-default bg-surface-sidebar"
    >
      <button
        type="button"
        onClick={actions.search}
        className="mx-2 mt-2 flex h-11 shrink-0 items-center gap-2 rounded-lg px-2 text-left text-body text-fg-secondary hover:bg-surface-hover"
      >
        <Icon name="search" className="text-fg-muted" />
        <span>{t("docs.nav.search")}</span>
      </button>
      <DndContext
        sensors={sensors}
        onDragMove={onDragMove}
        onDragEnd={onDragEnd}
        onDragCancel={() => setIndicator(null)}
      >
        <DropIndicatorContext.Provider value={indicator}>
          <div className="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto px-2 pt-2 pb-3">
            <GroupHeader
              label={t("docs.nav.groupPersonal")}
              action={
                <QuickAddMenu
                  location={{ container: PERSONAL_CONTAINER, parentId: null }}
                  label={t("docs.nav.groupPersonal")}
                  placement="bottom-start"
                  trigger={(props) => (
                    <IconButton
                      ref={props.ref}
                      label={t("docs.nav.newInGroup")}
                      icon={<Icon name="plus" />}
                      size="28"
                      variant="ghost"
                      active={props.open}
                      onClick={props.toggle}
                    />
                  )}
                />
              }
              drop={{
                kind: "container",
                container: PERSONAL_CONTAINER,
                writable: true,
              }}
            />
            <DocChildren
              orgId={orgId}
              spaceId={spaceId}
              container={PERSONAL_CONTAINER}
              parentId={null}
              depth={0}
              path={[]}
              selectedId={selectedId}
              empty={
                <button
                  type="button"
                  onClick={() =>
                    actions.createNode(
                      { container: PERSONAL_CONTAINER, parentId: null },
                      "doc",
                    )
                  }
                  className="flex h-8 items-center gap-2 rounded-lg px-2 text-left text-body-sm text-fg-secondary hover:bg-surface-hover"
                >
                  <Icon name="plus" size={14} />
                  {t("docs.nav.createFirstDoc")}
                </button>
              }
            />
            <GroupHeader
              label={t("docs.nav.groupTeam")}
              action={
                <IconButton
                  label={t("docs.nav.newSet")}
                  icon={<Icon name="plus" />}
                  size="28"
                  variant="ghost"
                  onClick={actions.newSet}
                />
              }
            />
            {sets.data?.length === 0 && (
              <p className="px-2 py-1 text-body-sm text-fg-muted">
                {t("docs.nav.noSets")}
              </p>
            )}
            {(sets.data ?? []).map((docSet) => (
              <SetRow
                key={docSet.id}
                orgId={orgId}
                spaceId={spaceId}
                docSet={docSet}
                selectedId={selectedId}
              />
            ))}
          </div>
        </DropIndicatorContext.Provider>
      </DndContext>
      <WidthHandle width={width} onWidthChange={onWidthChange} />
    </aside>
  );
}

/** 把拖放结果换算成移动请求的目标位置；同级前后插入需要兄弟节点的顺序。 */
function dropTarget(
  queryClient: ReturnType<typeof useQueryClient>,
  orgId: string,
  spaceId: string,
  dragged: DocNode,
  data: DropData,
  zone: DropZone,
) {
  if (data.kind === "container")
    return { container: data.container, parent_id: null, before_id: null };
  const target = data.node;
  if (zone === "inside")
    return {
      container: target.container,
      parent_id: target.id,
      before_id: null,
    };
  const siblings =
    queryClient.getQueryData<DocNode[]>(
      keys.docNodes(
        orgId,
        spaceId,
        target.container.kind,
        setIdOf(target.container),
        target.parent_id ?? "",
      ),
    ) ?? [];
  return {
    container: target.container,
    parent_id: target.parent_id,
    before_id: beforeIdFor(siblings, target.id, zone, dragged.id),
  };
}

/** 分组标题：「我的文档」「团队文档」，右侧是「+」。可作为放置目标（放到容器根）。 */
function GroupHeader({
  label,
  action,
  drop,
}: {
  label: string;
  action: ReactNode;
  drop?: Extract<DropData, { kind: "container" }>;
}) {
  const droppable = useDroppable({
    id: `drop:group:${label}`,
    data: drop,
    disabled: !drop,
  });
  const indicator = useContext(DropIndicatorContext);
  const hot = indicator?.id === `drop:group:${label}`;
  return (
    <div
      ref={droppable.setNodeRef}
      className={cn(
        "group flex h-8 shrink-0 items-center rounded-lg pr-1 pl-2 text-caption text-fg-muted",
        hot && "bg-surface-active",
      )}
    >
      <span className="flex-1 truncate font-medium">{label}</span>
      <span className="opacity-0 group-hover:opacity-100 focus-within:opacity-100">
        {action}
      </span>
    </div>
  );
}

/** 一个文档集：点击只展开，不跳页。 */
function SetRow({
  orgId,
  spaceId,
  docSet,
  selectedId,
}: {
  orgId: string;
  spaceId: string;
  docSet: DocSet;
  selectedId?: string;
}) {
  const t = useT();
  const actions = useDocActions();
  const key = `set:${docSet.id}`;
  const open = actions.expanded.has(key);
  const container = setContainer(docSet.id);
  const droppable = useDroppable({
    id: `drop:container:${docSet.id}`,
    data: {
      kind: "container",
      container,
      writable: docSet.can_edit,
    } satisfies DropData,
  });
  const indicator = useContext(DropIndicatorContext);
  const hot = indicator?.id === `drop:container:${docSet.id}`;
  return (
    <div className="flex flex-col gap-0.5">
      <div
        ref={droppable.setNodeRef}
        style={{ paddingLeft: indentFor(0) }}
        className={cn(
          "group relative flex h-8 items-center gap-1 rounded-lg pr-1 text-body",
          hot ? "bg-surface-active" : "hover:bg-surface-hover",
        )}
      >
        <button
          type="button"
          aria-label={
            open ? t("docs.nav.collapseNode") : t("docs.nav.expandNode")
          }
          aria-expanded={open}
          onClick={() => actions.toggleExpanded(key)}
          className="flex size-5 shrink-0 items-center justify-center text-fg-muted"
        >
          <Icon name={open ? "chevron-down" : "chevron-right"} size={12} />
        </button>
        <button
          type="button"
          onClick={() => actions.toggleExpanded(key)}
          className="flex min-w-0 flex-1 items-center gap-2 py-1 text-left"
        >
          <DocSetIcon docSet={docSet} />
          <span className="truncate">{docSet.name}</span>
        </button>
        {docSet.can_edit && (
          <span className="flex items-center opacity-0 group-hover:opacity-100 focus-within:opacity-100">
            {/* 文档集根的快速添加：与「我的文档」分组的「+」相同的菜单，目标为文档集根 */}
            <QuickAddMenu
              location={{ container, parentId: null }}
              label={t("docs.nav.newInSet")}
              placement="bottom-start"
              trigger={(props) => (
                <IconButton
                  ref={props.ref}
                  label={t("docs.nav.newInSet")}
                  icon={<Icon name="plus" />}
                  size="28"
                  variant="ghost"
                  active={props.open}
                  onClick={props.toggle}
                />
              )}
            />
            <DocSetMenu docSet={docSet} />
          </span>
        )}
      </div>
      {open && (
        <DocChildren
          orgId={orgId}
          spaceId={spaceId}
          container={container}
          parentId={null}
          depth={1}
          path={[]}
          selectedId={selectedId}
        />
      )}
    </div>
  );
}

/** 某个父节点下的子节点（按需加载）。 */
function DocChildren({
  orgId,
  spaceId,
  container,
  parentId,
  depth,
  path,
  selectedId,
  empty,
}: {
  orgId: string;
  spaceId: string;
  container: DocContainer;
  parentId: string | null;
  depth: number;
  /** 父节点的祖先 id（含父节点自身），用于拖拽校验 */
  path: readonly string[];
  selectedId?: string;
  /** 没有子节点时显示的内容 */
  empty?: ReactNode;
}) {
  const t = useT();
  const nodes = useDocChildren(orgId, spaceId, container, parentId);
  if (nodes.isPending)
    return (
      <div
        aria-busy="true"
        aria-label={t("docs.nav.loading")}
        className="flex flex-col gap-1 py-1"
      >
        <div
          className="h-6 animate-pulse rounded bg-surface-muted"
          style={{ marginLeft: indentFor(depth) + 24 }}
        />
      </div>
    );
  if (nodes.isError)
    return (
      <p
        role="alert"
        style={{ paddingLeft: indentFor(depth) + 24 }}
        className="py-1 text-caption text-danger"
      >
        {t("docs.loadFailed")}
      </p>
    );
  if (empty && nodes.data?.length === 0) return empty;
  return (nodes.data ?? []).map((node) => (
    <DocRow
      key={node.id}
      orgId={orgId}
      spaceId={spaceId}
      node={node}
      depth={depth}
      path={path}
      selectedId={selectedId}
    />
  ));
}

/** 树里的一行：文件夹可展开；拖拽与放置都在这一行上完成。 */
function DocRow({
  orgId,
  spaceId,
  node,
  depth,
  path,
  selectedId,
}: {
  orgId: string;
  spaceId: string;
  node: DocNode;
  depth: number;
  path: readonly string[];
  selectedId?: string;
}) {
  const t = useT();
  const navigate = useNavigate();
  const actions = useDocActions();
  const indicator = useContext(DropIndicatorContext);
  const isFolder = node.kind === "folder";
  const open = isFolder && actions.expanded.has(node.id);
  const childPath = [...path, node.id];
  const drag = useDraggable({
    id: `drag:${node.id}`,
    data: { node } satisfies DragPayload,
    disabled: !node.can_edit,
  });
  const drop = useDroppable({
    id: `drop:node:${node.id}`,
    data: { kind: "node", node, path } satisfies DropData,
  });
  const hot = indicator?.id === `drop:node:${node.id}` ? indicator.zone : null;
  const selected = node.id === selectedId;

  return (
    <div className="flex flex-col gap-0.5">
      <div
        ref={(el) => {
          drag.setNodeRef(el);
          drop.setNodeRef(el);
        }}
        {...drag.listeners}
        style={{ paddingLeft: indentFor(depth) }}
        className={cn(
          "group relative flex h-8 items-center gap-1 rounded-lg pr-1 text-body",
          selected ? "bg-surface-active font-medium" : "hover:bg-surface-hover",
          drag.isDragging && "opacity-50",
          hot === "inside" && "bg-surface-active",
        )}
      >
        {hot === "before" && (
          <span className="pointer-events-none absolute inset-x-0 top-0 h-0.5 rounded bg-primary" />
        )}
        {hot === "after" && (
          <span className="pointer-events-none absolute inset-x-0 bottom-0 h-0.5 rounded bg-primary" />
        )}
        {isFolder && node.has_children ? (
          <button
            type="button"
            aria-label={
              open ? t("docs.nav.collapseNode") : t("docs.nav.expandNode")
            }
            aria-expanded={open}
            onClick={() => actions.toggleExpanded(node.id)}
            className="flex size-5 shrink-0 items-center justify-center text-fg-muted"
          >
            <Icon name={open ? "chevron-down" : "chevron-right"} size={12} />
          </button>
        ) : (
          <span className="size-5 shrink-0" />
        )}
        <button
          type="button"
          onClick={() => navigate(docsPath(orgId, spaceId, node.id))}
          className="flex min-w-0 flex-1 items-center gap-2 py-1 text-left"
        >
          <NodeIcon node={node} />
          <span className="truncate">{node.title || t("docs.untitled")}</span>
        </button>
        <span className="flex items-center opacity-0 group-hover:opacity-100 focus-within:opacity-100">
          {isFolder && node.can_edit && (
            <QuickAddMenu
              location={{ container: node.container, parentId: node.id }}
              label={t("docs.nav.newInFolder")}
              placement="bottom-start"
              trigger={(props) => (
                <IconButton
                  ref={props.ref}
                  label={t("docs.nav.newInFolder")}
                  icon={<Icon name="plus" />}
                  size="28"
                  variant="ghost"
                  active={props.open}
                  onClick={props.toggle}
                />
              )}
            />
          )}
          <DocRowMenu node={node} />
        </span>
      </div>
      {open && (
        <DocChildren
          orgId={orgId}
          spaceId={spaceId}
          container={node.container}
          parentId={node.id}
          depth={depth + 1}
          path={childPath}
          selectedId={selectedId}
        />
      )}
    </div>
  );
}

/** 导航宽度的拖动手柄：240–480，松开后由调用方保存。 */
function WidthHandle({
  width,
  onWidthChange,
}: {
  width: number;
  onWidthChange: (width: number) => void;
}) {
  const t = useT();
  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    event.preventDefault();
    const startX = event.clientX;
    const startWidth = width;
    const move = (next: PointerEvent) =>
      onWidthChange(clampNavWidth(startWidth + next.clientX - startX));
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };
  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={t("docs.nav.resize")}
      aria-valuemin={DOC_NAV_WIDTH.min}
      aria-valuemax={DOC_NAV_WIDTH.max}
      aria-valuenow={width}
      onPointerDown={onPointerDown}
      className="absolute inset-y-0 -right-1 z-10 w-2 cursor-col-resize hover:bg-primary/20"
    />
  );
}
