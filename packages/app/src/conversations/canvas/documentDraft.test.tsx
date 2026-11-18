import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createApi, HttpClient, type DocNodeDetail } from "@openfox/api";
import { ApiProvider } from "../../context";
import { beforeEach, expect, it } from "vitest";
import {
  canvasDraftKey,
  flushCanvasSource,
  useCanvasDocument,
} from "./documentDraft";
import { resetWorkspaceMemory, workspaceStore } from "../workspaceStorage";
const node = {
  id: "n1",
  title: "Draft",
  body: "original",
  version: 1,
  can_edit: true,
  kind: "doc",
} as DocNodeDetail;
beforeEach(resetWorkspaceMemory);
function fixture(
  handle: (method: string, body: any) => Response | Promise<Response>,
  initial = node,
) {
  const api = createApi(
    new HttpClient("/api", async (_, init) =>
      handle(
        init?.method ?? "GET",
        typeof init?.body === "string" ? JSON.parse(init.body) : null,
      ),
    ),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderHook(({ remote }) => useCanvasDocument("o1", "s1", remote), {
    initialProps: { remote: initial },
    wrapper: ({ children }) => (
      <QueryClientProvider client={client}>
        <ApiProvider api={api}>{children}</ApiProvider>
      </QueryClientProvider>
    ),
  });
}
it("retains a failed draft across remount with its original version, even after a remote change", async () => {
  const versions: number[] = [];
  const f = fixture((method, b) => {
    if (method === "PATCH") {
      versions.push(b.version);
      return Response.json(
        { error: { code: "VERSION_CONFLICT", message: "changed" } },
        { status: 409 },
      );
    }
    return Response.json({ ...node, body: "remote", version: 2 });
  });
  act(() => f.result.current.change("local"));
  f.rerender({ remote: { ...node, body: "remote", version: 2 } });
  await act(async () => expect(await f.result.current.flush()).toBe(false));
  expect(f.result.current.body.current).toBe("local");
  expect(versions).toEqual([1]);
  expect(workspaceStore().read<any>(canvasDraftKey("s1", "n1")).body).toBe(
    "local",
  );
  f.unmount();
  const g = fixture(
    () =>
      Response.json(
        { error: { code: "VERSION_CONFLICT", message: "changed" } },
        { status: 409 },
      ),
    { ...node, body: "remote", version: 2 },
  );
  expect(g.result.current.body.current).toBe("local");
  expect(g.result.current.base.current.version).toBe(1);
});
it("serializes a new edit behind an in-flight save and preserves both versions", async () => {
  let release!: (r: Response) => void;
  const calls: any[] = [];
  const f = fixture((_method, b) => {
    calls.push(b);
    return calls.length === 1
      ? new Promise<Response>((r) => {
          release = r;
        })
      : Response.json({ ...node, body: b.body, version: 3 });
  });
  act(() => f.result.current.change("first"));
  let done!: Promise<boolean>;
  act(() => {
    done = f.result.current.flush();
  });
  await waitFor(() => expect(calls).toHaveLength(1));
  act(() => f.result.current.change("second"));
  await act(async () => {
    release(Response.json({ ...node, body: "first", version: 2 }));
    expect(await done).toBe(true);
  });
  expect(calls).toEqual([
    { version: 1, body: "first" },
    { version: 2, body: "second" },
  ]);
  expect(f.result.current.body.current).toBe("second");
  expect(f.result.current.dirty).toBe(false);
});
it("treats a lost successful response as success only when reloaded content matches", async () => {
  let attempts = 0;
  const f = fixture((method, _b) =>
    method === "GET"
      ? Response.json({ ...node, body: "saved", version: 2 })
      : ++attempts === 1
        ? Promise.reject(Error("lost"))
        : Response.json(
            { error: { code: "VERSION_CONFLICT", message: "changed" } },
            { status: 409 },
          ),
  );
  act(() => f.result.current.change("saved"));
  await act(async () => expect(await f.result.current.flush()).toBe(false));
  await act(async () => expect(await f.result.current.flush()).toBe(true));
  expect(f.result.current.base.current.version).toBe(2);
  expect(workspaceStore().read(canvasDraftKey("s1", "n1"))).toBeUndefined();
});
it("blocks accepting a proposal while an unmounted document has a recoverable draft", async () => {
  workspaceStore().write(canvasDraftKey("s1", "n1"), {
    base: node,
    body: "local",
  });
  expect(await flushCanvasSource("s1", "n1")).toBe(false);
  expect(await flushCanvasSource("s1", "other")).toBe(true);
});
