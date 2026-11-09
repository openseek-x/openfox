// 空间名称与标识符的输入规则（新建与修改只允许新格式；存量旧格式只显示不校验）。

export const SPACE_NAME_MAX = 32;
export const IDENTIFIER_MIN = 2;
export const IDENTIFIER_MAX = 7;
const IDENTIFIER_PATTERN = /^[A-Z0-9]{2,7}$/;

/** 输入时规整：转大写、去掉非字母数字、最多 7 位。 */
export function normalizeIdentifier(input: string): string {
  return input
    .toUpperCase()
    .replace(/[^A-Z0-9]/g, "")
    .slice(0, IDENTIFIER_MAX);
}

export function isValidIdentifier(value: string): boolean {
  return IDENTIFIER_PATTERN.test(value);
}

/** 名称按字符计数（中文、表情各算 1 个）。 */
export function nameLength(name: string): number {
  return [...name].length;
}

export type NameError = "empty" | "tooLong" | null;
export function validateSpaceName(name: string): NameError {
  const trimmed = name.trim();
  if (!trimmed) return "empty";
  if (nameLength(trimmed) > SPACE_NAME_MAX) return "tooLong";
  return null;
}

/** 由空间名推荐标识符：取英文单词首字母或前几个字母，取不到时为空串。 */
export function suggestIdentifier(name: string): string {
  const words = name
    .toUpperCase()
    .split(/[^A-Z0-9]+/)
    .filter(Boolean);
  if (words.length === 0) return "";
  const candidate =
    words.length === 1
      ? words[0]!.slice(0, 4)
      : words.map((word) => word[0]).join("");
  const normalized = normalizeIdentifier(candidate);
  return normalized.length >= IDENTIFIER_MIN ? normalized : "";
}

/** 与列表里其他空间的现值冲突（历史值只有服务端知道，以 409 IDENTIFIER_TAKEN 为准）。 */
export function findIdentifierOwner<T extends { id: string; prefix: string }>(
  spaces: readonly T[],
  identifier: string,
  selfId?: string,
): T | undefined {
  const target = identifier.toUpperCase();
  return spaces.find(
    (space) => space.id !== selfId && space.prefix.toUpperCase() === target,
  );
}
