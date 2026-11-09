import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";

import type { AgentConfigVersion, SpaceAgent } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Icon,
  Select,
  Switch,
  TextInput,
  Textarea,
  Tooltip,
  type IconName,
} from "@openfox/ui";

import {
  QUEUE_TIMEOUT_HOURS,
  RUN_TIMEOUT_MINUTES,
  hoursToSeconds,
  minutesToSeconds,
  parseDirectoryRoots,
  parseInRange,
  secondsToHours,
  secondsToMinutes,
  validDirectoryRoot,
} from "../model";
import { SettingRow } from "./parts";
import type { useAgentWrites } from "./writes";
import { AgentImDialog } from "./AgentImDialog";

type Writes = ReturnType<typeof useAgentWrites>;

export interface SectionProps {
  orgId: string;
  agent: SpaceAgent;
  writes: Writes;
  /** 登记未保存的修改（离开时确认） */
  setDirty: (section: string, dirty: boolean) => void;
}

/** 与草稿比较后报告是否有未保存修改 */
function useDirtyReport(
  section: string,
  dirty: boolean,
  setDirty: SectionProps["setDirty"],
) {
  useEffect(() => {
    setDirty(section, dirty);
    return () => setDirty(section, false);
  }, [section, dirty, setDirty]);
}

/** Agent 指令：字数统计，保存时新建配置版本；自动翻译即将开放。 */
export function InstructionsSection({ agent, writes, setDirty }: SectionProps) {
  const t = useT();
  const saved = agent.config?.instructions ?? "";
  const [draft, setDraft] = useState(saved);
  useEffect(() => setDraft(saved), [saved]);
  const dirty = draft !== saved;
  useDirtyReport("instructions", dirty, setDirty);
  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-3">
        <span className="flex-1 text-caption text-fg-muted">
          {t("agents.sections.instructions.chars", { count: draft.length })}
        </span>
        <Tooltip content={t("agents.comingSoon")}>
          <span className="flex items-center gap-2 text-caption text-fg-muted">
            {t("agents.sections.instructions.translate")}
            <Switch
              checked={false}
              disabled
              label={t("agents.sections.instructions.translate")}
            />
          </span>
        </Tooltip>
        <Button
          size="28"
          disabled={!dirty || !agent.config}
          loading={writes.configPending}
          onClick={() => void writes.saveConfig({ instructions: draft })}
        >
          {t("agents.sections.instructions.save")}
        </Button>
      </div>
      <Textarea
        rows={12}
        aria-label={t("agents.sections.instructions.title")}
        value={draft}
        maxLength={100000}
        disabled={!agent.config}
        placeholder={t("agents.sections.instructions.placeholder")}
        onChange={(event) => setDraft(event.target.value)}
        className="bg-surface-canvas"
      />
    </div>
  );
}

const TRIGGERS: Array<{
  field:
    | "allow_assign"
    | "allow_direct_chat"
    | "trigger_crew"
    | "trigger_mention"
    | "trigger_workflow";
  label: "assign" | "chat" | "crew" | "mention" | "workflow";
  icon: IconName;
}> = [
  { field: "allow_assign", label: "assign", icon: "square-check" },
  { field: "allow_direct_chat", label: "chat", icon: "message" },
  { field: "trigger_crew", label: "crew", icon: "users" },
  { field: "trigger_mention", label: "mention", icon: "at-sign" },
  { field: "trigger_workflow", label: "workflow", icon: "workflow" },
];

export function TriggersSection({ orgId, agent, writes }: SectionProps) {
  const t = useT();
  const [integrating, setIntegrating] = useState(false);
  return (
    <div className="flex flex-col gap-3">
      <h4 className="text-body font-medium">
        {t("agents.sections.triggers.internal")}
      </h4>
      <ul className="flex flex-col gap-2">
        {TRIGGERS.map((item) => (
          <li key={item.field} className="flex items-center gap-2 text-body">
            <Icon name={item.icon} className="text-fg-muted" />
            <span className="flex-1">
              {t(`agents.sections.triggers.${item.label}`)}
            </span>
            <Switch
              tone="success"
              checked={agent[item.field]}
              label={t(`agents.sections.triggers.${item.label}`)}
              onCheckedChange={(checked) =>
                void writes.patchProfile({ [item.field]: checked }, true)
              }
            />
          </li>
        ))}
      </ul>
      <Button
        variant="outline"
        size="28"
        className="self-start"
        disabled={!!agent.archived_at}
        leading={<Icon name="plus" />}
        onClick={() => setIntegrating(true)}
      >
        {t("agents.sections.triggers.addIm")}
      </Button>
      {integrating && (
        <AgentImDialog
          key={agent.id}
          orgId={orgId}
          agentId={agent.id}
          name={agent.name}
          onClose={() => setIntegrating(false)}
        />
      )}
    </div>
  );
}

interface RunDraft {
  directory: "temp" | "explicit";
  roots: string;
  isolation: AgentConfigVersion["workdir_isolation"];
  taskMode: "default" | "manual";
  taskValue: string;
  chatMode: AgentConfigVersion["chat_concurrency"];
  chatValue: string;
  runMinutes: string;
  queueHours: string;
}

const TASK_RANGE = { min: 1, max: 16 } as const;
const CHAT_RANGE = { min: 1, max: 64 } as const;

function runDraftFrom(config: AgentConfigVersion): RunDraft {
  return {
    directory: config.directory_mode === "EXPLICIT" ? "explicit" : "temp",
    roots: config.directory_roots.join("\n"),
    isolation: config.workdir_isolation,
    taskMode: config.max_concurrency === 1 ? "default" : "manual",
    taskValue: String(config.max_concurrency),
    chatMode: config.chat_concurrency,
    chatValue: String(config.chat_concurrency_value ?? 10),
    runMinutes: String(secondsToMinutes(config.run_timeout_seconds)),
    queueHours: String(secondsToHours(config.queue_timeout_seconds)),
  };
}

/**
 * 运行设置：工作目录、隔离方式、任务 / 聊天并发、单任务时长（分钟 1–480）、排队超时（小时 0.25–24），
 * 保存时换算成秒并新建配置版本。
 */
export function RunSection({
  orgId,
  agent,
  writes,
  setDirty,
  deviceSlots,
}: SectionProps & { deviceSlots: number | null }) {
  const t = useT();
  const navigate = useNavigate();
  const config = agent.config;
  const [draft, setDraft] = useState<RunDraft | null>(() =>
    config ? runDraftFrom(config) : null,
  );
  useEffect(() => {
    if (config) setDraft(runDraftFrom(config));
  }, [config]);
  const base = config ? runDraftFrom(config) : null;
  const dirty =
    draft !== null &&
    base !== null &&
    JSON.stringify(draft) !== JSON.stringify(base);
  useDirtyReport("run", dirty, setDirty);
  if (!config || !draft) return null;

  const set = (patch: Partial<RunDraft>) =>
    setDraft((prev) => (prev ? { ...prev, ...patch } : prev));
  const roots = parseDirectoryRoots(draft.roots);
  const rootsBad =
    draft.directory === "explicit" &&
    (roots.length === 0 ||
      roots.length > 32 ||
      !roots.every(validDirectoryRoot));
  const taskValue =
    draft.taskMode === "manual" ? parseInRange(draft.taskValue, TASK_RANGE) : 1;
  const chatValue =
    draft.chatMode === "MANUAL"
      ? parseInRange(draft.chatValue, CHAT_RANGE)
      : null;
  const minutes = parseInRange(draft.runMinutes, RUN_TIMEOUT_MINUTES);
  const hours = parseInRange(draft.queueHours, QUEUE_TIMEOUT_HOURS);
  const invalid =
    rootsBad ||
    taskValue === null ||
    (draft.chatMode === "MANUAL" && chatValue === null) ||
    minutes === null ||
    hours === null;

  const save = () => {
    if (invalid || minutes === null || hours === null || taskValue === null)
      return;
    void writes.saveConfig({
      directory_mode:
        draft.directory === "explicit"
          ? "EXPLICIT"
          : config.directory_mode === "EXPLICIT"
            ? "WORKSPACE"
            : config.directory_mode,
      directory_roots: draft.directory === "explicit" ? roots : [],
      workdir_isolation: draft.isolation,
      max_concurrency: taskValue,
      chat_concurrency: draft.chatMode,
      chat_concurrency_value: draft.chatMode === "MANUAL" ? chatValue : null,
      run_timeout_seconds: minutesToSeconds(minutes),
      queue_timeout_seconds: hoursToSeconds(hours),
    });
  };

  return (
    <div className="flex flex-col">
      <SettingRow
        label={t("agents.sections.run.workdir")}
        desc={t("agents.sections.run.workdirDesc")}
      >
        <Select
          className="w-44"
          aria-label={t("agents.sections.run.workdir")}
          value={draft.directory}
          onChange={(directory) => set({ directory })}
          options={[
            { value: "temp", label: t("agents.sections.run.workdirTemp") },
            {
              value: "explicit",
              label: t("agents.sections.run.workdirExplicit"),
            },
          ]}
        />
      </SettingRow>
      {draft.directory === "explicit" && (
        <div className="flex flex-col gap-1 border-b border-line-soft py-3">
          <Textarea
            rows={2}
            aria-label={t("agents.sections.run.roots")}
            placeholder={t("agents.create.rootsPlaceholder")}
            value={draft.roots}
            invalid={rootsBad}
            onChange={(event) => set({ roots: event.target.value })}
          />
          {rootsBad && (
            <p role="alert" className="text-caption text-danger">
              {t("agents.create.rootsInvalid")}
            </p>
          )}
        </div>
      )}
      <SettingRow
        label={t("agents.sections.run.isolation")}
        desc={t("agents.sections.run.isolationDesc")}
      >
        <Select
          className="w-44"
          aria-label={t("agents.sections.run.isolation")}
          value={draft.isolation}
          onChange={(isolation) => set({ isolation })}
          options={[
            {
              value: "WORKTREE",
              label: t("agents.sections.run.isolationWorktree"),
            },
            {
              value: "DIRECT",
              label: t("agents.sections.run.isolationDirect"),
            },
          ]}
        />
      </SettingRow>
      <SettingRow
        label={t("agents.sections.run.taskConcurrency")}
        desc={t("agents.sections.run.taskConcurrencyDesc")}
      >
        <div className="flex items-center gap-2">
          <Select
            className="w-32"
            aria-label={t("agents.sections.run.taskConcurrency")}
            value={draft.taskMode}
            onChange={(taskMode) =>
              set({
                taskMode,
                ...(taskMode === "default" ? { taskValue: "1" } : {}),
              })
            }
            options={[
              {
                value: "default",
                label: t("agents.sections.run.defaultValue", { value: 1 }),
              },
              { value: "manual", label: t("agents.sections.run.manual") },
            ]}
          />
          {draft.taskMode === "manual" && (
            <TextInput
              className="w-20"
              type="number"
              min={TASK_RANGE.min}
              max={TASK_RANGE.max}
              aria-label={t("agents.sections.run.taskConcurrency")}
              value={draft.taskValue}
              invalid={taskValue === null}
              onChange={(event) => set({ taskValue: event.target.value })}
            />
          )}
        </div>
      </SettingRow>
      <SettingRow
        label={t("agents.sections.run.chatConcurrency")}
        desc={t("agents.sections.run.chatConcurrencyDesc")}
      >
        <div className="flex items-center gap-2">
          <Select
            className="w-36"
            aria-label={t("agents.sections.run.chatConcurrency")}
            value={draft.chatMode}
            onChange={(chatMode) => set({ chatMode })}
            options={[
              { value: "DEFAULT", label: t("agents.sections.run.default") },
              {
                value: "SHARE_TASK",
                label: t("agents.sections.run.shareTask"),
              },
              { value: "MANUAL", label: t("agents.sections.run.manual") },
            ]}
          />
          {draft.chatMode === "MANUAL" && (
            <TextInput
              className="w-20"
              type="number"
              min={CHAT_RANGE.min}
              max={CHAT_RANGE.max}
              aria-label={t("agents.sections.run.chatConcurrency")}
              value={draft.chatValue}
              invalid={chatValue === null}
              onChange={(event) => set({ chatValue: event.target.value })}
            />
          )}
        </div>
      </SettingRow>
      <SettingRow
        label={t("agents.sections.run.runTimeout")}
        desc={t("agents.sections.run.runTimeoutRange")}
      >
        <div className="flex items-center gap-2">
          <TextInput
            className="w-28"
            type="number"
            min={RUN_TIMEOUT_MINUTES.min}
            max={RUN_TIMEOUT_MINUTES.max}
            step={1}
            aria-label={t("agents.sections.run.runTimeout")}
            value={draft.runMinutes}
            invalid={minutes === null}
            onChange={(event) => set({ runMinutes: event.target.value })}
          />
          <span className="w-8 text-body-sm text-fg-muted">
            {t("agents.sections.run.minutes")}
          </span>
        </div>
      </SettingRow>
      <SettingRow
        label={t("agents.sections.run.queueTimeout")}
        desc={t("agents.sections.run.queueTimeoutRange")}
      >
        <div className="flex items-center gap-2">
          <TextInput
            className="w-28"
            type="number"
            min={QUEUE_TIMEOUT_HOURS.min}
            max={QUEUE_TIMEOUT_HOURS.max}
            step={0.25}
            aria-label={t("agents.sections.run.queueTimeout")}
            value={draft.queueHours}
            invalid={hours === null}
            onChange={(event) => set({ queueHours: event.target.value })}
          />
          <span className="w-8 text-body-sm text-fg-muted">
            {t("agents.sections.run.hours")}
          </span>
        </div>
      </SettingRow>
      <div className="flex items-center justify-between pt-3">
        {deviceSlots !== null ? (
          <button
            type="button"
            onClick={() =>
              navigate(
                `/o/${orgId}/settings/computers?computer=${encodeURIComponent(config.computer_id)}`,
              )
            }
            className="text-caption text-fg-muted underline hover:text-fg-default"
          >
            {t("agents.sections.run.deviceLimit", { value: deviceSlots })}
          </button>
        ) : (
          <span />
        )}
        <div className="flex items-center gap-2">
          {invalid && (
            <span className="text-caption text-danger">
              {t("agents.sections.run.invalid")}
            </span>
          )}
          <Button
            size="28"
            disabled={!dirty || invalid}
            loading={writes.configPending}
            onClick={save}
          >
            {t("agents.sections.run.save")}
          </Button>
        </div>
      </div>
    </div>
  );
}
