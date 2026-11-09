import type { Skill, SkillFileEntry, SkillImportItem } from "@openfox/api";

// 技能的纯函数与常量：限额、SKILL.md 解析、文件树、导入汇总。

export const SKILL_NAME_PATTERN = /^[a-z0-9][a-z0-9-]{0,63}$/;
/** 每个技能的文件数与总大小上限（与服务端一致，客户端先拦一次） */
export const SKILL_MAX_FILES = 100;
export const SKILL_MAX_BYTES = 5 * 1024 * 1024;
/** Agent 最多装配的技能数 */
export const AGENT_SKILL_LIMIT = 20;
/** 一次从 Runtime 导入的上限 */
export const RUNTIME_IMPORT_LIMIT = 20;
/** 导入进度轮询间隔 */
export const IMPORT_POLL_MS = 1000;
/** 导入进度轮询兜底上限：命令执行超时 5 分钟 + 领取 2 分钟 */
export const IMPORT_POLL_LIMIT_MS = 8 * 60 * 1000;
/** 描述与指令的输入上限（契约：描述 ≤ 1024，指令 ≤ 262144） */
export const SKILL_DESCRIPTION_MAX = 1024;
export const SKILL_INSTRUCTIONS_MAX = 262144;
export const SKILL_FILE = "SKILL.md";

/** 4.4 来源行的五个外链 */
export const URL_SOURCES = [
  { label: "skillhub.club", href: "https://skillhub.club" },
  { label: "skillhub.cn", href: "https://skillhub.cn" },
  { label: "ClawHub", href: "https://clawhub.ai" },
  { label: "Hermes Agent", href: "https://hermes-agent.nousresearch.com" },
  { label: "Skills.sh", href: "https://skills.sh" },
] as const;

export type SkillSort = "usage" | "updated" | "created";

/** 上传前的本地检查：文件数与合计大小；.zip 解压后的大小由服务端把关。 */
export function checkUploadLimits(
  files: ReadonlyArray<{ size: number }>,
): "ok" | "tooMany" | "tooLarge" {
  if (files.length > SKILL_MAX_FILES) return "tooMany";
  const total = files.reduce((sum, file) => sum + file.size, 0);
  return total > SKILL_MAX_BYTES ? "tooLarge" : "ok";
}

export interface ParsedSkillMd {
  name: string;
  description: string;
  instructions: string;
  /** 是否有合法的 frontmatter（--- 包住） */
  frontmatter: boolean;
}

function unquote(value: string): string {
  const text = value.trim();
  if (
    text.length >= 2 &&
    ((text.startsWith('"') && text.endsWith('"')) ||
      (text.startsWith("'") && text.endsWith("'")))
  )
    return text.slice(1, -1);
  return text;
}

/** 解析 SKILL.md：只认简单的 `key: value`；解析不了时整篇当作指令。 */
export function parseSkillMd(source: string): ParsedSkillMd {
  const text = source.replace(/^﻿/, "").replace(/\r\n/g, "\n");
  const match = /^---\n([\s\S]*?)\n---[ \t]*(?:\n|$)/.exec(text);
  if (!match)
    return {
      name: "",
      description: "",
      instructions: text.trim(),
      frontmatter: false,
    };
  const fields: Record<string, string> = {};
  for (const line of match[1]!.split("\n")) {
    const pair = /^([A-Za-z0-9_-]+):\s*(.*)$/.exec(line);
    if (pair) fields[pair[1]!] = unquote(pair[2]!);
  }
  return {
    name: fields.name ?? "",
    description: fields.description ?? "",
    instructions: text.slice(match[0].length).trim(),
    frontmatter: true,
  };
}

/** 文件树节点：目录在前、SKILL.md 置顶，其余按名称排序。 */
export interface TreeNode {
  name: string;
  path: string;
  kind: "dir" | "file";
  children: TreeNode[];
}

export function buildTree(files: readonly SkillFileEntry[]): TreeNode[] {
  const root: TreeNode = { name: "", path: "", kind: "dir", children: [] };
  for (const file of files) {
    const parts = file.path.split("/");
    let cursor = root;
    parts.forEach((part, index) => {
      const path = parts.slice(0, index + 1).join("/");
      const last = index === parts.length - 1;
      let next = cursor.children.find(
        (child) =>
          child.name === part && child.kind === (last ? "file" : "dir"),
      );
      if (!next) {
        next = {
          name: part,
          path,
          kind: last ? "file" : "dir",
          children: [],
        };
        cursor.children.push(next);
      }
      cursor = next;
    });
  }
  const sort = (nodes: TreeNode[], top: boolean) => {
    nodes.sort((a, b) => {
      if (top && a.path === SKILL_FILE) return -1;
      if (top && b.path === SKILL_FILE) return 1;
      if (a.kind !== b.kind) return a.kind === "dir" ? -1 : 1;
      return a.name.localeCompare(b.name);
    });
    for (const node of nodes) sort(node.children, false);
  };
  sort(root.children, true);
  return root.children;
}

export interface ImportSummary {
  imported: number;
  skipped: number;
  failed: number;
  canceled: number;
  pending: number;
  /** 已结束（非 pending）的项数 */
  finished: number;
}

export function summarizeImport(items: readonly SkillImportItem[]) {
  const summary: ImportSummary = {
    imported: 0,
    skipped: 0,
    failed: 0,
    canceled: 0,
    pending: 0,
    finished: 0,
  };
  for (const item of items) {
    if (item.status === "imported") summary.imported++;
    else if (item.status === "skipped") summary.skipped++;
    else if (item.status === "failed") summary.failed++;
    else if (item.status === "canceled") summary.canceled++;
    else summary.pending++;
  }
  summary.finished = items.length - summary.pending;
  return summary;
}

/** 文件大小的简短显示。 */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

/** 来自市场且市场当前版本与添加 / 上次更新时的版本不同。 */
export function hasMarketUpdate(
  skill: Pick<
    Skill,
    "source_kind" | "market_version" | "market_latest_version"
  >,
): boolean {
  return (
    skill.source_kind === "market" &&
    Boolean(skill.market_latest_version) &&
    skill.market_latest_version !== skill.market_version
  );
}
