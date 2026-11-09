export interface CreateAgentToolInput {
  id?: string;
  catalogId: string;
  name: string;
  transport: "http" | "stdio";
  scope?: "PUBLIC" | "DEVICE_OWNER";
  url?: string;
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  headers?: Record<string, string>;
  authMode: "none" | "api_key" | "oauth";
  credential?: string;
  authHeader?: string;
  oauthClientId?: string;
  oauthClientSecret?: string;
}

type Pairs = Array<{ key: string; value: string }>;
export interface AgentToolDraft {
  transport: "http" | "stdio";
  url: string;
  command: string;
  args: string[];
  env: Pairs;
  headers: Pairs;
}

export const emptyToolDraft = (): AgentToolDraft => ({
  transport: "http",
  url: "",
  command: "",
  args: [],
  env: [],
  headers: [],
});

const isObject = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === "object" && !Array.isArray(value);
const strings = (value: unknown): string[] =>
  Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
const pairs = (value: unknown): Pairs =>
  isObject(value)
    ? Object.entries(value).flatMap(([key, value]) =>
        typeof value === "string" ? [{ key, value }] : [],
      )
    : [];
export const toMap = (rows: Pairs): Record<string, string> =>
  Object.fromEntries(
    rows
      .filter(({ key }) => key.trim())
      .map(({ key, value }) => [key.trim(), value]),
  );
export function validToolUrl(
  value: string,
  options: {
    deviceOnly?: boolean;
    oauth?: boolean;
    savedUrl?: string;
    example?: string;
  } = {},
) {
  try {
    const input = value.trim();
    const url = new URL(input);
    if (
      input.length > 2048 ||
      url.username ||
      url.password ||
      input.includes("#") ||
      (url.protocol !== "https:" &&
        !(options.deviceOnly && url.protocol === "http:"))
    )
      return false;
    if (
      input.includes("?") &&
      (options.oauth || url.protocol !== "https:" || !url.search.slice(1))
    )
      return false;
    const decoded = decodeURIComponent(input);
    if (decoded.includes("••••••"))
      return (
        input === options.savedUrl &&
        url.search === "?%E2%80%A2%E2%80%A2%E2%80%A2%E2%80%A2%E2%80%A2%E2%80%A2"
      );
    if (
      /[<>{}]/.test(decoded) ||
      Array.from(url.searchParams.values()).some((v) =>
        /^your_(?:api_)?(?:key|ak|token)$/i.test(v),
      )
    )
      return false;
    if (options.example) {
      const example = new URL(options.example);
      if (url.origin === example.origin && url.pathname === example.pathname)
        return Array.from(example.searchParams.keys()).every(
          (k) => !!url.searchParams.get(k)?.trim(),
        );
    }
    return true;
  } catch {
    return false;
  }
}
export const validToolName = (name: string) =>
  /^[a-z0-9-]{1,64}$/.test(name.trim());
type JsonResult =
  | {
      draft: AgentToolDraft;
      name: string;
      count: number;
      extras: Record<string, unknown>;
    }
  | { error: "invalidJson" | "object" | "empty" | "target" | "sse" };
export function parseToolJson(text: string, name: string): JsonResult {
  let input: unknown;
  try {
    input = JSON.parse(text);
  } catch {
    return { error: "invalidJson" };
  }
  if (!isObject(input)) return { error: "object" };
  const wrapped = input.mcpServers ?? input.mcp;
  const items = isObject(wrapped)
    ? Object.entries(wrapped).filter(
        (pair): pair is [string, Record<string, unknown>] => isObject(pair[1]),
      )
    : [[name, input] as const];
  if (!items.length) return { error: "empty" };
  const result: Array<{
    draft: AgentToolDraft;
    name: string;
    extras: Record<string, unknown>;
  }> = [];
  for (const [entryName, entry] of items) {
    const command =
      typeof entry.command === "string"
        ? [entry.command]
        : strings(entry.command);
    const url = typeof entry.url === "string" ? entry.url : "";
    if (!command[0]?.trim() && !url.trim()) return { error: "target" };
    if (!command.length && (entry.type === "sse" || entry.transport === "sse"))
      return { error: "sse" };
    const transport = command.length ? "stdio" : "http";
    const extras = Object.fromEntries(
      Object.entries(entry).filter(
        ([key]) =>
          ![
            "command",
            "args",
            "type",
            "transport",
            "url",
            "env",
            "environment",
            "headers",
          ].includes(key),
      ),
    );
    result.push({
      name: entryName,
      extras,
      draft: {
        transport,
        url,
        command: command[0] ?? "",
        args: Array.isArray(entry.args)
          ? strings(entry.args)
          : command.slice(1),
        env: pairs(entry.env ?? entry.environment),
        headers: pairs(entry.headers),
      },
    });
  }
  return { ...result[0]!, count: result.length };
}

export function draftToJson(
  draft: AgentToolDraft,
  extras: Record<string, unknown> = {},
) {
  const env = toMap(draft.env),
    headers = toMap(draft.headers),
    args = draft.args.filter((arg) => arg.trim());
  return JSON.stringify(
    draft.transport === "stdio"
      ? {
          ...extras,
          command: draft.command.trim(),
          ...(args.length && { args }),
          ...(Object.keys(env).length && { env }),
        }
      : {
          ...extras,
          type: "http",
          url: draft.url.trim(),
          ...(Object.keys(headers).length && { headers }),
        },
    null,
    2,
  );
}
