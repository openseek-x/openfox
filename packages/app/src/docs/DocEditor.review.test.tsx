import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { createApi, HttpClient, type DocNodeDetail } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ApiProvider } from "../context";
import { DocEditor } from "./DocEditor";

vi.mock("./BlockBody", () => ({
  default: ({
    initialMarkdown,
    editable,
    onChange,
  }: {
    initialMarkdown: string;
    editable: boolean;
    onChange: (s: string) => void;
  }) => (
    <textarea
      aria-label="document body"
      defaultValue={initialMarkdown}
      readOnly={!editable}
      onChange={(e) => onChange(e.target.value)}
    />
  ),
}));
vi.mock("./NativeReviewSetup", () => ({
  NativeReviewSetup: ({ onStart }: { onStart: (id: string) => void }) => (
    <button onClick={() => onStart("c1")}>start review</button>
  ),
}));
vi.mock("../conversations/artifacts/ArtifactReview", () => ({
  ArtifactReview: () => <div>review opened</div>,
}));
const node = {
  id: "n1",
  title: "Draft",
  body: "original",
  version: 1,
  can_edit: true,
} as DocNodeDetail;
const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

function fixture(
  handle: (
    path: string,
    method: string,
    body: any,
  ) => Promise<Response> | Response,
) {
  const api = createApi(
    new HttpClient("/api", (async (input, init) =>
      handle(
        String(input),
        init?.method ?? "GET",
        typeof init?.body === "string" ? JSON.parse(init.body) : null,
      )) as typeof fetch),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  let flush = async () => true;
  const onFlushReady = (fn: () => Promise<boolean>) => {
    flush = fn;
  };
  const tree = (n: DocNodeDetail) => (
    <I18nProvider initialLocale="zh-CN">
      <QueryClientProvider client={client}>
        <ApiProvider api={api}>
          <DocEditor
            orgId="o1"
            spaceId="s1"
            node={n}
            onFlushReady={onFlushReady}
          />
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>
  );
  const view = render(tree(node));
  return {
    flush: () => flush(),
    rerender: (n: DocNodeDetail) => view.rerender(tree(n)),
  };
}

describe("native review editor boundary", () => {
  it("keeps unsaved content and its original version after a remote refresh", async () => {
    const versions: number[] = [];
    let ensured = false;
    const f = fixture((path, method, body) => {
      if (method === "PATCH") {
        versions.push(body.version);
        return json(409, {
          error: { code: "VERSION_CONFLICT", message: "conflict" },
        });
      }
      if (path.endsWith("native-reviews")) ensured = true;
      return json(404, {});
    });
    fireEvent.change(await screen.findByLabelText("document body"), {
      target: { value: "local draft" },
    });
    f.rerender({ ...node, body: "remote body", version: 5 });
    let saved = true;
    await act(async () => {
      saved = await f.flush();
    });
    expect(saved).toBe(false);
    expect(versions).toEqual([1]);
    expect(screen.getByLabelText("document body")).toHaveValue("local draft");
    fireEvent.click(screen.getByText("批注与 AI 修改"));
    fireEvent.click(screen.getByText("start review"));
    await waitFor(() => expect(versions.length).toBe(2));
    expect(ensured).toBe(false);
    expect(screen.queryByText("review opened")).toBeNull();
  });
  it("waits for saving before selecting and reloads current content on exit", async () => {
    let complete: () => void = () => {};
    const order: string[] = [];
    fixture(async (_path, method, body) => {
      if (method === "PATCH") {
        order.push("save");
        await new Promise<void>((r) => {
          complete = r;
        });
        return json(200, { ...node, body: body.body, version: 2 });
      }
      order.push("read");
      return json(200, { ...node, body: "current content", version: 3 });
    });
    fireEvent.change(await screen.findByLabelText("document body"), {
      target: { value: "saved draft" },
    });
    fireEvent.click(screen.getByText("批注与 AI 修改"));
    fireEvent.click(screen.getByText("start review"));
    await waitFor(() => expect(order).toEqual(["save"]));
    expect(screen.getByLabelText("document body")).toHaveAttribute("readonly");
    await act(async () => complete());
    expect(await screen.findByText("review opened")).toBeInTheDocument();
    expect(order).toEqual(["save", "read"]);
    fireEvent.click(screen.getByText("返回文档编辑"));
    expect(await screen.findByLabelText("document body")).toHaveValue(
      "current content",
    );
  });
});

it("保存响应丢失后正文一致，自动对齐版本并继续保存", async () => {
  const versions: number[] = [];
  let remote = { ...node, body: "same", version: 2 };
  const f = fixture((_path, method, body) => {
    if (method === "PATCH") {
      versions.push(body.version);
      if (body.version === 1)
        return json(409, {
          error: { code: "VERSION_CONFLICT", message: "conflict" },
        });
      remote = { ...remote, ...body, version: remote.version + 1 };
      return json(200, remote);
    }
    return json(200, remote);
  });
  fireEvent.change(await screen.findByLabelText("document body"), {
    target: { value: "same" },
  });
  let ok = false;
  await act(async () => {
    ok = await f.flush();
  });
  expect(ok).toBe(true);
  fireEvent.change(screen.getByLabelText("document body"), {
    target: { value: "next" },
  });
  await act(async () => {
    ok = await f.flush();
  });
  expect(ok).toBe(true);
  expect(versions).toEqual([1, 2]);
});
it("真实冲突保留本地正文，展示远端内容，合并确认后才使用新版保存", async () => {
  const versions: number[] = [];
  const f = fixture((_path, method, body) => {
    if (method === "PATCH") {
      versions.push(body.version);
      return body.version === 1
        ? json(409, {
            error: { code: "VERSION_CONFLICT", message: "conflict" },
          })
        : json(200, { ...node, ...body, version: 3 });
    }
    return json(200, { ...node, body: "remote", version: 2 });
  });
  fireEvent.change(await screen.findByLabelText("document body"), {
    target: { value: "local" },
  });
  await act(async () => {
    expect(await f.flush()).toBe(false);
  });
  expect(screen.getByLabelText("document body")).toHaveValue("local");
  expect(screen.getByLabelText("远端正文")).toHaveValue("remote");
  fireEvent.change(screen.getByLabelText("document body"), {
    target: { value: "local + remote" },
  });
  fireEvent.click(screen.getByRole("button", { name: "已合并，保存本地内容" }));
  await waitFor(() => expect(versions).toEqual([1, 2]));
});
