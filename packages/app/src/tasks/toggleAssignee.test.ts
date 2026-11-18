import { describe, expect, it } from "vitest";

import type { TaskAssignee } from "@openfox/api";

import { toggleAssignee } from "./pickers";

const names = {
  people: [
    { id: "u1", email: "a@x.test" },
    { id: "u2", email: "b@x.test" },
    { id: "u3", email: "c@x.test" },
  ] as never,
  agents: [{ id: "g1", name: "Agent" }],
};
const human = (id: string): TaskAssignee => ({
  kind: "HUMAN",
  id,
  name: id,
});
const agent: TaskAssignee = { kind: "AGENT", id: "g1", name: "Agent" };

describe("toggleAssignee", () => {
  it("开关打开：追加人员，保留 Agent", () => {
    const next = toggleAssignee([human("u1"), agent], "user:u2", true, names);
    expect(next.map((a) => `${a.kind}:${a.id}`)).toEqual([
      "HUMAN:u1",
      "HUMAN:u2",
      "AGENT:g1",
    ]);
  });

  it("再次选择已选人员即移除", () => {
    const next = toggleAssignee(
      [human("u1"), human("u2")],
      "user:u1",
      true,
      names,
    );
    expect(next.map((a) => a.id)).toEqual(["u2"]);
  });

  it("开关关闭：选新人替换人员处理人，Agent 不受影响", () => {
    const next = toggleAssignee([human("u1"), agent], "user:u2", false, names);
    expect(next.map((a) => `${a.kind}:${a.id}`)).toEqual([
      "HUMAN:u2",
      "AGENT:g1",
    ]);
  });

  it("开关关闭：已有多人集合仍可逐个减少", () => {
    const next = toggleAssignee(
      [human("u1"), human("u2"), human("u3")],
      "user:u2",
      false,
      names,
    );
    expect(next.map((a) => a.id)).toEqual(["u1", "u3"]);
  });

  it("Agent 值沿用单选替换", () => {
    const next = toggleAssignee([human("u1")], "agent:g1", true, names);
    expect(next.map((a) => `${a.kind}:${a.id}`)).toEqual([
      "HUMAN:u1",
      "AGENT:g1",
    ]);
  });
});
