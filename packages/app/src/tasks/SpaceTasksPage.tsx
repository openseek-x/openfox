import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useParams } from "react-router-dom";

import type { TaskListQuery } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, Kbd, Spinner, cn } from "@openfox/ui";

import { keys, useApi } from "../context";
import { NotFoundPage } from "../pages/StatePages";
import { useSpaceEstimation } from "../spaces/EstimateField";
import { isRetired, useSpaceDetail } from "../spaces/queries";
import { ReadOnlyContext, useTaskReadOnlyReason } from "../spaces/readonly";
import { RetiredBanner } from "../spaces/settings/parts";
import { SpaceIcon } from "../spaces/SpaceIcon";
import { useCreateTaskDialog } from "./create/CreateTaskProvider";
import { useFilterOptions } from "./filters/options";
import { FILTER_FIELDS, spaceColumns } from "./model";
import { displayIdOf, useTaskParam } from "./parts";
import { TaskTypeIcon } from "./primitives/TaskTypeIcon";
import {
  useAgentDirectory,
  usePeople,
  usePickerLabels,
  useSpaceAgents,
  useSpaceSource,
  useSpaceTypes,
  useStatuses,
} from "./queries";
import { useProjects } from "../projects/queries";
import {
  iterationDisplayName,
  useIterationSettings,
  useIterations,
} from "../iterations/queries";
import { TaskState, TaskWorkspace } from "./TaskWorkspace";

const ALL = "all";

/** 空间任务页 /o/:orgId/spaces/:spaceId/tasks。 */
export function SpaceTasksPage() {
  const t = useT();
  const api = useApi();
  const { orgId = "", spaceId = "" } = useParams();
  const { openCreate } = useCreateTaskDialog();
  const taskParam = useTaskParam();
  const spaces = useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
  });
  // 已停用空间不在默认（活跃）列表里，取详情；已删除 / 不可读时详情 404。
  const listed = spaces.data?.find((s) => s.id === spaceId);
  const detail = useSpaceDetail(
    orgId,
    spaces.data && !listed ? spaceId : undefined,
  );
  const space = listed ?? detail.data;
  const retired = isRetired(space);
  const readOnlyReason = useTaskReadOnlyReason(orgId, spaceId);
  const estimation = useSpaceEstimation(orgId, spaceId);
  const iterationSettings = useIterationSettings(orgId, spaceId);
  const iterationList = useIterations(orgId, spaceId, {
    status: "planned,active",
  });
  const types = useSpaceTypes(orgId, spaceId);
  const statuses = useStatuses(orgId);
  const labels = usePickerLabels(orgId, spaceId);
  const people = usePeople(orgId, space);
  const spaceAgents = useSpaceAgents(orgId, spaceId);
  const directory = useAgentDirectory(orgId);
  const source = useSpaceSource(orgId, spaceId);
  const [tab, setTab] = useState(ALL);

  const agents = useMemo(
    () => (spaceAgents.data ?? []).filter((a) => !a.archived_at),
    [spaceAgents.data],
  );
  const crews = useMemo(
    () =>
      (directory.data?.entries ?? []).filter(
        (e) => e.type === "crew" && e.space_id === spaceId && !e.archived_at,
      ),
    [directory.data, spaceId],
  );
  const typeList = useMemo(() => types.data ?? [], [types.data]);
  const tabType = typeList.find((x) => x.id === tab);
  const activeTab = tabType ? tab : ALL;
  const columns = useMemo(
    () => spaceColumns(statuses.data ?? [], typeList, tabType?.id ?? null),
    [statuses.data, typeList, tabType],
  );
  const baseParams = useMemo<TaskListQuery>(
    () => (tabType ? { type_id: [tabType.id] } : {}),
    [tabType],
  );
  const allColumns = useMemo(
    () => spaceColumns(statuses.data ?? [], typeList, null),
    [statuses.data, typeList],
  );
  const projectList = useProjects(orgId, spaceId, {
    enabled: Boolean(space?.features.projects),
  });
  const refNames = useMemo(
    () =>
      new Map([
        ...(iterationList.data?.iterations ?? []).map(
          (item) => [item.id, iterationDisplayName(item)] as const,
        ),
        ...(projectList.data?.projects ?? []).map(
          (item) => [item.id, item.name] as const,
        ),
      ]),
    [iterationList.data, projectList.data],
  );
  const filterOptions = useFilterOptions({
    statuses: allColumns,
    types: typeList,
    labels: labels.data ?? [],
    people,
    agents,
    crews,
    iterations: iterationList.data?.iterations ?? [],
    projects: projectList.data?.projects ?? [],
  });

  if (spaces.data && !listed && detail.isError) return <NotFoundPage />;
  if (!space || types.isLoading || statuses.isLoading)
    return (
      <TaskState busy icon={<Spinner />} title={t("tasks.empty.loading")} />
    );

  const create = (statusId?: string) => {
    // 已停用空间只读
    if (!readOnlyReason) openCreate({ spaceId, statusId, typeId: tabType?.id });
  };

  const empty = tabType ? (
    <TaskState
      icon={<TaskTypeGlyph />}
      title={t("tasks.empty.typeTitle", { type: tabType.name })}
      actions={
        <>
          <Button
            onClick={() => create()}
            disabled={Boolean(readOnlyReason)}
            leading={<Icon name="plus" />}
          >
            {t("tasks.new")}
          </Button>
          <Button variant="outline" onClick={() => setTab(ALL)}>
            {t("tasks.allTypes")}
          </Button>
        </>
      }
    />
  ) : (
    <TaskState
      icon={<TaskTypeGlyph />}
      title={t("tasks.empty.title")}
      desc={t("tasks.empty.desc")}
      actions={
        <>
          <Button
            onClick={() => create()}
            disabled={Boolean(readOnlyReason)}
            leading={<Icon name="plus" />}
          >
            {t("tasks.new")}
            <Kbd className="ml-1 border-transparent bg-white/15 text-fg-inverse">
              C
            </Kbd>
          </Button>
          <Button
            variant="outline"
            disabled
            title={t("shell.unavailableTitle")}
            leading={<Icon name="book-open" />}
          >
            {t("tasks.helpDocs")}
          </Button>
        </>
      }
    />
  );

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center gap-1.5 border-b border-line-default px-4">
        <nav
          aria-label={t("tasks.breadcrumb")}
          className="flex items-center gap-1.5 text-body"
        >
          <SpaceIcon icon={space.icon} />
          <span className="text-fg-muted">{space.name}</span>
          <Icon name="chevron-right" size={12} className="text-fg-subtle" />
          <h1 className="font-medium">{t("tasks.title")}</h1>
        </nav>
      </header>
      {readOnlyReason && (
        <div className="px-4 pt-3">
          <RetiredBanner
            message={retired ? t("spaces.task.retiredBanner") : readOnlyReason}
          />
        </div>
      )}
      {/* 已停用空间：视图里的写操作（拖动、单元格编辑、移动状态）只读 */}
      <ReadOnlyContext.Provider value={readOnlyReason ?? false}>
        <TaskWorkspace
          // 切换类型页签时重置筛选
          key={`${spaceId}:${activeTab}`}
          orgId={orgId}
          source={source}
          pageKey={`space:${spaceId}`}
          kind="space"
          columns={columns}
          baseParams={baseParams}
          filterFields={
            estimation ? [...FILTER_FIELDS, "estimate"] : FILTER_FIELDS
          }
          filterOptions={filterOptions}
          onOpen={(task) => taskParam.open(displayIdOf(task))}
          onCreate={retired ? undefined : (statusId) => create(statusId)}
          estimation={estimation}
          iterationEnabled={Boolean(iterationSettings.data?.enabled)}
          projectEnabled={Boolean(space.features.projects)}
          refNames={refNames}
          people={people}
          agents={agents}
          empty={empty}
          leading={
            typeList.length >= 2 ? (
              <div
                role="tablist"
                aria-label={t("tasks.typeTabs")}
                className="flex items-center gap-1 overflow-x-auto"
              >
                {[
                  { id: ALL, name: t("tasks.allTypes"), icon: null },
                  ...typeList,
                ].map((item) => {
                  const selected = item.id === activeTab;
                  return (
                    <button
                      key={item.id}
                      type="button"
                      role="tab"
                      aria-selected={selected}
                      onClick={() => setTab(item.id)}
                      className={cn(
                        "flex h-7 shrink-0 items-center gap-1.5 rounded-full px-3 text-body-sm",
                        selected
                          ? "bg-surface-active font-medium text-fg-default"
                          : "text-fg-secondary hover:bg-surface-hover",
                      )}
                    >
                      {item.id !== ALL && (
                        <TaskTypeIcon
                          icon={item.icon}
                          size={14}
                          className="text-fg-muted"
                        />
                      )}
                      {item.name}
                    </button>
                  );
                })}
              </div>
            ) : undefined
          }
        />
      </ReadOnlyContext.Provider>
    </div>
  );
}

function TaskTypeGlyph() {
  return (
    <span
      aria-hidden
      className="flex size-14 items-center justify-center rounded-2xl bg-surface-muted text-fg-muted"
    >
      <Icon name="square-check-big" size={28} />
    </span>
  );
}
