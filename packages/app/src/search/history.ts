import { readLocal, writeLocal } from "../storage";
import { PALETTE_CATEGORIES, type PaletteFilter } from "./model";
import type { SearchTaskCondition } from "./taskFilterModel";

export interface SpaceScope {
  mode: "all" | "subset" | "none";
  ids: string[];
}

export const ALL_SPACES: SpaceScope = { mode: "all", ids: [] };

export interface SearchHistoryEntry {
  query: string;
  type: PaletteFilter;
  scope: SpaceScope;
  conditions: SearchTaskCondition[];
}

function readJSON(key: string): unknown {
  try {
    return JSON.parse(readLocal(key) ?? "null");
  } catch {
    return null;
  }
}

function validScope(value: unknown): value is SpaceScope {
  if (
    !value ||
    typeof value !== "object" ||
    !("mode" in value) ||
    !("ids" in value)
  )
    return false;
  return (
    (value.mode === "all" ||
      value.mode === "subset" ||
      value.mode === "none") &&
    Array.isArray(value.ids) &&
    value.ids.every((id) => typeof id === "string")
  );
}

export function readSearchScope(key: string): SpaceScope {
  const value = readJSON(key);
  return validScope(value) ? value : ALL_SPACES;
}

export function readSearchHistory(key: string): SearchHistoryEntry[] {
  const value = readJSON(key);
  if (!Array.isArray(value)) return [];
  return value
    .filter(
      (entry) =>
        entry &&
        typeof entry.query === "string" &&
        (entry.type === "all" || PALETTE_CATEGORIES.includes(entry.type)) &&
        validScope(entry.scope) &&
        Array.isArray(entry.conditions) &&
        entry.conditions.every(
          (condition: SearchTaskCondition) =>
            condition &&
            typeof condition.field === "string" &&
            Array.isArray(condition.values) &&
            condition.values.every((v) => typeof v === "string"),
        ),
    )
    .slice(0, 20);
}

export function recordSearch(key: string, entry: SearchHistoryEntry) {
  const query = entry.query.trim().replace(/\s+/g, " ");
  if (!query && entry.type === "all") return;
  const normal = { ...entry, query };
  const signature = (value: SearchHistoryEntry) =>
    JSON.stringify([
      value.query.toLowerCase(),
      value.type,
      value.scope.mode,
      [...value.scope.ids].sort(),
      value.conditions,
    ]);
  const existing = readSearchHistory(key).filter(
    (value) => signature(value) !== signature(normal),
  );
  writeLocal(key, JSON.stringify([normal, ...existing].slice(0, 20)));
}
