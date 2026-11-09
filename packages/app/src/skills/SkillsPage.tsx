import { useEffect, useState } from "react";
import { useParams, useSearchParams } from "react-router-dom";

import type { AgentAvatarValue, Skill } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Icon,
  Menu,
  MenuItem,
  Select,
  TextInput,
  type IconName,
} from "@openfox/ui";

import { AgentAvatar } from "../agents/AgentAvatar";
import { formatDateTime } from "../automations/parts";
import { useSpaceDetail } from "../spaces/queries";
import { TaskState } from "../tasks/TaskWorkspace";
import { useOrgPeople } from "../tasks/queries";
import { MarketPickerDialog } from "../market/MarketPickerDialog";
import { CreateSkillDialog } from "./CreateSkillDialog";
import { hasMarketUpdate, type SkillSort } from "./model";
import { RuntimeImportDialog } from "./RuntimeImportDialog";
import { SkillDetailDialog } from "./SkillDetailDialog";
import { UploadSkillDialog } from "./UploadSkillDialog";
import { UrlInstallDialog } from "./UrlInstallDialog";
import { useSkills } from "./queries";

// 技能列表：提供空态、添加菜单与技能卡片。
// 详情是 Dialog，地址里的 ?skill=<id> 记录当前打开的技能，刷新后保持。

/** 搜索输入的防抖，避免每敲一个字就发一次请求。 */
const SEARCH_DEBOUNCE_MS = 300;

type AddDialog = "market" | "create" | "upload" | "url" | "runtime";

export function SkillsPage() {
  const t = useT();
  const { orgId = "", spaceId = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const space = useSpaceDetail(orgId, spaceId);
  const people = useOrgPeople(orgId);
  const [sort, setSort] = useState<SkillSort>("updated");
  const [creator, setCreator] = useState("");
  const [search, setSearch] = useState("");
  const [q, setQ] = useState("");
  const [dialog, setDialog] = useState<AddDialog | null>(null);
  useEffect(() => {
    const timer = setTimeout(() => setQ(search.trim()), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [search]);
  const list = useSkills(orgId, spaceId, {
    sort,
    ...(creator ? { created_by: creator } : {}),
    ...(q ? { q } : {}),
  });

  const skillId = params.get("skill") ?? "";
  const openSkill = (id: string) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.set("skill", id);
        return next;
      },
      { replace: false },
    );
  const closeSkill = () =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        next.delete("skill");
        return next;
      },
      { replace: true },
    );

  if (list.isError && !list.data)
    return <TaskState title={t("skills.loadFailed")} />;
  if (!list.data)
    return (
      <div className="flex flex-col gap-3 p-4" aria-busy="true">
        {[0, 1, 2].map((index) => (
          <div
            key={index}
            className="h-[88px] animate-pulse rounded-xl bg-surface-muted"
          />
        ))}
      </div>
    );

  const skills = list.data.skills;
  const canCreate = list.data.can_create;
  const filtered = Boolean(q || creator);
  const empty = skills.length === 0 && !filtered;

  const addMenu = canCreate ? (
    <Menu
      placement="bottom-end"
      label={t("skills.menu.label")}
      className="w-[360px] p-6"
      trigger={(props) => (
        <Button
          ref={props.ref}
          size="28"
          aria-haspopup="menu"
          aria-expanded={props.open}
          leading={<Icon name="plus" />}
          trailing={<Icon name="chevron-down" />}
          onClick={props.toggle}
        >
          {t("skills.add")}
        </Button>
      )}
    >
      <AddItem
        icon="store"
        title={t("skills.menu.market")}
        desc={t("skills.menu.marketDesc")}
        onSelect={() => setDialog("market")}
      />
      <AddItem
        icon="pencil-line"
        title={t("skills.menu.manual")}
        desc={t("skills.menu.manualDesc")}
        onSelect={() => setDialog("create")}
      />
      <AddItem
        icon="upload"
        title={t("skills.menu.upload")}
        desc={t("skills.menu.uploadDesc")}
        onSelect={() => setDialog("upload")}
      />
      <AddItem
        icon="link"
        title={t("skills.menu.url")}
        desc={t("skills.menu.urlDesc")}
        onSelect={() => setDialog("url")}
      />
      <AddItem
        icon="terminal"
        title={t("skills.menu.runtime")}
        desc={t("skills.menu.runtimeDesc")}
        onSelect={() => setDialog("runtime")}
      />
    </Menu>
  ) : null;

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-default px-4">
        <h1 className="flex items-center gap-1.5 font-medium">
          <span className="text-fg-muted">{space.data?.name}</span>
          <Icon name="chevron-right" className="text-fg-muted" />
          <span>{t("skills.title")}</span>
        </h1>
        {addMenu && <div className="ml-auto">{addMenu}</div>}
      </header>
      {empty ? (
        <div className="flex flex-1 items-center justify-center p-6">
          <div className="flex w-[448px] max-w-full flex-col items-start gap-4">
            <Icon name="openfox-empty-skills" size={72} />
            <div className="flex flex-col gap-2">
              <h2 className="text-body font-medium">
                {t("skills.emptyTitle")}
              </h2>
              <p className="text-body-sm leading-relaxed text-fg-muted">
                {t("skills.emptyDesc")}
              </p>
            </div>
          </div>
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto">
          <div className="mx-auto flex max-w-[1100px] flex-col gap-3 px-8 pt-3 pb-10">
            <div className="flex flex-wrap items-center gap-2">
              <div className="w-[176px] shrink-0">
                <Select
                  variant="field"
                  aria-label={t("skills.toolbar.sort")}
                  leading={
                    <span className="text-fg-muted">
                      {t("skills.toolbar.sort")}
                    </span>
                  }
                  value={sort}
                  onChange={setSort}
                  options={[
                    { value: "usage", label: t("skills.toolbar.sortUsage") },
                    {
                      value: "updated",
                      label: t("skills.toolbar.sortUpdated"),
                    },
                    {
                      value: "created",
                      label: t("skills.toolbar.sortCreated"),
                    },
                  ]}
                />
              </div>
              <div className="w-[176px] shrink-0">
                <Select
                  variant="field"
                  aria-label={t("skills.toolbar.creator")}
                  leading={
                    <span className="text-fg-muted">
                      {t("skills.toolbar.creator")}
                    </span>
                  }
                  value={creator}
                  onChange={setCreator}
                  options={[
                    { value: "", label: t("skills.toolbar.creatorAll") },
                    ...(people.data ?? []).map((person) => ({
                      value: person.id,
                      label: person.name || person.email,
                    })),
                  ]}
                />
              </div>
              <TextInput
                variant="field"
                aria-label={t("skills.toolbar.search")}
                placeholder={t("skills.toolbar.searchPlaceholder")}
                leading={<Icon name="search" />}
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                className="ml-auto !w-72 shrink-0"
              />
            </div>
            {skills.length === 0 ? (
              <p className="py-10 text-center text-body-sm text-fg-muted">
                {t("skills.toolbar.noResults")}
              </p>
            ) : (
              // 容器宽 ≥1000 时两列
              <div className="@container">
                <div className="grid grid-cols-1 gap-2 @[1000px]:grid-cols-2">
                  {skills.map((skill) => (
                    <SkillCard
                      key={skill.id}
                      skill={skill}
                      onOpen={() => openSkill(skill.id)}
                    />
                  ))}
                </div>
              </div>
            )}
          </div>
        </div>
      )}

      {dialog === "market" && (
        <MarketPickerDialog
          orgId={orgId}
          spaceId={spaceId}
          onClose={() => setDialog(null)}
          onOpenSkill={(id) => {
            setDialog(null);
            openSkill(id);
          }}
        />
      )}
      {dialog === "create" && (
        <CreateSkillDialog
          orgId={orgId}
          spaceId={spaceId}
          onClose={() => setDialog(null)}
          onCreated={(id) => {
            setDialog(null);
            openSkill(id);
          }}
        />
      )}
      {dialog === "upload" && (
        <UploadSkillDialog
          orgId={orgId}
          spaceId={spaceId}
          onClose={() => setDialog(null)}
          onCreated={(id) => {
            setDialog(null);
            openSkill(id);
          }}
        />
      )}
      {dialog === "url" && (
        <UrlInstallDialog
          orgId={orgId}
          spaceId={spaceId}
          onClose={() => setDialog(null)}
          onCreated={(id) => {
            setDialog(null);
            openSkill(id);
          }}
        />
      )}
      {dialog === "runtime" && (
        <RuntimeImportDialog
          orgId={orgId}
          spaceId={spaceId}
          onClose={() => setDialog(null)}
          onOpenSkill={(id) => {
            setDialog(null);
            openSkill(id);
          }}
        />
      )}
      {skillId && (
        <SkillDetailDialog
          key={skillId}
          orgId={orgId}
          spaceId={spaceId}
          skillId={skillId}
          onClose={closeSkill}
        />
      )}
    </div>
  );
}

/** 添加菜单的一项：32×32 图标方块 + 标题 14 + 说明 12。 */
function AddItem({
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
      onSelect={onSelect}
      icon={
        <span className="flex size-8 items-center justify-center rounded-lg bg-surface-muted text-fg-secondary">
          <Icon name={icon} />
        </span>
      }
      description={desc}
    >
      {title}
    </MenuItem>
  );
}

/** 卡片：整张可点，没有启用开关与行内更多。 */
function SkillCard({ skill, onOpen }: { skill: Skill; onOpen: () => void }) {
  const t = useT();
  const { locale } = useI18n();
  return (
    <button
      type="button"
      aria-label={t("skills.card.open", { name: skill.name })}
      onClick={onOpen}
      className="flex w-full flex-col gap-2.5 rounded-xl border border-line-default bg-surface-card px-4 py-3.5 text-left hover:bg-surface-hover"
    >
      <span className="flex items-baseline gap-2">
        <span className="truncate font-mono text-body font-medium">
          {skill.name}
        </span>
        <span className="shrink-0 text-caption text-fg-muted">
          {t("skills.card.files", { count: skill.file_count })}
        </span>
        {skill.source_kind === "market" && (
          <Badge tone="neutral">{t("skills.detail.market")}</Badge>
        )}
        {hasMarketUpdate(skill) && (
          <Badge tone="info">{t("market.update.available")}</Badge>
        )}
      </span>
      <span className="line-clamp-2 text-body-sm text-fg-secondary">
        {skill.description || t("skills.card.noDescription")}
      </span>
      <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-caption text-fg-muted">
        {skill.used_count === 0 ? (
          <span>{`— ${t("skills.card.unused")}`}</span>
        ) : (
          <span
            className="flex items-center"
            title={t("skills.card.usedBy", { count: skill.used_count })}
          >
            {skill.used_by.slice(0, 3).map((agent) => (
              <span key={agent.id} className="-ml-1 first:ml-0">
                <AgentAvatar
                  name={agent.name}
                  avatar={agent.avatar as AgentAvatarValue}
                  size={20}
                />
              </span>
            ))}
          </span>
        )}
        {skill.source_label && (
          <span>
            · {t("skills.card.source", { label: skill.source_label })}
          </span>
        )}
        <span>
          · {t("skills.card.createdBy", { name: skill.created_by.name })}
        </span>
        <span>
          ·{" "}
          {t("skills.card.updatedAt", {
            time: formatDateTime(locale, skill.updated_at),
          })}
        </span>
      </span>
    </button>
  );
}
