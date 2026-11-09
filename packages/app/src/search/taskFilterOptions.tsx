import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useEffect } from "react";
import type { CustomField, SpaceDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, type SearchMenuOption } from "@openfox/ui";
import { keys, useApi } from "../context";
import { useFilterOptions } from "../tasks/filters/options";
import { useInvitationAssignees } from "../tasks/invitationAssignees";
import { spaceColumns } from "../tasks/model";
import { assigneeGroups, LabelDot } from "../tasks/pickers";
import { useAgentDirectory } from "../tasks/queries";
import {
  createdPresets,
  duePresets,
  sprintPresets,
  type SearchTaskCondition,
  type SearchTaskField,
} from "./taskFilterModel";

export type TaskFilterOption = SearchMenuOption & {
  aliases?: string[];
  groupLabel?: string;
};
export type ScopeMode = "all" | "subset" | "none";
export type FilterMenu = SearchTaskField | "more" | null;
type Lookup = { isPending: boolean; isError: boolean; refetch: () => unknown };
const unique = <T extends { id: string }>(items: T[]) =>
  Array.from(new Map(items.map((item) => [item.id, item])).values());
const uniqueOptions = (items: SearchMenuOption[]) =>
  Array.from(new Map(items.map((item) => [item.value, item])).values());

// Fetch by field on demand. In the all-spaces view the same visible space set is used for options and results.
export function useTaskFilterOptions(
  orgId: string,
  spaceIds: string[],
  scope: ScopeMode,
  menu: FilterMenu,
  conditions: SearchTaskCondition[],
) {
  const api = useApi(),
    t = useT();
  const needs = (field: SearchTaskField) =>
    menu === field || conditions.some((c) => c.field === field);
  const needCustom =
    menu === "more" ||
    menu?.startsWith("cf_") ||
    conditions.some((c) => c.field.startsWith("cf_"));
  const spaces = useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
  });
  const invites = useInvitationAssignees(
    orgId,
    needs("assignee") && scope !== "none",
    scope === "all" ? undefined : spaceIds,
  );
  const inScope = (id: string) =>
    scope === "all" || (scope === "subset" && spaceIds.includes(id));
  const visible = (spaces.data ?? []).filter((s) => inScope(s.id));
  const types = useQuery({
    queryKey: keys.orgTaskTypes(orgId),
    queryFn: () => api.orgTaskTypes.list(orgId),
    enabled: needs("type") || !!needCustom,
  });
  const statuses = useQuery({
    queryKey: keys.taskStatuses(orgId),
    queryFn: () => api.taskStatuses.list(orgId),
    enabled: needs("status"),
  });
  const queryClient = useQueryClient();
  const workflows = useSpacePages(
    orgId,
    visible,
    "workflows",
    scope === "subset" && needs("status") && spaces.isSuccess,
    async (id) => ({
      items: await queryClient.fetchQuery({
        queryKey: keys.spaceTaskTypes(orgId, id),
        queryFn: () => api.tasks.types(orgId, id),
        staleTime: 60_000,
      }),
    }),
  );
  const {
    fetchNextPage: nextWorkflow,
    hasNextPage: moreWorkflows,
    isFetching: isFetchingWorkflow,
    isError: failedWorkflow,
  } = workflows;
  // Identically named workflow statuses are one choice across spaces. Resolve every alias before selecting it.
  useEffect(() => {
    if (
      menu === "status" &&
      scope === "subset" &&
      moreWorkflows &&
      !isFetchingWorkflow &&
      !failedWorkflow
    )
      void nextWorkflow();
  }, [
    menu,
    scope,
    moreWorkflows,
    isFetchingWorkflow,
    failedWorkflow,
    nextWorkflow,
  ]);
  const directory = useAgentDirectory(
    orgId,
    !!needCustom || needs("assignee") || needs("creator") || needs("crew"),
  );
  const members = useInfiniteQuery({
    queryKey: ["org", orgId, "search-task-options", "members"],
    initialPageParam: "",
    queryFn: ({ pageParam }) => api.members.list(orgId, pageParam || undefined),
    getNextPageParam: (page) => page.next_cursor || undefined,
    enabled: needs("assignee") || needs("creator") || !!needCustom,
  });
  const people =
    members.data?.pages.flatMap((page) =>
      page.members
        .filter((m) => m.status === "ACTIVE")
        .map((m) => ({
          id: m.user_id,
          name: m.full_name || undefined,
          email: m.email,
          avatar_url: m.avatar_url,
        })),
    ) ?? [];
  const labelLists = useSpacePages(
    orgId,
    visible,
    "label",
    needs("label") && spaces.isSuccess,
    async (id) => {
      const list = (scope: "picker" | "space_archived") =>
        queryClient.fetchQuery({
          queryKey: keys.labels(orgId, scope + ":" + id),
          queryFn: () => api.labels.list(orgId, scope, id),
          staleTime: 60_000,
        });
      return {
        items: [...(await list("picker")), ...(await list("space_archived"))],
      };
    },
  );
  const archivedLabels = useQuery({
    queryKey: keys.labels(orgId, "archived"),
    queryFn: () => api.labels.list(orgId, "archived"),
    enabled: needs("label") && visible.length > 0,
  });
  const labels = unique([
    ...(labelLists.data?.pages.flatMap((p) => p.items) ?? []),
    ...(archivedLabels.data ?? []),
  ]);
  const definitions = useQuery({
    queryKey: keys.customFields(orgId),
    queryFn: () => api.customFields.list(orgId),
    enabled: !!needCustom,
  });
  const scopedTypes = (types.data ?? []).filter((group) =>
    inScope(group.space_id),
  );
  const fieldIds = new Set(
    scopedTypes.flatMap((group) =>
      group.task_types.flatMap((type) => type.field_ids),
    ),
  );
  const customFields = (definitions.data ?? []).filter(
    (field) =>
      !field.archived_at && (scope === "all" || fieldIds.has(field.id)),
  );
  const agents = (directory.data?.entries ?? []).filter(
    (entry) =>
      entry.type === "agent" && !entry.archived_at && inScope(entry.space_id),
  );
  const core = useFilterOptions({
    statuses:
      scope === "subset"
        ? spaceColumns(
            statuses.data ?? [],
            (workflows.data?.pages.flatMap((p) => p.items) ?? []).filter(
              (type) => type.enabled && !type.archived_at,
            ),
            null,
          )
        : (statuses.data ?? []).filter((s) => !s.archived_at),
    types: scopedTypes.flatMap((g) =>
      g.task_types.map((type) => ({ ...type, group: g.space_name })),
    ),
    labels,
    people,
    agents,
    crews: (directory.data?.entries ?? []).filter(
      (entry) =>
        entry.type === "crew" && !entry.archived_at && inScope(entry.space_id),
    ),
  });
  const projectOptions = useSpacePages(
    orgId,
    visible.filter((s) => s.features?.projects),
    "project",
    needs("project") && spaces.isSuccess,
    async (id, cursor) => {
      const query = { space_id: id, limit: 100, cursor };
      const page = await api.projects.list(orgId, query);
      return {
        items: page.projects.map((p) => ({ value: p.id, label: p.name })),
        cursor: page.next_cursor,
      };
    },
  );
  const sprintOptions = useSpacePages(
    orgId,
    visible.filter((s) => s.features?.iterations),
    "sprint",
    needs("sprint") && spaces.isSuccess,
    async (id, cursor) => {
      const page = await api.iterations.list(orgId, id, { limit: 100, cursor });
      return {
        items: page.iterations.map((i) => ({
          value: i.id,
          label: i.display_name || i.name || String(i.number),
        })),
        cursor: page.next_cursor,
      };
    },
  );
  const base = (field: keyof typeof core) => {
    const groups = core[field];
    return Array.isArray(groups) ? groups.flatMap((g) => g.options) : [];
  };
  const actorOptions = (field?: CustomField) =>
    assigneeGroups(t, people, agents, {
      none: false,
      humans: field?.member_scope !== "AGENTS",
      agents: field?.member_scope !== "HUMANS",
    }).flatMap((g) => g.options);
  function options(field: SearchTaskField): TaskFilterOption[] {
    if (
      scope === "none" &&
      ["type", "label", "crew", "status", "sprint"].includes(field)
    )
      return [];
    if (field === "status") {
      const categories = new Map(
        (statuses.data ?? []).map((status) => [status.id, status.category]),
      );
      const groups = new Map<string, TaskFilterOption>();
      for (const option of base("status")) {
        const key = categories.get(option.value) + ":" + option.label;
        const group = groups.get(key);
        if (group) group.aliases!.push(option.value);
        else groups.set(key, { ...option, aliases: [option.value] });
      }
      return [...groups.values()];
    }
    if (field === "label")
      return labels.map((l) => ({
        value: l.id,
        label: l.name,
        icon: <LabelDot color={l.color} />,
      }));
    if (field === "type")
      return base("type").map((o) => ({ ...o, description: o.keywords }));
    if (field === "dueDate")
      return duePresets.map((value) => ({
        value,
        label: t(`searchTaskFilters.due_date_values.${value}`),
        icon: <Icon name="calendar" size={14} />,
      }));
    if (field === "createdAt")
      return createdPresets.map((value) => ({
        value,
        label: t(`searchTaskFilters.created_date_values.${value}`),
        icon: <Icon name="calendar" size={14} />,
      }));
    if (field === "relation")
      return (
        [
          "parentTasks",
          "subtasks",
          "blockedTasks",
          "blockingTasks",
          "duplicates",
        ] as const
      ).map((value) => ({
        value,
        label: t(`searchTaskFilters.relation_values.${value}`),
        icon: <Icon name="link" size={14} />,
      }));
    if (field === "sprint")
      return [
        { value: "none", label: t("searchTaskFilters.no_sprint") },
        ...sprintPresets.map((v) => ({
          value: "@rel:" + v,
          label: t(`searchTaskFilters.relative_sprint.${v}`),
        })),
        ...uniqueOptions(
          sprintOptions.data?.pages.flatMap((p) => p.items) ?? [],
        ),
      ];
    if (field === "project")
      return [
        { value: "none", label: t("searchTaskFilters.no_project") },
        ...uniqueOptions(
          projectOptions.data?.pages.flatMap((p) => p.items) ?? [],
        ),
      ];
    if (field === "assignee") {
      const groups = assigneeGroups(t, people, agents, {
        invitations: invites.candidates,
      });
      // List people, pending invitations, then agents. The "No assignee" choice stands alone.
      const order = ["none", "people", "invitations", "agents"];
      return order.flatMap((id) => {
        const group = groups.find((group) => group.id === id);
        return (
          group?.options.map((option) => ({
            ...option,
            groupLabel: group.label,
            description: id === "invitations" ? undefined : option.description,
          })) ?? []
        );
      });
    }
    if (field === "creator") return actorOptions();
    const definition = customFields.find((f) => "cf_" + f.id === field);
    if (definition?.kind === "MEMBER")
      return actorOptions(definition).map((o) => ({
        ...o,
        value: o.value.split(":")[1]!,
      }));
    if (definition)
      return definition.options
        .filter((o) => !o.archived_at)
        .map((o) => ({ value: o.id, label: o.label }));
    return base(field as keyof typeof core);
  }
  function lookups(field: FilterMenu): Lookup[] {
    if (scope === "none") return [];
    if (field === "type") return [types];
    if (field === "status")
      return scope === "subset" ? [statuses, spaces, workflows] : [statuses];
    if (field === "label")
      return visible.length ? [spaces, archivedLabels, labelLists] : [spaces];
    if (field === "crew") return [directory];
    if (field === "assignee" || field === "creator")
      return field === "assignee" && invites.lookup
        ? [members, directory, invites.lookup]
        : [members, directory];
    if (field === "sprint" || field === "project")
      return [spaces, field === "sprint" ? sprintOptions : projectOptions];
    if (field === "more" || field?.startsWith("cf_")) {
      const sources: Lookup[] =
        scope === "subset" ? [types, definitions] : [definitions];
      if (customFields.find((f) => "cf_" + f.id === field)?.kind === "MEMBER")
        sources.push(members, directory);
      return sources;
    }
    return [];
  }
  const states = lookups(menu);
  function currentPage() {
    if (menu === "project") return projectOptions;
    if (menu === "sprint") return sprintOptions;
    if (menu === "label") return labelLists;
    if (
      menu === "assignee" ||
      menu === "creator" ||
      customFields.find((f) => "cf_" + f.id === menu)?.kind === "MEMBER"
    )
      return members;
    return null;
  }
  const paged = currentPage();
  return {
    customFields,
    options,
    loading:
      states.some((q) => q.isPending) ||
      (menu === "status" && scope === "subset" && moreWorkflows),
    failed: states.some((q) => q.isError),
    retry: () =>
      states.filter((q) => q.isError).forEach((q) => void q.refetch()),
    more: paged?.hasNextPage ? () => void paged.fetchNextPage() : undefined,
    loadingMore: paged?.isFetchingNextPage,
  };
}

/** One page covers at most eight visible spaces, then resumes each remaining space/cursor. */
function useSpacePages<T>(
  orgId: string,
  spaces: SpaceDetail[],
  resource: string,
  enabled: boolean,
  load: (
    id: string,
    cursor?: string,
  ) => Promise<{ items: T[]; cursor?: string }>,
) {
  const ids = spaces.map((s) => s.id).sort();
  return useInfiniteQuery({
    queryKey: ["org", orgId, "search-task-options", resource, ids],
    enabled,
    initialPageParam: ids.map((id) => ({ id, cursor: "" })),
    queryFn: async ({ pageParam }) => {
      const batch = pageParam.slice(0, 8);
      const pages = await Promise.all(
        batch.map(async ({ id, cursor }) => ({
          id,
          ...(await load(id, cursor || undefined)),
        })),
      );
      return {
        items: pages.flatMap((p) => p.items),
        next: [
          ...pageParam.slice(8),
          ...pages.flatMap((p) =>
            p.cursor ? [{ id: p.id, cursor: p.cursor }] : [],
          ),
        ],
      };
    },
    getNextPageParam: (page) => (page.next.length ? page.next : undefined),
  });
}
