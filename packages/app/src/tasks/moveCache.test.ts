import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";

import type { Task } from "@openfox/api";

import { keys } from "../context";
import { applyMoveToCache } from "./detail/MoveTaskDialog";

// 子任务 / 动态 / Git 关联的缓存键只有任务 id，请求却按 task.space_id 发。
// 移动成功后若在详情仍是旧空间时失效它们，会按旧空间重新请求（子任务为空、动态 404）。
describe("applyMoveToCache", () => {
  const ORG = "o1";
  const moved = {
    id: "t1",
    display_id: "PRO-7",
    space_id: "s2",
    version: 2,
  } as Task;

  it("先把新空间写进详情缓存，并移除移走任务的子资源缓存（不触发旧空间请求）", () => {
    const client = new QueryClient();
    client.setQueryData(keys.taskDetail(ORG, "OPEN-3"), {
      id: "t1",
      display_id: "OPEN-3",
      space_id: "s1",
      canonical_display_id: "OPEN-3",
      followers: [],
    });
    client.setQueryData(keys.taskDetail(ORG, "OPEN-9"), {
      id: "other",
      space_id: "s1",
    });
    client.setQueryData(keys.taskSub(ORG, "t1", "subtasks"), { tasks: [] });
    client.setQueryData(keys.taskSub(ORG, "t1", "timeline"), {});
    client.setQueryData(keys.taskSub(ORG, "t4", "git-links"), {});
    client.setQueryData(keys.taskSub(ORG, "keep", "timeline"), {});

    applyMoveToCache(client, ORG, "s1", ["t1", "t4"], moved);

    expect(client.getQueryData(keys.taskDetail(ORG, "OPEN-3"))).toMatchObject({
      id: "t1",
      space_id: "s2",
      display_id: "PRO-7",
      // 不改规范编号，避免旧编号重定向抢在调用方跳转之后改地址
      canonical_display_id: "OPEN-3",
      followers: [],
    });
    expect(client.getQueryData(keys.taskDetail(ORG, "OPEN-9"))).toMatchObject({
      space_id: "s1",
    });
    expect(
      client.getQueryData(keys.taskSub(ORG, "t1", "subtasks")),
    ).toBeUndefined();
    expect(
      client.getQueryData(keys.taskSub(ORG, "t1", "timeline")),
    ).toBeUndefined();
    expect(
      client.getQueryData(keys.taskSub(ORG, "t4", "git-links")),
    ).toBeUndefined();
    expect(client.getQueryData(keys.taskSub(ORG, "keep", "timeline"))).toEqual(
      {},
    );
  });
});
