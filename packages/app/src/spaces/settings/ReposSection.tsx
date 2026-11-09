import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import type { SpaceDetail, SpaceRepo, SpaceRepoInput } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Dialog,
  Field,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Select,
  Spinner,
  TextInput,
  Textarea,
  Tooltip,
  cn,
  useToast,
  type IconName,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { RepositoryConnectionField } from "../../integrations/RepositoryConnectionField";
import { canManageSpace, spaceErrorMessage, useOrgRole } from "../queries";
import { SettingsSection } from "./parts";

export const SPACE_REPO_LIMIT = 20;

/** 按域名匹配主机图标，其他用通用 Git 图标。 */
export function repoHostIcon(url: string): IconName {
  const host = url.toLowerCase();
  if (host.includes("github.com")) return "brand-github";
  if (host.includes("gitee.com")) return "brand-gitee";
  if (host.includes("gitlab")) return "brand-gitlab";
  if (host.includes("codeup")) return "brand-codeup";
  if (host.includes("cnb.cool")) return "brand-cnb";
  if (host.includes("bitbucket")) return "brand-bitbucket";
  return "folder-git-2";
}

/** 与后端 space_repos_url_valid 一致：https:// 或 [ssh://]user@host:path。 */
export function isValidRepoUrl(url: string): boolean {
  const value = url.trim();
  if (!value || value.length > 2048) return false;
  return (
    /^https:\/\/\S+$/.test(value) ||
    /^(ssh:\/\/)?[A-Za-z0-9._-]+@[A-Za-z0-9.-]+[:/]\S+$/.test(value)
  );
}

/** Git 仓库。 */
export function ReposSection({
  orgId,
  space,
  retired,
  id = "repos",
}: {
  orgId: string;
  space: SpaceDetail;
  retired: boolean;
  id?: string;
}) {
  const t = useT();
  const api = useApi();
  const queryClient = useQueryClient();
  const toast = useToast();
  const orgRole = useOrgRole(orgId);
  const canManage = canManageSpace(space, orgRole) && !retired;
  const [editing, setEditing] = useState<SpaceRepo | "new" | null>(null);
  const [removing, setRemoving] = useState<SpaceRepo | null>(null);
  const reposKey = keys.spaceRepos(orgId, space.id);
  const repos = useQuery({
    queryKey: reposKey,
    queryFn: () => api.spaceRepos.list(orgId, space.id),
  });
  const list = repos.data ?? [];
  const onSaved = () => toast(t("spaces.repos.saved"));
  const onFailed = (error: unknown) => {
    toast(spaceErrorMessage(t, error, "spaces.repos.saveFailed"), {
      tone: "error",
    });
    void queryClient.invalidateQueries({ queryKey: reposKey });
  };
  const remove = useMutation({
    mutationFn: (repo: SpaceRepo) =>
      api.spaceRepos.remove(orgId, space.id, repo.id, repo.version),
    onSuccess: (_result, repo) => {
      queryClient.setQueryData<SpaceRepo[]>(reposKey, (items) =>
        items?.filter((item) => item.id !== repo.id),
      );
      setRemoving(null);
      onSaved();
    },
    onError: onFailed,
  });

  return (
    <SettingsSection
      id={id}
      title={t("spaces.repos.title")}
      description={t("spaces.repos.description")}
    >
      <div className="flex flex-col overflow-hidden rounded-xl border border-line-default bg-surface-card">
        {repos.isLoading ? (
          <div className="flex justify-center p-4">
            <Spinner />
          </div>
        ) : (
          list.map((repo, index) => (
            <RepoRow
              key={repo.id}
              repo={repo}
              first={index === 0}
              canManage={canManage}
              onEdit={() => setEditing(repo)}
              onRemove={() => setRemoving(repo)}
            />
          ))
        )}
        {canManage ? (
          <div
            className={cn(
              "bg-surface-muted/30 p-4",
              list.length > 0 && "border-t border-line-default",
            )}
          >
            <Button
              variant="outline"
              size="32"
              leading={<Icon name="plus" />}
              disabled={list.length >= SPACE_REPO_LIMIT}
              title={
                list.length >= SPACE_REPO_LIMIT
                  ? t("spaces.repos.limit", { count: SPACE_REPO_LIMIT })
                  : undefined
              }
              onClick={() => setEditing("new")}
            >
              {t("spaces.repos.add")}
            </Button>
          </div>
        ) : (
          list.length === 0 && (
            <p className="p-4 text-caption text-fg-muted">
              {t("spaces.repos.readOnly")}
            </p>
          )
        )}
      </div>
      {editing && (
        <RepoDialog
          orgId={orgId}
          spaceId={space.id}
          repo={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            onSaved();
          }}
          onFailed={onFailed}
        />
      )}
      <ConfirmDialog
        open={Boolean(removing)}
        danger
        title={t("spaces.repos.deleteTitle")}
        description={t("spaces.repos.deleteDescription", {
          name: removing?.url ?? "",
        })}
        confirmLabel={t("spaces.repos.delete")}
        loading={remove.isPending}
        onCancel={() => setRemoving(null)}
        onConfirm={() => removing && remove.mutate(removing)}
      />
    </SettingsSection>
  );
}

function RepoRow({
  repo,
  first,
  canManage,
  onEdit,
  onRemove,
}: {
  repo: SpaceRepo;
  first: boolean;
  canManage: boolean;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const t = useT();
  const tooltip = (
    <span className="flex max-w-[360px] flex-col gap-0.5 text-left">
      <span>{repo.url}</span>
      {repo.description && <span>{repo.description}</span>}
      <span>
        {repo.base_branch
          ? t("spaces.repos.branchValue", { branch: repo.base_branch })
          : t("spaces.repos.defaultBranch")}
      </span>
    </span>
  );
  return (
    <div
      className={cn(
        "flex items-center gap-3 px-4 py-3",
        !first && "border-t border-line-default",
      )}
    >
      <Tooltip content={tooltip} delay={1000}>
        <div className="flex min-w-0 flex-1 items-center gap-3" tabIndex={0}>
          <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-surface-muted">
            <Icon name={repoHostIcon(repo.url)} size={16} />
          </span>
          <span className="flex min-w-0 flex-col">
            <span className="truncate text-body font-medium text-fg-default">
              {repo.url}
            </span>
            {repo.description && (
              <span className="truncate text-caption text-fg-muted">
                {repo.description}
              </span>
            )}
          </span>
        </div>
      </Tooltip>
      {canManage && (
        <Menu
          label={t("spaces.repos.actions", { name: repo.url })}
          placement="bottom-end"
          className="w-[176px]"
          trigger={({ ref, toggle, open }) => (
            <IconButton
              ref={ref}
              label={t("spaces.repos.actions", { name: repo.url })}
              icon={<Icon name="ellipsis" size={14} />}
              size="28"
              aria-expanded={open}
              onClick={toggle}
            />
          )}
        >
          <MenuItem icon={<Icon name="pencil" />} onSelect={onEdit}>
            {t("spaces.repos.edit")}
          </MenuItem>
          <MenuItem icon={<Icon name="trash-2" />} danger onSelect={onRemove}>
            {t("spaces.repos.delete")}
          </MenuItem>
        </Menu>
      )}
    </div>
  );
}

type BranchMode = "default" | "manual";

/** 添加 / 编辑仓库弹窗，宽 512。 */
function RepoDialog({
  orgId,
  spaceId,
  repo,
  onClose,
  onSaved,
  onFailed,
}: {
  orgId: string;
  spaceId: string;
  repo?: SpaceRepo;
  onClose: () => void;
  onSaved: () => void;
  onFailed: (error: unknown) => void;
}) {
  const { t } = useI18n();
  const api = useApi();
  const queryClient = useQueryClient();
  const [url, setUrl] = useState(repo?.url ?? "");
  const [connection, setConnection] = useState(repo?.connection_id ?? "");
  const [description, setDescription] = useState(repo?.description ?? "");
  const [mode, setMode] = useState<BranchMode>(
    repo?.base_branch ? "manual" : "default",
  );
  const [branch, setBranch] = useState(repo?.base_branch ?? "");
  const input: SpaceRepoInput = {
    connection_id: connection,
    url: url.trim(),
    description: description.trim(),
    base_branch: mode === "manual" ? branch.trim() : "",
  };
  const changed =
    !repo ||
    connection !== (repo.connection_id ?? "") ||
    input.url !== repo.url ||
    input.description !== repo.description ||
    input.base_branch !== repo.base_branch;
  const urlInvalid = input.url !== "" && !isValidRepoUrl(input.url);
  const branchInvalid = /\s/.test(branch.trim());
  const save = useMutation({
    mutationFn: () =>
      repo
        ? api.spaceRepos.update(orgId, spaceId, repo.id, {
            ...input,
            version: repo.version,
          })
        : api.spaceRepos.create(orgId, spaceId, input),
    onSuccess: (saved) => {
      queryClient.setQueryData<SpaceRepo[]>(
        keys.spaceRepos(orgId, spaceId),
        (items = []) =>
          repo
            ? items.map((item) => (item.id === saved.id ? saved : item))
            : [...items, saved],
      );
      onSaved();
    },
    onError: onFailed,
  });
  const disabled =
    !input.url || urlInvalid || branchInvalid || !changed || save.isPending;
  return (
    <Dialog
      open
      width={512}
      onClose={save.isPending ? undefined : onClose}
      title={repo ? t("spaces.repos.editTitle") : t("spaces.repos.addTitle")}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={save.isPending}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={disabled}
            loading={save.isPending}
            onClick={() => save.mutate()}
          >
            {save.isPending
              ? t("spaces.common.saving")
              : t("spaces.common.save")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-5">
        <Field
          label={t("spaces.repos.url")}
          error={urlInvalid ? t("spaces.repos.urlInvalid") : undefined}
        >
          <TextInput
            autoFocus
            value={url}
            placeholder="https://git.example.com/org/repo.git"
            onChange={(event) => setUrl(event.target.value)}
          />
        </Field>
        <div className="flex flex-col gap-1.5">
          <div className="flex items-center gap-2">
            <span className="text-body font-medium">
              {t("spaces.repos.branch")}
            </span>
            <Tooltip content={t("spaces.repos.branchHint")}>
              <span tabIndex={0} className="flex text-fg-muted">
                <Icon
                  name="info"
                  size={14}
                  label={t("spaces.repos.branchHint")}
                />
              </span>
            </Tooltip>
          </div>
          <div className="flex items-center gap-2">
            <Select<BranchMode>
              aria-label={t("spaces.repos.branch")}
              value={mode}
              className={mode === "manual" ? "max-w-[55%]" : "w-full"}
              options={[
                {
                  value: "default",
                  label: t("spaces.repos.defaultBranch"),
                  description: t("spaces.repos.defaultBranchHint"),
                },
                { value: "manual", label: t("spaces.repos.manualBranch") },
              ]}
              onChange={(next) => {
                setMode(next);
                if (next === "default") setBranch("");
              }}
            />
            {mode === "manual" && (
              <TextInput
                aria-label={t("spaces.repos.branchName")}
                className="flex-1"
                value={branch}
                invalid={branchInvalid}
                placeholder={t("spaces.repos.branchPlaceholder")}
                onChange={(event) => setBranch(event.target.value)}
              />
            )}
          </div>
        </div>
        <RepositoryConnectionField
          org={orgId}
          value={connection}
          onChange={setConnection}
        />
        <Field label={t("spaces.repos.descriptionLabel")}>
          <Textarea
            rows={2}
            maxLength={500}
            value={description}
            placeholder={t("spaces.repos.descriptionPlaceholder")}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
        <div className="flex flex-col gap-1">
          <span className="text-body font-medium">
            {t("spaces.repos.credentials")}
          </span>
          <p className="text-body-sm text-fg-muted">
            {t("spaces.repos.credentialsHint")}
          </p>
        </div>
      </div>
    </Dialog>
  );
}
