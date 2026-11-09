import {
  QueryClient,
  QueryClientProvider,
  useQueries,
} from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiProvider, keys } from "../context";
import { json, renderSettings } from "../settings/testing";
import {
  createEventBatcher,
  EVENT_COALESCE_MS,
  useOrgEvents,
} from "./useOrgEvents";

/** 假 EventSource：记录实例，测试里手动派发事件与连接状态。 */
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  readonly url: string;
  readonly withCredentials: boolean;
  onopen: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  closed = false;
  private listeners = new Map<string, Array<(event: Event) => void>>();

  constructor(url: string, init?: { withCredentials?: boolean }) {
    this.url = url;
    this.withCredentials = Boolean(init?.withCredentials);
    FakeEventSource.instances.push(this);
  }
  addEventListener(type: string, listener: (event: Event) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }
  close() {
    this.closed = true;
  }
  emit(type: string, data: unknown) {
    const event = new MessageEvent(type, { data: JSON.stringify(data) });
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
  drop() {
    this.onerror?.(new Event("error"));
  }
  open() {
    this.onopen?.(new Event("open"));
  }
}

const ORG = "o1";

/** 预置几类缓存，返回失效后被标记为 stale 的查询键 */
function seeded() {
  const client = new QueryClient();
  const seed = (key: readonly unknown[], data: unknown = {}) =>
    client.setQueryData(key, data);
  seed(keys.tasks(ORG));
  seed(keys.taskList(ORG, "space:s1", {}, null));
  seed(keys.taskList(ORG, "space:s2", {}, null));
  seed(keys.taskList(ORG, "my:all:", {}, null));
  seed(keys.taskCounts(ORG, "space:s1", {}));
  seed(keys.taskCounts(ORG, "space:s2", {}));
  seed(keys.taskDetail(ORG, "DEF-1"), { id: "t1" });
  seed(keys.taskDetail(ORG, "DEF-2"), { id: "t2" });
  seed(keys.taskSub(ORG, "t1", "timeline"));
  seed(keys.taskSub(ORG, "t2", "timeline"));
  seed(keys.inboxList(ORG, { section: "inbox" }));
  seed(keys.inboxStats(ORG));
  seed(keys.conversations(ORG), []);
  seed(keys.iterationSettings(ORG, "s1"));
  seed(keys.iterations(ORG, "s1", {}));
  seed(keys.iteration(ORG, "s1", "i1"));
  seed(keys.projectList(ORG, "s1"));
  seed(keys.project(ORG, "p1"));
  seed(keys.viewList(ORG, "s1"));
  seed(keys.view(ORG, "v1"));
  seed(keys.labels(ORG, "organization"), []);
  const stale = () =>
    client
      .getQueryCache()
      .getAll()
      .filter((query) => query.state.isInvalidated)
      .map((query) => JSON.stringify(query.queryKey))
      .sort();
  return { client, stale };
}

const k = (key: readonly unknown[]) => JSON.stringify(key);

describe("实时事件 → 缓存失效映射", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("private human chat events refresh only human chat queries", () => {
    const { client, stale } = seeded();
    client.setQueryData(["org", ORG, "direct-chats", "list"], {});
    client.setQueryData(["org", ORG, "direct-chats", "messages", "d1"], {});
    const batcher = createEventBatcher(client, ORG);
    batcher.push("direct_chat.changed", {
      type: "direct_chat.changed",
      payload: { chat_id: "d1" },
    });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    expect(stale()).toEqual([
      k(["org", ORG, "direct-chats", "list"]),
      k(["org", ORG, "direct-chats", "messages", "d1"]),
    ]);
  });

  it("task.changed：按空间失效列表与计数（我的任务总是失效），按 task_ids 失效详情与子资源", () => {
    const { client, stale } = seeded();
    const batcher = createEventBatcher(client, ORG);
    batcher.push("task.changed", {
      type: "task.changed",
      space_id: "s1",
      payload: { task_ids: ["t1"], count: 1 },
    });
    expect(stale()).toEqual([]);
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    expect(stale()).toEqual(
      [
        k(keys.taskList(ORG, "space:s1", {}, null)),
        k(keys.taskList(ORG, "my:all:", {}, null)),
        k(keys.taskCounts(ORG, "space:s1", {})),
        k(keys.taskDetail(ORG, "DEF-1")),
        k(keys.taskSub(ORG, "t1", "timeline")),
      ].sort(),
    );
  });

  it("task.changed 不带 space_id（契约当前形状）时失效全部任务列表；task_ids 被截断时失效全部详情", () => {
    const { client, stale } = seeded();
    const batcher = createEventBatcher(client, ORG);
    batcher.push("task.changed", {
      type: "task.changed",
      payload: { task_ids: ["t1"], count: 150 },
    });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    const result = stale();
    expect(result).toContain(k(keys.taskList(ORG, "space:s2", {}, null)));
    expect(result).toContain(k(keys.taskCounts(ORG, "space:s2", {})));
    expect(result).toContain(k(keys.taskDetail(ORG, "DEF-2")));
    expect(result).toContain(k(keys.taskSub(ORG, "t2", "timeline")));
    expect(result).not.toContain(k(keys.inboxStats(ORG)));
    expect(result).not.toContain(k(keys.labels(ORG, "organization")));
  });

  it("inbox.changed 失效收件箱列表与计数；会话类事件失效会话查询", () => {
    const { client, stale } = seeded();
    const batcher = createEventBatcher(client, ORG);
    batcher.push("inbox.changed", { type: "inbox.changed", payload: {} });
    batcher.push("conversation.message", {
      type: "conversation.message",
      payload: {},
    });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    expect(stale()).toEqual(
      [
        k(keys.inboxList(ORG, { section: "inbox" })),
        k(keys.inboxStats(ORG)),
        k(keys.conversations(ORG)),
      ].sort(),
    );
  });

  it("200ms 内的多个事件合并为一次失效", () => {
    const { client } = seeded();
    const spy = vi.spyOn(client, "invalidateQueries");
    const batcher = createEventBatcher(client, ORG);
    for (let i = 0; i < 5; i++) {
      batcher.push("inbox.changed", { type: "inbox.changed", payload: {} });
      vi.advanceTimersByTime(30);
    }
    expect(spy).not.toHaveBeenCalled();
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    expect(spy.mock.calls.map(([filter]) => filter?.queryKey)).toEqual([
      keys.inbox(ORG),
      keys.inboxStats(ORG),
      keys.conversationOffers(ORG),
    ]);
  });

  it.each(["run.state", "conversation.updated"])(
    "%s：运行推进时刷新本空间的自动化记录、结果抽屉与最近运行",
    (type) => {
      const { client, stale } = seeded();
      const own = [
        keys.automations(ORG, "s1"),
        keys.automation(ORG, "s1", "a1"),
        keys.automationRuns(ORG, "s1", "a1"),
        [...keys.automationRuns(ORG, "s1", "a1"), "r1"],
      ];
      const other = [
        keys.automationRuns(ORG, "s2", "a2"),
        keys.automationRuns("other-org", "s1", "a1"),
      ];
      for (const key of [...own, ...other]) client.setQueryData(key, {});
      const batcher = createEventBatcher(client, ORG);
      batcher.push(type, {
        type,
        space_id: "s1",
        conversation_id: "c1",
        payload: { state: "SUCCEEDED" },
      });
      vi.advanceTimersByTime(EVENT_COALESCE_MS);
      for (const key of own) expect(stale()).toContain(k(key));
      for (const key of other) expect(stale()).not.toContain(k(key));
    },
  );

  it("旧版运行事件未带空间时仍刷新本组织自动化", () => {
    const { client, stale } = seeded();
    client.setQueryData(keys.automationRuns(ORG, "s1", "a1"), {});
    client.setQueryData(keys.automationRuns("other-org", "s1", "a1"), {});
    const batcher = createEventBatcher(client, ORG);
    batcher.push("run.state", { type: "run.state", payload: {} });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    expect(stale()).toContain(k(keys.automationRuns(ORG, "s1", "a1")));
    expect(stale()).not.toContain(
      k(keys.automationRuns("other-org", "s1", "a1")),
    );
  });

  it("重连后整体失效任务、收件箱、会话，不动全局配置", () => {
    const { client, stale } = seeded();
    const batcher = createEventBatcher(client, ORG);
    batcher.resync();
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    const result = stale();
    expect(result).toContain(k(keys.taskDetail(ORG, "DEF-2")));
    expect(result).toContain(k(keys.inboxStats(ORG)));
    expect(result).toContain(k(keys.conversations(ORG)));
    expect(result).not.toContain(k(keys.labels(ORG, "organization")));
  });

  it("iteration.changed：只失效目标空间的迭代设置、列表和详情，并刷新任务查询", () => {
    const { client, stale } = seeded();
    const batcher = createEventBatcher(client, ORG);
    batcher.push("iteration.changed", {
      type: "iteration.changed",
      space_id: "s1",
      payload: { iteration_ids: ["i1"], count: 1 },
    });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    expect(stale()).toContain(k(keys.iterationSettings(ORG, "s1")));
    expect(stale()).toContain(k(keys.iterations(ORG, "s1", {})));
    expect(stale()).toContain(k(keys.iteration(ORG, "s1", "i1")));
    expect(stale()).toContain(k(keys.tasks(ORG)));
  });
  it.each([
    {
      type: "project.changed",
      own: [keys.projectList(ORG, "s1"), keys.project(ORG, "p1")],
      other: [keys.viewList(ORG, "s1"), keys.view(ORG, "v1")],
    },
    {
      type: "view.changed",
      own: [keys.viewList(ORG, "s1"), keys.view(ORG, "v1")],
      other: [keys.projectList(ORG, "s1"), keys.project(ORG, "p1")],
    },
  ])("$type：失效对应的列表与详情，并刷新任务查询", ({ type, own, other }) => {
    const { client, stale } = seeded();
    const batcher = createEventBatcher(client, ORG);
    batcher.push(type, { type, payload: { deleted: false } });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    for (const key of own) expect(stale()).toContain(k(key));
    for (const key of other) expect(stale()).not.toContain(k(key));
    expect(stale()).toContain(k(keys.tasks(ORG)));
  });
  it("triage.changed：只失效带 space_id 的目标空间", () => {
    const { client, stale } = seeded();
    client.setQueryData(keys.triage(ORG, "s1"), {});
    client.setQueryData(keys.triage(ORG, "s2"), {});
    const batcher = createEventBatcher(client, ORG);
    batcher.push("triage.changed", {
      type: "triage.changed",
      space_id: "s1",
      payload: { space_id: "s1" },
    });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    expect(stale()).toContain(k(keys.triage(ORG, "s1")));
    expect(stale()).not.toContain(k(keys.triage(ORG, "s2")));
  });
  it.each([
    {
      type: "organization.changed",
      own: [keys.organizations, keys.organizationSettings(ORG)],
      other: [keys.invitations(ORG)],
    },
    {
      type: "member.changed",
      own: [
        keys.tasks(ORG),
        ["org", ORG, "search"],
        keys.memberDetails(ORG),
        keys.invitations(ORG),
        keys.organizations,
        keys.spaces(ORG),
      ],
      other: [keys.spaces("another-org")],
    },
  ])("$type：失效组织设置 / 成员与邀请", ({ type, own, other }) => {
    const { client, stale } = seeded();
    for (const key of [...own, ...other]) client.setQueryData(key, {});
    const batcher = createEventBatcher(client, ORG);
    batcher.push(type, { type, payload: {} });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    for (const key of own) expect(stale()).toContain(k(key));
    for (const key of other) expect(stale()).not.toContain(k(key));
  });
  it("无效数据与未知事件类型被忽略", () => {
    const { client, stale } = seeded();
    const batcher = createEventBatcher(client, ORG);
    batcher.push("task.changed", null);
    batcher.push("unknown.type", { payload: {} });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    expect(stale()).toEqual([]);
  });
});

describe("useOrgEvents 挂载", () => {
  afterEach(() => {
    FakeEventSource.instances = [];
    vi.unstubAllGlobals();
  });

  it("首次建连只补读当前组织已打开会话的详情和消息", async () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const client = new QueryClient();
    const dormant = keys.conversationMessages(ORG, "closed");
    client.setQueryData(dormant, "cached");
    const current = [
      keys.conversation(ORG, "open"),
      keys.conversationMessages(ORG, "open"),
    ];
    const active = [
      ...current,
      keys.conversationLists(ORG),
      keys.conversationArtifacts(ORG, "open"),
      keys.conversation("other", "open"),
    ];
    const read = vi.fn(async (_key: readonly unknown[]) => "fresh");
    function Probe() {
      useOrgEvents(ORG);
      useQueries({
        queries: active.map((queryKey) => ({
          queryKey,
          queryFn: () => read(queryKey),
          initialData: "cached",
          staleTime: Infinity,
        })),
      });
      return null;
    }
    render(
      <ApiProvider>
        <QueryClientProvider client={client}>
          <Probe />
        </QueryClientProvider>
      </ApiProvider>,
    );
    expect(read).not.toHaveBeenCalled();
    FakeEventSource.instances[0]!.open();
    await waitFor(() =>
      expect(read.mock.calls.map(([key]) => k(key)).sort()).toEqual(
        current.map(k).sort(),
      ),
    );
    expect(client.getQueryState(dormant)?.isInvalidated).toBe(false);
  });

  it("每个组织一条 EventSource，inbox.changed 触发收件箱计数重新拉取；重连后整体刷新", async () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    let stats = 0;
    renderSettings("/o/o1/inbox", (call) => {
      if (call.path.endsWith("/inbox/stats")) {
        stats += 1;
        return json(200, {
          counts: {
            inbox: stats,
            inbox_unread: stats,
            inbox_important_unread: 0,
            inbox_other_unread: 0,
            later: 0,
            cleared: 0,
          },
        });
      }
      return undefined;
    });
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
    const source = FakeEventSource.instances[0]!;
    expect(source.url).toBe("/api/v1/organizations/o1/events");
    expect(source.withCredentials).toBe(true);
    await waitFor(() => expect(stats).toBeGreaterThan(0));
    const before = stats;
    source.emit("inbox.changed", {
      type: "inbox.changed",
      entity: "notification",
      id: null,
      version: null,
      payload: { count: 1 },
      created_at: "2026-10-29T00:00:00Z",
    });
    await waitFor(() => expect(stats).toBe(before + 1));

    source.drop();
    source.open();
    await waitFor(() => expect(stats).toBe(before + 2));
  });

  it("没有 EventSource 时安全降级", async () => {
    vi.stubGlobal("EventSource", undefined);
    renderSettings("/o/o1/inbox", () => undefined);
    expect(
      await screen.findByRole("button", { name: /^电脑：/ }),
    ).toBeInTheDocument();
  });
});
