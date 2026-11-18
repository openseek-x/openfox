import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import { createApi, HttpClient, type RunArtifact } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";
import { ApiProvider, keys } from "../../context";
import { ArtifactPanel } from "./ArtifactPanel";
import { currentTabKey, useArtifactPanel } from "./params";
import { workspaceStore } from "../workspaceStorage";
import {
  capturePreviewSelection,
  previewSourceKey,
  previewCapabilities,
} from "./previewProtocol";

const first: RunArtifact = {
  organization_id: "o1",
  id: "a1",
  run_id: "r1",
  name: "报告.md",
  mime_type: "text/markdown",
  size_bytes: 20,
  sha256: "a".repeat(64),
  state: "AVAILABLE",
  pinned: false,
  version: 1,
  created_at: "2026-11-17T00:00:00Z",
  content_id: "content1",
  content_revision: 1,
  head_artifact_id: "a1",
};
const second: RunArtifact = {
  ...first,
  id: "a2",
  sha256: "b".repeat(64),
  content_revision: 2,
  head_artifact_id: "a2",
  replaces_artifact_id: "a1",
};
beforeEach(() => {
  localStorage.clear();
});
function fixture(path = "current:content1", list = [first], resolve = first) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  let requestSignal: AbortSignal | undefined;
  const transport = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      if (url.pathname.endsWith("/artifact-preview"))
        return Response.json(resolve);
      if (url.pathname.endsWith("/conversations/c1/artifacts"))
        return Response.json({ artifacts: list });
      if (url.pathname.endsWith("/artifacts/a1"))
        return new Response("# 第一版正文");
      if (url.pathname.endsWith("/artifacts/a2"))
        return new Response("# 第二版正文");
      if (url.pathname.endsWith("/artifacts/loading")) {
        requestSignal = init?.signal ?? undefined;
        return new Promise<Response>((_, reject) =>
          init?.signal?.addEventListener("abort", () =>
            reject(new DOMException("Aborted", "AbortError")),
          ),
        );
      }
      return Response.json({});
    },
  );
  const api = createApi(new HttpClient("/api", transport));
  function Panel() {
    const panel = useArtifactPanel("c1");
    return <ArtifactPanel orgId="o1" conversationId="c1" panel={panel} />;
  }
  const result = render(
    <I18nProvider initialLocale="zh-CN">
      <ToastProvider>
        <ApiProvider api={api}>
          <QueryClientProvider client={client}>
            <MemoryRouter initialEntries={[`/?artifact=${path}`]}>
              <Panel />
            </MemoryRouter>
          </QueryClientProvider>
        </ApiProvider>
      </ToastProvider>
    </I18nProvider>,
  );
  return { ...result, client, transport, getSignal: () => requestSignal };
}

it("current tabs follow head while fixed tabs retain bytes, independent view state and annotation off", async () => {
  const { client, unmount } = fixture();
  await screen.findByRole("heading", { name: "第一版正文" });
  expect(screen.getByRole("button", { name: "批注" })).toHaveAttribute(
    "aria-pressed",
    "false",
  );
  await userEvent.click(screen.getByRole("button", { name: "更多预览操作" }));
  await userEvent.click(
    screen.getByRole("button", { name: "在固定版本标签中打开" }),
  );
  await waitFor(() =>
    expect(screen.getAllByRole("tab", { name: "报告.md" })).toHaveLength(2),
  );
  await userEvent.click(screen.getByRole("button", { name: "源码" }));
  act(() =>
    client.setQueryData(keys.conversationArtifacts("o1", "c1"), [
      second,
      { ...first, head_artifact_id: "a2" },
    ]),
  );
  expect(screen.getByText("# 第一版正文")).toBeInTheDocument();
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "批注" })).toBeNull(),
  );
  const tabs = screen.getAllByRole("tab", { name: "报告.md" });
  await userEvent.click(
    tabs.find((tab) =>
      tab.getAttribute("aria-description")?.startsWith("当前版"),
    )!,
  );
  await screen.findByRole("heading", { name: "第二版正文" });
  expect(screen.getByRole("button", { name: "批注" })).toHaveAttribute(
    "aria-pressed",
    "false",
  );
  await userEvent.click(
    tabs.find((tab) =>
      tab.getAttribute("aria-description")?.startsWith("固定版本"),
    )!,
  );
  await screen.findByText("# 第一版正文");
  unmount();
  // Stage 9 store is reused; refresh recreates both tab identities, not two aliases of head.
  expect(
    workspaceStore().read<{ tabs: string[] }>("workspace:c1")?.tabs,
  ).toEqual([currentTabKey(first.content_id), first.id]);
  fixture("a1", [second, { ...first, head_artifact_id: "a2" }]);
  await screen.findByText("# 第一版正文");
  expect(screen.getAllByRole("tab", { name: "报告.md" })).toHaveLength(2);
});

it("resolves a fixed revision outside the recent list instead of deleting its tab", async () => {
  const { transport } = fixture("a1", [], { ...first, head_artifact_id: "a2" });
  await screen.findByRole("heading", { name: "第一版正文" });
  expect(screen.getByRole("tab", { name: "报告.md" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  expect(
    transport.mock.calls.some(([url]) =>
      String(url).includes("artifact-preview?artifact_id=a1"),
    ),
  ).toBe(true);
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "批注" })).toBeNull(),
  );
});

it("closing a loading preview aborts its request and releases its cached content", async () => {
  const loading = { ...first, id: "loading", head_artifact_id: "loading" };
  const { getSignal, unmount, client } = fixture("loading", [loading]);
  await waitFor(() => expect(getSignal()).toBeDefined());
  unmount();
  expect(getSignal()?.aborted).toBe(true);
  await waitFor(() =>
    expect(
      client
        .getQueryCache()
        .getAll()
        .filter((q) => q.queryKey.includes("content")),
    ).toHaveLength(0),
  );
});

it("selection snapshots freeze source identity and state the exact encoding/geometry", () => {
  const source = {
    kind: "document" as const,
    spaceId: "s1",
    nodeId: "n1",
    nodeVersion: 4,
    sha256: first.sha256,
  };
  const anchor = { kind: "text" as const, start: 3, end: 7, quote: "🙂" };
  const selected = capturePreviewSelection(source, anchor);
  source.nodeVersion = 5;
  anchor.start = 9;
  expect(selected.source).toMatchObject({ nodeVersion: 4 });
  expect(selected.anchor.start).toBe(3);
  expect(selected.coordinates).toEqual({
    space: "source-text",
    encoding: "utf-8",
    offsets: "bytes-half-open",
  });
  expect(previewSourceKey(selected.source)).not.toBe(previewSourceKey(source));
  expect(
    capturePreviewSelection(source, {
      kind: "region",
      start: 0,
      end: 0,
      quote: "",
      page: 1,
      x: 0,
      y: 0,
      width: 1,
      height: 1,
    }).coordinates.space,
  ).toBe("unrotated-page-fraction");
  expect(
    previewCapabilities({ ...first, mime_type: "application/pdf" }).selection,
  ).toEqual(["pdf_text", "region"]);
});
