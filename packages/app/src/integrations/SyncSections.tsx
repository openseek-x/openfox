import { useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";

import type {
  IntegrationConnection,
  IntegrationDetail,
  IntegrationSyncLink,
} from "@openfox/api";
import {
  Badge,
  Button,
  Callout,
  Checkbox,
  ConfirmDialog,
  Dialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Radio,
  Select,
  Skeleton,
  Switch,
  TextInput,
  Tooltip,
  useToast,
} from "@openfox/ui";

import { useSpaceList } from "../spaces/queries";
import {
  SettingRow,
  SettingsCard,
  SettingsSection,
} from "../spaces/settings/parts";
import { useSettingsSave, WebhookBody } from "./GitHubSections";
import { useGitProvider, useGitT } from "./git-provider";
import {
  integrationErrorText,
  useIntegrationRepositories,
  useSyncLinkMutations,
} from "./queries";

// GitHub 详情页「同步 Issue 数据」区：新建同步、已关联仓库列表、编辑 / 暂停 / 移除 / 重试。

type Direction = IntegrationSyncLink["direction"];

const fmtTime = (iso: string) => new Date(iso).toLocaleString();

/** 同步对话框和列表共用的方向文案键。 */
const DIRECTION_KEY = {
  INBOUND: "integrations.sync.inbound",
  BIDIRECTIONAL: "integrations.sync.bidirectional",
} as const;

export function GitHubSyncSection({
  orgId,
  detail,
  title,
  description,
}: {
  orgId: string;
  detail: IntegrationDetail;
  title: string;
  description: string;
}) {
  const t = useGitT();
  const { id: providerId } = useGitProvider();
  const [searchParams, setSearchParams] = useSearchParams();
  const { can_manage: canManage, connections, sync_links: links } = detail;
  const usable = connections.filter((c) => c.status !== "DISCONNECTED");
  const [creating, setCreating] = useState(false);
  const [importEntry, setImportEntry] = useState(false);
  // 刚建好的同步所属连接：用于显示 Webhook 事件提醒
  const [created, setCreated] = useState<string | null>(null);
  const [guide, setGuide] = useState(false);
  const createdConnection = connections.find((c) => c.id === created);
  const disabledReason = !canManage
    ? t("integrations.sync.needAdmin")
    : usable.length === 0
      ? t("integrations.sync.needConnection")
      : "";
  useEffect(() => {
    if (
      providerId !== "github" ||
      searchParams.get("import") !== "existing" ||
      disabledReason
    )
      return;
    setImportEntry(true);
    setCreating(true);
    const next = new URLSearchParams(searchParams);
    next.delete("import");
    setSearchParams(next, { replace: true });
  }, [providerId, searchParams, setSearchParams, disabledReason]);
  return (
    <SettingsSection
      title={title}
      description={description}
      action={
        disabledReason ? (
          <Tooltip content={disabledReason}>
            <span tabIndex={0} className="inline-flex">
              <Button size="32" disabled>
                {t("integrations.newSync")}
              </Button>
            </span>
          </Tooltip>
        ) : (
          <Button
            size="32"
            onClick={() => {
              setImportEntry(false);
              setCreating(true);
            }}
          >
            {t("integrations.newSync")}
          </Button>
        )
      }
    >
      {created && (
        <Callout tone="info">
          <span>{t("integrations.sync.webhookReminder")} </span>
          {createdConnection && (
            <button
              type="button"
              aria-expanded={guide}
              className="font-medium underline"
              onClick={() => setGuide((v) => !v)}
            >
              {t("integrations.sync.openGuide")}
            </button>
          )}
          {guide && createdConnection && (
            <div className="mt-2">
              <WebhookBody orgId={orgId} connection={createdConnection} />
            </div>
          )}
        </Callout>
      )}
      {links.length === 0 ? (
        <SettingsCard>
          <SettingRow title={t("integrations.emptySync")} />
        </SettingsCard>
      ) : (
        <SettingsCard>
          {links.map((link) => (
            <SyncRow
              key={link.id}
              orgId={orgId}
              link={link}
              canManage={canManage}
            />
          ))}
        </SettingsCard>
      )}
      <IssueBacklinkCard orgId={orgId} />
      {creating && (
        <NewSyncDialog
          orgId={orgId}
          connections={usable}
          initialImportExisting={importEntry}
          onClose={() => setCreating(false)}
          onCreated={(connectionId) => {
            setCreating(false);
            setCreated(connectionId);
            setGuide(false);
          }}
        />
      )}
    </SettingsSection>
  );
}

// ---------- Issue 回链 ----------

/** 同步成任务的 Issue 下评论任务链接：私有 / 公开仓库各一个开关（保存方式同 PR 回链）。 */
function IssueBacklinkCard({ orgId }: { orgId: string }) {
  const t = useGitT();
  const { settings, put, save } = useSettingsSave(orgId);
  const data = settings.data;
  if (settings.isPending) return <Skeleton className="h-[120px] rounded-xl" />;
  if (settings.isError || !data)
    return (
      <p role="alert" className="text-body-sm text-fg-muted">
        {t("integrations.github.pr.loadFailed")}
      </p>
    );
  const disabled = !data.can_manage || put.isPending;
  const row = (
    key: "issue_backlink_private" | "issue_backlink_public",
    label: string,
  ) => (
    <SettingRow
      key={key}
      title={label}
      control={
        <Switch
          label={label}
          checked={Boolean(data.settings[key])}
          disabled={disabled}
          onCheckedChange={(checked) => save({ [key]: checked })}
        />
      }
    />
  );
  return (
    <SettingsCard>
      <SettingRow
        title={t("integrations.github.issue.backlink")}
        description={t("integrations.github.issue.backlinkDesc")}
      />
      {row(
        "issue_backlink_private",
        t("integrations.github.issue.backlinkPrivate"),
      )}
      {row(
        "issue_backlink_public",
        t("integrations.github.issue.backlinkPublic"),
      )}
    </SettingsCard>
  );
}

// ---------- 方向单选 ----------

function DirectionRadios({
  name,
  value,
  onChange,
}: {
  name: string;
  value: Direction;
  onChange: (value: Direction) => void;
}) {
  const t = useGitT();
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="mb-1.5 text-body-sm font-medium">
        {t("integrations.sync.direction")}
      </legend>
      {(["INBOUND", "BIDIRECTIONAL"] as const).map((option) => (
        <label key={option} className="flex cursor-pointer items-start gap-2">
          <Radio
            name={name}
            className="mt-0.5"
            checked={value === option}
            onChange={() => onChange(option)}
            aria-label={t(DIRECTION_KEY[option])}
          />
          <span className="flex flex-col">
            <span className="text-body-sm">{t(DIRECTION_KEY[option])}</span>
            {option === "BIDIRECTIONAL" && (
              <span className="text-caption text-fg-muted">
                {t("integrations.sync.bidirectionalDesc")}
              </span>
            )}
          </span>
        </label>
      ))}
    </fieldset>
  );
}

// ---------- 新建同步 ----------

function NewSyncDialog({
  orgId,
  connections,
  initialImportExisting,
  onClose,
  onCreated,
}: {
  orgId: string;
  connections: IntegrationConnection[];
  initialImportExisting: boolean;
  onClose: () => void;
  onCreated: (connectionId: string) => void;
}) {
  const t = useGitT();
  const toast = useToast();
  const { create } = useSyncLinkMutations(orgId);
  const spaces = useSpaceList(orgId);
  const [connectionId, setConnectionId] = useState(connections[0]?.id ?? "");
  const [draft, setDraft] = useState("");
  const [q, setQ] = useState("");
  const [page, setPage] = useState(1);
  const [repository, setRepository] = useState("");
  const [spaceId, setSpaceId] = useState("");
  const [direction, setDirection] = useState<Direction>("INBOUND");
  const [importExisting, setImportExisting] = useState(initialImportExisting);
  const [error, setError] = useState("");
  const repos = useIntegrationRepositories(orgId, connectionId, q, page);

  // 搜索词防抖：停止输入 250ms 后再请求
  useEffect(() => {
    const timer = setTimeout(() => {
      setQ(draft.trim());
      setPage(1);
    }, 250);
    return () => clearTimeout(timer);
  }, [draft]);

  // 当前成员可写的空间：已加入（或可管理）且未停用
  const writable = useMemo(
    () =>
      (spaces.data ?? []).filter(
        (space) =>
          space.status !== "retired" && (space.my_role || space.can_manage),
      ),
    [spaces.data],
  );

  const submit = () => {
    setError("");
    create.mutate(
      {
        connection_id: connectionId,
        repository,
        space_id: spaceId,
        direction,
        import_existing: importExisting,
      },
      {
        onSuccess: () => {
          toast(t("integrations.sync.created"));
          onCreated(connectionId);
        },
        onError: (e) => setError(integrationErrorText(t, e)),
      },
    );
  };

  return (
    <Dialog
      open
      width={520}
      onClose={create.isPending ? undefined : onClose}
      title={t("integrations.sync.dialogTitle")}
      footer={
        <>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={create.isPending}
          >
            {t("common.cancel")}
          </Button>
          <Button
            onClick={submit}
            loading={create.isPending}
            disabled={!connectionId || !repository || !spaceId}
          >
            {t("integrations.sync.confirm")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {error && <Callout tone="danger">{error}</Callout>}
        <div className="flex flex-col gap-1.5">
          <span className="text-body-sm font-medium">
            {t("integrations.sync.pickAccount")}
          </span>
          <Select
            aria-label={t("integrations.sync.pickAccount")}
            value={connectionId}
            options={connections.map((c) => ({
              value: c.id,
              label: c.display_name || c.external_id,
            }))}
            onChange={(id) => {
              setConnectionId(id);
              setRepository("");
              setDraft("");
              setQ("");
              setPage(1);
            }}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <span className="text-body-sm font-medium">
            {t("integrations.sync.pickRepo")}
          </span>
          <TextInput
            aria-label={t("integrations.sync.searchRepo")}
            placeholder={t("integrations.sync.searchRepo")}
            leading={<Icon name="search" />}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
          />
          <RepoList
            state={repos}
            selected={repository}
            onSelect={setRepository}
          />
          <div className="flex items-center justify-end gap-2">
            <Button
              size="28"
              variant="outline"
              disabled={page <= 1}
              onClick={() => setPage((p) => p - 1)}
            >
              {t("integrations.sync.prevPage")}
            </Button>
            <Button
              size="28"
              variant="outline"
              disabled={!repos.data?.has_more}
              onClick={() => setPage((p) => p + 1)}
            >
              {t("integrations.sync.nextPage")}
            </Button>
          </div>
        </div>
        <div className="flex flex-col gap-1.5">
          <span className="text-body-sm font-medium">
            {t("integrations.sync.pickSpace")}
          </span>
          <Select
            aria-label={t("integrations.sync.pickSpace")}
            value={spaceId || null}
            placeholder={t("integrations.sync.pickSpacePlaceholder")}
            options={writable.map((space) => ({
              value: space.id,
              label: space.name,
            }))}
            onChange={setSpaceId}
          />
        </div>
        <DirectionRadios
          name="sync-direction"
          value={direction}
          onChange={setDirection}
        />
        <label className="flex cursor-pointer items-start gap-2">
          <Checkbox
            className="mt-0.5"
            aria-label={t("integrations.sync.importExisting")}
            checked={importExisting}
            onChange={(e) => setImportExisting(e.target.checked)}
          />
          <span className="flex flex-col">
            <span className="text-body-sm">
              {t("integrations.sync.importExisting")}
            </span>
            <span className="text-caption text-fg-muted">
              {t("integrations.sync.importExistingDesc")}
            </span>
          </span>
        </label>
      </div>
    </Dialog>
  );
}

function RepoList({
  state,
  selected,
  onSelect,
}: {
  state: ReturnType<typeof useIntegrationRepositories>;
  selected: string;
  onSelect: (fullName: string) => void;
}) {
  const t = useGitT();
  if (state.isPending) return <Skeleton className="h-[120px] rounded-lg" />;
  if (state.isError)
    return (
      <p role="alert" className="text-body-sm text-fg-muted">
        {integrationErrorText(t, state.error)}
      </p>
    );
  const repositories = state.data.repositories;
  if (repositories.length === 0)
    return (
      <p className="text-body-sm text-fg-muted">
        {t("integrations.sync.noRepos")}
      </p>
    );
  return (
    <ul
      aria-label={t("integrations.sync.pickRepo")}
      className="flex max-h-[200px] flex-col overflow-y-auto rounded-lg border border-line-soft"
    >
      {repositories.map((repo) => {
        const linked = Boolean(repo.linked_space_id);
        return (
          <li key={repo.full_name}>
            <label
              className={
                "flex items-center gap-2 px-3 py-2 " +
                (linked
                  ? "cursor-not-allowed opacity-60"
                  : "cursor-pointer hover:bg-surface-active")
              }
            >
              <Radio
                name="sync-repository"
                aria-label={repo.full_name}
                disabled={linked}
                checked={selected === repo.full_name}
                onChange={() => onSelect(repo.full_name)}
              />
              <span className="min-w-0 flex-1 truncate text-body-sm">
                {repo.full_name}
              </span>
              {repo.private && (
                <Icon
                  name="lock"
                  size={12}
                  aria-label={t("integrations.sync.private")}
                  className="shrink-0 text-fg-muted"
                />
              )}
              {linked && (
                <span className="shrink-0 text-caption text-fg-muted">
                  {t("integrations.sync.linkedTo", {
                    space: repo.linked_space_name ?? "",
                  })}
                </span>
              )}
            </label>
          </li>
        );
      })}
    </ul>
  );
}

// ---------- 已关联仓库列表 ----------

function SyncRow({
  orgId,
  link,
  canManage,
}: {
  orgId: string;
  link: IntegrationSyncLink;
  canManage: boolean;
}) {
  const t = useGitT();
  const toast = useToast();
  const { update, remove, retry } = useSyncLinkMutations(orgId);
  const [editing, setEditing] = useState(false);
  const [removing, setRemoving] = useState(false);
  const fail = (error: unknown) =>
    toast(integrationErrorText(t, error), { tone: "error" });
  const { backfill, outbound } = link;
  const paused = link.status === "PAUSED";
  const tone =
    link.status === "ACTIVE" ? "success" : paused ? "muted" : "warning";
  const name = link.external_project_name;
  return (
    <div className="flex flex-col gap-1.5 p-4">
      <div className="flex min-h-9 items-center gap-3">
        <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
          {link.repository_url ? (
            <a
              href={link.repository_url}
              target="_blank"
              rel="noopener noreferrer"
              className="truncate text-body font-medium hover:underline"
            >
              {name}
            </a>
          ) : (
            <span className="truncate text-body font-medium">{name}</span>
          )}
          {link.repository_private && (
            <Icon
              name="lock"
              size={12}
              aria-label={t("integrations.sync.private")}
              className="text-fg-muted"
            />
          )}
          <Badge tone="neutral">{t(DIRECTION_KEY[link.direction])}</Badge>
          <span className="text-body-sm text-fg-muted">{link.space_name}</span>
        </span>
        <Badge variant="status" small tone={tone}>
          {link.status === "ACTIVE"
            ? t("integrations.sync.active")
            : paused
              ? t("integrations.sync.paused")
              : t("integrations.sync.needsAttention")}
        </Badge>
        {canManage && (
          <Menu
            label={t("integrations.rowMenu", { name })}
            placement="bottom-end"
            trigger={(props) => (
              <IconButton
                ref={props.ref}
                label={t("integrations.rowMenu", { name })}
                icon={<Icon name="ellipsis" />}
                size="28"
                onClick={props.toggle}
              />
            )}
          >
            {link.repository_url && (
              <MenuItem
                icon={<Icon name="external-link" />}
                onSelect={() =>
                  window.open(link.repository_url, "_blank", "noopener")
                }
              >
                {t("integrations.sync.openRepo")}
              </MenuItem>
            )}
            <MenuItem
              icon={<Icon name="pencil" />}
              onSelect={() => setEditing(true)}
            >
              {t("integrations.sync.edit")}
            </MenuItem>
            <MenuItem
              icon={<Icon name={paused ? "play" : "clock"} />}
              onSelect={() =>
                update.mutate(
                  {
                    id: link.id,
                    body: {
                      version: link.version,
                      status: paused ? "ACTIVE" : "PAUSED",
                    },
                  },
                  { onError: fail },
                )
              }
            >
              {paused
                ? t("integrations.sync.resume")
                : t("integrations.sync.pause")}
            </MenuItem>
            <MenuItem
              danger
              icon={<Icon name="trash-2" />}
              onSelect={() => setRemoving(true)}
            >
              {t("integrations.sync.remove")}
            </MenuItem>
          </Menu>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-body-sm text-fg-muted">
        <span>
          {link.last_synced_at
            ? t("integrations.sync.lastSynced", {
                time: fmtTime(link.last_synced_at),
              })
            : t("integrations.sync.neverSynced")}
        </span>
        {backfill.status === "running" && (
          <span>
            {t("integrations.sync.backfillRunning", { n: backfill.imported })}
          </span>
        )}
        {backfill.status === "failed" && (
          <span role="alert" className="text-danger">
            {t("integrations.sync.backfillFailed", { error: backfill.error })}
          </span>
        )}
        {outbound.failed > 0 && (
          <span className="flex items-center gap-2 text-danger">
            {t("integrations.sync.outboundFailed", { n: outbound.failed })}
            {canManage && (
              <Button
                size="28"
                variant="outline"
                loading={retry.isPending}
                onClick={() =>
                  retry.mutate(link.id, {
                    onSuccess: () => toast(t("integrations.sync.retried")),
                    onError: fail,
                  })
                }
              >
                {t("integrations.sync.retry")}
              </Button>
            )}
          </span>
        )}
      </div>
      {editing && (
        <EditSyncDialog
          link={link}
          pending={update.isPending}
          onClose={() => setEditing(false)}
          onSave={(direction) =>
            update.mutate(
              { id: link.id, body: { version: link.version, direction } },
              {
                onSuccess: () => setEditing(false),
                onError: (e) => {
                  fail(e);
                  setEditing(false);
                },
              },
            )
          }
        />
      )}
      <ConfirmDialog
        open={removing}
        danger
        title={t("integrations.sync.removeTitle", { name })}
        description={t("integrations.sync.removeBody")}
        confirmLabel={t("integrations.sync.remove")}
        loading={remove.isPending}
        onCancel={() => setRemoving(false)}
        onConfirm={() =>
          remove.mutate(
            { id: link.id, version: link.version },
            {
              onSuccess: () => {
                toast(t("integrations.sync.removed"));
                setRemoving(false);
              },
              onError: (e) => {
                fail(e);
                setRemoving(false);
              },
            },
          )
        }
      />
    </div>
  );
}

function EditSyncDialog({
  link,
  pending,
  onClose,
  onSave,
}: {
  link: IntegrationSyncLink;
  pending: boolean;
  onClose: () => void;
  onSave: (direction: Direction) => void;
}) {
  const t = useGitT();
  const [direction, setDirection] = useState<Direction>(link.direction);
  return (
    <Dialog
      open
      onClose={pending ? undefined : onClose}
      title={t("integrations.sync.editTitle", {
        name: link.external_project_name,
      })}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={pending}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={pending}
            disabled={direction === link.direction}
            onClick={() => onSave(direction)}
          >
            {t("common.save")}
          </Button>
        </>
      }
    >
      <DirectionRadios
        name="edit-direction"
        value={direction}
        onChange={setDirection}
      />
    </Dialog>
  );
}
