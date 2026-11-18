import { useLocation } from "react-router-dom";
import { useEffect, useState } from "react";
import { useT } from "@openfox/i18n";
import { ConfirmDialog, Select, Switch } from "@openfox/ui";

import { useIterations } from "../../iterations/queries";
import { useProjects } from "../../projects/queries";
import { useFilterOptions } from "../../tasks/filters/options";
import {
  useAgentDirectory,
  usePeople,
  usePickerLabels,
  useSpaceAgents,
  useSpaceTypes,
  useStatuses,
} from "../../tasks/queries";
import {
  useTriageCount,
  useTriageMembersMutation,
  useTriageSettings,
  useTriageSettingsMutation,
} from "../../triage/queries";
import { useSpaceList } from "../queries";
import type { SpacePageProps } from "./GeneralPage";
import {
  PageHeading,
  RetiredBanner,
  SettingRow,
  SettingsCard,
  SettingsColumn,
  SettingsSection,
} from "./parts";
import { TriageRules } from "./TriageRuleEditor";

/** 空间设置 · 分拣台（页 17 第 9 节 9.1–9.7）。 */
export function TriageSettingsPage({
  orgId,
  space,
  canManage,
  retired,
}: SpacePageProps) {
  const t = useT();
  const settings = useTriageSettings(orgId, space.id);
  const update = useTriageSettingsMutation(orgId, space.id);
  const members = useTriageMembersMutation(orgId, space.id);
  const count = useTriageCount(orgId, space.id);
  const people = usePeople(orgId, space);
  const agents = useSpaceAgents(orgId, space.id);
  const directory = useAgentDirectory(orgId);
  const labels = usePickerLabels(orgId, space.id);
  const types = useSpaceTypes(orgId, space.id);
  const statuses = useStatuses(orgId);
  const projects = useProjects(orgId, space.id, {
    enabled: Boolean(space.features.projects),
  });
  const iterations = useIterations(
    orgId,
    space.id,
    { status: "planned,active" },
    Boolean(space.features.iterations),
  );
  const spaces = useSpaceList(orgId);
  const options = useFilterOptions({
    statuses: statuses.data ?? [],
    types: types.data ?? [],
    labels: labels.data ?? [],
    people,
    agents: (agents.data ?? []).filter((agent) => !agent.archived_at),
    crews: (directory.data?.entries ?? []).filter(
      (entry) =>
        entry.type === "crew" &&
        entry.space_id === space.id &&
        !entry.archived_at,
    ),
    projects: projects.data?.projects ?? [],
    iterations: iterations.data?.iterations ?? [],
  });
  const [disableOpen, setDisableOpen] = useState(false);
  const value = settings.data;
  const { hash } = useLocation();
  useEffect(() => {
    if (value && hash === "#triage-responsibility")
      document
        .getElementById("triage-responsibility")
        ?.scrollIntoView?.({ block: "start" });
  }, [hash, !!value]);
  if (!value) return null;
  const editable = canManage && !retired && value.can_manage;
  const save = (patch: {
    enabled?: boolean;
    require_priority?: boolean;
    action?: typeof value.action;
  }) => update.mutate({ ...patch, version: value.version });
  // 关闭时有待分拣任务才需要确认（任务会移到待规划）。
  const toggleEnabled = (enabled: boolean) => {
    if (!enabled && (count.data?.total ?? 0) > 0) setDisableOpen(true);
    else save({ enabled });
  };
  const memberIds = value.members.map((member) => member.id);
  // 规则移动目标：当前用户可写的其他活跃空间（后端执行时会再校验创建者权限）。
  const targets = (spaces.data ?? []).filter(
    (item) =>
      item.id !== space.id &&
      item.status === "active" &&
      (item.can_manage ||
        item.my_role === "OWNER" ||
        item.my_role === "EDITOR" ||
        item.my_role === "MEMBER"),
  );
  const dimmed = !value.enabled && "pointer-events-none opacity-50";
  return (
    <SettingsColumn>
      {retired && <RetiredBanner />}
      <PageHeading
        title={t("spaces.sections.triage")}
        description={t("spaces.triage.settingsDescription")}
      />
      {!editable && (
        <div className="rounded-lg border border-line-default bg-surface-muted px-4 py-3 text-body-sm text-fg-muted">
          {t("spaces.triage.readOnly")}
        </div>
      )}

      <SettingsCard>
        <SettingRow
          title={t("spaces.features.triageToggle")}
          description={t("spaces.triage.enableDescription")}
          control={
            <Switch
              checked={value.enabled}
              disabled={!editable || update.isPending}
              label={t("spaces.features.triageToggle")}
              onCheckedChange={toggleEnabled}
            />
          }
        />
        <SettingRow
          title={t("spaces.triage.requirePriority")}
          description={t("spaces.triage.priorityDescription")}
          control={
            <Switch
              checked={value.require_priority}
              disabled={!editable || !value.enabled || update.isPending}
              label={t("spaces.triage.requirePriority")}
              onCheckedChange={(checked) => save({ require_priority: checked })}
            />
          }
        />
      </SettingsCard>
      <div className={dimmed || undefined}>
        <SettingsSection
          id="triage-responsibility"
          title={t("spaces.triage.members")}
          description={t("spaces.triage.membersDescription")}
        >
          <SettingsCard>
            <SettingRow
              title={t("spaces.triage.action")}
              description={t("spaces.triage.actionDescription")}
              control={
                <Select
                  value={value.action}
                  disabled={!editable || !value.enabled}
                  aria-label={t("spaces.triage.action")}
                  options={[
                    { value: "none", label: t("spaces.triage.actionNone") },
                    { value: "notify", label: t("spaces.triage.actionNotify") },
                    { value: "assign", label: t("spaces.triage.actionAssign") },
                  ]}
                  onChange={(action) => save({ action })}
                />
              }
            />
            {value.action !== "none" &&
              people.map((person) => (
                <SettingRow
                  key={person.id}
                  title={person.email}
                  control={
                    <Switch
                      checked={memberIds.includes(person.id)}
                      disabled={
                        !editable || !value.enabled || members.isPending
                      }
                      label={person.email}
                      onCheckedChange={(next) =>
                        members.mutate(
                          next
                            ? [...memberIds, person.id]
                            : memberIds.filter((id) => id !== person.id),
                        )
                      }
                    />
                  }
                />
              ))}
            {value.action !== "none" && people.length === 0 && (
              <div className="p-4 text-body-sm text-fg-muted">
                {t("spaces.triage.noMembers")}
              </div>
            )}
          </SettingsCard>
        </SettingsSection>
      </div>
      <div className={dimmed || undefined}>
        <TriageRules
          orgId={orgId}
          spaceId={space.id}
          rules={value.rules}
          editable={editable && value.enabled}
          sources={{ options, spaces: targets }}
        />
      </div>
      <ConfirmDialog
        open={disableOpen}
        title={t("spaces.triage.disableTitle", { name: space.name })}
        description={t("spaces.triage.disableDescription", {
          count: count.data?.total ?? 0,
        })}
        confirmLabel={t("common.continue")}
        danger
        loading={update.isPending}
        onCancel={() => setDisableOpen(false)}
        onConfirm={() => {
          setDisableOpen(false);
          save({ enabled: false });
        }}
      />
    </SettingsColumn>
  );
}
