import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import {
  createApi,
  HttpClient,
  type WorkspaceProposal,
  type WorkspaceProposalDecision,
} from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ApiProvider } from "../../context";
import { ProposalPreview } from "./ProposalPreview";
import { resetDrafts } from "../drafts";
import { changedLines } from "./diff";
import { workspaceStore } from "../workspaceStorage";
import { canvasDraftKey } from "../canvas/documentDraft";
const initial: WorkspaceProposal = {
  id: "p1",
  conversation_id: "c1",
  run_id: "r1",
  source: {
    id: "context",
    artifact_id: "a1",
    sha256: "a".repeat(64),
    comments: [],
  },
  base: {
    organization_id: "o1",
    id: "a1",
    run_id: "r1",
    name: "方案.md",
    mime_type: "text/markdown",
    size_bytes: 10,
    sha256: "a".repeat(64),
    state: "AVAILABLE",
    pinned: false,
    version: 1,
    created_at: "2026-11-17T00:00:00Z",
  },
  size_bytes: 10,
  sha256: "b".repeat(64),
  summary: "明确审阅流程",
  status: "PENDING",
  version: 1,
  created_by: "u1",
  created_at: "2026-11-17T00:00:00Z",
};
beforeEach(() => {
  resetDrafts();
  URL.createObjectURL = vi.fn(() => "blob:preview");
  URL.revokeObjectURL = vi.fn();
});
function fixture(
  options: {
    loseResponse?: boolean;
    conflict?: boolean;
    native?: boolean;
  } = {},
) {
  let p = structuredClone(initial);
  if (options.native)
    p.source = {
      ...p.source,
      artifact_id: undefined,
      space_id: "s1",
      node_id: "n1",
      node_version: 1,
    };
  const requests: WorkspaceProposalDecision[] = [];
  const transport = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      if (url.pathname.endsWith("/decision")) {
        const d = JSON.parse(String(init?.body)) as WorkspaceProposalDecision;
        requests.push(d);
        if (options.conflict)
          return Response.json(
            { error: { code: "REVIEW_CONFLICT", message: "changed" } },
            { status: 409 },
          );
        p = {
          ...p,
          status: d.action === "accept" ? "ACCEPTED" : "REJECTED",
          decision_id: d.operation_id,
          version: 2,
        };
        if (options.loseResponse) {
          options.loseResponse = false;
          throw new TypeError("connection lost");
        }
        return Response.json(p);
      }
      if (url.pathname.endsWith("/content"))
        return new Response(
          url.searchParams.get("side") === "base"
            ? "# 原始方案\n先交付"
            : "# 修改方案\n先审阅",
        );
      if (url.pathname.endsWith("/proposals/p1")) return Response.json(p);
      return Response.json({ proposals: [p], next_cursor: "" });
    },
  );
  const api = createApi(new HttpClient("/api", transport));
  const mount = () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    });
    const view = render(
      <I18nProvider initialLocale="zh-CN">
        <ApiProvider api={api}>
          <QueryClientProvider client={client}>
            <MemoryRouter>
              <ProposalPreview orgId="o1" conversationId="c1" id="p1" />
            </MemoryRouter>
          </QueryClientProvider>
        </ApiProvider>
      </I18nProvider>,
    );
    return { ...view, client };
  };
  return { mount, requests, transport };
}
it("requires an explicit decision and offers original/candidate/diff without a management sidebar", async () => {
  const f = fixture();
  f.mount();
  await screen.findByRole("heading", { name: "修改方案" });
  expect(f.requests).toHaveLength(0);
  expect(screen.queryByRole("button", { name: "批注" })).toBeNull();
  expect(document.querySelector("aside")).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "原件" }));
  await screen.findByRole("heading", { name: "原始方案" });
  await userEvent.click(screen.getByRole("button", { name: "差异" }));
  await screen.findByText("− # 原始方案");
  await screen.findByText("+ # 修改方案");
  await userEvent.click(screen.getByRole("button", { name: "拒绝" }));
  await screen.findByText("候选已拒绝，未修改原件。");
  expect(f.requests[0]?.action).toBe("reject");
  expect(screen.queryByRole("button", { name: "接受修改" })).toBeNull();
});
it("retries an uncertain acceptance with the exact operation and expected version", async () => {
  const f = fixture({ loseResponse: true });
  f.mount();
  await screen.findByRole("button", { name: "接受修改" });
  await userEvent.click(screen.getByRole("button", { name: "接受修改" }));
  await screen.findByRole("alert");
  expect(screen.queryByRole("button", { name: "拒绝" })).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "重试确认" }));
  await screen.findByText("修改已作为新版本应用。");
  expect(f.requests).toHaveLength(2);
  expect(f.requests[0]).toEqual(f.requests[1]);
  expect(f.requests[0]?.version).toBe(1);
});
it("recovers an acknowledged decision on reopen without reapplying", async () => {
  const f = fixture({ loseResponse: true });
  const view = f.mount();
  await screen.findByRole("button", { name: "接受修改" });
  await userEvent.click(screen.getByRole("button", { name: "接受修改" }));
  await screen.findByRole("alert");
  view.unmount();
  view.client.clear();
  f.mount();
  await screen.findByText("修改已作为新版本应用。");
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "重试确认" })).toBeNull(),
  );
  expect(f.requests).toHaveLength(1);
});
it("keeps a conflicted candidate reviewable and never automatically overwrites", async () => {
  const f = fixture({ conflict: true });
  f.mount();
  await screen.findByRole("button", { name: "接受修改" });
  await userEvent.click(screen.getByRole("button", { name: "接受修改" }));
  await screen.findByText(/原件或提案状态已变化/);
  expect(f.requests).toHaveLength(1);
  expect(screen.getByRole("heading", { name: "修改方案" })).toBeInTheDocument();
});
it("bounds diff rendering and distinguishes equal content from truncated changes", () => {
  expect(changedLines("a", "a").lines).toEqual([]);
  const diff = changedLines(
    "start\n" + "a\n".repeat(2000) + "end",
    "start\n" + "b\n".repeat(2000) + "end",
  );
  expect(diff.truncated).toBe(true);
  expect(diff.lines.length).toBeLessThan(2010);
  expect(diff.lines[0]?.text).toBe("start");
  expect(diff.lines.at(-1)?.text).toBe("end");
});

it("blocks native acceptance with a recovered local draft but still permits rejection", async () => {
  workspaceStore().write(canvasDraftKey("s1", "n1"), {
    body: "local edits",
    base: { version: 1 },
  });
  const f = fixture({ native: true });
  f.mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "接受修改" }),
  );
  await screen.findByText("请先打开文档并保存本地修改，再审阅这个提案。");
  expect(f.requests).toHaveLength(0);
  await userEvent.click(screen.getByRole("button", { name: "拒绝" }));
  await screen.findByText("候选已拒绝，未修改原件。");
  expect(f.requests[0]?.action).toBe("reject");
});
it("confirms a lost native decision even if a newer local draft appears", async () => {
  const f = fixture({ native: true, loseResponse: true });
  f.mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "接受修改" }),
  );
  await screen.findByRole("alert");
  workspaceStore().write(canvasDraftKey("s1", "n1"), {
    body: "newer edits",
    base: { version: 2 },
  });
  await userEvent.click(screen.getByRole("button", { name: "重试确认" }));
  await screen.findByText("修改已作为新版本应用。");
  expect(f.requests).toHaveLength(2);
  expect(f.requests[0]).toEqual(f.requests[1]);
  expect(workspaceStore().read(canvasDraftKey("s1", "n1"))).toMatchObject({
    body: "newer edits",
  });
});
