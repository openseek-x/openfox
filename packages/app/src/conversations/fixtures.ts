import type { Run } from "@openfox/api";

import { json, type Route } from "../settings/testing";
import { agent, device, entry } from "../agents/fixtures";

// 对话页签、对话页、小助手测试的数据夹具与假后端。

export const ORG = "/v1/organizations/o1";
export { message, summary, TS } from "./messageFixtures";
import { TS } from "./messageFixtures";

export function run(id: string, extra: Partial<Run> = {}): Run {
  return {
    id,
    organization_id: "o1",
    space_id: "s1",
    conversation_id: "c1",
    agent_id: "a1",
    computer_id: "d1",
    grant_id: "g-d1",
    config_version: 1,
    grant_version: 1,
    actor_id: "u1",
    state: "RUNNING",
    generation: 1,
    reconciliation_required: false,
    reason: "",
    version: 1,
    created_at: TS,
    updated_at: TS,
    failure_code: "",
    started_at: TS,
    usage: null,
    duration_ms: null,
    ...extra,
  };
}

const spaces = [
  {
    id: "s1",
    name: "默认空间",
    prefix: "DEF",
    visibility: "shared",
    created_at: TS,
  },
];

/** 公共接口：空间、目录、Agent 详情、电脑、Run 时间线；各测试用 extra 覆盖 */
export function chatBase(
  extra: Route = () => undefined,
  options: { entries?: ReturnType<typeof entry>[] } = {},
): Route {
  const entries = options.entries ?? [
    entry("a1", { name: "熊猫", title: "产品经理" }),
    entry("a2", { name: "阿虎", title: "后端" }),
  ];
  return (call) => {
    const custom = extra(call);
    if (custom) return custom;
    if (call.method !== "GET") return undefined;
    if (call.path === `${ORG}/spaces`) return json(200, { spaces });
    if (call.path === `${ORG}/agent-directory`)
      return json(200, { entries, archived_count: 0, truncated: false });
    if (call.path === `${ORG}/devices`)
      return json(200, { devices: [device("d1")], next_cursor: null });
    const agentMatch = /\/spaces\/s1\/agents\/([^/]+)$/.exec(call.path);
    if (agentMatch) {
      const id = agentMatch[1]!;
      const item = entries.find(
        (entry) => entry.type === "agent" && entry.id === id,
      );
      return json(
        200,
        agent(id, item ? { name: item.name, avatar: item.avatar } : {}),
      );
    }
    const runMatch = /\/runs\/([^/]+)(\/[a-z]+)?$/.exec(call.path);
    if (runMatch) {
      if (runMatch[2] === "/events")
        return json(200, { events: [], next_cursor: 0 });
      if (runMatch[2] === "/artifacts") return json(200, { artifacts: [] });
      if (runMatch[2] === "/result")
        return json(404, { error: { code: "NOT_FOUND", message: "x" } });
      return json(200, run(runMatch[1]!));
    }
    return undefined;
  };
}
