import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";

import {
  isApiError,
  type AgentEnvVar,
  type AgentEnvVarList,
  type ApprovalMode,
  type CollaborationMode,
  type SpaceAgent,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Select,
  TextInput,
  useToast,
} from "@openfox/ui";

import { useMe } from "../../computers/queries";
import { keys, useApi } from "../../context";
import { runtimeIcon } from "../../computers/model";
import { runtimeOptions } from "../CreateAgentDialog";
import {
  argsToRows,
  isReservedEnvName,
  rowsToArgs,
  runtimeChoices,
  runtimeLabel,
  splitArgs,
  type ArgRow,
} from "../model";
import {
  agentErrorMessage,
  useDevices,
  useEnvVars,
  useOptimisticMutation,
} from "../queries";
import {
  APPROVAL_MODES,
  approvalDescKey,
  approvalNameKey,
} from "../../conversations/overrides";
import {
  COLLABORATION_MODES,
  collaborationDescKey,
  collaborationModeOf,
  collaborationNameKey,
  supportedCollaborationModes,
} from "../collaboration";
import { SettingRow } from "./parts";
import { RuntimeTestButton } from "./RuntimeTest";
import type { SectionProps } from "./BasicSections";

const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;
const ENV_LIMIT = 100;
export const MASK = "••••••••••••";

/**
 * 电脑 & Runtime：Runtime 选择（按电脑分组）、默认模型（可手动输入）、审批 / 协作模式、
 * 环境变量表（值默认遮盖，悬停可显示）与自定义参数表。
 */
export function RuntimeSection({
  orgId,
  agent,
  writes,
  setDirty,
}: SectionProps) {
  const t = useT();
  const devices = useDevices(orgId);
  const me = useMe();
  const config = agent.config;
  const choices = useMemo(
    () =>
      runtimeChoices(devices.data ?? [], agent.space_id, agent.owner_user_id),
    [devices.data, agent.space_id, agent.owner_user_id],
  );
  const currentValue = config
    ? `${config.computer_id}:${config.runtime}`
    : null;
  const current = choices.find((choice) => choice.value === currentValue);
  const options = runtimeOptions(choices, t).map((option) =>
    option.value === currentValue ? { ...option, disabled: false } : option,
  );
  if (config && !current)
    options.unshift({
      value: currentValue!,
      label: `${runtimeLabel(config.runtime)} · ${agent.computer_name ?? config.computer_id.slice(0, 8)}`,
      icon: <Icon name={runtimeIcon(config.runtime)} />,
      disabled: false,
      description: t("agents.sections.runtime.offlineRuntime"),
    });

  const models = current?.models ?? [];
  const [manualModel, setManualModel] = useState(false);
  const [modelDraft, setModelDraft] = useState(config?.model ?? "default");
  useEffect(() => setModelDraft(config?.model ?? "default"), [config?.model]);
  const modelKnown =
    modelDraft === "default" || models.some((model) => model.id === modelDraft);

  const [rows, setRows] = useState<ArgRow[]>(() =>
    argsToRows(config?.custom_args ?? []),
  );
  useEffect(
    () => setRows(argsToRows(config?.custom_args ?? [])),
    [config?.custom_args],
  );
  const argsDirty =
    JSON.stringify(rowsToArgs(rows)) !==
    JSON.stringify(config?.custom_args ?? []);
  const modelDirty = modelDraft.trim() !== (config?.model ?? "default");
  useEffect(() => {
    setDirty("runtime", argsDirty || modelDirty);
    return () => setDirty("runtime", false);
  }, [argsDirty, modelDirty, setDirty]);

  if (!config) return null;

  // 四档权限；电脑上这个 Runtime 没有上报支持的档位置灰并说明（当前已选的保持可选，避免选项消失）
  const reportedModes = devices.data
    ?.find((item) => item.id === config.computer_id)
    ?.runtimes.find((item) => item.provider === config.runtime)?.approval_modes;
  const approvalOptions = APPROVAL_MODES.map((mode) => {
    const unsupported =
      reportedModes !== undefined &&
      mode !== "FOLLOW_RUNTIME" &&
      !reportedModes.includes(mode) &&
      mode !== config.approval_mode;
    return {
      value: mode,
      label: t(approvalNameKey(mode)),
      description: unsupported
        ? t("agents.sections.runtime.approvalUnsupported", {
            runtime: runtimeLabel(config.runtime),
          })
        : t(approvalDescKey(mode)),
      disabled: unsupported,
    };
  });

  // 协作模式三选一；电脑上这个 Runtime 不支持的模式照样显示但置灰（拿不到心跳时不做禁用，只靠派发时报错）
  const savedCollaboration = collaborationModeOf(config.collaboration_mode);
  const collaborationSupported = supportedCollaborationModes(
    devices.data,
    config.computer_id,
    config.runtime,
  );
  const collaborationAllowed = (mode: CollaborationMode) =>
    collaborationSupported === undefined ||
    mode === "FOLLOW_RUNTIME" ||
    collaborationSupported.includes(mode);
  const collaborationOptions = COLLABORATION_MODES.map((mode) => {
    const unsupported = !collaborationAllowed(mode);
    return {
      value: mode,
      label: t(collaborationNameKey(mode)),
      description: unsupported
        ? t("agents.sections.runtime.collaborationUnsupported", {
            runtime: runtimeLabel(config.runtime),
          })
        : t(collaborationDescKey(mode)),
      disabled: unsupported,
    };
  });

  const pickRuntime = (value: string) => {
    const choice = choices.find((item) => item.value === value);
    if (!choice || value === currentValue) return;
    void writes.saveConfig({
      computer_id: choice.deviceId,
      grant_id: choice.grantId,
      runtime: choice.provider,
      model: "default",
    });
  };

  const updateRow = (index: number, patch: Partial<ArgRow>) =>
    setRows((list) =>
      list.map((row, i) => (i === index ? { ...row, ...patch } : row)),
    );

  /** 粘贴多个参数时自动分行 */
  const pasteArgs = (index: number, text: string) => {
    const parts = splitArgs(text);
    if (parts.length <= 2) return false;
    const pasted = argsToRows(parts);
    setRows((list) => [
      ...list.slice(0, index),
      ...pasted,
      ...list.slice(index + 1),
    ]);
    return true;
  };

  return (
    <div className="flex flex-col">
      <SettingRow label={t("agents.sections.runtime.runtime")}>
        <RuntimeTestButton
          orgId={orgId}
          device={devices.data?.find((item) => item.id === config.computer_id)}
          provider={config.runtime}
          model={config.model}
          meId={me.data?.id}
        />
        <Select
          className="w-72"
          aria-label={t("agents.sections.runtime.runtime")}
          value={currentValue}
          onChange={pickRuntime}
          options={options}
        />
      </SettingRow>
      <SettingRow label={t("agents.sections.runtime.model")}>
        <div className="flex w-72 flex-col gap-2">
          {manualModel || !modelKnown ? (
            <div className="flex gap-2">
              <TextInput
                aria-label={t("agents.sections.runtime.modelManual")}
                placeholder={t(
                  "agents.sections.runtime.modelManualPlaceholder",
                )}
                value={modelDraft === "default" ? "" : modelDraft}
                maxLength={128}
                onChange={(event) => setModelDraft(event.target.value)}
              />
              <Button
                size="32"
                disabled={!modelDirty || !modelDraft.trim()}
                onClick={() =>
                  void writes
                    .saveConfig({ model: modelDraft.trim() })
                    .then(() => setManualModel(false))
                }
              >
                {t("agents.sections.runtime.save")}
              </Button>
            </div>
          ) : (
            <Select
              aria-label={t("agents.sections.runtime.model")}
              value={modelDraft}
              onChange={(value) => {
                if (value === "__manual") {
                  setManualModel(true);
                  return;
                }
                setModelDraft(value);
                void writes.saveConfig({ model: value });
              }}
              options={[
                {
                  value: "default",
                  label: `${t("agents.sections.runtime.modelDefault")} · ${t("agents.sections.runtime.modelDefaultDesc")}`,
                },
                ...models
                  .filter((model) => model.id !== "default")
                  .map((model) => ({
                    value: model.id,
                    label: model.display_name || model.id,
                  })),
                {
                  value: "__manual",
                  label: t("agents.sections.runtime.modelManual"),
                  icon: <Icon name="pencil" />,
                },
              ]}
            />
          )}
          {modelDraft === "default" && (
            <Callout tone="success">
              {t("agents.sections.runtime.modelHint")}
            </Callout>
          )}
        </div>
      </SettingRow>
      <SettingRow label={t("agents.sections.runtime.approval")}>
        <Select
          className="w-72"
          aria-label={t("agents.sections.runtime.approval")}
          value={config.approval_mode ?? "FOLLOW_RUNTIME"}
          onChange={(value) => {
            if (value !== config.approval_mode)
              void writes.saveConfig({ approval_mode: value as ApprovalMode });
          }}
          options={approvalOptions}
        />
      </SettingRow>
      <SettingRow label={t("agents.sections.runtime.collaboration")}>
        <div className="flex w-72 flex-col gap-2">
          <Select
            aria-label={t("agents.sections.runtime.collaboration")}
            value={savedCollaboration}
            onChange={(value) => {
              if (value !== savedCollaboration)
                void writes.saveConfig({ collaboration_mode: value });
            }}
            options={collaborationOptions}
          />
          {savedCollaboration === null ? (
            <Callout tone="warning">
              {t("agents.sections.runtime.collaborationSavedUnknown", {
                mode: config.collaboration_mode,
              })}
            </Callout>
          ) : (
            !collaborationAllowed(savedCollaboration) && (
              <Callout tone="warning">
                {t("agents.sections.runtime.collaborationSavedUnsupported", {
                  runtime: runtimeLabel(config.runtime),
                  mode: t(collaborationNameKey(savedCollaboration)),
                })}
              </Callout>
            )
          )}
        </div>
      </SettingRow>

      <EnvVarsTable orgId={orgId} agent={agent} />

      <div className="flex flex-col gap-2 pt-4">
        <div>
          <h4 className="text-body font-medium">
            {t("agents.sections.runtime.args")}
          </h4>
          <p className="text-caption text-fg-muted">
            {t("agents.sections.runtime.argsDesc")}
          </p>
        </div>
        <div className="overflow-hidden rounded-lg border border-line-default">
          <div className="grid grid-cols-[1fr_1fr_36px] bg-surface-muted text-caption text-fg-muted">
            <span className="px-3 py-1.5">
              {t("agents.sections.runtime.argKey")}
            </span>
            <span className="border-l border-line-default px-3 py-1.5">
              {t("agents.sections.runtime.argValue")}
            </span>
            <span />
          </div>
          {rows.map((row, index) => (
            <div
              key={index}
              className="grid grid-cols-[1fr_1fr_36px] items-center border-t border-line-default"
            >
              <input
                aria-label={`${t("agents.sections.runtime.argKey")} ${index + 1}`}
                value={row.key}
                onChange={(event) =>
                  updateRow(index, { key: event.target.value })
                }
                onPaste={(event) => {
                  if (pasteArgs(index, event.clipboardData.getData("text")))
                    event.preventDefault();
                }}
                className="h-9 bg-transparent px-3 font-mono text-caption outline-none"
              />
              <input
                aria-label={`${t("agents.sections.runtime.argValue")} ${index + 1}`}
                value={row.value}
                onChange={(event) =>
                  updateRow(index, { value: event.target.value })
                }
                className="h-9 border-l border-line-default bg-transparent px-3 font-mono text-caption outline-none"
              />
              <IconButton
                label={t("agents.sections.runtime.argRemove")}
                icon={<Icon name="x" size={14} />}
                size="28"
                onClick={() =>
                  setRows((list) => list.filter((_, i) => i !== index))
                }
              />
            </div>
          ))}
        </div>
        <div className="flex items-center justify-between">
          <Button
            variant="outline"
            size="28"
            leading={<Icon name="plus" />}
            disabled={rows.length >= 32}
            onClick={() => setRows((list) => [...list, { key: "", value: "" }])}
          >
            {t("agents.sections.runtime.add")}
          </Button>
          <Button
            size="28"
            disabled={!argsDirty}
            loading={writes.configPending}
            onClick={() =>
              void writes.saveConfig({ custom_args: rowsToArgs(rows) })
            }
          >
            {t("agents.sections.runtime.save")}
          </Button>
        </div>
      </div>
    </div>
  );
}

/**
 * 环境变量表：有查看权限时值默认遮盖、悬停或聚焦显示原值；无权限时整列遮盖并提示；
 * 未配置主密钥时提示并禁止添加 / 修改。
 */
export function EnvVarsTable({
  orgId,
  agent,
}: {
  orgId: string;
  agent: SpaceAgent;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const env = useEnvVars(agent, orgId);
  const queryKey = keys.agentSub(orgId, agent.id, "env-vars");
  const [revealed, setRevealed] = useState<string | null>(null);
  const [editing, setEditing] = useState<
    null | { mode: "new" } | { mode: "edit"; item: AgentEnvVar }
  >(null);
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);

  const remove = useOptimisticMutation<string, void, AgentEnvVarList>(orgId, {
    queryKey,
    mutationFn: (varName) =>
      api.agents.envVars.remove(orgId, agent.space_id, agent.id, varName),
    optimistic: (data, varName) => ({
      ...data,
      env_vars: data.env_vars.filter((item) => item.name !== varName),
    }),
  });

  const data = env.data;
  const canView = data?.can_view_values ?? false;
  const writable = canView && (data?.secrets_available ?? false);
  const list = data?.env_vars ?? [];

  const startNew = () => {
    setEditing({ mode: "new" });
    setName("");
    setValue("");
    setError(null);
  };

  const save = async () => {
    if (!editing) return;
    const varName = editing.mode === "new" ? name.trim() : editing.item.name;
    if (!ENV_NAME.test(varName)) {
      setError(t("agents.sections.runtime.envNameInvalid"));
      return;
    }
    try {
      const saved = await api.agents.envVars.put(
        orgId,
        agent.space_id,
        agent.id,
        varName,
        value,
        editing.mode === "edit" ? editing.item.version : undefined,
      );
      queryClient.setQueryData<AgentEnvVarList>(queryKey, (old) =>
        old
          ? {
              ...old,
              env_vars: [
                ...old.env_vars.filter((item) => item.name !== saved.name),
                saved,
              ].sort((a, b) => a.name.localeCompare(b.name)),
            }
          : old,
      );
      setEditing(null);
      toast(t("agents.toast.saved"));
    } catch (err) {
      if (isApiError(err, "NAME_CONFLICT"))
        setError(t("agents.sections.runtime.envNameTaken"));
      else {
        if (isApiError(err) && err.status === 409)
          void queryClient.invalidateQueries({ queryKey });
        toast(
          isApiError(err, "VERSION_CONFLICT")
            ? t("agents.toast.conflict")
            : agentErrorMessage(t, err),
          { tone: "error" },
        );
      }
    }
  };

  return (
    <div className="flex flex-col gap-2 border-t border-line-soft pt-4">
      <div>
        <h4 className="text-body font-medium">
          {t("agents.sections.runtime.env")}
        </h4>
        <p className="text-caption text-fg-muted">
          {t("agents.sections.runtime.envDesc")}
        </p>
      </div>
      {data && !canView && (
        <Callout tone="info">{t("agents.sections.runtime.envHidden")}</Callout>
      )}
      {data && canView && !data.secrets_available && (
        <Callout tone="warning">
          {t("agents.sections.runtime.envSecretsUnavailable")}
        </Callout>
      )}
      <div className="overflow-hidden rounded-lg border border-line-default">
        <div className="grid grid-cols-[1fr_1fr_36px] bg-surface-muted text-caption text-fg-muted">
          <span className="px-3 py-1.5">
            {t("agents.sections.runtime.envName")}
          </span>
          <span className="border-l border-line-default px-3 py-1.5">
            {t("agents.sections.runtime.envValue")}
          </span>
          <span />
        </div>
        {list.map((item) => {
          const show = canView && revealed === item.name && item.value !== null;
          return (
            <div
              key={item.name}
              className="grid grid-cols-[1fr_1fr_36px] items-center border-t border-line-default"
            >
              <span className="truncate px-3 py-2 font-mono text-caption">
                {item.name}
              </span>
              <span
                tabIndex={canView ? 0 : undefined}
                aria-label={
                  canView
                    ? t("agents.sections.runtime.envReveal", {
                        name: item.name,
                      })
                    : undefined
                }
                data-testid={`env-value-${item.name}`}
                onMouseEnter={() => setRevealed(item.name)}
                onMouseLeave={() => setRevealed(null)}
                onFocus={() => setRevealed(item.name)}
                onBlur={() => setRevealed(null)}
                className="truncate border-l border-line-default px-3 py-2 font-mono text-caption"
              >
                {show ? item.value : MASK}
              </span>
              {writable ? (
                <Menu
                  label={t("agents.sections.runtime.envMenu", {
                    name: item.name,
                  })}
                  placement="bottom-end"
                  trigger={({ ref, toggle }) => (
                    <IconButton
                      ref={ref}
                      label={t("agents.sections.runtime.envMenu", {
                        name: item.name,
                      })}
                      icon={<Icon name="ellipsis" size={14} />}
                      size="28"
                      onClick={toggle}
                    />
                  )}
                >
                  <MenuItem
                    icon={<Icon name="pencil" />}
                    onSelect={() => {
                      setEditing({ mode: "edit", item });
                      setValue(item.value ?? "");
                      setError(null);
                    }}
                  >
                    {t("agents.sections.runtime.envEdit")}
                  </MenuItem>
                  <MenuItem
                    danger
                    icon={<Icon name="trash-2" />}
                    onSelect={() => remove.mutate(item.name)}
                  >
                    {t("agents.sections.runtime.envDelete")}
                  </MenuItem>
                </Menu>
              ) : (
                <span />
              )}
            </div>
          );
        })}
        {editing && (
          <div className="flex flex-col gap-1 border-t border-line-default p-2">
            <div className="flex items-center gap-2">
              <TextInput
                className="flex-1 font-mono"
                aria-label={t("agents.sections.runtime.envName")}
                value={editing.mode === "edit" ? editing.item.name : name}
                disabled={editing.mode === "edit"}
                autoFocus={editing.mode === "new"}
                invalid={Boolean(error)}
                onChange={(event) => {
                  setName(event.target.value);
                  setError(null);
                }}
              />
              <TextInput
                className="flex-1 font-mono"
                aria-label={t("agents.sections.runtime.envValue")}
                value={value}
                maxLength={8192}
                autoFocus={editing.mode === "edit"}
                onChange={(event) => setValue(event.target.value)}
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
            {error && (
              <p role="alert" className="text-caption text-danger">
                {error}
              </p>
            )}
            {/* 中心不拒绝保留名，这里只提示：电脑运行时会忽略 */}
            {!error &&
              isReservedEnvName(
                (editing.mode === "edit" ? editing.item.name : name).trim(),
              ) && (
                <p className="text-caption text-warning">
                  {t("connect.env.reserved", {
                    name: (editing.mode === "edit"
                      ? editing.item.name
                      : name
                    ).trim(),
                  })}
                </p>
              )}
          </div>
        )}
      </div>
      <Button
        variant="outline"
        size="28"
        className="self-start"
        leading={<Icon name="plus" />}
        disabled={!writable || list.length >= ENV_LIMIT}
        title={
          list.length >= ENV_LIMIT
            ? t("agents.sections.runtime.envLimit")
            : undefined
        }
        onClick={startNew}
      >
        {t("agents.sections.runtime.add")}
      </Button>
    </div>
  );
}
