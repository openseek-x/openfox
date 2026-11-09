import { useMemo, useState } from "react";

import type { MarketSkillDetail } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Callout,
  Dialog,
  Icon,
  Skeleton,
  cn,
  useToast,
} from "@openfox/ui";

import {
  SKILL_FILE,
  buildTree,
  formatBytes,
  type TreeNode,
} from "../skills/model";
import { FilePreview } from "./FilePreview";
import { MarketIcon } from "./MarketIcon";
import { compactCount, pickText } from "./model";
import {
  existingSkillId,
  marketErrorText,
  useAddMarketSkill,
  useMarketCatalog,
  useMarketSkill,
} from "./queries";
import { MarketAgentPicker } from "./MarketAgentPicker";

// 市场技能详情：信息条、文件树、预览器、市场安装入口。

export function MarketSkillDialog({
  orgId,
  marketSkillId,
  spaceId,
  onClose,
  onOpenAdded,
}: {
  orgId: string;
  marketSkillId: string;
  /** 从空间技能页打开时给出：直接加到这个空间；首页市场则选 Agent */
  spaceId?: string;
  onClose: () => void;
  /** 添加成功后（或空间里已有时）打开空间里的这个技能 */
  onOpenAdded: (spaceId: string, skillId: string) => void;
}) {
  const t = useT();
  const { locale } = useI18n();
  const toast = useToast();
  const detail = useMarketSkill(orgId, marketSkillId);
  const catalog = useMarketCatalog(orgId);
  const add = useAddMarketSkill(orgId);
  const [picking, setPicking] = useState(false);
  const [exists, setExists] = useState<{
    spaceId: string;
    skillId: string;
  } | null>(null);
  const skill = detail.data;

  const doAdd = (targetSpace: string) => {
    setExists(null);
    add.mutate(
      { spaceId: targetSpace, marketSkillId },
      {
        onSuccess: (created) => {
          setPicking(false);
          toast(t("market.skill.added"), {
            tone: "success",
            action: {
              label: t("market.skill.openAdded"),
              onClick: () => onOpenAdded(targetSpace, created.id),
            },
          });
          if (spaceId) onOpenAdded(targetSpace, created.id);
        },
        onError: (error) => {
          setPicking(false);
          const id = existingSkillId(error);
          if (id) setExists({ spaceId: targetSpace, skillId: id });
        },
      },
    );
  };

  const onAddClick = () => {
    if (spaceId) doAdd(spaceId);
    else setPicking(true);
  };

  const name = skill ? pickText(skill.name, locale) : "";
  const categoryNames = useMemo(() => {
    const map = new Map(
      (catalog.data?.categories ?? []).map((c) => [
        c.id,
        pickText(c.name, locale),
      ]),
    );
    return (skill?.category_ids ?? [])
      .map((id) => map.get(id))
      .filter((value): value is string => Boolean(value));
  }, [catalog.data, skill, locale]);

  if (picking)
    return (
      <MarketAgentPicker
        orgId={orgId}
        marketId={marketSkillId}
        name={name}
        onBack={() => setPicking(false)}
        onClose={onClose}
      />
    );
  return (
    <Dialog
      open
      onClose={onClose}
      title={skill ? name : t("common.loading")}
      width={960}
      className="h-5/6 gap-3 !px-0 [&>div:first-child]:px-6"
      footer={
        skill ? (
          <div className="flex w-full items-center gap-3 px-6">
            <div className="min-w-0 flex-1">
              {add.isError && !exists && (
                <Callout tone="danger">
                  {marketErrorText(t, add.error, "market.skill.addFailed")}
                </Callout>
              )}
              {exists && (
                <Callout tone="info">
                  <span className="flex items-center gap-3">
                    <span>{t("market.errors.exists")}</span>
                    <Button
                      variant="outline"
                      size="28"
                      onClick={() =>
                        onOpenAdded(exists.spaceId, exists.skillId)
                      }
                    >
                      {t("market.skill.openExisting")}
                    </Button>
                  </span>
                </Callout>
              )}
            </div>
            <Button variant="outline" onClick={onClose}>
              {t(spaceId ? "common.close" : "common.cancel")}
            </Button>
            <Button loading={add.isPending} onClick={onAddClick}>
              {t(
                spaceId ? "market.skill.addToSpace" : "market.skill.addToAgent",
              )}
            </Button>
          </div>
        ) : undefined
      }
    >
      {detail.isError ? (
        <div role="alert" className="flex flex-col items-start gap-2 px-6">
          <p className="text-body text-danger">
            {t("market.skill.loadFailed")}
          </p>
          <Button
            variant="outline"
            size="28"
            onClick={() => void detail.refetch()}
          >
            {t("market.preview.reload")}
          </Button>
        </div>
      ) : !skill ? (
        <div aria-busy="true" className="flex flex-col gap-2 px-6">
          <Skeleton className="h-6 w-1/3" />
          <Skeleton className="h-4 w-full" />
        </div>
      ) : (
        <div className="flex h-full min-h-0 flex-col gap-3">
          <div className="flex flex-col gap-2 px-6">
            <div className="flex flex-wrap items-center gap-2">
              <MarketIcon src={skill.icon} name={name} size={32} />
              {categoryNames.map((label) => (
                <Badge key={label} tone="neutral">
                  {label}
                </Badge>
              ))}
              <Badge tone="muted">v{skill.version}</Badge>
              <span className="text-body-sm text-fg-muted">
                {t("market.skill.fileCount", {
                  count: skill.file_count,
                  size: formatBytes(skill.total_bytes),
                })}
              </span>
              {skill.publisher.name && (
                <span className="flex items-center gap-1 text-body-sm text-fg-muted">
                  {skill.publisher.name}
                  {skill.publisher.verified && (
                    <span title={t("market.card.verified")}>
                      <Icon name="check-circle" className="text-accent" />
                    </span>
                  )}
                </span>
              )}
            </div>
            {pickText(skill.description, locale) && (
              <p className="text-body text-fg-secondary">
                {pickText(skill.description, locale)}
              </p>
            )}
            <dl className="grid grid-cols-4 gap-px overflow-hidden rounded-xl bg-line-default">
              <InfoItem label={t("market.skill.origin")} value={skill.origin} />
              <InfoItem
                label={t("market.skill.downloads")}
                value={compactCount(skill.downloads)}
              />
              <InfoItem
                label={t("market.skill.stars")}
                value={compactCount(skill.stars)}
              />
              <InfoItem
                label={t("market.skill.version")}
                value={skill.version}
              />
            </dl>
          </div>
          <SkillFiles orgId={orgId} skill={skill} />
        </div>
      )}
    </Dialog>
  );
}

function InfoItem({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-0.5 bg-surface-muted px-4 py-2.5">
      <dt className="text-caption text-fg-muted">{label}</dt>
      <dd className="truncate text-body font-medium">{value}</dd>
    </div>
  );
}

/** 文件树 + 预览：默认选中 SKILL.md，文件夹在前，默认展开一级目录。 */
function SkillFiles({
  orgId,
  skill,
}: {
  orgId: string;
  skill: MarketSkillDetail;
}) {
  const t = useT();
  const tree = useMemo(() => buildTree(skill.files), [skill.files]);
  const [path, setPath] = useState(
    skill.files.find((f) => f.path === SKILL_FILE)?.path ??
      firstFile(tree) ??
      "",
  );
  // 默认只展开一级目录：嵌套目录（路径里还有 /）初始收起
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(
    () =>
      new Set(
        skill.files.flatMap((file) => {
          const parts = file.path.split("/").slice(0, -1);
          return parts
            .map((_, index) => parts.slice(0, index + 1).join("/"))
            .filter((dir) => dir.includes("/"));
        }),
      ),
  );
  const entry = skill.files.find((f) => f.path === path);
  const toggle = (dir: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(dir)) next.delete(dir);
      else next.add(dir);
      return next;
    });
  return (
    <div className="flex min-h-0 flex-1 border-t border-line-default">
      {skill.files.length > 1 && (
        <nav
          aria-label={t("market.skill.tree")}
          className="w-[220px] shrink-0 overflow-y-auto border-r border-line-default p-2"
        >
          <Tree
            nodes={tree}
            selected={path}
            collapsed={collapsed}
            onToggle={toggle}
            onSelect={setPath}
            depth={0}
          />
        </nav>
      )}
      {entry ? (
        <FilePreview
          key={entry.path}
          orgId={orgId}
          skillId={skill.market_skill_id}
          entry={entry}
        />
      ) : (
        <p className="p-4 text-body-sm text-fg-muted">
          {t("market.skill.noFiles")}
        </p>
      )}
    </div>
  );
}

function firstFile(nodes: TreeNode[]): string | undefined {
  for (const node of nodes) {
    if (node.kind === "file") return node.path;
    const inner = firstFile(node.children);
    if (inner) return inner;
  }
  return undefined;
}

function Tree({
  nodes,
  selected,
  collapsed,
  onToggle,
  onSelect,
  depth,
}: {
  nodes: TreeNode[];
  selected: string;
  collapsed: ReadonlySet<string>;
  onToggle: (path: string) => void;
  onSelect: (path: string) => void;
  depth: number;
}) {
  return (
    <ul className="flex flex-col">
      {nodes.map((node) => {
        const open = !collapsed.has(node.path);
        return (
          <li key={`${node.kind}:${node.path}`}>
            {node.kind === "dir" ? (
              <>
                <button
                  type="button"
                  aria-expanded={open}
                  onClick={() => onToggle(node.path)}
                  style={{ paddingLeft: 8 + depth * 12 }}
                  className="flex h-7 w-full items-center gap-1.5 rounded-md pr-2 text-left text-body-sm text-fg-muted hover:bg-surface-hover"
                >
                  <Icon name={open ? "chevron-down" : "chevron-right"} />
                  <Icon name="folder" />
                  <span className="truncate">{node.name}</span>
                </button>
                {open && (
                  <Tree
                    nodes={node.children}
                    selected={selected}
                    collapsed={collapsed}
                    onToggle={onToggle}
                    onSelect={onSelect}
                    depth={depth + 1}
                  />
                )}
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
              </button>
            )}
          </li>
        );
      })}
    </ul>
  );
}
