import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";

import { newIdempotencyKey, type AgentRepo } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Select,
  TextInput,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { RepositoryConnectionField } from "../../integrations/RepositoryConnectionField";
import { validRepoUrl } from "../CreateAgentDialog";
import { isValidEmail } from "../model";
import { agentErrorMessage, useOptimisticMutation, useRepos } from "../queries";
import { SettingRow } from "./parts";
import type { SectionProps } from "./BasicSections";
import {
  GIT_NAME_MODES as NAME_MODES,
  GIT_EMAIL_MODES as EMAIL_MODES,
} from "../../settings/gitModes";

const REPO_LIMIT = 20;

/** Git 仓库 & 设置：仓库增删改（不进配置版本）；提交人名称 / 邮箱模式（新建配置版本）。 */
export function GitSection({ orgId, agent, writes }: SectionProps) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const repos = useRepos(agent, orgId);
  const queryKey = keys.agentSub(orgId, agent.id, "repos");
  const [editing, setEditing] = useState<null | "new" | AgentRepo>(null);
  const [url, setUrl] = useState("");
  const [branch, setBranch] = useState("");
  const [connection, setConnection] = useState("");
  const [showError, setShowError] = useState(false);
  const config = agent.config;
  const [manualName, setManualName] = useState(config?.git_author_name ?? "");
  const [manualEmail, setManualEmail] = useState(
    config?.git_author_email ?? "",
  );
  useEffect(
    () => setManualName(config?.git_author_name ?? ""),
    [config?.git_author_name],
  );
  useEffect(
    () => setManualEmail(config?.git_author_email ?? ""),
    [config?.git_author_email],
  );
  // 选「手动填写」后先在本地展开输入框，填好再保存成新的配置版本
  const [nameMode, setNameMode] = useState(config?.git_author_name_mode);
  const [emailMode, setEmailMode] = useState(config?.git_author_email_mode);
  useEffect(
    () => setNameMode(config?.git_author_name_mode),
    [config?.git_author_name_mode],
  );
  useEffect(
    () => setEmailMode(config?.git_author_email_mode),
    [config?.git_author_email_mode],
  );

  const remove = useOptimisticMutation<AgentRepo, void, AgentRepo[]>(orgId, {
    queryKey,
    mutationFn: (repo) =>
      api.agents.repos.remove(orgId, agent.space_id, agent.id, repo.id),
    optimistic: (data, repo) => data.filter((item) => item.id !== repo.id),
  });

  const list = repos.data ?? [];

  const startEdit = (repo: AgentRepo | "new") => {
    setEditing(repo);
    setUrl(repo === "new" ? "" : repo.url);
    setBranch(repo === "new" ? "" : repo.base_branch);
    setConnection(repo === "new" ? "" : (repo.connection_id ?? ""));
    setShowError(false);
  };

  const save = async () => {
    setShowError(true);
    if (!editing || !validRepoUrl(url)) return;
    try {
      if (editing === "new")
        await api.agents.repos.create(
          orgId,
          agent.space_id,
          agent.id,
          {
            url: url.trim(),
            base_branch: branch.trim(),
            connection_id: connection,
          },
          newIdempotencyKey(),
        );
      else
        await api.agents.repos.update(
          orgId,
          agent.space_id,
          agent.id,
          editing.id,
          {
            url: url.trim(),
            base_branch: branch.trim(),
            connection_id: connection,
            version: editing.version,
          },
        );
      setEditing(null);
      toast(t("agents.toast.saved"));
    } catch (error) {
      toast(agentErrorMessage(t, error), { tone: "error" });
    } finally {
      void queryClient.invalidateQueries({ queryKey });
    }
  };

  if (!config) return null;
  const manualEmailBad = emailMode === "MANUAL" && !isValidEmail(manualEmail);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-2">
        <div>
          <h4 className="text-body font-medium">
            {t("agents.sections.git.repos")}
          </h4>
          <p className="text-caption text-fg-muted">
            {t("agents.sections.git.reposDesc")}
          </p>
        </div>
        {list.length === 0 && editing === null ? (
          <div className="rounded-lg border border-dashed border-line-default px-4 py-3 text-body-sm text-fg-muted">
            {t("agents.sections.git.empty")}
          </div>
        ) : (
          <ul className="flex flex-col overflow-hidden rounded-lg border border-line-default">
            {list.map((repo) => (
              <li
                key={repo.id}
                className="flex items-center gap-2 border-b border-line-soft px-3 py-2 last:border-b-0"
              >
                <Icon name="git-branch" className="text-fg-muted" />
                <span className="min-w-0 flex-1 truncate font-mono text-caption">
                  {repo.url}
                </span>
                {repo.base_branch && (
                  <span className="text-caption text-fg-muted">
                    {repo.base_branch}
                  </span>
                )}
                <Menu
                  label={t("agents.sections.git.repoMenu", { url: repo.url })}
                  placement="bottom-end"
                  trigger={({ ref, toggle }) => (
                    <IconButton
                      ref={ref}
                      label={t("agents.sections.git.repoMenu", {
                        url: repo.url,
                      })}
                      icon={<Icon name="ellipsis" size={14} />}
                      size="28"
                      onClick={toggle}
                    />
                  )}
                >
                  <MenuItem
                    icon={<Icon name="pencil" />}
                    onSelect={() => startEdit(repo)}
                  >
                    {t("agents.sections.git.edit")}
                  </MenuItem>
                  <MenuItem
                    danger
                    icon={<Icon name="trash-2" />}
                    onSelect={() => remove.mutate(repo)}
                  >
                    {t("agents.sections.git.remove")}
                  </MenuItem>
                </Menu>
              </li>
            ))}
            {editing !== null && (
              <li className="flex flex-col gap-1 p-2">
                <div className="flex items-center gap-2">
                  <TextInput
                    className="flex-1"
                    autoFocus
                    aria-label={t("agents.create.repoUrl")}
                    placeholder={t("agents.create.repoUrlPlaceholder")}
                    value={url}
                    invalid={showError && !validRepoUrl(url)}
                    onChange={(event) => setUrl(event.target.value)}
                  />
                  <TextInput
                    className="w-36"
                    aria-label={t("agents.create.repoBranch")}
                    placeholder={t("agents.create.repoBranch")}
                    value={branch}
                    onChange={(event) => setBranch(event.target.value)}
                  />
                  <Button size="28" onClick={() => void save()}>
                    {t("common.save")}
                  </Button>
                  <Button
                    variant="ghost"
                    size="28"
                    onClick={() => setEditing(null)}
                  >
                    {t("common.cancel")}
                  </Button>
                </div>
                {showError && !validRepoUrl(url) && (
                  <p role="alert" className="text-caption text-danger">
                    {t("agents.create.repoInvalid")}
                  </p>
                )}
                <RepositoryConnectionField
                  org={orgId}
                  value={connection}
                  onChange={setConnection}
                />
              </li>
            )}
          </ul>
        )}
        <Button
          variant="outline"
          size="28"
          className="self-start"
          leading={<Icon name="plus" />}
          disabled={list.length >= REPO_LIMIT || editing !== null}
          title={
            list.length >= REPO_LIMIT
              ? t("agents.sections.git.repoLimit")
              : undefined
          }
          onClick={() => startEdit("new")}
        >
          {t("agents.sections.git.add")}
        </Button>
      </div>

      <div className="flex flex-col border-t border-line-default pt-4">
        <h4 className="pb-2 text-body font-medium">
          {t("agents.sections.git.settings")}
        </h4>
        <SettingRow
          label={t("agents.sections.git.authorName")}
          desc={t("agents.sections.git.authorNameDesc")}
        >
          <Select
            className="w-64"
            aria-label={t("agents.sections.git.authorName")}
            value={nameMode ?? null}
            onChange={(mode) => {
              setNameMode(mode);
              if (mode !== "MANUAL")
                void writes.saveConfig({ git_author_name_mode: mode });
            }}
            options={NAME_MODES.map((mode) => ({
              value: mode,
              label: t(`agents.sections.git.nameModes.${mode}`),
            }))}
          />
        </SettingRow>
        {nameMode === "MANUAL" && (
          <div className="flex items-center gap-2 border-b border-line-soft py-3">
            <TextInput
              className="flex-1"
              aria-label={t("agents.sections.git.manualName")}
              placeholder={t("agents.sections.git.manualName")}
              value={manualName}
              maxLength={128}
              onChange={(event) => setManualName(event.target.value)}
            />
            <Button
              size="28"
              disabled={!manualName.trim()}
              onClick={() =>
                void writes.saveConfig({
                  git_author_name_mode: "MANUAL",
                  git_author_name: manualName.trim(),
                })
              }
            >
              {t("common.save")}
            </Button>
          </div>
        )}
        <SettingRow
          label={t("agents.sections.git.authorEmail")}
          desc={t("agents.sections.git.authorEmailDesc")}
        >
          <Select
            className="w-64"
            aria-label={t("agents.sections.git.authorEmail")}
            value={emailMode ?? null}
            onChange={(mode) => {
              setEmailMode(mode);
              if (mode !== "MANUAL")
                void writes.saveConfig({ git_author_email_mode: mode });
            }}
            options={EMAIL_MODES.map((mode) => ({
              value: mode,
              label: t(`agents.sections.git.emailModes.${mode}`),
            }))}
          />
        </SettingRow>
        {emailMode === "MANUAL" && (
          <div className="flex flex-col gap-1 py-3">
            <div className="flex items-center gap-2">
              <TextInput
                className="flex-1"
                type="email"
                aria-label={t("agents.sections.git.manualEmail")}
                placeholder={t("agents.sections.git.manualEmail")}
                value={manualEmail}
                maxLength={254}
                invalid={manualEmail !== "" && manualEmailBad}
                onChange={(event) => setManualEmail(event.target.value)}
              />
              <Button
                size="28"
                disabled={manualEmailBad}
                onClick={() =>
                  void writes.saveConfig({
                    git_author_email_mode: "MANUAL",
                    git_author_email: manualEmail.trim(),
                  })
                }
              >
                {t("common.save")}
              </Button>
            </div>
            {manualEmail !== "" && manualEmailBad && (
              <p role="alert" className="text-caption text-danger">
                {t("agents.sections.git.emailInvalid")}
              </p>
            )}
          </div>
        )}
        <SettingRow
          label={t("gitSettings.coAuthor")}
          desc={t("gitSettings.coAuthorHelp")}
        >
          <Select
            className="w-64"
            aria-label={t("gitSettings.coAuthor")}
            value={
              config.git_co_author_enabled == null
                ? "inherit"
                : config.git_co_author_enabled
                  ? "on"
                  : "off"
            }
            options={[
              { value: "inherit", label: t("gitSettings.inherit") },
              { value: "on", label: t("gitSettings.on") },
              { value: "off", label: t("gitSettings.off") },
            ]}
            onChange={(value) =>
              void writes.saveConfig({
                git_co_author_enabled:
                  value === "inherit" ? null : value === "on",
              })
            }
          />
        </SettingRow>
      </div>
    </div>
  );
}
