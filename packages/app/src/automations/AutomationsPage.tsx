import {
  DndContext,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  rectSortingStrategy,
  useSortable,
} from "@dnd-kit/sortable";
import { CSS as DndCSS } from "@dnd-kit/utilities";
import { useState, type ReactNode } from "react";
import { useNavigate, useParams } from "react-router-dom";

import type { Automation } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Icon,
  Menu,
  MenuItem,
  Switch,
  useToast,
} from "@openfox/ui";

import { AgentAvatar } from "../agents/AgentAvatar";
import { canWriteSpace, useSpaceDetail } from "../spaces/queries";
import { reorderIds } from "../spaces/SpacesNav";
import { TaskState } from "../tasks/TaskWorkspace";
import { useStatuses } from "../tasks/queries";
import { AutomationEditorDialog } from "./AutomationEditorDialog";
import { plainPrompt, triggerSummary } from "./parts";
import {
  automationErrorMessage,
  useAutomations,
  useDeleteAutomation,
  useReorderAutomations,
  useRunAutomation,
  useToggleAutomation,
} from "./queries";

// 自动化列表：空态、卡片网格、更多菜单、启停确认与删除确认。

/** 卡片网格 auto-fit minmax(360, 1fr)，间距 12。 */
const GRID_CLASS =
  "grid gap-3 [grid-template-columns:repeat(auto-fit,minmax(360px,1fr))]";

export function AutomationsPage() {
  const t = useT();
  const { orgId = "", spaceId = "" } = useParams();
  const space = useSpaceDetail(orgId, spaceId);
  const list = useAutomations(orgId, spaceId);
  const reorder = useReorderAutomations(orgId, spaceId);
  const [editing, setEditing] = useState<Automation | "new" | null>(null);
  // 拖拽只用指针：键盘传感器会占用卡片的回车键，与打开详情冲突
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
  );

  if (space.isError || list.isError) {
    return <TaskState title={t("automations.loadFailed")} />;
  }
  if (!space.data || list.isLoading) {
    return (
      <div className="flex flex-col gap-3 p-4" aria-busy="true">
        {/* 加载时显示 4 块 143 高的骨架 */}
        <div className={GRID_CLASS}>
          {[0, 1, 2, 3].map((index) => (
            <div
              key={index}
              aria-label={t("automations.cardSkeletonLabel")}
              className="h-[143px] animate-pulse rounded-xl bg-surface-muted"
            />
          ))}
        </div>
      </div>
    );
  }

  const canWrite = canWriteSpace(space.data);
  const automations = list.data ?? [];
  const create = (
    <Button
      size="28"
      disabled={!canWrite}
      leading={<Icon name="plus" />}
      onClick={() => setEditing("new")}
    >
      {t("automations.create")}
    </Button>
  );

  const onDragEnd = (event: DragEndEvent) => {
    const ids = reorderIds(
      automations.map((item) => item.id),
      String(event.active.id),
      event.over ? String(event.over.id) : null,
    );
    if (ids) reorder.mutate(ids);
  };

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-default px-4">
        <h1 className="font-medium">{t("automations.title")}</h1>
        {automations.length > 0 && <div className="ml-auto">{create}</div>}
      </header>
      {automations.length === 0 ? (
        <AutomationsEmpty create={create} />
      ) : (
        <div className="min-h-0 flex-1 overflow-auto p-4">
          <DndContext
            sensors={sensors}
            collisionDetection={closestCenter}
            onDragEnd={onDragEnd}
          >
            <SortableContext
              items={automations.map((item) => item.id)}
              strategy={rectSortingStrategy}
            >
              <div className={GRID_CLASS}>
                {automations.map((automation) => (
                  <AutomationCard
                    key={automation.id}
                    orgId={orgId}
                    spaceId={spaceId}
                    automation={automation}
                    canWrite={canWrite}
                    draggable={canWrite && !reorder.isPending}
                    onEdit={() => setEditing(automation)}
                  />
                ))}
              </div>
            </SortableContext>
          </DndContext>
        </div>
      )}
      <AutomationEditorDialog
        orgId={orgId}
        spaceId={spaceId}
        automation={editing && editing !== "new" ? editing : undefined}
        open={editing !== null}
        onClose={() => setEditing(null)}
      />
    </div>
  );
}

/** 空态块宽 448，居中；空态时顶栏不显示新建按钮。 */
function AutomationsEmpty({ create }: { create: ReactNode }) {
  const t = useT();
  return (
    <div className="flex flex-1 items-center justify-center p-6">
      <div className="flex w-[448px] max-w-full flex-col items-start gap-4">
        <Icon name="openfox-empty-automations" size={72} />
        <div className="flex flex-col gap-2">
          <h2 className="text-body font-medium">
            {t("automations.emptyTitle")}
          </h2>
          <p className="text-body-sm leading-relaxed text-fg-muted">
            {t("automations.emptyDesc")}
          </p>
        </div>
        {create}
      </div>
    </div>
  );
}

/** 卡片：头像、标题、启停、两行描述、虚线、触发器摘要与更多菜单。 */
function AutomationCard({
  orgId,
  spaceId,
  automation,
  canWrite,
  draggable,
  onEdit,
}: {
  orgId: string;
  spaceId: string;
  automation: Automation;
  canWrite: boolean;
  draggable: boolean;
  onEdit: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const sortable = useSortable({ id: automation.id, disabled: !draggable });
  const toggle = useToggleAutomation(orgId, spaceId);
  const remove = useDeleteAutomation(orgId, spaceId);
  const run = useRunAutomation(orgId, spaceId);
  // 任务状态触发器的摘要要显示状态名：与编辑弹窗共用组织状态列表的查询
  const statuses = useStatuses(orgId);
  const [confirmToggle, setConfirmToggle] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [index, setIndex] = useState(0);
  const triggers = automation.triggers;
  const current = triggers[index % Math.max(triggers.length, 1)];
  const running = automation.last_run?.status === "running" || run.isPending;
  const open = () =>
    navigate(`/o/${orgId}/spaces/${spaceId}/automations/${automation.id}`);

  const doRun = () => {
    run.mutate(automation.id, {
      onSuccess: (result) => {
        if (result.status === "skipped") toast(t("automations.runSkipped"));
        else toast(t("automations.runStarted"));
      },
      onError: (error) =>
        toast(automationErrorMessage(t, error, "automations.runFailed"), {
          tone: "error",
        }),
    });
  };

  return (
    <article
      ref={sortable.setNodeRef}
      style={{
        transform: DndCSS.Translate.toString(sortable.transform),
        transition: sortable.transition,
      }}
      {...(draggable ? sortable.listeners : {})}
      role="button"
      tabIndex={0}
      onClick={(event) => {
        if (
          !(event.target as HTMLElement).closest(
            "button,[role=menu],[role=switch]",
          )
        )
          open();
      }}
      onKeyDown={(event) => {
        if (event.key === "Enter" && event.target === event.currentTarget)
          open();
      }}
      aria-label={automation.title}
      className="flex h-[143px] cursor-pointer flex-col gap-2 rounded-xl border border-line-default bg-surface-card p-4 hover:bg-surface-hover"
    >
      <div className="flex items-center gap-2">
        <AgentAvatar name={automation.assignee.name} size={16} />
        <span className="min-w-0 flex-1 truncate font-medium">
          {automation.title}
        </span>
        <div
          onClick={(event) => event.stopPropagation()}
          onKeyDown={(event) => event.stopPropagation()}
        >
          <Switch
            checked={automation.enabled}
            disabled={!canWrite || toggle.isPending}
            aria-label={automation.title}
            onCheckedChange={() => setConfirmToggle(true)}
          />
        </div>
        <div
          onClick={(event) => event.stopPropagation()}
          onKeyDown={(event) => event.stopPropagation()}
        >
          <Menu
            className="!w-44 !min-w-44"
            label={`${automation.title} ${t("automations.more")}`}
            trigger={({ ref, toggle: openMenu }) => (
              <button
                ref={ref}
                type="button"
                aria-label={`${automation.title} ${t("automations.more")}`}
                onClick={openMenu}
                className="flex size-7 items-center justify-center rounded-md text-fg-muted hover:bg-surface-muted"
              >
                <Icon name="ellipsis" />
              </button>
            )}
          >
            <MenuItem
              icon={<Icon name="play" />}
              // 停用（暂停）的规则也能手动运行一次，说明只运行这一次
              disabled={running || !canWrite}
              description={
                automation.enabled
                  ? undefined
                  : t("automations.runOnceWhilePaused")
              }
              onSelect={doRun}
            >
              {running ? t("automations.running") : t("automations.runNow")}
            </MenuItem>
            <MenuItem
              icon={<Icon name="pencil" />}
              disabled={!canWrite}
              onSelect={onEdit}
            >
              {t("automations.edit")}
            </MenuItem>
            <MenuItem
              danger
              icon={<Icon name="trash-2" />}
              disabled={!canWrite}
              onSelect={() => setConfirmDelete(true)}
            >
              {t("automations.delete")}
            </MenuItem>
          </Menu>
        </div>
      </div>
      <p className="line-clamp-2 text-body-sm text-fg-muted">
        {plainPrompt(automation.prompt)}
      </p>
      <div className="mt-auto flex items-center gap-2 border-t border-dashed border-line-default pt-2 text-body-sm text-fg-secondary">
        {triggers.length > 1 && (
          <button
            type="button"
            className="shrink-0 text-fg-muted hover:text-fg-default"
            onClick={(event) => {
              event.stopPropagation();
              setIndex((value) => value + 1);
            }}
          >
            {t("automations.moreTriggers", {
              index: (index % triggers.length) + 1,
              total: triggers.length,
            })}
          </button>
        )}
        <span className="min-w-0 flex-1 truncate">
          {current ? triggerSummary(t, current, statuses.data) : ""}
        </span>
      </div>

      <ConfirmDialog
        open={confirmToggle}
        onCancel={() => setConfirmToggle(false)}
        onConfirm={() =>
          toggle.mutate(
            { automation, enabled: !automation.enabled },
            {
              onSuccess: () => {
                setConfirmToggle(false);
                toast(
                  automation.enabled
                    ? t("automations.paused")
                    : t("automations.resumed"),
                );
              },
              onError: (error) => {
                setConfirmToggle(false);
                toast(
                  automationErrorMessage(t, error, "automations.toggleFailed"),
                  { tone: "error" },
                );
              },
            },
          )
        }
        title={
          automation.enabled
            ? t("automations.pauseTitle")
            : t("automations.resumeTitle")
        }
        description={
          automation.enabled
            ? t("automations.pauseDesc")
            : t("automations.resumeDesc")
        }
        confirmLabel={
          toggle.isPending
            ? t("automations.saving")
            : automation.enabled
              ? t("automations.pauseConfirm")
              : t("automations.resumeConfirm")
        }
        loading={toggle.isPending}
      />
      <ConfirmDialog
        open={confirmDelete}
        danger
        onCancel={() => setConfirmDelete(false)}
        onConfirm={() =>
          remove.mutate(automation, {
            onSuccess: () => {
              setConfirmDelete(false);
              toast(t("automations.deleted"));
            },
            onError: (error) => {
              setConfirmDelete(false);
              toast(
                automationErrorMessage(t, error, "automations.deleteFailed"),
                { tone: "error" },
              );
            },
          })
        }
        title={t("automations.deleteTitle")}
        description={t("automations.deleteDesc")}
        confirmLabel={
          remove.isPending ? t("automations.deleting") : t("automations.delete")
        }
        loading={remove.isPending}
      />
    </article>
  );
}
