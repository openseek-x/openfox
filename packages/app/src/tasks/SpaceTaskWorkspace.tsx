import { useMemo, type ComponentProps } from "react";

import type { TaskListQuery } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Spinner } from "@openfox/ui";

import { useIterations, iterationDisplayName } from "../iterations/queries";
import { useProjects } from "../projects/queries";
import { useSpaceEstimation } from "../spaces/EstimateField";
import { useSpaceDetail } from "../spaces/queries";
import { useFilterOptions } from "./filters/options";
import { FILTER_FIELDS, spaceColumns } from "./model";
import { displayIdOf, useTaskParam } from "./parts";
import {
  useAgentDirectory,
  usePeople,
  usePickerLabels,
  useSpaceAgents,
  useSpaceSource,
  useSpaceTypes,
  useStatuses,
} from "./queries";
import { TaskState, TaskWorkspace } from "./TaskWorkspace";

/**
 * 固定在某个空间里的任务工作区：迭代详情、专项详情、打开视图、视图编辑预览共用。
 * baseParams 固定查询范围（如 iteration_id / project_id / view_id）。
 */
export function SpaceTaskWorkspace({
  orgId,
  spaceId,
  pageKey,
  baseParams,
  empty,
  ...workspace
}: {
  orgId: string;
  spaceId: string;
  pageKey: string;
  baseParams: TaskListQuery;
  empty: React.ReactNode;
} & Pick<
  ComponentProps<typeof TaskWorkspace>,
  | "initialConditions"
  | "initialDisplay"
  | "fallbackDisplay"
  | "onCreate"
  | "onChange"
  | "leading"
>) {
  const t = useT();
  const space = useSpaceDetail(orgId, spaceId);
  const taskParam = useTaskParam();
  const estimation = useSpaceEstimation(orgId, spaceId);
  const types = useSpaceTypes(orgId, spaceId);
  const statuses = useStatuses(orgId);
  const labels = usePickerLabels(orgId, spaceId);
  const people = usePeople(orgId, space.data);
  const spaceAgents = useSpaceAgents(orgId, spaceId);
  const directory = useAgentDirectory(orgId);
  const source = useSpaceSource(orgId, spaceId);
  const iterationEnabled = Boolean(space.data?.features.iterations);
  const projectEnabled = Boolean(space.data?.features.projects);
  const iterations = useIterations(
    orgId,
    spaceId,
    { status: "planned,active" },
    iterationEnabled,
  );
  const projects = useProjects(orgId, spaceId, { enabled: projectEnabled });
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
  const columns = useMemo(
    () => spaceColumns(statuses.data ?? [], typeList, null),
    [statuses.data, typeList],
  );
  const iterationList = useMemo(
    () => iterations.data?.iterations ?? [],
    [iterations.data],
  );
  const projectList = useMemo(
    () => projects.data?.projects ?? [],
    [projects.data],
  );
  const refNames = useMemo(
    () =>
      new Map([
        ...iterationList.map(
          (item) => [item.id, iterationDisplayName(item)] as const,
        ),
        ...projectList.map((item) => [item.id, item.name] as const),
      ]),
    [iterationList, projectList],
  );
  const filterOptions = useFilterOptions({
    statuses: columns,
    types: typeList,
    labels: labels.data ?? [],
    people,
    agents,
    crews,
    iterations: iterationList,
    projects: projectList,
  });
  if (types.isLoading || statuses.isLoading || space.isLoading) {
    return (
      <TaskState busy icon={<Spinner />} title={t("tasks.empty.loading")} />
    );
  }
  return (
    <TaskWorkspace
      key={pageKey}
      orgId={orgId}
      source={source}
      pageKey={pageKey}
      kind="space"
      columns={columns}
      baseParams={baseParams}
      filterFields={estimation ? [...FILTER_FIELDS, "estimate"] : FILTER_FIELDS}
      filterOptions={filterOptions}
      onOpen={(task) => taskParam.open(displayIdOf(task))}
      estimation={estimation}
      iterationEnabled={iterationEnabled}
      projectEnabled={projectEnabled}
      refNames={refNames}
      people={people}
      agents={agents}
      empty={empty}
      {...workspace}
    />
  );
}
