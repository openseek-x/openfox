import { useState } from "react";

import type { AgentAvatarValue, SkillDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Callout,
  ConfirmDialog,
  Dialog,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  Segmented,
  Skeleton,
  Textarea,
  cn,
  useToast,
} from "@openfox/ui";

import { CodeView } from "../code/CodeView";
import { languageForPath } from "../code/languages";
import { AgentAvatar } from "../agents/AgentAvatar";
import { Markdown } from "../conversations/Markdown";
import { marketErrorText, useUpdateMarketSkill } from "../market/queries";
import {
  SKILL_FILE,
  buildTree,
  hasMarketUpdate,
  parseSkillMd,
  type TreeNode,
} from "./model";
import {
  skillErrorText,
  useDeleteSkill,
  useSaveSkillFiles,
  useSkill,
  useSkillFile,
} from "./queries";

// 技能详情：左侧文件树（文件数 > 1）、右侧查看器（编辑 / 预览切换）、
// 有修改时出现未保存提示与「保存修改」。只读时不可编辑；非文本文件显示「无法预览二进制文件」。

export function SkillDetailDialog({
  orgId,
  spaceId,
  skillId,
  onClose,
}: {
  orgId: string;
  spaceId: string;
  skillId: string;
  onClose: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const detail = useSkill(orgId, spaceId, skillId);
  const save = useSaveSkillFiles(orgId, spaceId, skillId);
  const remove = useDeleteSkill(orgId, spaceId);
  const update = useUpdateMarketSkill(orgId, spaceId);
  const skill = detail.data;
  const [path, setPath] = useState(SKILL_FILE);
  const [mode, setMode] = useState<"preview" | "edit">("preview");
  // 已修改但还没保存的文件内容（与原文相同时会被移除）
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [confirm, setConfirm] = useState<
    null | "delete-skill" | "delete-file" | "discard"
  >(null);
  const dirty = Object.keys(drafts).length > 0;

  const requestClose = () => {
    if (dirty) setConfirm("discard");
    else onClose();
  };

  const saveAll = () => {
    if (!skill || !dirty || save.isPending) return;
    save.mutate(
      {
        version: skill.version,
        changes: Object.entries(drafts).map(([filePath, content]) => ({
          path: filePath,
          content,
        })),
      },
      {
        onSuccess: () => {
          setDrafts({});
          toast(t("skills.detail.saved"), { tone: "success" });
        },
      },
    );
  };

  const deleteFile = () => {
    if (!skill) return;
    save.mutate(
      { version: skill.version, changes: [{ path, delete: true }] },
      {
        onSuccess: () => {
          setConfirm(null);
          setDrafts(({ [path]: _removed, ...rest }) => rest);
          setPath(SKILL_FILE);
          toast(t("skills.detail.fileDeleted"), { tone: "success" });
        },
        onError: () => setConfirm(null),
      },
    );
  };

  const updateFromMarket = () => {
    if (!skill || update.isPending) return;
    update.mutate(
      { skillId: skill.id, version: skill.version },
      {
        onSuccess: (updated) =>
          toast(
            t("market.update.done", { version: updated.market_version ?? "" }),
            { tone: "success" },
          ),
      },
    );
  };

  const deleteSkill = () => {
    if (!skill) return;
    remove.mutate(skill, {
      onSuccess: () => {
        toast(t("skills.detail.deleted"), { tone: "success" });
        onClose();
      },
    });
  };

  return (
    <>
      <Dialog
        open
        onClose={requestClose}
        title={skill?.name ?? t("common.loading")}
        width={896}
        className="h-5/6 gap-3 !px-0 [&>div:first-child]:px-6 [&>div:first-child_h2]:font-mono"
      >
        {detail.isError ? (
          <p role="alert" className="px-6 text-body text-danger">
            {t("skills.detail.loadFailed")}
          </p>
        ) : !skill ? (
          <div aria-busy="true" className="flex flex-col gap-2 px-6">
            <Skeleton className="h-6 w-1/3" />
            <Skeleton className="h-4 w-full" />
          </div>
        ) : (
          <div className="flex h-full min-h-0 flex-col gap-2">
            {update.isError && (
              <Callout tone="danger" className="mx-6">
                {marketErrorText(t, update.error, "market.update.failed")}
              </Callout>
            )}
            <div className="flex min-h-8 items-center gap-2 px-6">
              {skill.source_kind === "market" && (
                <Badge tone="neutral">{t("skills.detail.market")}</Badge>
              )}
              {hasMarketUpdate(skill) && (
                <Badge tone="info">{t("market.update.available")}</Badge>
              )}
              {hasMarketUpdate(skill) && skill.can_edit && (
                <Button
                  size="28"
                  variant="outline"
                  loading={update.isPending}
                  onClick={updateFromMarket}
                >
                  {t("market.update.action", {
                    version: skill.market_latest_version ?? "",
                  })}
                </Button>
              )}
              {!skill.can_edit && (
                <Badge tone="muted">{t("skills.detail.readOnly")}</Badge>
              )}
              {skill.agents.length > 0 && (
                <span
                  className="flex items-center"
                  title={t("skills.detail.agents")}
                >
                  {skill.agents.slice(0, 6).map((agent) => (
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
              {skill.can_edit && (
                <div className="ml-auto">
                  <Menu
                    placement="bottom-end"
                    label={t("skills.detail.more")}
                    trigger={(props) => (
                      <IconButton
                        ref={props.ref}
                        label={t("skills.detail.more")}
                        icon={<Icon name="ellipsis" />}
                        size="28"
                        active={props.open}
                        onClick={props.toggle}
                      />
                    )}
                  >
                    <MenuItem
                      danger
                      icon={<Icon name="trash-2" />}
                      onSelect={() => setConfirm("delete-skill")}
                    >
                      {t("skills.detail.deleteSkill")}
                    </MenuItem>
                  </Menu>
                </div>
              )}
            </div>
            <div className="flex min-h-0 flex-1 border-t border-line-default">
              {skill.files.length > 1 && (
                <nav
                  aria-label={t("skills.detail.tree")}
                  className="w-[200px] shrink-0 overflow-y-auto border-r border-line-default p-2"
                >
                  <FileTree
                    nodes={buildTree(skill.files)}
                    selected={path}
                    modified={Object.keys(drafts)}
                    onSelect={setPath}
                    depth={0}
                  />
                </nav>
              )}
              <Viewer
                key={path}
                orgId={orgId}
                spaceId={spaceId}
                skill={skill}
                path={path}
                mode={mode}
                onModeChange={setMode}
                draft={drafts[path]}
                onDraft={(original, value) =>
                  setDrafts((prev) => {
                    const next = { ...prev };
                    if (value === original) delete next[path];
                    else next[path] = value;
                    return next;
                  })
                }
                onDeleteFile={() => setConfirm("delete-file")}
                dirty={dirty}
                saving={save.isPending}
                onSave={saveAll}
                saveError={
                  save.isError
                    ? skillErrorText(
                        t,
                        save.error,
                        "skills.detail.saveFailed",
                        "skills.detail.versionConflict",
                      )
                    : null
                }
              />
            </div>
          </div>
        )}
      </Dialog>

      <ConfirmDialog
        open={confirm === "delete-skill"}
        danger
        title={t("skills.detail.deleteTitle")}
        description={t("skills.detail.deleteDesc")}
        confirmLabel={t("skills.detail.deleteSkill")}
        loading={remove.isPending}
        onCancel={() => setConfirm(null)}
        onConfirm={deleteSkill}
      >
        {remove.isError && (
          <Callout tone="danger">
            {skillErrorText(t, remove.error, "skills.detail.deleteFailed")}
          </Callout>
        )}
      </ConfirmDialog>
      <ConfirmDialog
        open={confirm === "delete-file"}
        danger
        title={t("skills.detail.deleteFileTitle", { path })}
        description={t("skills.detail.deleteFileDesc")}
        confirmLabel={t("skills.detail.deleteFile")}
        loading={save.isPending}
        onCancel={() => setConfirm(null)}
        onConfirm={deleteFile}
      />
      <ConfirmDialog
        open={confirm === "discard"}
        danger
        title={t("skills.detail.closeTitle")}
        description={t("skills.detail.closeDesc")}
        confirmLabel={t("skills.detail.discard")}
        cancelLabel={t("skills.detail.keepEditing")}
        onCancel={() => setConfirm(null)}
        onConfirm={onClose}
      />
    </>
  );
}

function FileTree({
  nodes,
  selected,
  modified,
  onSelect,
  depth,
}: {
  nodes: TreeNode[];
  selected: string;
  modified: string[];
  onSelect: (path: string) => void;
  depth: number;
}) {
  return (
    <ul className="flex flex-col">
      {nodes.map((node) => (
        <li key={`${node.kind}:${node.path}`}>
          {node.kind === "dir" ? (
            <>
              <div
                style={{ paddingLeft: 8 + depth * 12 }}
                className="flex h-7 items-center gap-1.5 text-body-sm text-fg-muted"
              >
                <Icon name="folder" />
                <span className="truncate">{node.name}</span>
              </div>
              <FileTree
                nodes={node.children}
                selected={selected}
                modified={modified}
                onSelect={onSelect}
                depth={depth + 1}
              />
            </>
          ) : (
            <button
              type="button"
              aria-current={node.path === selected ? "true" : undefined}
              onClick={() => onSelect(node.path)}
              style={{ paddingLeft: 8 + depth * 12 }}
              className={cn(
                "flex h-7 w-full items-center gap-1.5 rounded-md pr-2 text-left text-body-sm hover:bg-surface-hover",
                node.path === selected && "bg-surface-active font-medium",
              )}
            >
              <Icon name="file-text" className="text-fg-muted" />
              <span className="min-w-0 flex-1 truncate">{node.name}</span>
              {modified.includes(node.path) && (
                <span className="size-1.5 rounded-full bg-warning" />
              )}
            </button>
          )}
        </li>
      ))}
    </ul>
  );
}

function Viewer({
  orgId,
  spaceId,
  skill,
  path,
  mode,
  onModeChange,
  draft,
  onDraft,
  onDeleteFile,
  dirty,
  saving,
  onSave,
  saveError,
}: {
  orgId: string;
  spaceId: string;
  skill: SkillDetail;
  path: string;
  mode: "preview" | "edit";
  onModeChange: (mode: "preview" | "edit") => void;
  draft: string | undefined;
  onDraft: (original: string, value: string) => void;
  onDeleteFile: () => void;
  dirty: boolean;
  saving: boolean;
  onSave: () => void;
  saveError: string | null;
}) {
  const t = useT();
  const file = useSkillFile(orgId, spaceId, skill.id, path);
  const original = file.data?.content ?? "";
  const binary = file.data?.binary ?? false;
  const editable = skill.can_edit && !binary;
  const text = draft ?? original;
  const editing = mode === "edit" && editable;

  return (
    <section className="flex min-w-0 flex-1 flex-col">
      <div className="flex h-10 shrink-0 items-center gap-2 border-b border-line-default px-3">
        <span className="min-w-0 flex-1 truncate font-mono text-body-sm">
          {path}
        </span>
        {editable && (
          <Segmented
            label={t("skills.detail.mode")}
            value={mode}
            onChange={onModeChange}
            className="h-7"
            options={[
              { value: "edit", label: t("skills.detail.edit") },
              { value: "preview", label: t("skills.detail.preview") },
            ]}
          />
        )}
        {skill.can_edit && path !== SKILL_FILE && (
          <IconButton
            label={t("skills.detail.deleteFile")}
            icon={<Icon name="trash-2" />}
            size="28"
            onClick={onDeleteFile}
          />
        )}
      </div>
      {dirty && (
        <div className="flex shrink-0 items-center gap-3 border-b border-line-default bg-warning-soft px-3 py-2">
          <span className="flex-1 text-body-sm">
            {t("skills.detail.unsaved")}
          </span>
          <Button size="28" loading={saving} onClick={onSave}>
            {saving ? t("skills.detail.saving") : t("skills.detail.save")}
          </Button>
        </div>
      )}
      {saveError && (
        <Callout tone="danger" className="m-3">
          {saveError}
        </Callout>
      )}
      <div className="min-h-0 flex-1 overflow-auto p-4">
        {file.isError ? (
          <p role="alert" className="text-body text-danger">
            {t("skills.detail.fileLoadFailed")}
          </p>
        ) : !file.data ? (
          <div aria-busy="true" className="flex flex-col gap-2">
            <Skeleton className="h-4 w-1/2" />
            <Skeleton className="h-4 w-full" />
          </div>
        ) : binary ? (
          <p className="text-body-sm text-fg-muted">
            {t("skills.detail.binary")}
          </p>
        ) : editing ? (
          <Textarea
            aria-label={t("skills.detail.editor")}
            className="min-h-full font-mono text-body-sm"
            rows={20}
            value={text}
            onChange={(event) => onDraft(original, event.target.value)}
          />
        ) : (
          <Preview path={path} text={text} />
        )}
      </div>
    </section>
  );
}

/** 预览：SKILL.md 分名称 / 描述 / 指令三段，其他 .md 用 Markdown，其余纯文本。 */
function Preview({ path, text }: { path: string; text: string }) {
  const t = useT();
  if (path === SKILL_FILE) {
    const parsed = parseSkillMd(text);
    return (
      <dl className="flex flex-col gap-4">
        <Section label={t("skills.detail.previewName")}>
          <span className="font-mono text-[24px] leading-8 font-bold break-all">
            {parsed.name || t("skills.detail.emptyValue")}
          </span>
        </Section>
        <Section label={t("skills.detail.previewDescription")}>
          <p className="text-body whitespace-pre-wrap">
            {parsed.description || t("skills.detail.emptyValue")}
          </p>
        </Section>
        <Section label={t("skills.detail.previewInstructions")}>
          {parsed.instructions ? (
            <Markdown source={parsed.instructions} />
          ) : (
            <span className="text-body text-fg-muted">
              {t("skills.detail.emptyValue")}
            </span>
          )}
        </Section>
      </dl>
    );
  }
  if (path.toLowerCase().endsWith(".md")) return <Markdown source={text} />;
  return <CodeView code={text} language={languageForPath(path)} />;
}

function Section({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-1">
      <dt className="text-caption font-medium text-fg-muted">{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}
