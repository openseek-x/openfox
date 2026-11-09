import { useEffect, useRef, useState } from "react";

import type { SpaceDetail, SpacePurpose } from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import { Select, Switch, TextInput } from "@openfox/ui";

import { useIterationSettings } from "../../iterations/queries";
import { useTriageSettings } from "../../triage/queries";
import { scaleName } from "../estimation-labels";
import {
  findIdentifierOwner,
  isValidIdentifier,
  nameLength,
  normalizeIdentifier,
  SPACE_NAME_MAX,
} from "../identifier";
import { useSpaceList, useUpdateSpace } from "../queries";
import { SpaceIconPicker } from "../SpaceIcon";
import { spaceSettingsPath } from "../SpacesNav";
import { DangerZone } from "./DangerZone";
import { IdentifierDialog } from "./IdentifierDialog";
import {
  RetiredBanner,
  SettingNavRow,
  SettingRow,
  SettingsCard,
  SettingsColumn,
  SettingsSection,
} from "./parts";
import { ReposSection } from "./ReposSection";
import { GitSettingsSection } from "../../settings/GitSettingsPage";

export const SPACE_PURPOSES: SpacePurpose[] = [
  "software_development",
  "product_management",
  "marketing",
  "design",
  "project_management",
  "operations",
  "it_support",
  "human_resources",
  "customer_service",
  "legal",
  "finance",
  "sales",
  "data_science",
  "other",
];

export const purposeLabel = (purpose: SpacePurpose): MessageKey =>
  `spaces.purposes.${purpose}`;

export interface SpacePageProps {
  orgId: string;
  space: SpaceDetail;
  /** 当前用户能否管理设置（不含停用只读） */
  canManage: boolean;
  /** 已停用：所有设置只读 */
  retired: boolean;
}

/**
 * 空间设置概览：通用 / 成员 / 任务 / Git 仓库 / 危险操作。
 * 通用卡没有保存按钮：选图标、访问权限、用途立即保存；名称与标识符失焦或回车提交。
 */
export function GeneralPage({
  orgId,
  space,
  canManage,
  retired,
  focus,
}: SpacePageProps & { focus?: "repos" }) {
  const t = useT();
  const update = useUpdateSpace(orgId, space.id);
  const editable = canManage && !retired;
  const base = (section: string) => spaceSettingsPath(orgId, space.id, section);
  const reposRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (focus === "repos")
      reposRef.current?.scrollIntoView?.({ block: "start" });
  }, [focus]);

  // 9a · 迭代、分拣台的摘要按真实开关显示；与各自设置页共用查询键，加载中或出错时不显示摘要
  const iterationSettings = useIterationSettings(orgId, space.id);
  const triageSettings = useTriageSettings(orgId, space.id);
  const enabledSummary = (query: {
    isSuccess: boolean;
    data?: { enabled: boolean };
  }) =>
    query.isSuccess && query.data
      ? t(
          query.data.enabled
            ? "spaces.overview.featureOn"
            : "spaces.overview.featureOff",
        )
      : undefined;

  const estimationSummary =
    space.estimation.scale === "notUsed"
      ? t("spaces.overview.estimationOff")
      : t(scaleName(space.estimation.scale));

  return (
    <SettingsColumn>
      {retired && <RetiredBanner />}
      <SettingsSection title={t("spaces.sections.general")}>
        <SettingsCard>
          <SettingRow
            title={t("spaces.general.iconNameTitle")}
            description={t("spaces.general.iconNameDescription")}
            control={
              <div className="flex w-[276px] items-center gap-2">
                <SpaceIconPicker
                  key={`${orgId}:${space.id}`}
                  orgId={orgId}
                  icon={space.icon}
                  disabled={!editable || update.isPending}
                  onChange={(icon) => update.mutate({ icon })}
                />
                <NameField
                  space={space}
                  disabled={!editable}
                  onCommit={(name) => update.mutate({ name })}
                />
              </div>
            }
          />
          <IdentifierRow orgId={orgId} space={space} disabled={!editable} />
          <SettingRow
            title={t("spaces.access.title")}
            description={t("spaces.access.description")}
            control={
              <Select
                aria-label={t("spaces.access.title")}
                value={space.visibility}
                disabled={!editable}
                className="min-w-[124px]"
                options={[
                  { value: "shared", label: t("spaces.access.public") },
                  { value: "private", label: t("spaces.access.private") },
                ]}
                onChange={(visibility) => {
                  if (visibility !== space.visibility)
                    update.mutate({ visibility });
                }}
              />
            }
          />
          <SettingRow
            title={t("spaces.general.purposeTitle")}
            description={t("spaces.general.purposeDescription")}
            control={
              <Select
                aria-label={t("spaces.general.purposeTitle")}
                value={space.purpose}
                disabled={!editable}
                className="min-w-[112px]"
                options={SPACE_PURPOSES.map((purpose) => ({
                  value: purpose,
                  label: t(purposeLabel(purpose)),
                }))}
                onChange={(purpose) => {
                  if (purpose !== space.purpose) update.mutate({ purpose });
                }}
              />
            }
          />
        </SettingsCard>
      </SettingsSection>

      <SettingsSection title={t("spaces.sections.members")}>
        <SettingsCard>
          <SettingNavRow
            to={base("members")}
            icon="users"
            title={t("spaces.overview.membersTitle")}
            description={t("spaces.overview.membersDescription")}
            summary={t("spaces.overview.memberCount", {
              count: space.member_count,
            })}
          />
        </SettingsCard>
      </SettingsSection>

      <SettingsSection title={t("spaces.overview.tasksSection")}>
        <SettingsCard>
          <SettingNavRow
            to={base("task-types")}
            icon="layers"
            title={t("spaces.sections.taskTypes")}
            description={t("spaces.overview.taskTypesDescription")}
            summary={t("spaces.overview.typeCount", {
              count: space.task_type_count,
            })}
          />
          <SettingNavRow
            to={base("labels")}
            icon="tag"
            title={t("spaces.sections.labels")}
            description={t("spaces.overview.labelsDescription")}
            summary={
              space.label_count
                ? t("spaces.overview.labelCount", { count: space.label_count })
                : undefined
            }
          />
          <SettingNavRow
            to={base("iterations")}
            icon="repeat"
            title={t("spaces.sections.iterations")}
            description={t("spaces.overview.iterationsDescription")}
            summary={enabledSummary(iterationSettings)}
          />
          <SettingNavRow
            to={base("triage")}
            icon="inbox"
            title={t("spaces.sections.triage")}
            description={t("spaces.overview.triageDescription")}
            summary={enabledSummary(triageSettings)}
          />
          <SettingNavRow
            to={base("estimates")}
            icon="clock"
            title={t("spaces.sections.estimates")}
            description={t("spaces.overview.estimatesDescription")}
            summary={estimationSummary}
          />
          <SettingRow
            icon="users"
            title={t("spaces.general.multiAssigneeTitle")}
            description={t("spaces.general.multiAssigneeDescription")}
            control={
              <Switch
                checked={space.multi_assignee}
                disabled={!editable}
                label={t("spaces.general.multiAssigneeTitle")}
                onCheckedChange={(multi_assignee) =>
                  update.mutate({ multi_assignee })
                }
              />
            }
          />
        </SettingsCard>
      </SettingsSection>

      <div ref={reposRef}>
        <ReposSection orgId={orgId} space={space} retired={retired} />
        <GitSettingsSection
          orgId={orgId}
          spaceId={space.id}
          disabled={!editable}
        />
      </div>
      {canManage && (
        <DangerZone orgId={orgId} space={space} retired={retired} />
      )}
    </SettingsColumn>
  );
}

/** 名称：失焦或回车提交；超过 32 字符时说明文字换成提示，且不提交。 */
function NameField({
  space,
  disabled,
  onCommit,
}: {
  space: SpaceDetail;
  disabled: boolean;
  onCommit: (name: string) => void;
}) {
  const t = useT();
  const [draft, setDraft] = useState(space.name);
  useEffect(() => setDraft(space.name), [space.name]);
  const tooLong = nameLength(draft.trim()) > SPACE_NAME_MAX;
  const commit = () => {
    const next = draft.trim();
    if (!next || tooLong) {
      if (!next) setDraft(space.name);
      return;
    }
    if (next !== space.name) onCommit(next);
  };
  return (
    <div className="flex min-w-0 flex-1 flex-col gap-1">
      <TextInput
        aria-label={t("spaces.general.nameLabel")}
        value={draft}
        disabled={disabled}
        invalid={tooLong}
        placeholder={t("spaces.general.namePlaceholder")}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={commit}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            commit();
          }
        }}
      />
      {tooLong && (
        <span role="alert" className="text-caption text-fg-muted">
          {t("spaces.validation.nameTooLong")}
        </span>
      )}
    </div>
  );
}

/** 标识符：输入转大写、过滤、最多 7 位；合法且不同于原值时先弹确认。 */
function IdentifierRow({
  orgId,
  space,
  disabled,
}: {
  orgId: string;
  space: SpaceDetail;
  disabled: boolean;
}) {
  const t = useT();
  const spaces = useSpaceList(orgId);
  const current = space.prefix.toUpperCase();
  const [draft, setDraft] = useState(current);
  const [confirming, setConfirming] = useState<string | null>(null);
  useEffect(() => setDraft(current), [current]);
  const owner = findIdentifierOwner(spaces.data ?? [], draft, space.id);
  const invalid = draft !== current && !isValidIdentifier(draft);
  const description = invalid
    ? t("spaces.validation.identifierFormat")
    : owner
      ? t("spaces.validation.identifierConflict", {
          identifier: draft,
          spaceName: owner.name,
        })
      : t("spaces.general.identifierDescription");
  const commit = () => {
    if (draft === current || invalid || owner || confirming) return;
    setConfirming(draft);
  };
  return (
    <>
      <SettingRow
        title={t("spaces.general.identifierTitle")}
        description={<span aria-live="polite">{description}</span>}
        control={
          <TextInput
            aria-label={t("spaces.general.identifierTitle")}
            className="w-[240px]"
            value={draft}
            disabled={disabled}
            invalid={invalid || Boolean(owner)}
            placeholder={t("spaces.general.identifierPlaceholder")}
            onChange={(event) =>
              setDraft(normalizeIdentifier(event.target.value))
            }
            onBlur={commit}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                commit();
              }
            }}
          />
        }
      />
      {confirming && (
        <IdentifierDialog
          orgId={orgId}
          space={space}
          next={confirming}
          onClose={() => {
            setConfirming(null);
            setDraft(current);
          }}
          onChanged={() => setConfirming(null)}
        />
      )}
    </>
  );
}
