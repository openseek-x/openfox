import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";

import { isApiError, type ExternalAccount } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Avatar,
  Badge,
  Button,
  ConfirmDialog,
  Icon,
  SearchMenu,
  Select,
  Skeleton,
  TextInput,
  cn,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import { ProviderIcon } from "../integrations/ProviderIcon";
import { personName, useOrgPeople } from "../tasks/queries";

// 成员页「外部账号」页签。

type Person = NonNullable<ReturnType<typeof useOrgPeople>["data"]>[number];

const PAGE_LIMIT = 50;
const ALL = "__all__";
const UNLINK = "__unlink__";
const GRID =
  "grid grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1.2fr)] items-center gap-3";

type Edits = ReadonlyMap<string, string | null>;
type LinkFilter = typeof ALL | "linked" | "unlinked";

interface Filters {
  q: string;
  provider: string;
  source: string;
  link: LinkFilter;
}

export function ExternalAccountsTab({
  orgId,
  onDirtyChange,
}: {
  orgId: string;
  /** 未保存的关联数；父级据此在切换页签前确认 */
  onDirtyChange: (count: number) => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const people = useOrgPeople(orgId).data ?? [];
  const [filters, setFilters] = useState<Filters>({
    q: "",
    provider: ALL,
    source: ALL,
    link: ALL,
  });
  const [debouncedQ, setDebouncedQ] = useState("");
  const [edits, setEdits] = useState<Edits>(new Map());
  const [guarded, setGuarded] = useState<(() => void) | null>(null);

  useEffect(() => {
    const timer = setTimeout(() => setDebouncedQ(filters.q.trim()), 250);
    return () => clearTimeout(timer);
  }, [filters.q]);
  useEffect(() => onDirtyChange(edits.size), [edits.size, onDirtyChange]);
  useEffect(() => () => onDirtyChange(0), [onDirtyChange]);

  const query = useMemo(
    () => ({
      ...(debouncedQ ? { q: debouncedQ } : {}),
      ...(filters.provider !== ALL ? { provider: filters.provider } : {}),
      ...(filters.source !== ALL ? { source: filters.source } : {}),
      ...(filters.link !== ALL ? { link: filters.link } : {}),
    }),
    [debouncedQ, filters.provider, filters.source, filters.link],
  );
  const list = useInfiniteQuery({
    queryKey: keys.externalAccountList(orgId, query),
    queryFn: ({ pageParam }) =>
      api.externalAccounts.list(orgId, {
        ...query,
        limit: PAGE_LIMIT,
        cursor: pageParam || undefined,
      }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
  });
  const pages = list.data?.pages ?? [];
  const first = pages[0];
  const accounts = pages.flatMap((page) => page.accounts);
  const canManage = first?.can_manage ?? false;
  const filtered =
    Boolean(debouncedQ) ||
    filters.provider !== ALL ||
    filters.source !== ALL ||
    filters.link !== ALL;

  // 有未保存的关联时，改筛选要先确认
  const change = (action: () => void) => {
    if (edits.size > 0) setGuarded(() => action);
    else action();
  };

  const select = (account: ExternalAccount, userId: string | null) => {
    const next = new Map(edits);
    if (userId === (account.linked_user?.id ?? null)) next.delete(account.id);
    else next.set(account.id, userId);
    setEdits(next);
  };

  const save = useMutation({
    mutationFn: () => {
      const byId = new Map(accounts.map((account) => [account.id, account]));
      const changes = [...edits]
        .filter(([id]) => byId.has(id))
        .map(([id, userId]) => ({
          id,
          version: byId.get(id)!.version,
          user_id: userId,
        }));
      return api.externalAccounts.saveLinks(orgId, changes);
    },
    onSuccess: (result) => {
      setEdits(new Map());
      toast(
        t("organization.members.ext.saved", { count: result.reassigned_tasks }),
      );
    },
    onError: (error) => {
      if (isApiError(error, "VERSION_CONFLICT")) {
        // 冲突的账号已被他人修改：丢掉对应改动，其余改动保留
        const ids = error.details?.ids;
        if (Array.isArray(ids)) {
          const next = new Map(edits);
          for (const id of ids) next.delete(String(id));
          setEdits(next);
        }
        toast(t("organization.members.ext.conflict"));
      } else toast(t("organization.members.ext.saveFailed"));
    },
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: keys.externalAccounts(orgId) }),
  });

  const providerOptions = [
    { value: ALL, label: t("organization.members.ext.allProviders") },
    ...(first?.providers ?? []).map((value) => ({ value, label: value })),
  ];
  const sourceOptions = [
    { value: ALL, label: t("organization.members.ext.allSources") },
    ...(first?.sources ?? []).map((value) => ({ value, label: value })),
  ];
  const linkOptions: { value: LinkFilter; label: string }[] = [
    { value: ALL, label: t("organization.members.ext.allLinks") },
    { value: "linked", label: t("organization.members.ext.linked") },
    { value: "unlinked", label: t("organization.members.ext.unlinked") },
  ];

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center gap-2">
        <TextInput
          className="min-w-0 flex-1"
          aria-label={t("organization.members.ext.search")}
          placeholder={t("organization.members.ext.search")}
          leading={<Icon name="search" />}
          value={filters.q}
          onChange={(event) => {
            const q = event.target.value;
            change(() => setFilters((current) => ({ ...current, q })));
          }}
        />
        <Select
          className="w-[150px] shrink-0"
          aria-label={t("organization.members.ext.filterProvider")}
          value={filters.provider}
          options={providerOptions}
          onChange={(provider) =>
            change(() => setFilters((current) => ({ ...current, provider })))
          }
        />
        <Select
          className="w-[170px] shrink-0"
          aria-label={t("organization.members.ext.filterSource")}
          value={filters.source}
          options={sourceOptions}
          onChange={(source) =>
            change(() => setFilters((current) => ({ ...current, source })))
          }
        />
        <Select<LinkFilter>
          className="w-[170px] shrink-0"
          aria-label={t("organization.members.ext.filterLink")}
          value={filters.link}
          options={linkOptions}
          onChange={(link) =>
            change(() => setFilters((current) => ({ ...current, link })))
          }
        />
      </div>

      {list.isPending ? (
        <div className="flex flex-col gap-2" aria-busy>
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-14 rounded-lg" />
          ))}
        </div>
      ) : list.isError ? (
        <div className="flex flex-col items-center gap-3 py-16">
          <p role="alert" className="text-body text-fg-muted">
            {t("organization.members.ext.loadFailed")}
          </p>
          <Button variant="secondary" onClick={() => void list.refetch()}>
            {t("organization.members.ext.retry")}
          </Button>
        </div>
      ) : accounts.length === 0 ? (
        <p className="py-16 text-center text-body text-fg-muted">
          {filtered
            ? t("organization.members.ext.noMatch")
            : t("organization.members.externalEmpty")}
        </p>
      ) : (
        <div role="table" aria-label={t("organization.members.ext.table")}>
          <div
            role="row"
            className={cn(
              GRID,
              "h-10 rounded-lg bg-surface-muted/50 px-2 text-caption text-fg-muted",
            )}
          >
            <span role="columnheader">
              {t("organization.members.ext.colAccount")}
            </span>
            <span role="columnheader">
              {t("organization.members.ext.colSource")}
            </span>
            <span role="columnheader">
              {t("organization.members.ext.colStatus")}
            </span>
            <span role="columnheader">
              {t("organization.members.ext.colMember")}
            </span>
          </div>
          {accounts.map((account) => (
            <AccountRow
              key={account.id}
              account={account}
              people={people}
              canManage={canManage}
              edited={edits.has(account.id)}
              value={
                edits.has(account.id)
                  ? (edits.get(account.id) ?? null)
                  : (account.linked_user?.id ?? null)
              }
              onSelect={(userId) => select(account, userId)}
            />
          ))}
        </div>
      )}

      {first && (
        <div className="flex items-center justify-between text-body-sm text-fg-muted">
          <span>
            {t("organization.members.ext.footer", {
              total: first.total,
              unlinked: first.unlinked,
            })}
          </span>
          {list.hasNextPage && (
            <Button
              variant="ghost-muted"
              size="32"
              loading={list.isFetchingNextPage}
              onClick={() => void list.fetchNextPage()}
            >
              {t("organization.members.ext.loadMore")}
            </Button>
          )}
        </div>
      )}

      {canManage && edits.size > 0 && (
        <div
          role="region"
          aria-label={t("organization.members.ext.pending", {
            count: edits.size,
          })}
          className="sticky bottom-4 flex items-center gap-3 rounded-xl border border-line-default bg-surface-card px-4 py-3 shadow-lg"
        >
          <span className="flex-1 text-body font-medium">
            {t("organization.members.ext.pending", { count: edits.size })}
          </span>
          <Button
            variant="secondary"
            disabled={save.isPending}
            onClick={() => setEdits(new Map())}
          >
            {t("organization.members.ext.cancel")}
          </Button>
          <Button loading={save.isPending} onClick={() => save.mutate()}>
            {t("organization.members.ext.save")}
          </Button>
        </div>
      )}

      <ConfirmDialog
        open={guarded !== null}
        onCancel={() => setGuarded(null)}
        onConfirm={() => {
          const action = guarded;
          setGuarded(null);
          setEdits(new Map());
          action?.();
        }}
        title={t("organization.members.ext.leaveTitle")}
        description={t("organization.members.ext.leaveBody", {
          count: edits.size,
        })}
        confirmLabel={t("organization.members.ext.discard")}
        cancelLabel={t("organization.members.ext.keepEditing")}
        danger
      />
    </div>
  );
}

function AccountRow({
  account,
  people,
  canManage,
  edited,
  value,
  onSelect,
}: {
  account: ExternalAccount;
  people: Person[];
  canManage: boolean;
  edited: boolean;
  value: string | null;
  onSelect: (userId: string | null) => void;
}) {
  const t = useT();
  const linked = Boolean(account.linked_user);
  const suggested = people.find((p) => p.id === account.suggested_user_id);
  return (
    <div
      role="row"
      data-testid={`external-${account.id}`}
      className={cn(GRID, "min-h-14 border-b border-line-soft px-2 py-2")}
    >
      <span role="cell" className="flex min-w-0 items-center gap-2.5">
        <Avatar
          name={
            account.display_name || account.email || account.external_user_id
          }
          size={28}
        />
        <span className="flex min-w-0 flex-col">
          <span className="truncate text-body font-medium text-fg-default">
            {account.display_name || account.external_user_id}
          </span>
          {account.email && (
            <span className="truncate text-caption text-fg-muted">
              {account.email}
            </span>
          )}
        </span>
      </span>
      <span role="cell" className="flex min-w-0 items-center gap-2">
        <ProviderIcon name={account.provider} size={24} />
        <span className="truncate text-body-sm text-fg-secondary">
          {account.source_label}
        </span>
      </span>
      <span role="cell" className="flex min-w-0 flex-col gap-0.5">
        <span className="flex items-center gap-1.5">
          <Badge variant="outline">
            {t(
              `organization.members.ext.status.${linked ? account.link_method || "manual" : "unlinked"}`,
            )}
          </Badge>
          {!linked && account.pending_invitation && (
            <span className="text-caption text-fg-muted">
              {t("imports.externalPending")}
            </span>
          )}
        </span>
        {!linked && account.suggested_user_id && (
          <span className="text-caption text-accent">
            {t("organization.members.ext.autoLink")}
          </span>
        )}
      </span>
      <span role="cell" className="min-w-0">
        <MemberPicker
          account={account}
          people={people}
          value={value}
          edited={edited}
          readOnly={!canManage}
          suggestedName={suggested ? personName(suggested) : ""}
          onSelect={onSelect}
        />
      </span>
    </div>
  );
}

function MemberPicker({
  account,
  people,
  value,
  edited,
  readOnly,
  suggestedName,
  onSelect,
}: {
  account: ExternalAccount;
  people: Person[];
  value: string | null;
  edited: boolean;
  readOnly: boolean;
  suggestedName: string;
  onSelect: (userId: string | null) => void;
}) {
  const t = useT();
  const anchor = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  const current = people.find((p) => p.id === value);
  const currentName = current
    ? personName(current)
    : (account.linked_user?.name ?? "");
  const name = account.display_name || account.external_user_id;
  if (readOnly)
    return (
      <span className="truncate text-body-sm text-fg-secondary">
        {account.linked_user?.name || t("organization.members.ext.none")}
      </span>
    );
  const showSuggestion = value === null && Boolean(suggestedName);
  const groups = [
    {
      id: "members",
      options: people.map((person) => ({
        value: person.id,
        label: personName(person),
        description: person.name ? person.email : undefined,
        keywords: person.email,
        ...(person.id === account.suggested_user_id
          ? { icon: <Icon name="sparkles" /> }
          : {}),
      })),
    },
    ...(value !== null
      ? [
          {
            id: "unlink",
            options: [
              { value: UNLINK, label: t("organization.members.ext.unlink") },
            ],
          },
        ]
      : []),
  ];
  return (
    <>
      <button
        ref={anchor}
        type="button"
        aria-label={t("organization.members.ext.memberAria", { name })}
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className={cn(
          "flex h-8 w-full items-center gap-2 rounded-lg border bg-surface-card px-2 text-left text-body-sm hover:bg-surface-hover",
          edited ? "border-accent" : "border-line-default",
        )}
      >
        <span
          className={cn(
            "min-w-0 flex-1 truncate",
            current || currentName ? "" : "text-fg-muted",
          )}
        >
          {current || currentName
            ? currentName
            : showSuggestion
              ? t("organization.members.ext.suggested", {
                  name: suggestedName,
                })
              : t("organization.members.ext.memberPlaceholder")}
        </span>
        <Icon name="chevron-down" className="text-fg-muted" />
      </button>
      <SearchMenu
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        groups={groups}
        selected={value ? [value] : []}
        onSelect={(next) => onSelect(next === UNLINK ? null : next)}
        label={t("organization.members.ext.memberAria", { name })}
        placeholder={t("organization.members.ext.searchMembers")}
        emptyText={t("organization.members.ext.noMembers")}
      />
    </>
  );
}
