import { useMemo } from "react";

import type {
  AgentDirectoryEntry,
  Iteration,
  Label,
  Project,
  TaskStatus,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Avatar, Icon } from "@openfox/ui";

import {
  assigneeGroups,
  labelGroups,
  priorityGroups,
  statusGroups,
} from "../pickers";
import { iterationDisplayName } from "../../iterations/queries";
import { TaskTypeIcon } from "../primitives/TaskTypeIcon";
import type { Person } from "../queries";
import type { FilterOptions } from "./Filters";
import { usePriorityLevels } from "../../organization/queries";

/** 按页面已有的数据组装筛选候选项。 */
export function useFilterOptions({
  statuses,
  types,
  labels,
  people,
  agents,
  crews,
  iterations = [],
  projects = [],
}: {
  statuses: TaskStatus[];
  types: { id: string; name: string; icon?: string | null; group?: string }[];
  labels: Label[];
  people: Person[];
  agents: { id: string; name: string }[];
  crews: AgentDirectoryEntry[];
  iterations?: Iteration[];
  projects?: Project[];
}): FilterOptions {
  const t = useT();
  const priorityLevels = usePriorityLevels();
  return useMemo(() => {
    const typeGroups = new Map<string, typeof types>();
    for (const type of types)
      typeGroups.set(type.group ?? "", [
        ...(typeGroups.get(type.group ?? "") ?? []),
        type,
      ]);
    return {
      status: statusGroups(statuses),
      priority: priorityGroups(t, priorityLevels),
      type: [...typeGroups].map(([group, list]) => ({
        id: group || "types",
        label: group || undefined,
        options: list.map((type) => ({
          value: type.id,
          label: type.name,
          icon: <TaskTypeIcon icon={type.icon} />,
          keywords: group,
        })),
      })),
      assignee: assigneeGroups(t, people, agents),
      crew: [
        {
          options: crews.map((crew) => ({
            value: crew.id,
            label: crew.name,
            icon: (
              <Avatar
                name={crew.name}
                kind="agent"
                size={20}
                glyph={<Icon name="users" size={12} />}
              />
            ),
            keywords: crew.space_name,
          })),
        },
      ],
      label: labelGroups(labels),
      creator: [
        {
          options: people.map((p) => ({
            value: p.id,
            label: p.email,
            icon: <Avatar name={p.email} size={20} />,
          })),
        },
      ],
      iteration: [
        {
          options: [
            { value: "current", label: t("spaces.iteration.filterCurrent") },
            { value: "none", label: t("spaces.iteration.filterNone") },
            ...iterations.map((item) => ({
              value: item.id,
              label: iterationDisplayName(item),
            })),
          ],
        },
      ],
      project: [
        {
          options: [
            { value: "none", label: t("projects.filterNone") },
            ...projects.map((item) => ({ value: item.id, label: item.name })),
          ],
        },
      ],
    };
  }, [
    t,
    priorityLevels,
    statuses,
    types,
    labels,
    people,
    agents,
    crews,
    iterations,
    projects,
  ]);
}
