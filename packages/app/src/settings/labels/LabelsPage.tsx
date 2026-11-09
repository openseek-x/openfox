import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useNavigate, useParams } from "react-router-dom";

import {
  newIdempotencyKey,
  type Label,
  type LabelGroup,
  type UpdateLabelGroupInput,
  type UpdateLabelInput,
} from "@openfox/api";
import { useI18n } from "@openfox/i18n";
import {
  Button,
  Checkbox,
  Icon,
  IconButton,
  LABEL_COLORS,
  Select,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
  TextInput,
  cn,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { localKeys, readLocal, writeLocal } from "../../storage";
import {
  formatMonthDay,
  formatRelativeTime,
} from "../../tasks/primitives/dates";
import { ColorDotPicker } from "../ColorPicker";
import {
  ConfigPageHeader,
  EmptyState,
  ListSkeleton,
  describeError,
  useCanManage,
  useConfigMutation,
} from "../common";
import { InlineEdit } from "../InlineEdit";
import {
  CreateGroupDialog,
  CreateLabelDialog,
  LabelConfirmDialog,
  type LabelConfirm,
} from "./LabelDialogs";
import { BulkMergeButton, GroupRowMenu, LabelRowMenu } from "./LabelMenus";

type Scope = "organization" | "archived";
type Editing = {
  kind: "label" | "group";
  id: string;
  field: "name" | "description";
} | null;

/** 批量接口每次最多 100 个，超出时分批顺序调用。 */
const BULK_LIMIT = 100;
async function inChunks(
  ids: string[],
  run: (chunk: string[]) => Promise<unknown>,
) {
  for (let i = 0; i < ids.length; i += BULK_LIMIT)
    await run(ids.slice(i, i + BULK_LIMIT));
}

function readCollapsed(orgId: string): Set<string> {
  try {
    const raw = readLocal(localKeys.labelGroupsCollapsed(orgId));
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return new Set(
      Array.isArray(parsed)
        ? parsed.filter((item): item is string => typeof item === "string")
        : [],
    );
  } catch {
    return new Set();
  }
}

/** 新建的项置顶直到刷新：pinned 越靠前越新。 */
function pinnedFirst<T extends { id: string }>(items: T[], pinned: string[]) {
  const rank = (id: string) => {
    const index = pinned.indexOf(id);
    return index === -1 ? Number.MAX_SAFE_INTEGER : index;
  };
  return [...items].sort((a, b) => rank(a.id) - rank(b.id));
}

/** 空间标签页复用本组件时的空间范围。 */
export interface LabelSpaceScope {
  id: string;
  /** 空间管理权限（已停用空间为 false） */
  canManage: boolean;
}

/** 全局配置 · 任务标签；传 space 时为空间标签页。 */
export function LabelsPage({ space }: { space?: LabelSpaceScope } = {}) {
  const { orgId = "" } = useParams();
  const { t, locale } = useI18n();
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const navigate = useNavigate();
  const orgCanManage = useCanManage(orgId);
  const canManage = space ? space.canManage : orgCanManage;
  const spaceId = space?.id;
  /** 界面范围（未归档 / 已归档）对应的接口 scope */
  const apiScope = (value: Scope) =>
    spaceId ? (value === "organization" ? "space" : "space_archived") : value;
  /** 空间标签的缓存与组织级分开 */
  const keyScope = (value: Scope) =>
    spaceId ? `space:${spaceId}:${value}` : value;

  const [scope, setScope] = useState<Scope>("organization");
  const [query, setQuery] = useState("");
  const [collapsed, setCollapsed] = useState(() => readCollapsed(orgId));
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [editing, setEditing] = useState<Editing>(null);
  const [createLabel, setCreateLabel] = useState<{ groupId?: string } | null>(
    null,
  );
  const [createGroup, setCreateGroup] = useState(false);
  const [confirm, setConfirm] = useState<LabelConfirm | null>(null);
  const [pinned, setPinned] = useState<string[]>([]);

  useEffect(() => setCollapsed(readCollapsed(orgId)), [orgId]);
  useEffect(() => setSelected(new Set()), [scope, orgId]);

  const groupsKey = spaceId
    ? keys.spaceLabelGroups(orgId, spaceId)
    : keys.labelGroups(orgId);
  const labelsKey = keys.labels(orgId, keyScope(scope));
  const orgLabelsKey = keys.labels(orgId, keyScope("organization"));
  const archivedKey = keys.labels(orgId, keyScope("archived"));
  const groupsQuery = useQuery({
    queryKey: groupsKey,
    queryFn: () => api.labelGroups.list(orgId, spaceId),
  });
  const labelsQuery = useQuery({
    queryKey: labelsKey,
    queryFn: () => api.labels.list(orgId, apiScope(scope), spaceId),
  });
  // 合并目标只能是同范围的未归档标签；在「已归档」视图下也需要这份列表
  const orgLabelsQuery = useQuery({
    queryKey: orgLabelsKey,
    queryFn: () => api.labels.list(orgId, apiScope("organization"), spaceId),
    enabled: canManage,
  });

  const groups = useMemo(() => groupsQuery.data ?? [], [groupsQuery.data]);
  const labels = useMemo(() => labelsQuery.data ?? [], [labelsQuery.data]);
  const mergeCandidates = orgLabelsQuery.data ?? [];

  const toggleCollapsed = (groupId: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(groupId)) next.delete(groupId);
      else next.add(groupId);
      writeLocal(
        localKeys.labelGroupsCollapsed(orgId),
        next.size ? JSON.stringify([...next]) : null,
      );
      return next;
    });

  // ---------- 写操作 ----------
  const otherLabelKeys = [
    scope === "organization" ? archivedKey : orgLabelsKey,
    groupsKey,
    // 空间标签变化后任务里的标签选择器也要刷新
    ...(spaceId ? [keys.labels(orgId, `picker:${spaceId}`)] : []),
  ];
  const removeFromList =
    (ids: Set<string>) =>
    (list: Label[]): Label[] =>
      list.filter((label) => !ids.has(label.id));

  const updateLabel = useConfigMutation<
    { label: Label; patch: Omit<UpdateLabelInput, "version"> },
    Label,
    Label[]
  >({
    queryKey: labelsKey,
    mutationFn: ({ label, patch }) =>
      api.labels.update(orgId, label.id, { ...patch, version: label.version }),
    optimistic: (list, { label, patch }) =>
      list.map((item) =>
        item.id === label.id
          ? {
              ...item,
              ...(patch.name !== undefined && { name: patch.name }),
              ...(patch.description !== undefined && {
                description: patch.description,
              }),
              ...(patch.color !== undefined && { color: patch.color }),
              ...(patch.group_id !== undefined && {
                group_id: patch.group_id,
              }),
            }
          : item,
      ),
    onSuccess: (result, { patch }) => {
      queryClient.setQueryData<Label[]>(labelsKey, (list) =>
        list?.map((item) => (item.id === result.id ? result : item)),
      );
      if (patch.group_id !== undefined) {
        const group = groups.find((item) => item.id === patch.group_id);
        toast(
          group
            ? t("settings.labels.toast.addedToGroup", { name: group.name })
            : t("settings.labels.toast.removedFromGroup"),
        );
      }
    },
    invalidate: otherLabelKeys,
  });

  const archiveLabels = useConfigMutation<
    { labels: Label[]; archived: boolean },
    unknown,
    Label[]
  >({
    queryKey: labelsKey,
    mutationFn: async ({ labels: targets, archived }) => {
      if (targets.length === 1) {
        const [label] = targets;
        return api.labels.update(orgId, label!.id, {
          archived,
          version: label!.version,
        });
      }
      return inChunks(
        targets.map((label) => label.id),
        (label_ids) =>
          api.labels.bulk(orgId, {
            action: archived ? "ARCHIVE" : "UNARCHIVE",
            label_ids,
            space_id: spaceId,
          }),
      );
    },
    optimistic: (list, { labels: targets }) =>
      removeFromList(new Set(targets.map((label) => label.id)))(list),
    onSuccess: (_, { labels: targets, archived }) => {
      setConfirm(null);
      setSelected(new Set());
      toast(
        t(
          archived
            ? "settings.labels.toast.archived"
            : "settings.labels.toast.unarchived",
          { count: targets.length },
        ),
      );
    },
    invalidate: otherLabelKeys,
  });

  const deleteLabels = useConfigMutation<Label[], unknown, Label[]>({
    queryKey: labelsKey,
    mutationFn: async (targets) => {
      if (targets.length === 1) return api.labels.remove(orgId, targets[0]!.id);
      return inChunks(
        targets.map((label) => label.id),
        (label_ids) =>
          api.labels.bulk(orgId, {
            action: "DELETE",
            label_ids,
            space_id: spaceId,
          }),
      );
    },
    optimistic: (list, targets) =>
      removeFromList(new Set(targets.map((label) => label.id)))(list),
    onSuccess: (_, targets) => {
      setConfirm(null);
      setSelected(new Set());
      toast(t("settings.labels.toast.deleted", { count: targets.length }));
    },
    invalidate: otherLabelKeys,
  });

  const mergeLabels = useConfigMutation<
    { labels: Label[]; target: Label },
    unknown,
    Label[]
  >({
    queryKey: labelsKey,
    mutationFn: async ({ labels: sources, target }) => {
      if (sources.length === 1)
        return api.labels.merge(orgId, sources[0]!.id, target.id);
      return inChunks(
        sources.map((label) => label.id),
        (label_ids) =>
          api.labels.bulk(orgId, {
            action: "MERGE",
            label_ids,
            target_label_id: target.id,
            space_id: spaceId,
          }),
      );
    },
    optimistic: (list, { labels: sources }) =>
      removeFromList(new Set(sources.map((label) => label.id)))(list),
    onSuccess: (_, { target }) => {
      setConfirm(null);
      setSelected(new Set());
      toast(t("settings.labels.toast.merged", { name: target.name }));
    },
    invalidate: [orgLabelsKey, archivedKey, groupsKey],
  });

  const updateGroup = useConfigMutation<
    { group: LabelGroup; patch: Omit<UpdateLabelGroupInput, "version"> },
    LabelGroup,
    LabelGroup[]
  >({
    queryKey: groupsKey,
    mutationFn: ({ group, patch }) =>
      api.labelGroups.update(orgId, group.id, {
        ...patch,
        version: group.version,
      }),
    optimistic: (list, { group, patch }) =>
      list.map((item) =>
        item.id === group.id
          ? {
              ...item,
              ...(patch.name !== undefined && { name: patch.name }),
              ...(patch.description !== undefined && {
                description: patch.description,
              }),
              ...(patch.color !== undefined && { color: patch.color }),
            }
          : item,
      ),
    onSuccess: (result) =>
      queryClient.setQueryData<LabelGroup[]>(groupsKey, (list) =>
        list?.map((item) => (item.id === result.id ? result : item)),
      ),
  });

  /** 撤销删除分组：用 label_ids 重建分组（新分组 id 不同，创建顺序排到最后）。 */
  const restoreGroup = async (group: LabelGroup, labelIds: string[]) => {
    try {
      const restored = await api.labelGroups.create(
        orgId,
        {
          name: group.name,
          description: group.description || undefined,
          color: group.color,
          label_ids: labelIds.length ? labelIds : undefined,
          space_id: spaceId,
        },
        newIdempotencyKey(),
      );
      setPinned((prev) => [restored.id, ...prev]);
      toast(t("settings.labels.toast.groupRestored", { name: group.name }));
    } catch (error) {
      toast(describeError(error, t), { tone: "error" });
    } finally {
      void queryClient.invalidateQueries({ queryKey: groupsKey });
      void queryClient.invalidateQueries({
        queryKey: ["org", orgId, "labels"],
      });
    }
  };

  const deleteGroup = useConfigMutation<
    LabelGroup,
    { removed_label_ids: string[] },
    LabelGroup[]
  >({
    queryKey: groupsKey,
    mutationFn: (group) => api.labelGroups.remove(orgId, group.id),
    optimistic: (list, group) => list.filter((item) => item.id !== group.id),
    onSuccess: ({ removed_label_ids }, group) => {
      setSelected((prev) => {
        const next = new Set(prev);
        next.delete(`group:${group.id}`);
        return next;
      });
      // 组内标签已被移出分组，本地先改掉，避免刷新前它们“消失”
      queryClient.setQueriesData<Label[]>(
        { queryKey: ["org", orgId, "labels"] },
        (list) =>
          list?.map((label) =>
            label.group_id === group.id ? { ...label, group_id: null } : label,
          ),
      );
      toast(t("settings.labels.toast.groupDeleted", { name: group.name }), {
        duration: 6000,
        action: {
          label: t("settings.labels.toast.undo"),
          onClick: () => void restoreGroup(group, removed_label_ids),
        },
      });
    },
    invalidate: [orgLabelsKey, archivedKey],
  });

  // ---------- 列表 ----------
  const needle = query.trim().toLowerCase();
  const view = useMemo(() => {
    const matches = (label: Label) =>
      !needle ||
      label.name.toLowerCase().includes(needle) ||
      label.description.toLowerCase().includes(needle);
    const visible = pinnedFirst(labels.filter(matches), pinned);
    const groupIds = new Set(groups.map((group) => group.id));
    const children = new Map<string, Label[]>();
    const ungrouped: Label[] = [];
    for (const label of visible) {
      if (label.group_id && groupIds.has(label.group_id)) {
        const list = children.get(label.group_id) ?? [];
        list.push(label);
        children.set(label.group_id, list);
      } else ungrouped.push(label);
    }
    // 搜索或查看已归档时，没有命中子标签的分组隐藏；组织视图下保留空分组，方便往里加标签
    const showEmptyGroups = !needle && scope === "organization";
    const visibleGroups = pinnedFirst(groups, pinned).filter(
      (group) => showEmptyGroups || (children.get(group.id)?.length ?? 0) > 0,
    );
    return { visibleGroups, children, ungrouped, total: visible.length };
  }, [labels, groups, needle, scope, pinned]);

  const selectedLabels = useMemo(() => {
    const ids = new Set<string>();
    for (const key of selected) {
      const [kind, id] = key.split(":") as [string, string];
      if (kind === "label") ids.add(id);
      else
        for (const label of labels)
          if (label.group_id === id) ids.add(label.id);
    }
    return labels.filter((label) => ids.has(label.id));
  }, [selected, labels]);
  const hasGroupSelected = [...selected].some((key) =>
    key.startsWith("group:"),
  );
  const allArchived =
    selectedLabels.length > 0 &&
    selectedLabels.every((label) => label.archived_at);

  const toggleSelected = (key: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  const viewTasks = (label: Label) =>
    navigate(`/o/${orgId}/my-tasks?label=${encodeURIComponent(label.id)}`);

  const isLoading = labelsQuery.isLoading || groupsQuery.isLoading;
  const isEmpty =
    !isLoading && view.total === 0 && view.visibleGroups.length === 0;
  const busy =
    archiveLabels.isPending || deleteLabels.isPending || mergeLabels.isPending;

  let empty: ReactNode = null;
  if (labelsQuery.isError || groupsQuery.isError)
    empty = (
      <EmptyState
        title={t("settings.labels.loadFailed")}
        action={
          <Button
            variant="outline"
            onClick={() => {
              void labelsQuery.refetch();
              void groupsQuery.refetch();
            }}
          >
            {t("common.retry")}
          </Button>
        }
      />
    );
  else if (isEmpty) {
    if (needle)
      empty = (
        <EmptyState
          title={t("settings.labels.empty.search")}
          description={t("settings.labels.empty.searchDesc")}
        />
      );
    else if (!canManage)
      empty = spaceId ? (
        <EmptyState
          title={t("spaces.labels.readonlyEmpty")}
          description={t("spaces.labels.readonlyEmptyDesc")}
        />
      ) : (
        <EmptyState title={t("settings.labels.empty.readonly")} />
      );
    else if (scope === "archived")
      empty = <EmptyState title={t("settings.labels.empty.archived")} />;
    else
      empty = (
        <EmptyState
          title={t("settings.labels.empty.none")}
          description={t("settings.labels.empty.noneDesc")}
          action={
            <Button
              leading={<Icon name="plus" />}
              onClick={() => setCreateLabel({})}
            >
              {t("settings.labels.create")}
            </Button>
          }
        />
      );
  }

  const columnCount = canManage ? 7 : 6;

  const labelRow = (label: Label, indented: boolean) => {
    const key = `label:${label.id}`;
    const isEditing = editing?.kind === "label" && editing.id === label.id;
    return (
      <TableRow
        key={key}
        selected={selected.has(key)}
        className="group/row hover:bg-surface-hover"
      >
        {canManage && (
          <TableCell className="w-10 pr-0">
            <Checkbox
              aria-label={t("settings.labels.selectItem", { name: label.name })}
              checked={selected.has(key)}
              onChange={() => toggleSelected(key)}
            />
          </TableCell>
        )}
        <TableCell className={cn(indented && "pl-10")}>
          <div className="flex min-w-0 items-center gap-2">
            <ColorDotPicker
              color={label.color}
              colors={LABEL_COLORS}
              disabled={!canManage}
              label={t("settings.labels.labelColor", { name: label.name })}
              onChange={(color) =>
                updateLabel.mutate({ label, patch: { color } })
              }
            />
            {isEditing && editing.field === "name" ? (
              <InlineEdit
                value={label.name}
                maxLength={50}
                label={t("settings.labels.nameInput")}
                onCommit={(name) =>
                  updateLabel.mutate({ label, patch: { name } })
                }
                onDone={() => setEditing(null)}
              />
            ) : (
              <EditableText
                text={label.name}
                editable={canManage}
                label={t("settings.labels.editName", { name: label.name })}
                onEdit={() =>
                  setEditing({ kind: "label", id: label.id, field: "name" })
                }
                className="font-medium"
              />
            )}
          </div>
        </TableCell>
        <TableCell className="text-fg-secondary">
          {isEditing && editing.field === "description" ? (
            <InlineEdit
              value={label.description}
              maxLength={1000}
              label={t("settings.labels.descriptionInput")}
              onCommit={(description) =>
                updateLabel.mutate({ label, patch: { description } })
              }
              onDone={() => setEditing(null)}
            />
          ) : (
            <EditableText
              text={label.description}
              editable={canManage}
              label={t("settings.labels.editDescription", { name: label.name })}
              onEdit={() =>
                setEditing({
                  kind: "label",
                  id: label.id,
                  field: "description",
                })
              }
            />
          )}
        </TableCell>
        <TableCell className="w-16 text-fg-muted tabular-nums">
          {label.task_count > 0 ? label.task_count : ""}
        </TableCell>
        <TableCell className="w-28 text-fg-muted">
          {label.last_applied_at
            ? formatRelativeTime(label.last_applied_at, locale)
            : ""}
        </TableCell>
        <TableCell className="w-28 text-fg-muted">
          {formatMonthDay(label.created_at, locale)}
        </TableCell>
        <TableCell className="w-12 pl-0 text-right">
          <LabelRowMenu
            label={label}
            canManage={canManage}
            groups={groups}
            mergeCandidates={mergeCandidates}
            onRename={() =>
              setEditing({ kind: "label", id: label.id, field: "name" })
            }
            onViewTasks={() => viewTasks(label)}
            onMoveToGroup={(group_id) =>
              updateLabel.mutate({ label, patch: { group_id } })
            }
            onMerge={(target) =>
              setConfirm({ kind: "merge", labels: [label], target })
            }
            onArchive={() => setConfirm({ kind: "archive", labels: [label] })}
            onUnarchive={() =>
              archiveLabels.mutate({ labels: [label], archived: false })
            }
            onDelete={() => setConfirm({ kind: "delete", labels: [label] })}
          />
        </TableCell>
      </TableRow>
    );
  };

  const groupRow = (group: LabelGroup) => {
    const key = `group:${group.id}`;
    const open = Boolean(needle) || !collapsed.has(group.id);
    const isEditing = editing?.kind === "group" && editing.id === group.id;
    return (
      <TableRow key={key} selected={selected.has(key)}>
        {canManage && (
          <TableCell className="w-10 pr-0">
            <Checkbox
              aria-label={t("settings.labels.selectItem", { name: group.name })}
              checked={selected.has(key)}
              onChange={() => toggleSelected(key)}
            />
          </TableCell>
        )}
        <TableCell>
          <div className="flex min-w-0 items-center gap-1.5">
            <button
              type="button"
              aria-expanded={open}
              aria-label={t(
                open
                  ? "settings.labels.collapseGroup"
                  : "settings.labels.expandGroup",
                { name: group.name },
              )}
              onClick={() => toggleCollapsed(group.id)}
              className="-ml-1 flex size-5 items-center justify-center rounded-md text-fg-muted hover:bg-surface-muted"
            >
              <Icon
                name="chevron-right"
                size={14}
                className={cn("transition-transform", open && "rotate-90")}
              />
            </button>
            <ColorDotPicker
              glyph="group"
              color={group.color}
              colors={LABEL_COLORS}
              disabled={!canManage}
              label={t("settings.labels.groupColor", { name: group.name })}
              onChange={(color) =>
                updateGroup.mutate({ group, patch: { color } })
              }
            />
            {isEditing && editing.field === "name" ? (
              <InlineEdit
                value={group.name}
                maxLength={50}
                label={t("settings.labels.groupNameInput")}
                onCommit={(name) =>
                  updateGroup.mutate({ group, patch: { name } })
                }
                onDone={() => setEditing(null)}
              />
            ) : (
              <EditableText
                text={group.name}
                editable={canManage}
                label={t("settings.labels.editName", { name: group.name })}
                onEdit={() =>
                  setEditing({ kind: "group", id: group.id, field: "name" })
                }
                className="font-medium"
              />
            )}
          </div>
        </TableCell>
        <TableCell className="text-fg-secondary">
          {isEditing && editing.field === "description" ? (
            <InlineEdit
              value={group.description}
              maxLength={1000}
              label={t("settings.labels.groupDescriptionInput")}
              onCommit={(description) =>
                updateGroup.mutate({ group, patch: { description } })
              }
              onDone={() => setEditing(null)}
            />
          ) : (
            <EditableText
              text={group.description}
              editable={canManage}
              label={t("settings.labels.editDescription", { name: group.name })}
              onEdit={() =>
                setEditing({
                  kind: "group",
                  id: group.id,
                  field: "description",
                })
              }
            />
          )}
        </TableCell>
        <TableCell className="w-16 text-fg-muted">-</TableCell>
        <TableCell className="w-28 text-fg-muted">-</TableCell>
        <TableCell className="w-28 text-fg-muted">-</TableCell>
        <TableCell className="w-12 pl-0 text-right">
          {canManage && (
            <GroupRowMenu
              group={group}
              onAddLabel={() => setCreateLabel({ groupId: group.id })}
              onRename={() =>
                setEditing({ kind: "group", id: group.id, field: "name" })
              }
              onDelete={() => deleteGroup.mutate(group)}
            />
          )}
        </TableCell>
      </TableRow>
    );
  };

  return (
    <div className="mx-auto flex max-w-[1004px] flex-col gap-5 px-12 pt-10 pb-16">
      <ConfigPageHeader
        title={t("settings.labels.title")}
        subtitle={
          spaceId ? t("spaces.labels.subtitle") : t("settings.labels.subtitle")
        }
        action={
          canManage && (
            <Button
              leading={<Icon name="plus" />}
              onClick={() => setCreateLabel({})}
            >
              {t("settings.labels.create")}
            </Button>
          )
        }
      />
      <div className="flex items-center gap-2">
        <TextInput
          type="search"
          className="w-[300px]"
          aria-label={t("settings.labels.search")}
          placeholder={t("settings.labels.search")}
          leading={<Icon name="search" />}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <Select
          aria-label={t("settings.labels.scopeLabel")}
          className="w-[120px]"
          value={scope}
          onChange={setScope}
          options={[
            {
              value: "organization",
              label: spaceId
                ? t("spaces.labels.scopeSpace")
                : t("settings.labels.scope.organization"),
            },
            { value: "archived", label: t("settings.labels.scope.archived") },
          ]}
        />
        <span className="flex-1" />
        {canManage && (
          <Button
            variant="outline"
            leading={<Icon name="plus" />}
            onClick={() => setCreateGroup(true)}
          >
            {t("settings.labels.createGroup")}
          </Button>
        )}
      </div>

      {isLoading ? (
        <ListSkeleton label={t("settings.labels.loading")} />
      ) : empty ? (
        empty
      ) : (
        <div className="overflow-x-auto">
          <Table
            aria-label={t("settings.labels.title")}
            className="min-w-[760px] table-fixed"
          >
            <TableHead>
              <TableRow>
                {canManage && (
                  <TableHeaderCell className="w-10">
                    <span className="sr-only">
                      {t("settings.labels.columns.select")}
                    </span>
                  </TableHeaderCell>
                )}
                <TableHeaderCell className="w-[32%]">
                  {t("settings.labels.columns.name")}
                </TableHeaderCell>
                <TableHeaderCell>
                  {t("settings.labels.columns.description")}
                </TableHeaderCell>
                <TableHeaderCell className="w-16">
                  {t("settings.labels.columns.tasks")}
                </TableHeaderCell>
                <TableHeaderCell className="w-28">
                  {t("settings.labels.columns.lastApplied")}
                </TableHeaderCell>
                <TableHeaderCell className="w-28">
                  {t("settings.labels.columns.createdAt")}
                </TableHeaderCell>
                <TableHeaderCell className="w-12">
                  <span className="sr-only">
                    {t("settings.labels.columns.actions")}
                  </span>
                </TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {view.visibleGroups.flatMap((group) => {
                const open = Boolean(needle) || !collapsed.has(group.id);
                const children = view.children.get(group.id) ?? [];
                return [
                  groupRow(group),
                  ...(open
                    ? children.map((label) => labelRow(label, true))
                    : []),
                ];
              })}
              {view.ungrouped.map((label) => labelRow(label, false))}
              {view.total === 0 && (
                <TableRow>
                  <TableCell
                    colSpan={columnCount}
                    className="text-center text-fg-muted"
                  >
                    {t(
                      needle
                        ? "settings.labels.empty.search"
                        : scope === "archived"
                          ? "settings.labels.empty.archived"
                          : "settings.labels.empty.none",
                    )}
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </div>
      )}

      {canManage && selected.size > 0 && (
        <div
          role="toolbar"
          aria-label={t("settings.labels.bulk.bar")}
          className="sticky bottom-4 mx-auto flex items-center gap-1.5 rounded-xl border border-line-default bg-surface-card py-1.5 pr-1.5 pl-3.5 shadow-popover"
        >
          <span className="pr-2 text-body text-fg-secondary">
            {t("settings.labels.bulk.selected", { count: selected.size })}
          </span>
          {allArchived ? (
            <Button
              variant="outline"
              size="28"
              disabled={busy}
              onClick={() =>
                archiveLabels.mutate({
                  labels: selectedLabels,
                  archived: false,
                })
              }
            >
              {t("settings.labels.bulk.unarchive")}
            </Button>
          ) : (
            <Button
              variant="outline"
              size="28"
              disabled={busy || selectedLabels.length === 0}
              onClick={() =>
                setConfirm({
                  kind: "archive",
                  labels: selectedLabels.filter((label) => !label.archived_at),
                })
              }
            >
              {t("settings.labels.bulk.archive")}
            </Button>
          )}
          <BulkMergeButton
            sources={selectedLabels}
            candidates={mergeCandidates}
            onMerge={(target) =>
              setConfirm({
                kind: "merge",
                labels: selectedLabels.filter(
                  (label) => label.id !== target.id,
                ),
                target,
              })
            }
          >
            {({ ref, toggle, open }) => (
              <Button
                ref={ref}
                variant="outline"
                size="28"
                aria-expanded={open}
                disabled={busy || selectedLabels.length === 0}
                onClick={toggle}
              >
                {t("settings.labels.bulk.merge")}
              </Button>
            )}
          </BulkMergeButton>
          {!hasGroupSelected && (
            <Button
              variant="danger-outline"
              size="28"
              disabled={busy || selectedLabels.length === 0}
              onClick={() =>
                setConfirm({ kind: "delete", labels: selectedLabels })
              }
            >
              {t("settings.labels.bulk.delete")}
            </Button>
          )}
          <IconButton
            label={t("settings.labels.bulk.clear")}
            icon={<Icon name="x" />}
            size="28"
            onClick={() => setSelected(new Set())}
          />
        </div>
      )}

      {createLabel && (
        <CreateLabelDialog
          orgId={orgId}
          spaceId={spaceId}
          groups={groups}
          initialGroupId={createLabel.groupId}
          onClose={() => setCreateLabel(null)}
          onCreated={(label) => {
            setCreateLabel(null);
            setPinned((prev) => [label.id, ...prev]);
            queryClient.setQueryData<Label[]>(orgLabelsKey, (list) =>
              list
                ? [...list.filter((item) => item.id !== label.id), label]
                : [label],
            );
            if (scope !== "organization") setScope("organization");
            toast(t("settings.labels.toast.created", { name: label.name }));
          }}
        />
      )}
      {createGroup && (
        <CreateGroupDialog
          orgId={orgId}
          spaceId={spaceId}
          onClose={() => setCreateGroup(false)}
          onCreated={(group) => {
            setCreateGroup(false);
            setPinned((prev) => [group.id, ...prev]);
            queryClient.setQueryData<LabelGroup[]>(groupsKey, (list) =>
              list
                ? [...list.filter((item) => item.id !== group.id), group]
                : [group],
            );
            if (scope !== "organization") setScope("organization");
            toast(
              t("settings.labels.toast.groupCreated", { name: group.name }),
            );
          }}
        />
      )}
      {confirm && (
        <LabelConfirmDialog
          confirm={confirm}
          busy={busy}
          onCancel={() => setConfirm(null)}
          onArchive={(targets) =>
            archiveLabels.mutate({ labels: targets, archived: true })
          }
          onDelete={(targets) => deleteLabels.mutate(targets)}
          onMerge={(sources, target) =>
            mergeLabels.mutate({ labels: sources, target })
          }
        />
      )}
    </div>
  );
}

/** 可点击进入编辑的文字；只读时为普通文本。 */
function EditableText({
  text,
  editable,
  label,
  onEdit,
  className,
}: {
  text: string;
  editable: boolean;
  label: string;
  onEdit: () => void;
  className?: string;
}) {
  if (!editable)
    return <span className={cn("min-w-0 truncate", className)}>{text}</span>;
  return (
    <button
      type="button"
      aria-label={label}
      onClick={onEdit}
      className={cn(
        "min-h-6 min-w-8 max-w-full truncate rounded-md px-1 -mx-1 text-left hover:bg-surface-muted",
        className,
      )}
    >
      {text}
    </button>
  );
}
