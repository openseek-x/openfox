import { useEffect, useRef, useState } from "react";
import {
  useLocation,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { SpaceDetail, TriageTask } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Avatar,
  Button,
  Icon,
  IconButton,
  Popover,
  Spinner,
  Skeleton,
  cn,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import { useMe } from "../agents/queries";
import { useCreateTaskDialog } from "../tasks/create/CreateTaskProvider";
import { TaskDetailView } from "../tasks/detail/TaskDetailDrawer";
import { useIsNarrow } from "../tasks/detail/useIsNarrow";
import { useOpenTaskPage } from "../tasks/navigation";
import { displayIdOf } from "../tasks/parts";
import { TaskTypeIcon } from "../tasks/primitives/TaskTypeIcon";
import { formatRelativeTime, formatMonthDay } from "../tasks/primitives/dates";
import { useOrgRole, useSpaceDetail } from "../spaces/queries";
import { canWriteTasksIn } from "../spaces/readonly";
import { useProjects } from "../projects/queries";
import { useIterations } from "../iterations/queries";
import {
  FilterButton,
  FilterChips,
  type FilterOptions,
} from "../tasks/filters/Filters";
import { useFilterOptions } from "../tasks/filters/options";
import {
  filtersToQuery,
  queryToConditions,
  type FilterCondition,
  type FilterQuery,
  type CustomFilterField,
} from "../tasks/model";
import {
  usePeople,
  usePickerLabels,
  useSpaceAgents,
  useSpaceTypes,
  useTaskByDisplayId,
  useStatuses,
  useAgentDirectory,
} from "../tasks/queries";
import {
  useTriageList,
  useTriageCount,
  useTriageSettings,
  triageErrorMessage,
} from "./queries";
import { TriageState, QueueIllustration, TriageIcon } from "./TriageState";
import { useTriageDisplay, TriageDisplay } from "./TriageDisplay";

export function TriagePage() {
  const t = useT(),
    navigate = useNavigate();
  const { orgId = "", spaceId = "" } = useParams();
  const detail = useSpaceDetail(orgId, spaceId),
    me = useMe();
  if (detail.isLoading || me.isLoading)
    return (
      <div className="m-auto">
        <Spinner />
      </div>
    );
  if (!detail.data || !me.data)
    return (
      <div className="m-auto text-fg-muted">
        {t("spaces.triage.loadFailed")}
      </div>
    );
  if (!detail.data.features.triage)
    return (
      <TriageState
        media={
          <span className="opacity-65">
            <TriageIcon size={40} />
          </span>
        }
        title={t("spaces.triage.title")}
        description={t("spaces.triage.disabledDescription")}
        notice={t("spaces.triage.disabledTitle")}
        action={
          <Button
            size="28"
            onClick={() =>
              navigate(`/o/${orgId}/settings/spaces/${spaceId}/triage`)
            }
          >
            {t("spaces.triage.enable")}
          </Button>
        }
      />
    );
  return (
    <Queue
      key={`${orgId}:${spaceId}:${me.data.id}`}
      orgId={orgId}
      space={detail.data}
      user={me.data.id}
    />
  );
}
function Queue({
  orgId,
  space,
  user,
}: {
  orgId: string;
  space: SpaceDetail;
  user: string;
}) {
  const spaceId = space.id,
    t = useT(),
    api = useApi(),
    queryClient = useQueryClient(),
    toast = useToast();
  const navigate = useNavigate(),
    location = useLocation(),
    openPage = useOpenTaskPage();
  const [params, setParams] = useSearchParams();
  const selected = params.get("task")?.toUpperCase() || null;
  const selectedTask = useTaskByDisplayId(orgId, selected);
  const [processed, setProcessed] = useState<TriageTask[]>([]);
  const filterQuery: FilterQuery = {};
  for (const key of new Set(params.keys()))
    filterQuery[key] = params.getAll(key);
  const conditions = queryToConditions(filterQuery);
  function setConditions(next: FilterCondition[]) {
    setProcessed([]);
    setParams(
      (previous) => {
        const out = new URLSearchParams(previous);
        for (const key of Object.keys(filtersToQuery(conditions)))
          out.delete(key);
        for (const [key, values] of Object.entries(filtersToQuery(next)))
          for (const value of [values].flat()) out.append(key, value);
        out.delete("task");
        return out;
      },
      { replace: true },
    );
  }
  function select(id: string | null) {
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        if (id) next.set("task", id);
        else next.delete("task");
        return next;
      },
      { replace: true },
    );
  }
  const display = useTriageDisplay(user, orgId, spaceId);
  const types = useSpaceTypes(orgId, spaceId),
    statuses = useStatuses(orgId),
    people = usePeople(orgId, space),
    agents = useSpaceAgents(orgId, spaceId),
    directory = useAgentDirectory(orgId),
    labels = usePickerLabels(orgId, spaceId);
  const projects = useProjects(orgId, spaceId, {
    enabled: space.features.projects,
  });
  const iterations = useIterations(
    orgId,
    spaceId,
    { status: "planned,active" },
    space.features.iterations,
  );
  const coreOptions = useFilterOptions({
    statuses: statuses.data ?? [],
    types: types.data ?? [],
    labels: labels.data ?? [],
    people,
    agents: (agents.data ?? []).filter((item) => !item.archived_at),
    crews: (directory.data?.entries ?? []).filter(
      (item) =>
        item.type === "crew" && item.space_id === spaceId && !item.archived_at,
    ),
    iterations: iterations.data?.iterations ?? [],
    projects: projects.data?.projects ?? [],
  });
  const fieldsQuery = useQuery({
    queryKey: [...keys.customFields(orgId), spaceId],
    queryFn: () => api.customFields.list(orgId, false, spaceId),
  });
  const customFields = (fieldsQuery.data ?? []).filter((f) => !f.archived_at);
  const options: FilterOptions = {
    ...coreOptions,
    triage: {
      key: [...keys.triage(orgId, spaceId), display.value.snoozed],
      conditions,
      load: (field, q) =>
        api.triage.candidates(orgId, spaceId, field, {
          ...q,
          include_snoozed: display.value.snoozed,
        }),
    },
    customNames: Object.fromEntries(
      customFields.map((f) => ["cf_" + f.id, f.name]),
    ),
  };
  options.subscriber = [
    {
      options: [
        ...people.map((p) => ({ value: p.id, label: p.email })),
        ...(agents.data ?? [])
          .filter((a) => !a.archived_at)
          .map((a) => ({ value: a.id, label: a.name })),
      ],
    },
  ];
  options.relation = [
    {
      options: (
        ["parent", "subtask", "blocked", "blocking", "duplicate"] as const
      ).map((value) => ({
        value,
        label: t(`spaces.triage.relationValues.${value}`),
      })),
    },
  ];
  for (const f of customFields) {
    const key: CustomFilterField = `cf_${f.id}`;
    if (f.kind === "SELECT" || f.kind === "MULTI_SELECT")
      options[key] = [
        {
          options: f.options
            .filter((o) => !o.archived_at)
            .map((o) => ({ value: o.id, label: o.label })),
        },
      ];
    if (f.kind === "BOOLEAN")
      options[key] = [
        {
          options: ["true", "false"].map((v) => ({
            value: v,
            label: t(
              v === "true"
                ? "settings.customFields.boolean.true"
                : "settings.customFields.boolean.false",
            ),
          })),
        },
      ];
    if (f.kind === "MEMBER")
      options[key] = [
        {
          options: [
            ...(f.member_scope === "AGENTS"
              ? []
              : people.map((p) => ({ value: p.id, label: p.email }))),
            ...(f.member_scope === "HUMANS"
              ? []
              : (agents.data ?? []).map((a) => ({
                  value: a.id,
                  label: a.name,
                }))),
          ],
        },
      ];
  }
  const queue = useTriageList(orgId, spaceId, {
    ...filtersToQuery(conditions),
    sort_mode: "workbench",
    sort: display.value.sort === "entered" ? "created" : display.value.sort,
    order: display.value.order,
    include_snoozed: display.value.snoozed,
    limit: 100,
  });
  const pendingTasks = queue.data?.pages.flatMap((page) => page.tasks) ?? [],
    total = queue.data?.pages[0]?.total ?? 0;
  const retained = processed.filter(
    (row) => !pendingTasks.some((item) => item.id === row.id),
  );
  const tasks = retained.length
    ? [...pendingTasks, ...retained].sort((a, b) => {
        const { sort, order } = display.value;
        let delta = 0;
        if (sort === "due") {
          if (!a.due_date && b.due_date) return 1;
          if (a.due_date && !b.due_date) return -1;
          delta = (a.due_date ?? "").localeCompare(b.due_date ?? "");
        } else if (sort === "priority") {
          const priorities = [
            "NONE",
            "URGENT",
            "HIGH",
            "NORMAL",
            "LOW",
            "LOWEST",
          ];
          delta =
            priorities.indexOf(a.priority) - priorities.indexOf(b.priority);
        } else delta = Date.parse(a.created_at) - Date.parse(b.created_at);
        return (order === "asc" ? delta : -delta) || a.id.localeCompare(b.id);
      })
    : pendingTasks;
  const wrongSpace = Boolean(
    selectedTask.data && selectedTask.data.space_id !== spaceId,
  );
  useEffect(() => {
    if (wrongSpace)
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          next.delete("task");
          return next;
        },
        { replace: true },
      );
  }, [wrongSpace, setParams]);
  const filtered = conditions.length > 0 && !total;
  const count = useTriageCount(orgId, spaceId);
  const nextWake = count.data?.next_snooze_at;
  useEffect(() => {
    if (!nextWake) return;
    const timer = setTimeout(
      () =>
        void queryClient.invalidateQueries({
          queryKey: keys.triage(orgId, spaceId),
        }),
      Math.min(
        2147483647,
        Math.max(500, Date.parse(nextWake) - Date.now() + 50),
      ),
    );
    return () => clearTimeout(timer);
  }, [nextWake, count.dataUpdatedAt, queryClient, orgId, spaceId]);
  const settings = useTriageSettings(orgId, spaceId);
  const orgRole = useOrgRole(orgId),
    { openCreate } = useCreateTaskDialog();
  const canCreate =
    space.status === "active" &&
    canWriteTasksIn(space, orgRole) &&
    statuses.data?.some((s) => s.category === "TRIAGE" && !s.archived_at);
  const create = () => {
    if (canCreate)
      openCreate({ spaceId, submitToTriage: true, openAfterCreate: true });
  };
  const favorite = useMutation({
    mutationFn: () =>
      api.triage.setFavorite(orgId, spaceId, !space.triage_favorited),
    onSuccess: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: keys.space(orgId, spaceId) }),
        queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) }),
      ]),
    onError: (e) => toast(triageErrorMessage(t, e), { tone: "error" }),
  });
  const root = useRef<HTMLDivElement>(null),
    narrow = useIsNarrow(root, 1001);
  useEffect(() => {
    root.current
      ?.querySelector<HTMLElement>('ul button[aria-pressed="true"]')
      ?.scrollIntoView?.({ block: "nearest" });
  }, [selected]);
  const [width, setWidth] = useState(420);
  const clamp = (w: number) =>
    Math.max(280, Math.min(520, (root.current?.clientWidth || 1440) - 720, w));
  const selectRelative = (delta: number) => {
    const i = tasks.findIndex((task) => displayIdOf(task) === selected);
    const index =
      i < 0
        ? delta > 0
          ? 0
          : tasks.length - 1
        : Math.max(0, Math.min(tasks.length - 1, i + delta));
    if (tasks[index]) select(displayIdOf(tasks[index]));
  };
  const shortcut = useRef({
    selectRelative,
    create,
    selected,
    close: () => select(null),
  });
  shortcut.current = {
    selectRelative,
    create,
    selected,
    close: () => select(null),
  };
  useEffect(() => {
    const handle = (e: KeyboardEvent) => {
      if (
        e.defaultPrevented ||
        e.ctrlKey ||
        e.altKey ||
        e.metaKey ||
        e.shiftKey ||
        document.querySelector(
          '[role="dialog"],[role="menu"],[role="listbox"]',
        ) ||
        (e.target instanceof HTMLElement &&
          e.target.closest(
            "input,textarea,select,[contenteditable=true],[role=textbox]",
          ))
      )
        return;
      const s = shortcut.current;
      switch (e.key.toLowerCase()) {
        case "j":
          s.selectRelative(1);
          break;
        case "k":
          s.selectRelative(-1);
          break;
        case "escape":
          if (!s.selected) return;
          s.close();
          break;
        case "c":
          s.create();
          break;
        default:
          return;
      }
      e.preventDefault();
      e.stopPropagation();
    };
    document.addEventListener("keydown", handle, true);
    return () => document.removeEventListener("keydown", handle, true);
  }, []);
  const showList =
    !narrow ||
    (!selected &&
      (tasks.length > 0 || filtered || queue.isLoading || queue.isError));
  const showDetail = !narrow || !showList;
  const cleared = () => setConditions([]);
  const state = filtered ? (
    <TriageState
      center
      media={<Icon name="list-filter" size={32} />}
      title={t("spaces.triage.filtered")}
      description={
        count.data
          ? t("spaces.triage.hidden", {
              count:
                count.data.total -
                (display.value.snoozed ? 0 : count.data.snoozed),
            })
          : ""
      }
      action={
        <Button variant="outline" size="28" onClick={cleared}>
          {t("tasks.filters.clear")}
        </Button>
      }
    />
  ) : (
    <TriageState
      media={<QueueIllustration />}
      title={
        total
          ? t("spaces.triage.pending", { count: total })
          : t("spaces.triage.empty")
      }
      description={t("spaces.triage.emptyDescription")}
      action={
        <Button
          size="28"
          aria-label={t("spaces.triage.create")}
          disabled={!canCreate}
          onClick={create}
        >
          {t("spaces.triage.create")}{" "}
          <kbd aria-hidden className="ml-1 rounded bg-white/20 px-1">
            C
          </kbd>
        </Button>
      }
    />
  );
  const responsibilityAnchor = useRef<HTMLButtonElement>(null);
  const [hoverMembers, setHoverMembers] = useState(false);
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | undefined>(
    undefined,
  );
  const delayMembers = (show: boolean) => {
    clearTimeout(hoverTimer.current);
    hoverTimer.current = setTimeout(
      () => setHoverMembers(show),
      show ? 200 : 100,
    );
  };
  useEffect(() => () => clearTimeout(hoverTimer.current), []);
  const responsibility = settings.data,
    members =
      responsibility?.action !== "none" ? (responsibility?.members ?? []) : [];
  const canConfigure =
    space.can_manage && space.status === "active" && responsibility?.can_manage;
  return (
    <div
      ref={root}
      className="relative flex h-full min-h-0 min-w-0 flex-1 overflow-hidden"
    >
      {showList && (
        <section
          aria-label={t("spaces.triage.queue")}
          className={cn(
            "flex min-h-0 min-w-0 flex-col",
            narrow ? "flex-1" : "shrink-0",
          )}
          style={
            narrow
              ? undefined
              : { width: `clamp(280px, min(${width}px, 100% - 720px), 520px)` }
          }
        >
          <header className="flex h-11 shrink-0 items-center gap-1 border-b border-line-soft px-3">
            <h1 className="shrink-0 text-body font-semibold">
              {t("spaces.triage.title")}
            </h1>
            <span
              aria-label={t("spaces.triage.pending", { count: total })}
              className="text-caption tabular-nums text-fg-muted"
            >
              {total}
            </span>
            <IconButton
              size="28"
              aria-pressed={!!space.triage_favorited}
              disabled={favorite.isPending}
              label={t(
                space.triage_favorited
                  ? "spaces.triage.removeFavorite"
                  : "spaces.triage.addFavorite",
              )}
              icon={
                <Icon
                  name="star"
                  size={14}
                  className={
                    space.triage_favorited
                      ? "fill-current text-warning"
                      : undefined
                  }
                />
              }
              onClick={() => favorite.mutate()}
            />
            <div className="ml-auto flex items-center gap-1">
              {(canConfigure || members.length > 0) && (
                <button
                  type="button"
                  ref={responsibilityAnchor}
                  aria-label={t(
                    !members.length
                      ? "spaces.triage.configure"
                      : responsibility?.action === "notify"
                        ? "spaces.triage.membersNotify"
                        : "spaces.triage.membersAssign",
                  )}
                  title={
                    canConfigure ? t("spaces.triage.configure") : undefined
                  }
                  aria-haspopup={members.length ? "dialog" : undefined}
                  onMouseEnter={() => delayMembers(true)}
                  onMouseLeave={() => delayMembers(false)}
                  onFocus={() => delayMembers(true)}
                  onBlur={() => delayMembers(false)}
                  onClick={() =>
                    canConfigure &&
                    navigate(
                      `/o/${orgId}/settings/spaces/${spaceId}/triage#triage-responsibility`,
                    )
                  }
                  className="flex h-7 min-w-7 items-center justify-center rounded-full text-fg-muted hover:bg-surface-hover"
                >
                  {members.length ? (
                    <span className="flex -space-x-1">
                      {members.slice(0, 4).map((m) => (
                        <Avatar key={m.id} name={m.email} size={24} />
                      ))}
                      {members.length > 4 && <span>+{members.length - 4}</span>}
                    </span>
                  ) : (
                    <span className="flex size-[18px] items-center justify-center rounded-full border border-dashed border-current">
                      <Icon name="user" size={12} />
                    </span>
                  )}
                </button>
              )}
              {members.length > 0 && (
                <Popover
                  open={hoverMembers}
                  onOpenChange={setHoverMembers}
                  anchorRef={responsibilityAnchor}
                  placement="bottom-end"
                  className="w-72 p-0"
                >
                  <div
                    onMouseEnter={() => delayMembers(true)}
                    onMouseLeave={() => delayMembers(false)}
                  >
                    <div className="border-b border-line-soft px-3 py-2.5 text-body font-medium">
                      {t(
                        responsibility?.action === "notify"
                          ? "spaces.triage.membersNotify"
                          : "spaces.triage.membersAssign",
                      )}
                    </div>
                    <div className="max-h-72 overflow-y-auto p-2">
                      {members.map((m) => (
                        <div
                          key={m.id}
                          className="flex min-h-11 items-center gap-3 px-1 py-2"
                        >
                          <Avatar name={m.email} size={28} />
                          <span className="truncate text-body">{m.email}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                </Popover>
              )}
              <FilterButton
                fields={[
                  "priority",
                  "type",
                  "assignee",
                  "crew",
                  "subscriber",
                  "label",
                  "estimate",
                  "content",
                  "relation",
                  "date",
                  "creator",
                  "project",
                  "iteration",
                  ...customFields.map((f) => `cf_${f.id}` as CustomFilterField),
                ]}
                conditions={conditions}
                onChange={setConditions}
                options={options}
              />
              <TriageDisplay {...display} />
              <IconButton
                label={t("spaces.triage.create")}
                icon={<Icon name="plus" />}
                disabled={!canCreate}
                onClick={create}
              />
            </div>
          </header>
          <FilterChips
            conditions={conditions}
            onChange={setConditions}
            options={options}
          />
          {queue.isError ? (
            <Button variant="ghost-muted" onClick={() => void queue.refetch()}>
              {t("common.retry")}
            </Button>
          ) : queue.isLoading ? (
            <div aria-busy="true">
              {Array.from({ length: 6 }, (_, i) => (
                <div
                  key={i}
                  className="mx-2 my-1 flex h-16 flex-col justify-center gap-2 px-3"
                >
                  <Skeleton className="h-4 w-2/3" />
                  <Skeleton className="h-3 w-24" />
                </div>
              ))}
            </div>
          ) : (
            <ul
              className="min-h-0 overflow-y-auto"
              tabIndex={0}
              onKeyDown={(event) => {
                if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                  event.preventDefault();
                  selectRelative(event.key === "ArrowDown" ? 1 : -1);
                }
              }}
              onScroll={(e) => {
                const node = e.currentTarget;
                if (
                  queue.hasNextPage &&
                  !queue.isFetchingNextPage &&
                  node.scrollHeight - node.scrollTop - node.clientHeight < 80
                )
                  void queue.fetchNextPage();
              }}
            >
              {tasks.map((task) => (
                <TriageRow
                  key={task.id}
                  task={task}
                  processed={processed.some((row) => row.id === task.id)}
                  icon={types.data?.find((ty) => ty.id === task.type_id)?.icon}
                  selected={selected === displayIdOf(task)}
                  onClick={() => select(displayIdOf(task))}
                  showId={display.value.identifier}
                  showDue={display.value.dueDate}
                />
              ))}
              {queue.hasNextPage && (
                <li>
                  <Button
                    variant="ghost-muted"
                    disabled={queue.isFetchingNextPage}
                    onClick={() => void queue.fetchNextPage()}
                  >
                    {t("tasks.loadMore")}
                  </Button>
                </li>
              )}
            </ul>
          )}
          {filtered && narrow && state}
        </section>
      )}
      {!narrow && (
        <div
          role="separator"
          aria-label={t("spaces.triage.resize")}
          aria-orientation="vertical"
          aria-valuemin={280}
          aria-valuemax={520}
          aria-valuenow={clamp(width)}
          tabIndex={0}
          className="relative z-10 w-px shrink-0 cursor-col-resize touch-none bg-line-soft before:absolute before:inset-y-0 before:-inset-x-1 focus-visible:bg-fg-muted"
          onPointerDown={(e) => {
            e.preventDefault();
            e.currentTarget.setPointerCapture(e.pointerId);
          }}
          onPointerMove={(e) => {
            if (e.currentTarget.hasPointerCapture(e.pointerId) && root.current)
              setWidth(
                clamp(e.clientX - root.current.getBoundingClientRect().left),
              );
          }}
          onDoubleClick={() => setWidth(420)}
          onKeyDown={(e) => {
            if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
              e.preventDefault();
              setWidth(
                clamp(clamp(width) + (e.key === "ArrowLeft" ? -16 : 16)),
              );
            }
          }}
        />
      )}
      {showDetail && (
        <section className="flex min-h-0 min-w-0 flex-1 flex-col">
          {selected && !wrongSpace ? (
            <TaskDetailView
              key={selected}
              orgId={orgId}
              displayId={selected}
              onOpenTask={(id) => openPage(id)}
              onDeleted={() => select(null)}
              onExpand={(id) => openPage(id)}
              expandLabel={t("tasks.detail.expand")}
              expandIcon="maximize-2"
              triageWorkbench={{
                onProcessed: () => {
                  const row = pendingTasks.find(
                    (item) => displayIdOf(item) === selected,
                  );
                  if (row)
                    setProcessed((items) =>
                      items.some((i) => i.id === row.id)
                        ? items
                        : [...items, row],
                    );
                },
                onSnoozed: () => {
                  if (!display.value.snoozed) select(null);
                },
              }}
              rejectReplacement={location.pathname}
              frame={({ title, actions, children }) => (
                <>
                  <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-soft px-3">
                    {narrow && (
                      <IconButton
                        label={t("common.back")}
                        icon={<Icon name="chevron-left" />}
                        onClick={() => select(null)}
                      />
                    )}
                    <span
                      className={cn(
                        "min-w-0 flex-1 truncate text-body font-medium",
                        processed.some(
                          (item) => displayIdOf(item) === selected,
                        ) && "text-fg-muted",
                      )}
                    >
                      {title}
                    </span>
                    {actions}
                  </header>
                  <div className="relative min-h-0 flex-1">{children}</div>
                </>
              )}
            />
          ) : queue.isLoading ? (
            <Spinner className="m-auto" />
          ) : (
            !queue.isError && state
          )}
        </section>
      )}
    </div>
  );
}
function TriageRow({
  task,
  icon,
  selected,
  onClick,
  showId,
  showDue,
  processed,
}: {
  processed: boolean;
  task: TriageTask;
  icon?: string | null;
  selected: boolean;
  onClick: () => void;
  showId: boolean;
  showDue: boolean;
}) {
  const t = useT(),
    { locale } = useI18n();
  const owner = task.assignees[0],
    until = task.triage.snoozed_until;
  return (
    <li>
      <button
        type="button"
        aria-pressed={selected}
        className={cn(
          "mx-2 my-1 flex h-[var(--density-triage-row,64px)] w-[calc(100%-1rem)] min-w-0 flex-col justify-center rounded-lg px-3 text-left hover:bg-surface-hover",
          selected && "bg-surface-active",

          until && Date.parse(until) > Date.now() && "opacity-70",
        )}
        onClick={onClick}
      >
        <span className="flex w-full items-center gap-1.5">
          <TaskTypeIcon icon={icon} size={14} />
          <span
            className={cn(
              "min-w-0 flex-1 truncate text-body font-medium",
              processed && "text-fg-muted",
            )}
          >
            {task.title}
          </span>
          {showId && (
            <span className="shrink-0 text-caption tabular-nums text-fg-muted">
              {displayIdOf(task)}
            </span>
          )}
        </span>
        <span className="mt-1.5 flex w-full items-center gap-1.5 text-caption text-fg-muted">
          {owner ? (
            <>
              <Avatar name={owner.name} size={16} />
              <span className="truncate">{owner.name}</span>·
            </>
          ) : (
            <span className="size-[18px] shrink-0 rounded-full border border-line-default bg-surface-muted" />
          )}
          <span>{formatRelativeTime(task.created_at, locale)}</span>
          {showDue && task.due_date && (
            <span className="inline-flex items-center gap-1">
              · <Icon name="calendar" size={12} />
              {formatMonthDay(task.due_date, locale)}
            </span>
          )}
          {until && Date.parse(until) > Date.now() && (
            <span className="ml-auto inline-flex min-w-0 items-center gap-1 truncate">
              <Icon name="clock" size={12} />
              {t("spaces.triage.snoozedUntil", {
                time: formatRelativeTime(until, locale),
              })}
            </span>
          )}
        </span>
      </button>
    </li>
  );
}
