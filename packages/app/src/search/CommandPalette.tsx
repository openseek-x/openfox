import { useQueries, useQueryClient } from "@tanstack/react-query";
import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { useLocation, useNavigate } from "react-router-dom";

import type { SearchItem, SearchType } from "@openfox/api";
import { useT, useI18n, type MessageKey } from "@openfox/i18n";
import {
  Icon,
  IconButton,
  Avatar,
  ConfirmDialog,
  Spinner,
  cn,
  useModalBehavior,
  useToast,
  type IconName,
} from "@openfox/ui";

import { AgentAvatar, PersonAvatar } from "../agents/AgentAvatar";
import { SpaceIcon } from "../spaces/SpaceIcon";
import { ViewIcon } from "../views/ViewIcon";
import { rememberSpace, useContacts, useMe } from "../agents/queries";
import { keys, useApi } from "../context";
import { isOrgAdmin, useOrgRole, useSpaceList } from "../spaces/queries";
import { useCreateTaskDialog } from "../tasks/create/CreateTaskProvider";
import type { Person } from "../tasks/queries";
import { formatRelativeTime } from "../tasks/primitives/dates";
import { useTaskParam } from "../tasks/parts";
import {
  PALETTE_CATEGORIES,
  SEARCH_DEBOUNCE_MS,
  SEARCH_LIMIT_MIXED,
  SEARCH_LIMIT_SINGLE,
  SEARCH_MAX_LENGTH,
  SEARCH_TYPES,
  resultPath,
  type PaletteCategory,
  type PaletteFilter,
} from "./model";

import { writeLocal } from "../storage";
import { searchConversationPath } from "./conversationResult";
import {
  ALL_SPACES,
  readSearchHistory,
  readSearchScope,
  recordSearch,
  type SearchHistoryEntry,
  type SpaceScope,
} from "./history";
import {
  SearchTaskFilters,
  searchTaskFilterParams,
  type SearchTaskCondition,
} from "./SearchTaskFilters";
import { SearchSpaceScope } from "./SearchSpaceScope";
import { useSearchResults } from "./useSearchResults";

const ICONS: Record<PaletteCategory | "message" | "more", IconName> = {
  task: "list-checks",
  project: "folder-kanban",
  view: "layout-grid",
  agent: "bot",
  person: "users",
  space: "layers",
  conversation: "messages",
  message: "message",
  more: "chevron-down",
  command: "keyboard",
};

const SUGGEST_LIMIT = 5;

interface Row {
  key: string;
  category: keyof typeof ICONS;
  more?: boolean;
  icon?: ReactNode;
  title: string;
  subtitle?: string;
  occurredAt?: string;
  badge?: string;
  run: () => void;
}

interface Section {
  id: string;
  label: string;
  rows: Row[];
}

export function CommandPalette({
  orgId,
  onClose,
}: {
  orgId: string;
  onClose: () => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const client = useQueryClient();
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const location = useLocation();
  const { openCreate } = useCreateTaskDialog();
  const taskParam = useTaskParam();
  const role = useOrgRole(orgId);
  const spaces = useSpaceList(orgId);
  const contacts = useContacts(orgId);
  const panel = useRef<HTMLDivElement | null>(null);
  const listId = useId();
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const pendingEnter = useRef(false);
  const me = useMe().data;
  const historyKey = me ? `openfox:search-history:${orgId}:${me.id}` : "";
  const scopeKey = me ? `openfox:search-spaces:${orgId}:${me.id}` : "";
  const [historyVersion, setHistoryVersion] = useState(0);
  const history = useMemo(
    () => (historyKey ? readSearchHistory(historyKey) : []),
    [historyKey, historyVersion],
  );
  const [confirmClear, setConfirmClear] = useState(false);
  const [category, setCategory] = useState<PaletteFilter>("all");
  const [expanded, setExpanded] = useState<string[]>([]);
  const [conditions, setConditions] = useState<SearchTaskCondition[]>([]);
  const [localScope, setLocalScope] = useState<{
    key: string;
    scope: SpaceScope;
  }>();
  const savedScope = useMemo(
    () => (scopeKey ? readSearchScope(scopeKey) : ALL_SPACES),
    [scopeKey],
  );
  const preferredScope =
    localScope?.key === scopeKey ? localScope.scope : savedScope;
  const [active, setActive] = useState(0);
  useModalBehavior(true, panel, confirmClear ? undefined : onClose);

  useEffect(() => {
    const timer = window.setTimeout(
      () => setDebounced(query.trim()),
      SEARCH_DEBOUNCE_MS,
    );
    return () => window.clearTimeout(timer);
  }, [query]);

  const spaceList = spaces.data ?? [];
  // 当前所在空间（命令「前往迭代」等的默认空间）；不在空间页时取第一个空间
  const routeSpace = /\/spaces\/([^/]+)/.exec(location.pathname)?.[1];
  const commandSpace = routeSpace
    ? decodeURIComponent(routeSpace)
    : spaceList[0]?.id;

  const validIds = preferredScope.ids.filter(
    (id) => !spaces.isSuccess || spaceList.some((space) => space.id === id),
  );
  const scope: SpaceScope =
    preferredScope.mode === "subset"
      ? validIds.length
        ? { mode: "subset", ids: validIds }
        : ALL_SPACES
      : preferredScope;
  const scopeIds =
    scope.mode === "all" ? spaceList.map((space) => space.id) : scope.ids;
  const scopeApplies = (type: SearchType) =>
    type !== "conversation" && type !== "message" && type !== "person";
  const included = (type: PaletteCategory) =>
    category === "all" || category === type;
  const types = SEARCH_TYPES.filter(
    (type) =>
      (category === "all" ||
        type === category ||
        (category === "conversation" && type === "message")) &&
      (scope.mode !== "none" || !scopeApplies(type)),
  );
  const searchable =
    !!debounced || category === "task" || category === "project";
  const searching = searchable && types.length > 0;
  const settled = query.trim() === debounced;
  const taskParams = useMemo(
    () => (conditions.length ? searchTaskFilterParams(conditions) : undefined),
    [conditions],
  );
  const {
    first: result,
    groups,
    next,
  } = useSearchResults(
    orgId,
    [me?.id, role],
    {
      q: debounced,
      types,
      limit: category === "all" ? SEARCH_LIMIT_MIXED : SEARCH_LIMIT_SINGLE,
      offset: 0,
      spaceIds:
        scope.mode === "subset" && types.some(scopeApplies)
          ? scope.ids
          : undefined,
      filters: category === "task" ? taskParams : undefined,
    },
    searching,
  );
  // With an empty query, the dedicated Views picker browses the same readable views as the space sidebar.
  const viewSources = useQueries({
    queries: (category === "view" && !query.trim()
      ? spaceList
          .filter(
            (space) => space.features?.views && scopeIds.includes(space.id),
          )
          .map((space) => space.id)
      : []
    ).map((id) => ({
      queryKey: keys.viewList(orgId, id),
      queryFn: () => api.views.list(orgId, id),
      retry: false,
    })),
  });
  const changeScope = (value: SpaceScope) => {
    pendingEnter.current = false;
    setExpanded([]);
    setLocalScope({ key: scopeKey, scope: value });
    if (scopeKey) writeLocal(scopeKey, JSON.stringify(value));
    setActive(0);
  };
  const selectType = (value: PaletteFilter) => {
    pendingEnter.current = false;
    setCategory(value);
    setExpanded([]);
    setConditions([]);
    setActive(0);
    inputRef.current?.focus();
  };
  const saveHistory = () => {
    if (!historyKey) return;
    recordSearch(historyKey, { query, type: category, scope, conditions });
    setHistoryVersion((n) => n + 1);
  };
  const restoreHistory = (entry: SearchHistoryEntry) => {
    setQuery(entry.query);
    setDebounced(entry.query);
    setCategory(entry.type);
    setConditions(entry.conditions);
    changeScope(entry.scope);
    inputRef.current?.focus();
  };

  const go = (path: string | null) => {
    if (!path) return;
    onClose();
    navigate(path);
  };

  /**
   * 任务结果在当前页打开详情抽屉：当前地址加 ?task=（保留其余查询参数），
   * 历史规则与列表打开一致（useTaskParam.open）：没开抽屉时 push 并打标记，关闭时后退回原页；
   * 已开着别的任务时 replace 切换；在任务全页上则跳到目标任务的全页。
   * 缺少所需字段时（resultPath 为 null）不打开，与原来一致。复制链接（taskUrl）不受影响。
   */
  const openTask = (
    item: Pick<SearchItem, "id" | "space_id" | "display_id">,
  ) => {
    if (!resultPath(orgId, "task", item) || !item.display_id) return;
    onClose();
    taskParam.open(item.display_id);
  };

  // 命令：只做现有入口已经支持的
  const commands = useMemo(() => {
    const list: Row[] = [
      {
        key: "cmd:newTask",
        category: "command",
        title: t("commandPalette.commands.newTask"),
        run: () => {
          onClose();
          openCreate(
            commandSpace && routeSpace ? { spaceId: commandSpace } : {},
          );
        },
      },
    ];
    if (isOrgAdmin(role))
      list.push({
        key: "cmd:newSpace",
        category: "command",
        title: t("commandPalette.commands.newSpace"),
        run: () => {
          onClose();
          const next = new URLSearchParams(location.search);
          next.set("createSpace", "1");
          navigate({ pathname: location.pathname, search: `?${next}` });
        },
      });
    list.push({
      key: "cmd:goSpace",
      category: "command",
      title: t("commandPalette.commands.goSpace"),
      // 留在面板里，切到「空间」类别列出空间
      run: () => {
        setQuery("");
        setDebounced("");
        selectType("space");
        setActive(0);
      },
    });
    if (commandSpace) {
      const base = `/o/${encodeURIComponent(orgId)}/spaces/${encodeURIComponent(commandSpace)}/iterations`;
      // 当前空间（不在空间页时为第一个空间）的「最近删除」
      list.push({
        key: "cmd:goTrash",
        category: "command",
        title: t("commandPalette.commands.goTrash"),
        run: () =>
          go(
            `/o/${encodeURIComponent(orgId)}/spaces/${encodeURIComponent(commandSpace)}/trash`,
          ),
      });
      list.push(
        {
          key: "cmd:goIterations",
          category: "command",
          title: t("commandPalette.commands.goIterations"),
          run: () => go(base),
        },
        {
          key: "cmd:goCurrentIteration",
          category: "command",
          title: t("commandPalette.commands.goCurrentIteration"),
          run: () => {
            void api.iterations
              .list(orgId, commandSpace, { status: "active" })
              .then((page) => {
                const current = page.iterations[0];
                if (!current) {
                  toast(t("commandPalette.commands.noCurrentIteration"));
                  return;
                }
                go(`${base}/${encodeURIComponent(current.id)}`);
              })
              .catch(() =>
                toast(t("commandPalette.commands.iterationFailed"), {
                  tone: "error",
                }),
              );
          },
        },
      );
    }
    return list;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    t,
    role,
    commandSpace,
    routeSpace,
    orgId,
    location.pathname,
    location.search,
  ]);

  const needle = debounced.toLowerCase();
  const commandRows = commands.filter(
    (row) => !needle || row.title.toLowerCase().includes(needle),
  );

  const sections: Section[] = [];
  const items = (contacts.data?.groups ?? []).flatMap((group) => group.items);
  const peopleById = new Map(items.map((item) => [item.id, item]));
  const spacesById = new Map(spaceList.map((space) => [space.id, space]));
  const memberAvatars = new Map(
    (client.getQueryData<Person[]>(keys.members(orgId)) ?? []).map((member) => [
      member.id,
      member.avatar_url,
    ]),
  );
  function actorIcon(
    type: string,
    id: string,
    name: string,
    size: 16 | 20 = 16,
  ) {
    if (type === "agent") {
      const agent = peopleById.get(id);
      return agent?.kind === "crew" ? (
        <Icon name="users" size={size} className="text-fg-muted" />
      ) : (
        <AgentAvatar size={size} name={name} avatar={agent?.avatar} />
      );
    }
    if (type === "person") {
      const src = me?.id === id ? me.avatar_url : memberAvatars.get(id);
      return src ? (
        <Avatar size={size} name={name} src={src} />
      ) : (
        <PersonAvatar size={size} name={name} />
      );
    }
    if (type === "space")
      return <SpaceIcon size={size} icon={spacesById.get(id)?.icon} />;
    return undefined;
  }
  function hitIcon(type: SearchType, item: SearchItem) {
    if (type !== "message" && type !== "conversation")
      return actorIcon(type, item.id, item.title);
    if (!item.sender_kind) return undefined;
    if (
      item.sender_kind === "system" ||
      (type === "message" && item.sender_kind === "agent")
    )
      return <Icon name="bot" size={16} className="text-fg-muted" />;
    return actorIcon(
      item.sender_kind === "user" ? "person" : "agent",
      item.sender_id ?? "",
      item.sender_name || item.title,
      type === "conversation" ? 20 : 16,
    );
  }
  if (!searchable) {
    // 空输入的建议：人员、命令、空间、Agent（复用已有数据）
    if (included("person")) {
      const people = items
        .filter((item) => item.kind === "user")
        .map((item): Row => ({
          key: `person:${item.id}`,
          category: "person",
          icon: actorIcon("person", item.id, item.name),
          title: item.name,
          subtitle: item.email ?? undefined,
          run: () =>
            go(
              resultPath(orgId, "person", {
                id: item.id,
                space_id: undefined,
                display_id: undefined,
              }),
            ),
        }));
      if (people.length)
        sections.push({
          id: "person",
          label: t("commandPalette.groups.person"),
          rows: people,
        });
    }
    if (included("command"))
      sections.push({
        id: "command",
        label: t("commandPalette.groups.command"),
        rows: commands,
      });
    if (included("space")) {
      const rows = spaceList
        .filter((space) => scopeIds.includes(space.id))
        .map((space): Row => ({
          key: `space:${space.id}`,
          category: "space",
          icon: actorIcon("space", space.id, space.name),
          title: space.name,
          subtitle: space.prefix,
          run: () =>
            go(
              resultPath(orgId, "space", {
                id: space.id,
                space_id: space.id,
                display_id: undefined,
              }),
            ),
        }));
      if (rows.length)
        sections.push({
          id: "space",
          label: t("commandPalette.groups.space"),
          rows,
        });
    }
    if (included("agent")) {
      const rows = items
        .filter(
          (item) =>
            item.kind === "agent" &&
            (scope.mode === "all" ||
              (!!item.space_id && scopeIds.includes(item.space_id))),
        )
        .map((item): Row => ({
          key: `agent:${item.id}`,
          category: "agent",
          icon: actorIcon("agent", item.id, item.name),
          title: item.name,
          subtitle: item.title ?? undefined,
          run: () => {
            if (item.space_id) rememberSpace(item.id, item.space_id);
            go(
              resultPath(orgId, "agent", {
                id: item.id,
                space_id: item.space_id ?? undefined,
                display_id: undefined,
              }),
            );
          },
        }));
      if (rows.length)
        sections.push({
          id: "agent",
          label: t("commandPalette.groups.agent"),
          rows,
        });
    }
    for (const section of sections) {
      if (
        category === "all" &&
        section.id !== "command" &&
        section.rows.length > SUGGEST_LIMIT &&
        !expanded.includes(section.id)
      ) {
        section.rows = section.rows.slice(0, SUGGEST_LIMIT);
        section.rows.push({
          key: `more:${section.id}`,
          category: "more",
          more: true,
          title: t("commandPalette.more"),
          run: () => setExpanded((previous) => [...previous, section.id]),
        });
      }
    }
    const views = viewSources.flatMap((source) => source.data ?? []);
    if (views.length)
      sections.push({
        id: "view",
        label: t("commandPalette.groups.view"),
        rows: views.map((view) => ({
          key: `view:${view.id}`,
          category: "view",
          icon: <ViewIcon view={view} />,
          title: view.name,
          run: () => go(resultPath(orgId, "view", view)),
        })),
      });
  } else {
    for (const group of groups) {
      if (group.type === "doc" || !group.items.length) continue;
      const groupType = group.type;
      sections.push({
        id: group.type,
        label: t(`commandPalette.groups.${groupType}`),
        rows: group.items.map((item): Row => ({
          key: `${group.type}:${item.id}`,
          category: groupType,
          icon: hitIcon(groupType, item),
          occurredAt: item.occurred_at,
          title: item.title,
          subtitle:
            groupType === "conversation"
              ? undefined
              : item.subtitle || undefined,
          badge: (groupType === "message"
            ? undefined
            : (item.task_display_id ?? item.display_id)
          )?.toUpperCase(),
          run: () => {
            if (group.type === "task") return openTask(item);
            // Agent 设置抽屉按空间请求详情：先登记空间，省去一次目录查询
            if (group.type === "agent" && item.space_id)
              rememberSpace(item.id, item.space_id);
            if (groupType === "message" || groupType === "conversation")
              go(searchConversationPath(orgId, item));
            else go(resultPath(orgId, groupType, item));
          },
        })),
      });
      if (group.items.length < group.total || group.retry) {
        const single =
          category === "all" &&
          (
            ["task", "project", "conversation", "message"] as SearchType[]
          ).includes(groupType);
        sections[sections.length - 1]!.rows.push({
          key: `more:${groupType}`,
          category: "more",
          more: true,
          title: t(
            group.retry
              ? "commandPalette.retry"
              : group.pending
                ? "commandPalette.loading"
                : "commandPalette.more",
          ),
          run:
            group.retry ??
            (() => {
              if (single)
                selectType(
                  groupType === "message" ? "conversation" : groupType,
                );
              else if (!group.pending) next(group);
            }),
        });
      }
    }
    if (needle && included("command")) {
      const base = `/o/${encodeURIComponent(orgId)}`;
      const space = commandSpace ? spacesById.get(commandSpace) : undefined;
      const spaceBase = space
        ? `${base}/spaces/${encodeURIComponent(space.id)}`
        : undefined;
      const pages: [MessageKey, IconName, string | undefined, string][] = [
        ["shell.inbox", "inbox", `${base}/inbox`, "inbox"],
        ["shell.myTasks", "square-check", `${base}/my-tasks`, "my tasks"],
        [
          "shell.rail.tasks",
          "square-check",
          spaceBase && `${spaceBase}/tasks`,
          "tasks",
        ],
        [
          "shell.space.projects",
          "folder-kanban",
          space?.features?.projects ? `${spaceBase}/projects` : undefined,
          "projects",
        ],
        ["shell.allAgents", "bot", `${base}/agents`, "agents"],
        [
          "shell.rail.computers",
          "monitor",
          `${base}/settings/computers`,
          "computers runtimes",
        ],
        [
          "shell.space.skills",
          "sparkles",
          spaceBase && `${spaceBase}/skills`,
          "skills",
        ],
        ["shell.settings", "settings", `${base}/settings`, "settings"],
      ];
      const rows = pages.flatMap(([label, icon, path, keywords]): Row[] => {
        const title = t(label);
        if (
          !path ||
          !needle
            .split(/\s+/)
            .every((word) =>
              (title + " " + keywords).toLowerCase().includes(word),
            )
        )
          return [];
        return [
          {
            key: `page:${label}`,
            category: "command",
            icon: <Icon name={icon} />,
            title,
            run: () => go(path),
          },
        ];
      });
      if (rows.length)
        sections.push({ id: "pages", label: t("commandPalette.pages"), rows });
    }
    if (included("command") && commandRows.length)
      sections.push({
        id: "command",
        label: t("commandPalette.groups.command"),
        rows: commandRows,
      });
  }
  const order = [
    "task",
    "conversation",
    "message",
    "person",
    "pages",
    "command",
    "project",
    "view",
    "space",
    "agent",
  ];
  sections.sort((a, b) => order.indexOf(a.id) - order.indexOf(b.id));
  const choose = (row: Row) => {
    if (!row.more) saveHistory();
    row.run();
  };
  const flat = sections.flatMap((section) => section.rows);
  const current = Math.min(active, Math.max(flat.length - 1, 0));
  useEffect(() => {
    document
      .getElementById(`${listId}-${current}`)
      ?.scrollIntoView?.({ block: "nearest" });
  }, [current, listId, flat.length]);

  const loading =
    !settled ||
    (searching && result.isPending) ||
    viewSources.some((source) => source.isPending);
  const failed =
    settled &&
    ((searching && result.isError) ||
      viewSources.some((source) => source.isError));
  const empty =
    settled &&
    !loading &&
    !failed &&
    !flat.length &&
    (query.trim() || (category !== "all" && category !== "conversation"));
  const ready = settled && !loading && !(searching && result.isFetching);

  useEffect(() => {
    if (!pendingEnter.current || !ready) return;
    pendingEnter.current = false;
    if (flat[current]) choose(flat[current]!);
    else saveHistory();
  }, [ready, flat, current]);
  const categoryLabel = (value: PaletteCategory) =>
    t(`commandPalette.category.${value}`);
  const total = groups.reduce((sum, group) => sum + group.total, 0);

  let index = -1;
  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-scrim/25 pt-[8vh]"
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-label={t("commandPalette.title")}
        className="flex h-[82vh] max-h-[900px] w-[90vw] max-w-[1080px] min-w-[min(560px,calc(100vw-2rem))] flex-col overflow-hidden rounded-xl border border-line-default bg-surface-card shadow-dialog"
      >
        <h2 className="sr-only">{t("commandPalette.title")}</h2>
        <p className="sr-only">{t("commandPalette.subtitle")}</p>
        <div className="flex shrink-0 items-center gap-2 px-4 pt-4">
          <div className="flex h-9 min-w-0 flex-1 items-center gap-2 rounded-full border border-line-default px-3 focus-within:border-line-focus">
            <Icon name="search" className="text-fg-muted" />
            {category !== "all" && (
              <button
                type="button"
                aria-label={t("commandPalette.exitType", {
                  name: categoryLabel(category),
                })}
                onClick={() => selectType("all")}
                className="group relative shrink-0 rounded-sm bg-surface-active px-2 py-0.5 text-body-sm"
              >
                <span className="group-hover:opacity-55">
                  {categoryLabel(category)}
                </span>
                <Icon
                  name="x"
                  size={12}
                  className="absolute right-px top-1/2 -translate-y-1/2 bg-surface-active opacity-0 group-hover:opacity-100 group-focus-visible:opacity-100"
                />
              </button>
            )}
            <input
              ref={inputRef}
              data-autofocus
              role="combobox"
              aria-expanded
              aria-controls={listId}
              aria-activedescendant={
                flat[current] ? `${listId}-${current}` : undefined
              }
              aria-label={t("commandPalette.inputLabel")}
              placeholder={
                category === "all"
                  ? t("commandPalette.placeholder")
                  : t("commandPalette.typedPlaceholder", {
                      name: categoryLabel(category),
                    })
              }
              maxLength={SEARCH_MAX_LENGTH}
              value={query}
              onChange={(event) => {
                setQuery(event.target.value);
                setExpanded([]);
                setActive(0);
                pendingEnter.current = false;
              }}
              onKeyDown={(event) => {
                if (event.nativeEvent.isComposing) return;
                if (
                  event.key === "Backspace" &&
                  category !== "all" &&
                  event.currentTarget.selectionStart === 0 &&
                  event.currentTarget.selectionEnd === 0
                ) {
                  event.preventDefault();
                  selectType("all");
                } else if (event.key === "ArrowDown" && flat.length) {
                  event.preventDefault();
                  setActive((current + 1) % flat.length);
                } else if (event.key === "ArrowUp" && flat.length) {
                  event.preventDefault();
                  setActive((current - 1 + flat.length) % flat.length);
                } else if (event.key === "Enter") {
                  event.preventDefault();
                  if (!ready) {
                    pendingEnter.current = true;
                    setActive(0);
                    setDebounced(query.trim());
                  } else if (flat[current]) choose(flat[current]!);
                  else saveHistory();
                }
              }}
              className="min-w-0 flex-1 bg-transparent text-body outline-none placeholder:text-fg-subtle"
            />
            {(query || category !== "all") && (
              <button
                type="button"
                onClick={() => {
                  setQuery("");
                  setDebounced("");
                  selectType("all");
                }}
                className="shrink-0 px-1 text-body-sm text-fg-muted hover:text-fg-default"
              >
                {t("commandPalette.clearInput")}
              </button>
            )}
          </div>
          <IconButton
            size="32"
            label={t("commandPalette.close")}
            icon={<Icon name="x" />}
            onClick={onClose}
            className="size-9 rounded-full"
          />
        </div>
        {category !== "conversation" && (
          <div className="relative z-10 flex shrink-0 items-center gap-1 px-4 py-4 shadow-[0_6px_12px_-8px_rgba(0,0,0,0.12)]">
            <SearchSpaceScope
              spaces={spaceList}
              scope={scope}
              onChange={changeScope}
            />
            {(category === "all" || category === "task") && (
              <span
                aria-hidden
                className="mx-1 h-4 w-px shrink-0 bg-line-default"
              />
            )}
            {category === "all" && (
              <div
                role="group"
                aria-label={t("commandPalette.categories")}
                className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto"
              >
                {PALETTE_CATEGORIES.map((value) => (
                  <button
                    type="button"
                    key={value}
                    aria-pressed={false}
                    onClick={() => selectType(value)}
                    className="h-7 shrink-0 rounded-full bg-surface-muted px-2.5 text-caption text-fg-muted hover:bg-surface-hover hover:text-fg-default"
                  >
                    {categoryLabel(value)}
                  </button>
                ))}
              </div>
            )}
            {category === "task" && (
              <SearchTaskFilters
                orgId={orgId}
                spaceIds={scopeIds}
                spaceSelection={scope.mode}
                value={conditions}
                onChange={(value) => {
                  setConditions(value);
                  setActive(0);
                }}
              />
            )}
          </div>
        )}
        {(category === "task" ||
          category === "project" ||
          (category === "conversation" && !!debounced)) && (
          <div
            className="flex shrink-0 items-center justify-between px-6 pt-4 text-caption text-fg-muted"
            aria-live="polite"
          >
            <span>
              {!loading && !failed
                ? t("commandPalette.count", { count: total })
                : ""}
            </span>
            {category !== "conversation" &&
              (conditions.length > 0 || scope.mode !== "all") && (
                <button
                  type="button"
                  onClick={() => {
                    setConditions([]);
                    changeScope(ALL_SPACES);
                  }}
                  className="hover:text-fg-default"
                >
                  {t("commandPalette.clearFilters")}
                </button>
              )}
          </div>
        )}
        <div
          id={listId}
          role="listbox"
          aria-label={t("commandPalette.results")}
          aria-busy={loading}
          className="min-h-0 flex-1 overflow-y-auto"
        >
          {!query.trim() && category === "all" && history.length > 0 && (
            <div className="px-6 pt-3 pb-1">
              <div className="flex items-center justify-between py-1.5 text-[13px] font-medium text-fg-muted">
                {t("commandPalette.history")}
                <IconButton
                  size="28"
                  className="size-6"
                  label={t("commandPalette.clearHistory")}
                  icon={<Icon name="trash-2" size={14} />}
                  onClick={() => setConfirmClear(true)}
                />
              </div>
              <div className="flex flex-wrap gap-2 py-1">
                {history.map((entry, i) => (
                  <button
                    key={i}
                    type="button"
                    aria-label={t("commandPalette.applyHistory", {
                      query:
                        entry.query ||
                        (entry.type === "all" ? "" : categoryLabel(entry.type)),
                    })}
                    onClick={() => restoreHistory(entry)}
                    className="max-w-full truncate rounded-full border border-line-default px-3 py-1 text-body hover:bg-surface-hover"
                  >
                    {entry.type !== "all" && (
                      <span className="mr-1.5 font-medium text-accent">
                        {categoryLabel(entry.type)}
                      </span>
                    )}
                    {entry.query}
                  </button>
                ))}
              </div>
            </div>
          )}
          {loading && (
            <div
              role="status"
              className="flex justify-center py-10 text-fg-muted"
            >
              <Spinner />
              <span className="sr-only">{t("commandPalette.loading")}</span>
            </div>
          )}
          {failed && (
            <div
              role="alert"
              className="flex items-center justify-center gap-3 px-4 py-6 text-body text-danger"
            >
              {t("commandPalette.error")}
              <button
                type="button"
                onClick={() => {
                  if (searching) void result.refetch();
                  viewSources.forEach((source) => {
                    if (source.isError) void source.refetch();
                  });
                }}
                className="underline"
              >
                {t("commandPalette.retry")}
              </button>
            </div>
          )}
          {empty && (
            <p
              role="status"
              className="py-10 text-center text-body text-fg-muted"
            >
              {t("commandPalette.empty")}
            </p>
          )}
          {!loading &&
            sections.map((section) => (
              <div
                key={section.id}
                role="group"
                aria-label={section.label}
                className="px-3 py-2"
              >
                <div className="px-3 py-1.5 text-[13px] font-medium text-fg-muted">
                  {section.label}
                </div>
                {section.rows.map((row) => {
                  const mine = ++index;
                  const selected = mine === current;
                  return (
                    <div
                      key={row.key}
                      role="option"
                      id={`${listId}-${mine}`}
                      aria-label={[row.badge, row.title, row.subtitle]
                        .filter(Boolean)
                        .join(" ")}
                      aria-selected={selected}
                      onMouseMove={() => setActive(mine)}
                      onClick={() => choose(row)}
                      className={cn(
                        "flex cursor-pointer flex-col gap-1 rounded-lg px-3 py-2.5 text-body select-none",
                        selected && "bg-surface-active hover:bg-surface-hover",
                      )}
                    >
                      <div
                        className={cn(
                          "flex min-w-0 items-center gap-2.5",
                          row.more && "text-fg-muted",
                        )}
                      >
                        {row.icon ?? (
                          <Icon
                            name={ICONS[row.category]}
                            size={row.more ? 14 : 16}
                            className="text-fg-muted"
                          />
                        )}
                        {row.badge && row.category !== "conversation" && (
                          <span className="shrink-0 text-caption text-fg-muted">
                            <Hit text={row.badge} query={debounced} />
                          </span>
                        )}
                        <span className="min-w-0 truncate">
                          <Hit text={row.title} query={debounced} />
                        </span>
                        {(row.category === "conversation" ||
                          row.category === "message") && (
                          <span className="ml-auto flex shrink-0 items-center gap-2 text-caption text-fg-muted">
                            {row.category === "conversation" && row.badge && (
                              <Hit text={row.badge} query={debounced} />
                            )}
                            {row.occurredAt && (
                              <time dateTime={row.occurredAt}>
                                {formatRelativeTime(row.occurredAt, locale)}
                              </time>
                            )}
                          </span>
                        )}
                        {row.subtitle && row.category !== "message" && (
                          <span className="ml-auto max-w-[40%] truncate text-caption text-fg-muted">
                            <Hit text={row.subtitle} query={debounced} />
                          </span>
                        )}
                      </div>
                      {row.category === "message" && row.subtitle && (
                        <span className="line-clamp-2 pl-9 text-caption text-fg-muted">
                          <Hit text={row.subtitle} query={debounced} />
                        </span>
                      )}
                    </div>
                  );
                })}
              </div>
            ))}
        </div>
      </div>
      <ConfirmDialog
        open={confirmClear}
        title={t("commandPalette.clearHistoryTitle")}
        description={t("commandPalette.clearHistoryDescription")}
        confirmLabel={t("commandPalette.confirmClear")}
        danger
        onCancel={() => setConfirmClear(false)}
        onConfirm={() => {
          if (historyKey) writeLocal(historyKey, null);
          setHistoryVersion((n) => n + 1);
          setConfirmClear(false);
          inputRef.current?.focus();
        }}
      />
    </div>,
    document.body,
  );
}

function Hit({ text, query }: { text: string; query: string }) {
  const term = query.trim();
  if (!term) return text;
  const escaped = [...new Set([term, ...term.split(/\s+/)])].map((word) =>
    word.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"),
  );
  return text
    .split(new RegExp(`(${escaped.join("|")})`, "gi"))
    .map((part, index) =>
      index % 2 ? (
        <mark key={index} className="rounded-sm bg-warning-soft text-inherit">
          {part}
        </mark>
      ) : (
        part
      ),
    );
}
