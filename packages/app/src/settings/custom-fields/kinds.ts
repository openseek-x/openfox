import type { CustomFieldKind, CustomFieldMemberScope } from "@openfox/api";
import type { IconName } from "@openfox/ui";

export const FIELD_KINDS: CustomFieldKind[] = [
  "TEXT",
  "NUMBER",
  "SELECT",
  "MULTI_SELECT",
  "BOOLEAN",
  "DATE",
  "MEMBER",
  "URL",
  "RICH_TEXT",
];

/** 每种字段类型的图标与颜色（列表行左侧的类型图标）。 */
export const KIND_META: Record<
  CustomFieldKind,
  { icon: IconName; color: string }
> = {
  TEXT: { icon: "type", color: "#6B7280" },
  NUMBER: { icon: "hash", color: "#0284C7" },
  SELECT: { icon: "list", color: "#7C3AED" },
  MULTI_SELECT: { icon: "list-checks", color: "#C026D3" },
  BOOLEAN: { icon: "circle-check", color: "#16A34A" },
  DATE: { icon: "calendar", color: "#EA580C" },
  MEMBER: { icon: "user", color: "#0D9488" },
  URL: { icon: "link", color: "#DB2777" },
  RICH_TEXT: { icon: "file-text", color: "#CA8A04" },
};

export const MEMBER_SCOPES: CustomFieldMemberScope[] = [
  "ALL",
  "HUMANS",
  "AGENTS",
];

export const hasOptions = (kind: CustomFieldKind) =>
  kind === "SELECT" || kind === "MULTI_SELECT";

export const TEXT_MAX = 256;
export const RICH_TEXT_MAX = 65536;
export const URL_MAX = 2048;

export function isHttpUrl(value: string): boolean {
  if (value.length > URL_MAX) return false;
  try {
    const url = new URL(value);
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}

/** 选项行：key 在创建时是本地 id，编辑时是选项 id。 */
export interface OptionRow {
  key: string;
  /** 已保存到服务端的选项 id */
  id?: string;
  label: string;
  color: string;
  version?: number;
}

/**
 * 默认值的表单状态：scalar 用于文本 / 数字 / 布尔 / 日期 / 成员 / URL / 富文本，
 * keys 用于单选 / 多选（引用 OptionRow.key）。
 */
export interface DefaultDraft {
  scalar: string;
  keys: string[];
}

export const emptyDraft = (): DefaultDraft => ({ scalar: "", keys: [] });

/** 把已有字段的默认值转成表单状态（编辑时，选项 key = 选项 id）。 */
export function draftFromValue(
  kind: CustomFieldKind,
  value: unknown,
): DefaultDraft {
  if (value === null || value === undefined) return emptyDraft();
  switch (kind) {
    case "SELECT":
      return { scalar: "", keys: typeof value === "string" ? [value] : [] };
    case "MULTI_SELECT":
      return {
        scalar: "",
        keys: Array.isArray(value)
          ? value.filter((v): v is string => typeof v === "string")
          : [],
      };
    case "BOOLEAN":
      return { scalar: value === true ? "true" : "false", keys: [] };
    case "NUMBER":
      return {
        scalar: typeof value === "number" ? String(value) : "",
        keys: [],
      };
    case "MEMBER": {
      const member = value as { kind?: string; id?: string };
      return {
        scalar: member.kind && member.id ? `${member.kind}:${member.id}` : "",
        keys: [],
      };
    }
    default:
      return { scalar: typeof value === "string" ? value : "", keys: [] };
  }
}

export type DefaultResult =
  { ok: true; value: unknown } | { ok: false; reason: "number" | "url" };

/**
 * 表单状态转请求里的 default_value。
 * create：单选 / 多选用 option_index / option_indexes 引用本次提交的选项（只算有名称的行）；
 * update：直接用选项 id。返回 value=null 表示无默认值。
 */
export function buildDefault(
  kind: CustomFieldKind,
  draft: DefaultDraft,
  rows: OptionRow[],
  mode: "create" | "update",
): DefaultResult {
  const scalar = draft.scalar.trim();
  switch (kind) {
    case "SELECT":
    case "MULTI_SELECT": {
      const saved = rows.filter((row) => row.label.trim());
      const refs = draft.keys
        .map((key) => {
          const index = saved.findIndex((row) => row.key === key);
          if (index === -1) return null;
          return mode === "create" ? index : (saved[index]!.id ?? null);
        })
        .filter((ref): ref is number | string => ref !== null);
      if (refs.length === 0) return { ok: true, value: null };
      if (kind === "SELECT")
        return {
          ok: true,
          value: mode === "create" ? { option_index: refs[0] } : refs[0],
        };
      return {
        ok: true,
        value: mode === "create" ? { option_indexes: refs } : refs,
      };
    }
    case "NUMBER": {
      if (!scalar) return { ok: true, value: null };
      const n = Number(scalar);
      return Number.isFinite(n)
        ? { ok: true, value: n }
        : { ok: false, reason: "number" };
    }
    case "BOOLEAN":
      return { ok: true, value: scalar ? scalar === "true" : null };
    case "URL":
      if (!scalar) return { ok: true, value: null };
      return isHttpUrl(scalar)
        ? { ok: true, value: scalar }
        : { ok: false, reason: "url" };
    case "MEMBER": {
      const [memberKind, id] = scalar.split(":");
      return {
        ok: true,
        value: memberKind && id ? { kind: memberKind, id } : null,
      };
    }
    case "RICH_TEXT":
      return { ok: true, value: draft.scalar.trim() ? draft.scalar : null };
    default:
      return { ok: true, value: scalar || null };
  }
}
