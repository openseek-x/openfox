import { useMemo, useState, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";

import type { Skill } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuLabel,
  useToast,
  type IconName,
} from "@openfox/ui";

import { isOrgAdmin } from "../../spaces/queries";
import { CreateSkillDialog } from "../../skills/CreateSkillDialog";
import { AGENT_SKILL_LIMIT } from "../../skills/model";
import {
  skillErrorText,
  useAgentSkills,
  useSetAgentSkills,
  useSkills,
} from "../../skills/queries";
import { RuntimeImportDialog } from "../../skills/RuntimeImportDialog";
import { UploadSkillDialog } from "../../skills/UploadSkillDialog";
import { UrlInstallDialog } from "../../skills/UrlInstallDialog";
import { useMe, useOrgRole } from "../queries";
import type { SectionProps } from "./BasicSections";

// Agent 设置抽屉的技能区块：列出已装配技能，从本空间技能里添加（最多 20 个）、移除。
// 装配不随配置版本固定，下一次 Run 起生效；没有编辑权限时只读（服务端的 Agent 编辑权限判断是最终依据）。
// 「添加技能」来源菜单：市场 / 空间技能（下一级）/ 手动创建 / 上传 / URL / Runtime。
// 后四项先进入 Agent 所在空间的技能库（对话框原有行为），成功后自动装配到当前 Agent，受 20 个上限约束。

type SourceDialog = "create" | "upload" | "url" | "runtime";

export function SkillsSection({ orgId, agent }: SectionProps) {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const me = useMe();
  const role = useOrgRole(orgId);
  const attached = useAgentSkills(orgId, agent.space_id, agent.id);
  const spaceSkills = useSkills(orgId, agent.space_id, { sort: "updated" });
  const save = useSetAgentSkills(orgId, agent.space_id, agent.id);
  const [level, setLevel] = useState<"root" | "space">("root");
  const [dialog, setDialog] = useState<SourceDialog | null>(null);
  const editable =
    !agent.archived_at &&
    (agent.owner_user_id === me.data?.id || isOrgAdmin(role));
  // 在空间里创建技能的权限，与空间技能页一致取列表接口的 can_create
  const canCreate = spaceSkills.data?.can_create ?? false;
  const current = useMemo(() => attached.data ?? [], [attached.data]);
  const candidates = useMemo(
    () =>
      (spaceSkills.data?.skills ?? []).filter(
        (skill) => !current.some((item) => item.id === skill.id),
      ),
    [spaceSkills.data, current],
  );
  const full = current.length >= AGENT_SKILL_LIMIT;

  const write = (skills: Skill[]) =>
    save.mutate(skills.map((skill) => skill.id));

  /** 把新建 / 导入的技能装配到 Agent：去重，只加到上限，超出的提示数量。 */
  const attach = (skillIds: string[]) => {
    const have = new Set(current.map((skill) => skill.id));
    const fresh = [...new Set(skillIds)].filter((id) => !have.has(id));
    if (fresh.length === 0) return;
    const room = Math.max(0, AGENT_SKILL_LIMIT - current.length);
    const added = fresh.slice(0, room);
    if (added.length > 0)
      save.mutate([...current.map((skill) => skill.id), ...added]);
    const skipped = fresh.length - added.length;
    if (skipped > 0)
      toast(t("skills.agent.overLimit", { count: skipped }), {
        tone: "info",
      });
  };
  const created = (skillId: string) => {
    setDialog(null);
    attach([skillId]);
  };
  const noCreate = canCreate ? undefined : t("skills.agent.noCreate");

  if (attached.isError)
    return <Callout tone="danger">{t("skills.agent.loadFailed")}</Callout>;

  return (
    <div className="flex flex-col gap-3">
      <p className="text-caption text-fg-muted">{t("skills.agent.desc")}</p>
      {!editable && (
        <p className="text-caption text-fg-muted">
          {t("skills.agent.readOnly")}
        </p>
      )}
      {save.isError && (
        <Callout tone="danger">
          {skillErrorText(t, save.error, "skills.agent.saveFailed")}
        </Callout>
      )}
      {attached.data && current.length === 0 ? (
        <div className="rounded-lg border border-dashed border-line-default px-4 py-5 text-center text-body-sm text-fg-muted">
          {t("skills.agent.none")}
        </div>
      ) : (
        <ul className="flex flex-col divide-y divide-line-soft rounded-lg border border-line-default">
          {current.map((skill) => (
            <li key={skill.id} className="flex items-center gap-3 px-3 py-2">
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate font-mono text-body-sm font-medium">
                  {skill.name}
                </span>
                <span className="truncate text-caption text-fg-muted">
                  {skill.description || t("skills.card.noDescription")}
                </span>
              </span>
              {editable && (
                <IconButton
                  label={t("skills.agent.remove", { name: skill.name })}
                  icon={<Icon name="x" />}
                  size="28"
                  disabled={save.isPending}
                  onClick={() =>
                    write(current.filter((item) => item.id !== skill.id))
                  }
                />
              )}
            </li>
          ))}
        </ul>
      )}
      {editable && (
        <div className="flex items-center gap-3">
          <Menu
            label={t("skills.menu.label")}
            className="max-h-[420px] w-[360px] overflow-y-auto"
            trigger={(props) => (
              <Button
                ref={props.ref}
                variant="outline"
                size="28"
                disabled={full || save.isPending}
                aria-haspopup="menu"
                aria-expanded={props.open}
                leading={<Icon name="plus" />}
                onClick={() => {
                  setLevel("root");
                  props.toggle();
                }}
              >
                {t("skills.agent.add")}
              </Button>
            )}
          >
            {level === "root" ? (
              <>
                <SourceItem
                  icon="store"
                  title={t("skills.menu.market")}
                  desc={t("skills.menu.marketDesc")}
                  onSelect={() => navigate(`/o/${orgId}/market`)}
                />
                {candidates.length === 0 ? (
                  <SourceItem
                    icon="sparkles"
                    title={t("skills.agent.fromSpace")}
                    desc={t("skills.agent.nothingToAdd")}
                    disabled
                  />
                ) : (
                  <SpaceLevelButton
                    title={t("skills.agent.fromSpace")}
                    desc={t("skills.agent.fromSpaceDesc")}
                    onOpen={() => setLevel("space")}
                  />
                )}
                {(
                  [
                    ["create", "pencil-line", "manual"],
                    ["upload", "upload", "upload"],
                    ["url", "link", "url"],
                    ["runtime", "terminal", "runtime"],
                  ] as const
                ).map(([value, icon, key]) => (
                  <SourceItem
                    key={value}
                    icon={icon}
                    title={t(`skills.menu.${key}`)}
                    desc={noCreate ?? t(`skills.menu.${key}Desc`)}
                    disabled={!canCreate}
                    onSelect={() => setDialog(value)}
                  />
                ))}
              </>
            ) : (
              <>
                <SpaceLevelButton
                  back
                  title={t("skills.agent.back")}
                  onOpen={() => setLevel("root")}
                />
                <MenuLabel>{t("skills.agent.pick")}</MenuLabel>
                {candidates.map((skill) => (
                  <MenuItem
                    key={skill.id}
                    description={skill.description || undefined}
                    onSelect={() => write([...current, skill])}
                  >
                    {skill.name}
                  </MenuItem>
                ))}
              </>
            )}
          </Menu>
          {full && (
            <span className="text-caption text-fg-muted">
              {t("skills.agent.limit")}
            </span>
          )}
        </div>
      )}
      {dialog === "create" && (
        <CreateSkillDialog
          orgId={orgId}
          spaceId={agent.space_id}
          onClose={() => setDialog(null)}
          onCreated={created}
        />
      )}
      {dialog === "upload" && (
        <UploadSkillDialog
          orgId={orgId}
          spaceId={agent.space_id}
          onClose={() => setDialog(null)}
          onCreated={created}
        />
      )}
      {dialog === "url" && (
        <UrlInstallDialog
          orgId={orgId}
          spaceId={agent.space_id}
          onClose={() => setDialog(null)}
          onCreated={created}
        />
      )}
      {dialog === "runtime" && (
        // 导入结束即装配（onImported 只回传一次）；「完成」只关闭弹窗，不再打开技能详情
        <RuntimeImportDialog
          orgId={orgId}
          spaceId={agent.space_id}
          onClose={() => setDialog(null)}
          onOpenSkill={() => setDialog(null)}
          onImported={attach}
        />
      )}
    </div>
  );
}

/** 来源菜单的一项：32×32 图标方块 + 标题 + 说明（与空间技能页的添加菜单一致）。 */
function SourceItem({
  icon,
  title,
  desc,
  disabled,
  onSelect,
}: {
  icon: IconName;
  title: string;
  desc: string;
  disabled?: boolean;
  onSelect?: () => void;
}) {
  return (
    <MenuItem
      disabled={disabled}
      disabledReason={disabled ? desc : undefined}
      onSelect={onSelect}
      icon={<SourceIcon icon={icon} />}
      description={desc}
    >
      {title}
    </MenuItem>
  );
}

function SourceIcon({ icon }: { icon: IconName }) {
  return (
    <span className="flex size-8 items-center justify-center rounded-lg bg-surface-muted text-fg-secondary">
      <Icon name={icon} />
    </span>
  );
}

/** 进入 / 返回下一级的菜单项：阻止冒泡，Menu 不会因此关闭（同任务详情的召唤菜单）。 */
function SpaceLevelButton({
  title,
  desc,
  back,
  onOpen,
}: {
  title: string;
  desc?: ReactNode;
  back?: boolean;
  onOpen: () => void;
}) {
  return (
    <button
      type="button"
      role="menuitem"
      aria-haspopup={back ? undefined : "menu"}
      onClick={(event) => {
        event.stopPropagation();
        onOpen();
      }}
      className="flex min-h-[34px] w-full items-center gap-2.5 rounded-lg py-1.5 pr-2 pl-2.5 text-left text-body outline-none hover:bg-surface-active focus-visible:bg-surface-active"
    >
      {back ? (
        <Icon name="chevron-left" className="text-fg-muted" />
      ) : (
        <SourceIcon icon="sparkles" />
      )}
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate">{title}</span>
        {desc && <span className="text-caption text-fg-muted">{desc}</span>}
      </span>
      {!back && <Icon name="chevron-right" className="text-fg-muted" />}
    </button>
  );
}
