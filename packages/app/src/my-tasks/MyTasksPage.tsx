import { useQuery } from "@tanstack/react-query";
import { useMemo, useRef, useState } from "react";
import { useParams, useSearchParams } from "react-router-dom";

import type { TaskListQuery } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Avatar, Button, Icon, SearchMenu, Spinner, cn } from "@openfox/ui";

import { keys, useApi } from "../context";
import { useCreateTaskDialog } from "../tasks/create/CreateTaskProvider";
import { useFilterOptions } from "../tasks/filters/options";
import {
  FILTER_FIELDS,
  sortStatuses,
  type FilterCondition,
} from "../tasks/model";
import { displayIdOf, useTaskParam } from "../tasks/parts";
import {
  useAgentDirectory,
  useMySource,
  useOrgPeople,
  usePickerLabels,
  useStatuses,
} from "../tasks/queries";
import { TaskState, TaskWorkspace } from "../tasks/TaskWorkspace";

type Scope = "all" | "assigned" | "created";
const SCOPES: Scope[] = ["all", "assigned", "created"];
const EMPTY_PARAMS: TaskListQuery = {};

/** 我的任务 /o/:orgId/my-tasks：跨空间汇总，视图与空间任务页共用。 */
export function MyTasksPage() {
  const t = useT();
  const api = useApi();
  const { orgId = "" } = useParams();
  const [search] = useSearchParams();
  const { openCreate } = useCreateTaskDialog();
  const taskParam = useTaskParam();
  const [scope, setScope] = useState<Scope>("all");
  const [spaceIds, setSpaceIds] = useState<string[]>([]);
  const [spaceOpen, setSpaceOpen] = useState(false);
  const spaceAnchor = useRef<HTMLButtonElement | null>(null);

  const spaces = useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
  });
  const statuses = useStatuses(orgId);
  const labels = usePickerLabels(orgId);
  const people = useOrgPeople(orgId).data ?? [];
  const directory = useAgentDirectory(orgId);
  const orgTypes = useQuery({
    queryKey: keys.orgTaskTypes(orgId),
    queryFn: () => api.orgTaskTypes.list(orgId),
  });
  const source = useMySource(orgId, {
    scope,
    space_id: spaceIds.length ? spaceIds : undefined,
  });

  const columns = useMemo(
    () => sortStatuses((statuses.data ?? []).filter((s) => !s.archived_at)),
    [statuses.data],
  );
  const agents = useMemo(
    () =>
      (directory.data?.entries ?? [])
        .filter((e) => e.type === "agent" && !e.archived_at)
        .map((e) => ({ id: e.id, name: e.name })),
    [directory.data],
  );
  const crews = useMemo(
    () =>
      (directory.data?.entries ?? []).filter(
        (e) => e.type === "crew" && !e.archived_at,
      ),
    [directory.data],
  );
  const types = useMemo(
    () =>
      (orgTypes.data ?? []).flatMap((s) =>
        s.task_types.map((type) => ({ ...type, group: s.space_name })),
      ),
    [orgTypes.data],
  );
  const filterOptions = useFilterOptions({
    statuses: columns,
    types,
    labels: labels.data ?? [],
    people,
    agents,
    crews,
  });
  // 全局配置「查看已标记任务」跳转：?label=<id> 预置标签筛选
  const labelParam = search.get("label");
  const initialConditions = useMemo<FilterCondition[]>(
    () =>
      labelParam ? [{ field: "label", op: "is", values: [labelParam] }] : [],
    [labelParam],
  );

  if (statuses.isLoading)
    return (
      <TaskState busy icon={<Spinner />} title={t("tasks.empty.loading")} />
    );

  const spaceList = spaces.data ?? [];
  const spaceLabel =
    spaceIds.length === 0
      ? t("myTasks.allSpaces")
      : spaceIds.length === 1
        ? (spaceList.find((s) => s.id === spaceIds[0])?.name ??
          t("myTasks.allSpaces"))
        : t("myTasks.spaces", { count: spaceIds.length });

  const emptyText =
    scope === "created"
      ? {
          title: t("myTasks.emptyCreatedTitle"),
          desc: t("myTasks.emptyCreatedDesc"),
        }
      : scope === "assigned"
        ? {
            title: t("myTasks.emptyAssignedTitle"),
            desc: t("myTasks.emptyAssignedDesc"),
          }
        : {
            title: t("myTasks.emptyAllTitle"),
            desc: t("myTasks.emptyAllDesc"),
          };

  return (
    <div className="flex h-full flex-col">
      <h1 className="sr-only">{t("myTasks.title")}</h1>
      <TaskWorkspace
        key={`${labelParam ?? ""}`}
        orgId={orgId}
        source={source}
        pageKey="my-tasks"
        kind="my"
        columns={columns}
        baseParams={EMPTY_PARAMS}
        filterFields={FILTER_FIELDS}
        filterOptions={filterOptions}
        initialConditions={initialConditions}
        onOpen={(task) => taskParam.open(displayIdOf(task))}
        people={people}
        agents={agents}
        empty={
          <TaskState
            icon={
              <span
                aria-hidden
                className="flex size-14 items-center justify-center rounded-2xl bg-surface-muted text-fg-muted"
              >
                <Icon name="user-check" size={28} />
              </span>
            }
            title={emptyText.title}
            desc={emptyText.desc}
            actions={
              scope !== "assigned" ? (
                <Button
                  leading={<Icon name="plus" />}
                  onClick={() => openCreate()}
                >
                  {t("tasks.new")}
                </Button>
              ) : undefined
            }
          />
        }
        leading={
          <>
            <button
              ref={spaceAnchor}
              type="button"
              aria-label={`${t("myTasks.spaceFilter")}: ${spaceLabel}`}
              aria-expanded={spaceOpen}
              onClick={() => setSpaceOpen((v) => !v)}
              className="flex h-8 items-center gap-1.5 rounded-full border border-line-default px-3 text-body-sm hover:bg-surface-hover"
            >
              <Icon name="layers" size={14} className="text-fg-muted" />
              {spaceLabel}
              <Icon name="chevron-down" size={12} className="text-fg-muted" />
            </button>
            <SearchMenu
              open={spaceOpen}
              onOpenChange={setSpaceOpen}
              anchorRef={spaceAnchor}
              label={t("myTasks.spaceFilter")}
              multiple
              groups={[
                {
                  options: [
                    {
                      value: "",
                      label: t("myTasks.allSpaces"),
                      icon: <Icon name="layers" />,
                    },
                    ...spaceList.map((s) => ({
                      value: s.id,
                      label: s.name,
                      icon: <Avatar name={s.name} kind="agent" size={16} />,
                    })),
                  ],
                },
              ]}
              selected={spaceIds.length ? spaceIds : [""]}
              onSelect={(id) =>
                setSpaceIds((prev) =>
                  id === ""
                    ? []
                    : prev.includes(id)
                      ? prev.filter((x) => x !== id)
                      : [...prev, id],
                )
              }
            />
            <div
              role="radiogroup"
              aria-label={t("myTasks.scope")}
              className="flex items-center gap-1"
            >
              {SCOPES.map((item) => (
                <button
                  key={item}
                  type="button"
                  role="radio"
                  aria-checked={scope === item}
                  onClick={() => setScope(item)}
                  className={cn(
                    "h-7 rounded-full px-3 text-body-sm",
                    scope === item
                      ? "bg-surface-active font-medium"
                      : "text-fg-secondary hover:bg-surface-hover",
                  )}
                >
                  {t(`myTasks.scopes.${item}`)}
                </button>
              ))}
            </div>
          </>
        }
      />
    </div>
  );
}
