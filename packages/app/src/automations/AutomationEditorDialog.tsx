import { useState } from "react";

import { useQuery } from "@tanstack/react-query";

import type {
  Automation,
  AutomationInput,
  AutomationSaveResult,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Dialog,
  Field,
  Icon,
  Textarea,
  TextInput,
  cn,
  useToast,
  type SearchMenuGroup,
} from "@openfox/ui";

import { AgentAvatar } from "../agents/AgentAvatar";
import { keys, useApi } from "../context";
import { useSpaceDetail } from "../spaces/queries";
import {
  useAgentDirectory,
  usePickerLabels,
  usePeople,
  useSpaceAgents,
  useSpaceTypes,
  useStatuses,
} from "../tasks/queries";
import { useProjects } from "../projects/queries";
import { Picker } from "../tasks/pickers";
import type { Person } from "../tasks/queries";
import { TriggerEditor } from "./TriggerEditor";
import type { TriggerOptions } from "./StatusEditor";
import {
  MAX_TRIGGERS,
  draftsFromAutomation,
  isAutomationReady,
  newTriggerDraft,
  triggerInput,
  type TriggerDraft,
} from "./model";
import { browserTimezone } from "./ScheduleEditor";
import { automationErrorMessage, useSaveAutomation } from "./queries";

// 新建 / 编辑自动化弹窗；Webhook URL 在单独的小窗展示。
// 960×720，点遮罩不关闭；创建或保存后若返回了 webhook_urls，切换为 400 宽小窗一次性展示。

interface Assignee {
  kind: "AGENT" | "CREW";
  id: string;
  name: string;
}

interface UrlSheet {
  urls: { trigger_id: string; url: string }[];
  /** 创建或保存后关闭弹窗；重新生成 URL 时只叠加小窗 */
  closeAfter: boolean;
}

let keySeq = 0;
const nextKey = () => `draft-${++keySeq}`;

/** 弹窗关闭时整体卸载，下次打开时表单从服务端数据重新初始化。 */
export function AutomationEditorDialog({
  orgId,
  spaceId,
  automation,
  open,
  onClose,
}: {
  orgId: string;
  spaceId: string;
  /** 有值为编辑，无值为新建 */
  automation?: Automation;
  open: boolean;
  onClose: () => void;
}) {
  if (!open) return null;
  return (
    <AutomationEditorBody
      key={automation?.id ?? "new"}
      orgId={orgId}
      spaceId={spaceId}
      automation={automation}
      onClose={onClose}
    />
  );
}

function AutomationEditorBody({
  orgId,
  spaceId,
  automation,
  onClose,
}: {
  orgId: string;
  spaceId: string;
  automation?: Automation;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const timezone = browserTimezone();
  const space = useSpaceDetail(orgId, spaceId);
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  const agents = useSpaceAgents(orgId, spaceId);
  const directory = useAgentDirectory(orgId);
  const people: Person[] = usePeople(orgId, space.data);
  const statuses = useStatuses(orgId);
  const types = useSpaceTypes(orgId, spaceId);
  const projects = useProjects(orgId, spaceId, {
    enabled: Boolean(space.data?.features.projects),
  });
  const labels = usePickerLabels(orgId, spaceId);
  const save = useSaveAutomation(orgId, spaceId, automation?.id);

  const [title, setTitle] = useState(automation?.title ?? "");
  const [prompt, setPrompt] = useState(automation?.prompt ?? "");
  const [assignee, setAssignee] = useState<Assignee | null>(
    automation?.assignee ?? null,
  );
  const [notify, setNotify] = useState<string[]>(
    automation?.notify_user_ids ?? (me.data ? [me.data.id] : []),
  );
  const [triggers, setTriggers] = useState<TriggerDraft[]>(() =>
    automation
      ? draftsFromAutomation(automation, timezone)
      : [newTriggerDraft("schedule", nextKey(), timezone)],
  );
  const [sheet, setSheet] = useState<UrlSheet | null>(null);

  const crews = (directory.data?.entries ?? []).filter(
    (entry) =>
      entry.type === "crew" && entry.space_id === spaceId && !entry.archived_at,
  );
  const options: TriggerOptions = {
    statuses: statuses.data ?? [],
    types: types.data ?? [],
    projects: projects.data?.projects ?? [],
    labels: labels.data ?? [],
    people,
    agents: agents.data ?? [],
  };
  // 未开启直接对话的 Agent 不能用于自动化（后端保存时同样拒绝）
  const noDirectChat = t("automations.form.assigneeNoDirectChat");
  const assigneeGroups: SearchMenuGroup[] = [
    {
      id: "agents",
      label: t("automations.form.agentGroup"),
      options: (agents.data ?? []).map((agent) => ({
        value: `AGENT:${agent.id}`,
        label: agent.name,
        icon: <AgentAvatar name={agent.name} size={16} />,
        // 不可指派的 Agent 禁用并说明原因
        disabled: !agent.allow_assign || !agent.allow_direct_chat,
        description: !agent.allow_direct_chat
          ? noDirectChat
          : agent.allow_assign
            ? undefined
            : t("automations.form.assigneeDisabled"),
      })),
    },
  ];
  if (crews.length > 0)
    assigneeGroups.push({
      id: "crews",
      label: t("automations.form.crewGroup"),
      options: crews.map((crew) => {
        // 小队的直接对话取决于队长（队长不在空间 Agent 列表里时不禁用）
        const leader = (agents.data ?? []).find(
          (agent) => agent.id === crew.leader?.id,
        );
        const blocked = leader !== undefined && !leader.allow_direct_chat;
        return {
          value: `CREW:${crew.id}`,
          label: crew.name,
          icon: <AgentAvatar name={crew.name} size={16} />,
          disabled: blocked,
          description: blocked ? noDirectChat : undefined,
        };
      }),
    });
  const peopleGroups: SearchMenuGroup[] = [
    {
      options: people.map((person) => ({
        value: person.id,
        label: person.name || person.email,
        description: person.name ? person.email : undefined,
      })),
    },
  ];

  const ready = isAutomationReady({
    title,
    assigneeId: assignee?.id ?? null,
    triggers,
  });

  const submit = () => {
    if (!ready || !assignee) return;
    const payload: AutomationInput = {
      title: title.trim(),
      prompt,
      assignee: { kind: assignee.kind, id: assignee.id },
      notify_user_ids: notify,
      triggers: triggers.map(triggerInput),
    };
    save.mutate(
      automation ? { ...payload, version: automation.version } : payload,
      {
        onSuccess: (result: AutomationSaveResult) => {
          if (result.webhook_urls.length > 0) {
            setSheet({ urls: result.webhook_urls, closeAfter: true });
            return;
          }
          toast(
            automation
              ? t("automations.form.saved")
              : t("automations.form.created"),
          );
          onClose();
        },
        onError: (error) =>
          toast(
            automationErrorMessage(
              t,
              error,
              automation
                ? "automations.form.saveFailed"
                : "automations.form.createFailed",
            ),
            { tone: "error" },
          ),
      },
    );
  };

  const sheetClosesEditor = sheet?.closeAfter === true;

  return (
    <>
      <Dialog
        open={!sheetClosesEditor}
        onClose={onClose}
        closeOnBackdrop={false}
        width={960}
        title={
          automation ? t("automations.editTitle") : t("automations.create")
        }
        className="h-[720px]"
        footer={
          <>
            <Button variant="secondary" onClick={onClose}>
              {t("automations.cancel")}
            </Button>
            <Button
              disabled={!ready || save.isPending}
              loading={save.isPending}
              onClick={submit}
            >
              {automation
                ? t("automations.save")
                : t("automations.form.submitCreate")}
            </Button>
          </>
        }
      >
        <div className="flex h-full min-h-0 gap-6">
          {/* 左栏 520：标题、提示词、指派给 */}
          <div className="flex w-[520px] shrink-0 flex-col gap-4 overflow-y-auto">
            <Field label={t("automations.form.titleLabel")} required>
              <TextInput
                autoFocus
                value={title}
                placeholder={t("automations.form.titlePlaceholder")}
                onChange={(event) => setTitle(event.target.value)}
              />
            </Field>
            <Field
              label={t("automations.form.promptLabel")}
              className="flex min-h-0 flex-1 flex-col"
            >
              <Textarea
                value={prompt}
                rows={12}
                placeholder={t("automations.form.promptPlaceholder")}
                className="min-h-[220px] flex-1"
                onChange={(event) => setPrompt(event.target.value)}
              />
            </Field>
            <Field label={t("automations.form.assigneeLabel")} required>
              <Picker
                label={t("automations.form.assigneeLabel")}
                placement="top-start"
                className="!w-56"
                groups={assigneeGroups}
                selected={assignee ? [`${assignee.kind}:${assignee.id}`] : []}
                placeholder={t("automations.form.assigneeSearch")}
                onSelect={(value) => {
                  const [kind, id = ""] = value.split(":") as [
                    "AGENT" | "CREW",
                    string,
                  ];
                  const source =
                    kind === "AGENT"
                      ? agents.data?.find((agent) => agent.id === id)
                      : crews.find((crew) => crew.id === id);
                  if (source) setAssignee({ kind, id, name: source.name });
                }}
                trigger={({ ref, toggle }) => (
                  <button
                    ref={ref}
                    type="button"
                    onClick={toggle}
                    className="flex h-9 w-full items-center gap-2 rounded-lg border border-line-default bg-surface-card px-3 text-left text-body hover:bg-surface-hover"
                  >
                    {assignee ? (
                      <>
                        <AgentAvatar name={assignee.name} size={16} />
                        <span className="min-w-0 flex-1 truncate">
                          {assignee.name}
                        </span>
                      </>
                    ) : (
                      <span className="min-w-0 flex-1 truncate text-fg-subtle">
                        {t("automations.form.assigneePlaceholder")}
                      </span>
                    )}
                    <Icon
                      name="chevron-down"
                      size={14}
                      className="text-fg-muted"
                    />
                  </button>
                )}
              />
            </Field>
          </div>

          {/* 右栏：触发器与通知人员 */}
          <div className="flex min-w-0 flex-1 flex-col gap-3 overflow-y-auto">
            <div className="flex items-center justify-between">
              <span className="text-body font-medium">
                {t("automations.form.triggersLabel")}
              </span>
              <span className="text-caption text-fg-muted">
                {t("automations.form.triggersLimit")}
              </span>
            </div>
            {triggers.map((draft, index) => (
              <TriggerEditor
                key={draft.key}
                draft={draft}
                index={index}
                timezone={timezone}
                orgId={orgId}
                spaceId={spaceId}
                automationId={automation?.id}
                options={options}
                onChange={(next) =>
                  setTriggers((list) =>
                    list.map((item) => (item.key === draft.key ? next : item)),
                  )
                }
                onRemove={() =>
                  setTriggers((list) =>
                    list.filter((item) => item.key !== draft.key),
                  )
                }
                onUrlRegenerated={(triggerId, url) =>
                  setSheet({
                    urls: [{ trigger_id: triggerId, url }],
                    closeAfter: false,
                  })
                }
              />
            ))}
            <button
              type="button"
              disabled={triggers.length >= MAX_TRIGGERS}
              onClick={() =>
                setTriggers((list) => [
                  ...list,
                  newTriggerDraft("schedule", nextKey(), timezone),
                ])
              }
              className={cn(
                "flex h-10 w-full items-center justify-center gap-1.5 rounded-xl border border-dashed border-line-default text-body text-fg-secondary hover:bg-surface-hover",
                "disabled:cursor-not-allowed disabled:opacity-50",
              )}
            >
              <Icon name="plus" size={14} />
              {t("automations.form.addTrigger")}
            </button>

            <Field
              label={t("automations.form.notifyLabel")}
              helper={t("automations.form.notifyHint")}
            >
              <Picker
                label={t("automations.form.notifyLabel")}
                groups={peopleGroups}
                multiple
                selected={notify}
                placeholder={t("automations.form.notifyPlaceholder")}
                placement="top-start"
                onSelect={(id) =>
                  setNotify((list) =>
                    list.includes(id)
                      ? list.filter((item) => item !== id)
                      : [...list, id],
                  )
                }
                trigger={({ ref, toggle }) => (
                  <button
                    ref={ref}
                    type="button"
                    onClick={toggle}
                    className="flex h-9 w-full items-center rounded-lg border border-line-default bg-surface-card px-3 text-left text-body hover:bg-surface-hover"
                  >
                    <span className="min-w-0 flex-1 truncate">
                      {notify.length === 0
                        ? t("automations.form.notifyPlaceholder")
                        : people
                            .filter((person) => notify.includes(person.id))
                            .map((person) => person.name || person.email)
                            .join("、")}
                    </span>
                    <Icon
                      name="chevron-down"
                      size={14}
                      className="text-fg-muted"
                    />
                  </button>
                )}
              />
            </Field>
          </div>
        </div>
      </Dialog>

      <WebhookUrlSheet
        sheet={sheet}
        onClose={() => {
          const closeAll = sheet?.closeAfter === true;
          setSheet(null);
          if (closeAll) onClose();
        }}
      />
    </>
  );
}

/** 400 宽小窗，完整 URL 只显示这一次。 */
function WebhookUrlSheet({
  sheet,
  onClose,
}: {
  sheet: UrlSheet | null;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  return (
    <Dialog
      open={sheet !== null}
      onClose={onClose}
      width={400}
      title={t("automations.webhook.urlOnceTitle")}
      description={t("automations.webhook.urlOnceDesc")}
      footer={
        <Button onClick={onClose}>{t("automations.webhook.done")}</Button>
      }
    >
      <div className="flex flex-col gap-3">
        {sheet?.urls.map((item) => (
          <div key={item.trigger_id} className="flex items-start gap-2">
            <code className="min-w-0 flex-1 break-all rounded-md bg-surface-muted px-2.5 py-2 font-mono text-[12px]">
              {item.url}
            </code>
            <Button
              variant="ghost"
              size="32"
              leading={<Icon name="copy" />}
              onClick={() => {
                void navigator.clipboard?.writeText(item.url);
                toast(t("automations.webhook.copied"));
              }}
            >
              {t("automations.webhook.copyUrl")}
            </Button>
          </div>
        ))}
      </div>
    </Dialog>
  );
}
