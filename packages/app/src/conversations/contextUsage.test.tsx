import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { createApi, HttpClient, type Run, type RunEvent } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ApiProvider, keys } from "../context";
import { ContextUsage } from "./ContextUsage";
import { contextSample } from "./contextUsageModel";
import { appendRunEvent } from "./steps/queries";

const at = "2026-11-13T08:00:00Z";
const run: Run = {
  id: "r1",
  organization_id: "o1",
  space_id: "s1",
  conversation_id: "c1",
  agent_id: "a1",
  computer_id: "d1",
  grant_id: "g1",
  config_version: 1,
  grant_version: 1,
  actor_id: "u1",
  state: "RUNNING",
  generation: 2,
  reconciliation_required: false,
  reason: "",
  version: 3,
  created_at: at,
  updated_at: at,
  failure_code: "",
  started_at: at,
  usage: null,
  duration_ms: null,
};
function event(
  sequence: number,
  data: Record<string, unknown> = {},
  extra: Partial<RunEvent> = {},
): RunEvent {
  return {
    id: `e${sequence}`,
    organization_id: "o1",
    run_id: "r1",
    generation: 2,
    sequence,
    stream_seq: sequence,
    type: "context_usage",
    occurred_at: at,
    received_at: at,
    safe_payload: {
      data: {
        schema: 1,
        runtime: "codex",
        requested_model: "m1",
        model: "actual-m1",
        current_tokens: 100,
        window_tokens: 1000,
        ...data,
      },
    },
    ...extra,
  };
}
function preparation(runtime: string, extra: Partial<RunEvent> = {}): RunEvent {
  return event(
    0,
    {},
    {
      type: "step",
      safe_payload: {
        data: {
          schema: 1,
          step: {
            id: "prep",
            kind: "prep_env",
            state: "done",
            substeps: [{ kind: "runtime", state: "done", label: runtime }],
          },
        },
      },
      ...extra,
    },
  );
}
function mount(
  events: RunEvent[],
  options: {
    truncated?: boolean;
    terminal?: boolean;
    failed?: boolean;
    usage?: Run["usage"];
  } = {},
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  const api = createApi(
    new HttpClient(
      "/api",
      (async () =>
        new Response("{}", {
          status: 503,
          headers: { "Content-Type": "application/json" },
        })) as typeof fetch,
    ),
  );
  if (!options.failed) {
    client.setQueryData(keys.run("o1", "r1"), {
      ...run,
      state: options.terminal ? "SUCCEEDED" : "RUNNING",
      usage: options.usage ?? null,
    });
    client.setQueryData(keys.runSub("o1", "r1", "events"), {
      events,
      truncated: options.truncated ?? false,
    });
  }
  const view = (model: string, runtime = "codex") => (
    <I18nProvider initialLocale="zh-CN">
      <ApiProvider api={api}>
        <QueryClientProvider client={client}>
          <ContextUsage orgId="o1" runId="r1" effective={{ runtime, model }} />
        </QueryClientProvider>
      </ApiProvider>
    </I18nProvider>
  );
  const result = render(view("m1"));
  return {
    client,
    change: (model: string, runtime = "codex") =>
      result.rerender(view(model, runtime)),
  };
}

describe("context sample selection", () => {
  it("uses sequence within this run/generation, including compaction decreases", () => {
    const sample = contextSample(run, [
      event(8, { current_tokens: 60000 }),
      event(10, { current_tokens: 6236 }),
      event(9, { current_tokens: 50000 }),
      event(99, {}, { generation: 1 }),
      event(100, {}, { run_id: "other" }),
    ]);
    expect(sample?.current).toBe(6236);
    expect(contextSample({ ...run, generation: 3 }, [event(10)])).toBeNull();
  });
  it.each([-1, 0.5, Number.MAX_SAFE_INTEGER + 1, "9", null, undefined])(
    "invalid current %s is unknown",
    (value) => {
      expect(
        contextSample(run, [event(1, { current_tokens: value })])?.current,
      ).toBeNull();
    },
  );
  it("zero and safe maximum are real samples; zero window is unknown", () => {
    expect(
      contextSample(run, [event(1, { current_tokens: 0, window_tokens: 0 })]),
    ).toMatchObject({ current: 0, window: null });
    expect(
      contextSample(run, [
        event(1, { current_tokens: Number.MAX_SAFE_INTEGER }),
      ])?.current,
    ).toBe(Number.MAX_SAFE_INTEGER);
  });
  it("does not show an older sample after malformed metadata or unsupported schema", () => {
    expect(contextSample(run, [event(1), event(2, { schema: 2 })])).toBeNull();
    expect(
      contextSample(run, [event(1), event(2, { runtime: "other" })]),
    ).toBeNull();
    expect(
      contextSample(run, [event(1, {}, { occurred_at: "invalid" })]),
    ).toBeNull();
  });
});

describe("ContextUsage", () => {
  it("renders a compact real zero and retains sample metadata in its title", () => {
    mount([event(1, { current_tokens: 0 })]);
    expect(screen.getByText("0% 上下文")).toBeVisible();
    const summary = screen.getByTitle(/上下文：0 token/);
    expect(screen.getByRole("progressbar")).toHaveAttribute(
      "aria-valuenow",
      "0",
    );
    expect(summary.title).toContain("actual-m1");
    expect(summary.title).toContain("本轮样本");
    expect(summary.title).toContain(new Date(at).toLocaleString("zh-CN"));
    expect(summary.title).toContain("Runtime 报告的估计");
    expect(screen.queryByText("actual-m1")).toBeNull();
    expect(screen.queryByTitle("本轮样本")).toBeNull();
    expect(screen.queryByText(/缓存命中/)).toBeNull();
  });
  it("accepts live decreasing samples and ignores repeated/late updates", async () => {
    const { client } = mount([event(5, { current_tokens: 900 })]);
    act(() => {
      appendRunEvent(client, "o1", event(7, { current_tokens: 200 }));
      appendRunEvent(client, "o1", event(6, { current_tokens: 800 }));
      appendRunEvent(client, "o1", event(7, { current_tokens: 999 }));
    });
    await waitFor(() => expect(screen.getByText("20% 上下文")).toBeVisible());
    expect(screen.getByTitle(/上下文：200 token/)).toBeVisible();
  });
  it("shows the real over-window ratio while clamping the visual meter", () => {
    mount([event(1, { current_tokens: 1500 })]);
    expect(screen.getByRole("progressbar")).toHaveAttribute(
      "aria-valuenow",
      "100",
    );
    expect(screen.getByRole("progressbar")).toHaveAttribute(
      "aria-valuetext",
      "150%",
    );
  });
  it("keeps known tokens without inventing a window", () => {
    mount([event(1, { window_tokens: null, model: null })]);
    expect(screen.getByText("上下文 100 token")).toBeVisible();
    expect(screen.getByTitle(/上限未知/).title).toContain("模型未报告");
    expect(screen.queryByRole("progressbar")).toBeNull();
  });
  it("labels terminal samples and hides them after a model change", () => {
    const { change } = mount([event(1)], { terminal: true });
    expect(screen.getByTitle(/最近运行样本/)).toBeVisible();
    change("m2");
    expect(screen.getByTitle(/配置已变更，等待新运行样本/)).toHaveTextContent(
      "上下文 未知",
    );
    expect(screen.queryByRole("progressbar")).toBeNull();
  });
  it("retains the running model's sample when next-run configuration changes", () => {
    const { change } = mount([event(1)]);
    change("m2");
    expect(screen.getByRole("progressbar")).toBeVisible();
    expect(screen.queryByTitle(/配置已变更，等待新运行样本/)).toBeNull();
  });
  it("invalidates terminal context after changing Runtime without reinterpreting the old Run's usage", () => {
    const { change } = mount([preparation("codex"), event(1)], {
      terminal: true,
      usage: {
        input: 1000,
        cached_input: 800,
        cache_write_input: 100,
        output: 500,
        reasoning_output: 100,
      },
    });
    change("m1", "claude-code");
    expect(screen.getByText("上下文 未知")).toBeVisible();
    expect(screen.queryByRole("progressbar")).toBeNull();
    expect(screen.getByText("1.5K tok")).toBeVisible();
    expect(screen.getByText("缓存命中 80%")).toBeVisible();
  });
  it("does not present truncated history or legacy events as current usage", () => {
    mount([event(1)], { truncated: true });
    expect(screen.getByText("上下文 未知")).toBeVisible();
    expect(screen.queryByRole("progressbar")).toBeNull();
  });
  it("waits for the full event snapshot when realtime populates the cache during loading", async () => {
    const { client } = mount([event(1)]);
    let finish!: (value: { events: RunEvent[]; truncated: boolean }) => void;
    let loading!: Promise<unknown>;
    act(() => {
      loading = client.fetchQuery({
        queryKey: keys.runSub("o1", "r1", "events"),
        staleTime: 0,
        queryFn: () =>
          new Promise((resolve) => {
            finish = resolve;
          }),
      });
      appendRunEvent(client, "o1", event(2, { current_tokens: 900 }));
    });
    await waitFor(() => expect(screen.queryByRole("progressbar")).toBeNull());
    await act(async () => {
      finish({ events: [event(1), event(2)], truncated: true });
      await loading;
    });
    expect(screen.queryByRole("progressbar")).toBeNull();
  });
  it("shows unknown when authenticated event loading fails", async () => {
    const { client } = mount([], { failed: true });
    await waitFor(() =>
      expect(
        client.getQueryState(keys.runSub("o1", "r1", "events"))?.status,
      ).toBe("error"),
    );
    expect(screen.getByText("上下文 未知")).toBeVisible();
  });

  it("shows Codex's reported cumulative total and input cache hit rate", () => {
    mount([event(1)], {
      terminal: true,
      usage: {
        input: 900000,
        cached_input: 819000,
        output: 36000,
        cache_write_input: 0,
        reasoning_output: 1000,
      },
    });
    expect(screen.getByText("936K tok")).toBeVisible();
    expect(screen.getByText("缓存命中 91%")).toBeVisible();
    expect(screen.getByTitle("最近运行样本")).toHaveTextContent("936K tok");
    expect(screen.getByTitle("936,000 token")).toBeVisible();
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("keeps real zero usage visible without inventing a cache ratio", () => {
    mount([event(1)], {
      usage: {
        input: 0,
        cached_input: 0,
        output: 0,
        cache_write_input: 0,
        reasoning_output: 0,
      },
    });
    expect(screen.getByText("0 tok")).toBeVisible();
    expect(screen.queryByText(/缓存命中/)).toBeNull();
  });

  it.each(["claude-code", "opencode", "pi", "codebuddy"])(
    "includes separate cache reads and writes for %s, using the Run's preparation snapshot",
    (runtime) => {
      const { change } = mount([preparation(runtime)], {
        terminal: true,
        usage: {
          input: 1000,
          cached_input: 8000,
          cache_write_input: 1000,
          output: 500,
          reasoning_output: 100,
        },
      });
      expect(screen.getByText("10.5K tok")).toBeVisible();
      expect(screen.getByText("缓存命中 80%")).toBeVisible();
      expect(screen.getByText("上下文 未知")).toBeVisible();
      change("next-model");
      expect(screen.getByText("10.5K tok")).toBeVisible();
      expect(screen.getByText("缓存命中 80%")).toBeVisible();
    },
  );

  it("ignores preparation from another run/generation and selects the latest snapshot", () => {
    mount(
      [
        preparation("claude-code", { generation: 1, sequence: 100 }),
        preparation("claude-code", { run_id: "other", sequence: 101 }),
        preparation("codex", { sequence: 2 }),
        preparation("claude-code", { sequence: 1 }),
      ],
      {
        usage: {
          input: 1000,
          cached_input: 800,
          cache_write_input: 100,
          output: 500,
          reasoning_output: 100,
        },
      },
    );
    expect(screen.getByText("1.5K tok")).toBeVisible();
    expect(screen.getByText("缓存命中 80%")).toBeVisible();
  });

  it.each(["hermes", "gemini", "unknown"])(
    "does not invent unverified cache semantics for %s",
    (runtime) => {
      mount([preparation(runtime)], {
        usage: {
          input: 1000,
          cached_input: 800,
          cache_write_input: 0,
          output: 500,
          reasoning_output: 100,
        },
      });
      expect(screen.getByText("↓ 1K tok")).toBeVisible();
      expect(screen.getByText("↑ 500 tok")).toBeVisible();
      expect(screen.queryByText(/缓存命中/)).toBeNull();
    },
  );

  it("does not infer total tokens or cache semantics from the next-run configuration", () => {
    mount([], {
      terminal: true,
      usage: {
        input: 1000,
        cached_input: 2000,
        output: 100,
        cache_write_input: 0,
        reasoning_output: 0,
      },
    });
    expect(screen.getByText("↓ 1K tok")).toBeVisible();
    expect(screen.getByText("↑ 100 tok")).toBeVisible();
    expect(screen.queryByText(/缓存命中/)).toBeNull();
    expect(screen.getByText("上下文 未知")).toBeVisible();
  });

  it("omits invalid counters and impossible cache hit rates", () => {
    mount([event(1)], {
      usage: {
        input: 100,
        cached_input: 200,
        output: -1,
        cache_write_input: 0,
        reasoning_output: 0,
      },
    });
    expect(screen.getByText("↓ 100 tok")).toBeVisible();
    expect(screen.queryByText(/↑/)).toBeNull();
    expect(screen.queryByText(/缓存命中/)).toBeNull();
  });
  it("does not replace missing cache counters with zero", () => {
    mount([preparation("claude-code")], {
      usage: {
        input: 1000,
        output: 500,
        reasoning_output: 0,
      } as Run["usage"],
    });
    expect(screen.getByText("↓ 1K tok")).toBeVisible();
    expect(screen.getByText("↑ 500 tok")).toBeVisible();
    expect(screen.queryByText(/缓存命中/)).toBeNull();
  });
});
