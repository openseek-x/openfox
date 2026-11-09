import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { DocNode, DocNodeDetail, DocSet } from "@openfox/api";

import { App, createQueryClient } from "../App";
import { keys } from "../context";
import {
  backend,
  json,
  ORG,
  SPACE,
  type Call,
  type Override,
} from "../tasks/test-backend";
import { createEventBatcher, collectEvent } from "../realtime/useOrgEvents";

// 文档库：空态与快速添加、文档集的新建 / 编辑 / 删除、树的按需加载与行菜单、
// 文档页的标题与正文保存、版本冲突、只读、表格编辑、上传（文件与文件夹）、移动到、搜索、doc.changed 失效。

// 页面级测试中替换掉 TipTap 正文编辑器：这里只放一个按钮，点击即把一段 Markdown 交给 onChange。
vi.mock("./BlockBody", async () => {
  const React = await import("react");
  return {
    default: (props: {
      onChange: (markdown: string) => void;
      editable: boolean;
      initialMarkdown: string;
    }) =>
      React.createElement(
        "button",
        {
          type: "button",
          disabled: !props.editable,
          onClick: () => props.onChange("# 新正文"),
        },
        props.editable
          ? "模拟编辑正文"
          : props.initialMarkdown || "模拟编辑正文",
      ),
  };
});

const o = `/v1/organizations/${ORG}`;
const sp = `${o}/spaces/${SPACE}`;
const docs = `${sp}/docs`;
const base = `/o/${ORG}/spaces/${SPACE}/docs`;
const NOW = "2026-10-30T08:00:00Z";

function node(id: string, extra: Partial<DocNode> = {}): DocNode {
  return {
    id,
    space_id: SPACE,
    container: { kind: "personal" },
    parent_id: null,
    kind: "doc",
    title: `文档 ${id}`,
    position: 0,
    size_bytes: null,
    content_type: null,
    created_by: "u1",
    updated_by: "u1",
    updated_by_name: "林",
    created_at: NOW,
    updated_at: NOW,
    version: 1,
    has_children: false,
    can_edit: true,
    ...extra,
  };
}

function docSet(id: string, name: string, extra: Partial<DocSet> = {}): DocSet {
  return {
    id,
    space_id: SPACE,
    name,
    icon_kind: null,
    icon: null,
    icon_url: null,
    position: 0,
    created_by: "u1",
    created_at: NOW,
    updated_at: NOW,
    version: 1,
    can_edit: true,
    ...extra,
  };
}

/** 内存版文档接口：记录请求，并按请求改写数据，便于断言保存结果。 */
function docBackend(options: {
  nodes?: DocNode[];
  sets?: DocSet[];
  bodies?: Record<string, string>;
  conflict?: boolean;
}) {
  let nodes = [...(options.nodes ?? [])];
  let sets = [...(options.sets ?? [])];
  const bodies: Record<string, string> = { ...(options.bodies ?? {}) };
  let seq = 0;
  const detail = (n: DocNode): DocNodeDetail => ({
    ...n,
    body: n.kind === "doc" || n.kind === "sheet" ? (bodies[n.id] ?? "") : null,
    breadcrumb: [],
  });
  const override: Override = (call: Call) => {
    const { method, path, query } = call;
    if (path === `${sp}/doc-sets` && method === "GET")
      return json(200, { doc_sets: sets });
    if (path === `${sp}/doc-sets` && method === "POST") {
      const body = call.body as { name: string };
      const created = docSet(`set-new-${++seq}`, body.name);
      sets = [...sets, created];
      return json(201, created);
    }
    const setMatch = new RegExp(`^${sp}/doc-sets/([^/]+)$`).exec(path);
    if (setMatch && method === "PATCH") {
      const body = call.body as { name: string; version: number };
      const current = sets.find((s) => s.id === setMatch[1])!;
      const next = {
        ...current,
        name: body.name,
        version: current.version + 1,
      };
      sets = sets.map((s) => (s.id === next.id ? next : s));
      return json(200, next);
    }
    if (setMatch && method === "DELETE")
      return new Response(null, { status: 204 });
    if (path === docs && method === "GET") {
      const container = query.get("container");
      const setId = query.get("set_id");
      const parent = query.get("parent_id");
      return json(200, {
        nodes: nodes.filter(
          (n) =>
            (container === "personal"
              ? n.container.kind === "personal"
              : n.container.kind === "set" && n.container.set_id === setId) &&
            (n.parent_id ?? null) === (parent ?? null),
        ),
      });
    }
    if (path === `${docs}/search` && method === "GET") {
      const q = query.get("q") ?? "";
      return json(200, {
        nodes: nodes.filter((n) => n.title.includes(q)),
      });
    }
    if (path === `${docs}/upload` && method === "POST") {
      const form = call.body as FormData;
      const file = form.get("file") as File;
      const created = node(`up-${++seq}`, {
        title: file.name.replace(/\.[^.]+$/, ""),
        kind: file.name.endsWith(".md") ? "doc" : "file",
        parent_id: (form.get("parent_id") as string | null) ?? null,
      });
      nodes = [...nodes, created];
      return json(201, created);
    }
    if (path === docs && method === "POST") {
      const body = call.body as {
        container: DocNode["container"];
        parent_id?: string;
        kind: DocNode["kind"];
        title: string;
      };
      const created = node(`new-${++seq}`, {
        container: body.container,
        parent_id: body.parent_id ?? null,
        kind: body.kind,
        title: body.title,
        has_children: body.kind === "folder",
      });
      nodes = [...nodes, created];
      return json(201, detail(created));
    }
    const moveMatch = new RegExp(`^${docs}/([^/]+)/move$`).exec(path);
    if (moveMatch && method === "POST") {
      const body = call.body as Partial<DocNode> & {
        parent_id: string | null;
        container: DocNode["container"];
      };
      const current = nodes.find((n) => n.id === moveMatch[1])!;
      const next = {
        ...current,
        parent_id: body.parent_id,
        container: body.container,
        version: current.version + 1,
      };
      nodes = nodes.map((n) => (n.id === next.id ? next : n));
      return json(200, next);
    }
    const nodeMatch = new RegExp(`^${docs}/([^/]+)$`).exec(path);
    if (nodeMatch) {
      const id = nodeMatch[1]!;
      const current = nodes.find((n) => n.id === id);
      if (!current)
        return json(404, {
          error: { code: "NOT_FOUND", message: "x", request_id: "r" },
        });
      if (method === "GET") return json(200, detail(current));
      if (method === "DELETE") {
        nodes = nodes.filter((n) => n.id !== id);
        return new Response(null, { status: 204 });
      }
      if (method === "PATCH") {
        if (options.conflict)
          return json(409, {
            error: { code: "VERSION_CONFLICT", message: "x", request_id: "r" },
          });
        const body = call.body as {
          version: number;
          title?: string;
          body?: string;
        };
        if (body.body !== undefined) bodies[id] = body.body;
        const next = {
          ...current,
          ...(body.title !== undefined ? { title: body.title } : {}),
          version: current.version + 1,
        };
        nodes = nodes.map((n) => (n.id === id ? next : n));
        return json(200, detail(next));
      }
    }
    return undefined;
  };
  return { override, bodies, nodes: () => nodes };
}

function renderDocs(
  path: string,
  options: Parameters<typeof docBackend>[0] & { override?: Override } = {},
) {
  const store = docBackend(options);
  const server = backend(
    (call) => store.override(call) ?? options.override?.(call),
  );
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return { server, store, queryClient };
}

/** 导航树在 aside（complementary）里；目录表格与导航会显示同样的名称，树相关查询都限定在导航内。 */
const navOf = async () =>
  within(await screen.findByRole("complementary", { name: "文档库" }));

const folder = node("f1", {
  kind: "folder",
  title: "需求",
  has_children: true,
});
const doc = node("d1", { title: "产品需求说明", updated_by_name: "林" });
const child = node("c1", { parent_id: "f1", title: "子文档" });

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

describe("文档库页面", () => {
  it("空态显示还没有文档，快速添加菜单列出新建与上传", async () => {
    renderDocs(base, { nodes: [] });
    expect(await screen.findByText("还没有文档")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "快速添加" }));
    expect(
      within(screen.getByRole("menu")).getByRole("menuitem", {
        name: "新建文档 (.md)",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("menuitem", { name: "新建表格 (.csv)" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("menuitem", { name: "新建文件夹" }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /上传和导入/ }));
    expect(
      screen.getByRole("menuitem", { name: "上传文件" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("menuitem", { name: "上传文件夹（保留目录结构）" }),
    ).toBeInTheDocument();
  });

  it("新建文件夹：请求体带容器、类型与默认名称，文件夹不打开", async () => {
    const { server } = renderDocs(base, { nodes: [] });
    await screen.findByText("还没有文档");
    await userEvent.click(screen.getByRole("button", { name: "快速添加" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "新建文件夹" }));
    await waitFor(() => expect(server.find("POST", "/docs")).toHaveLength(1));
    expect(server.find("POST", "/docs")[0]!.body).toEqual({
      container: { kind: "personal" },
      kind: "folder",
      title: "新建文件夹",
    });
  });

  it("我的文档首页不重复显示列表，仍能从导航打开文档并返回首页", async () => {
    renderDocs(base, { nodes: [folder, doc] });
    const nav = await navOf();
    const openDoc = await nav.findByRole("button", { name: "产品需求说明" });
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.queryByText("已经到底了")).not.toBeInTheDocument();
    expect(screen.queryByText("还没有文档")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "快速添加" }),
    ).toBeInTheDocument();
    await userEvent.click(openDoc);
    expect(await screen.findByText("模拟编辑正文")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "我的文档" }));
    expect(
      await screen.findByRole("button", { name: "快速添加" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("文件夹仍显示名称（N 项）、更新人与「已经到底了」", async () => {
    renderDocs(`${base}/f1`, { nodes: [folder, child] });
    expect(await screen.findByText("名称（1 项）")).toBeInTheDocument();
    expect(
      within(screen.getByRole("table")).getByText("子文档"),
    ).toBeInTheDocument();
    expect(screen.getByText("已经到底了")).toBeInTheDocument();
  });
});

describe("文档集", () => {
  it("新建：名称计数、超过 64 字提示并禁用，成功后请求体带默认图标", async () => {
    const { server } = renderDocs(base, { nodes: [], sets: [] });
    await userEvent.click(
      await screen.findByRole("button", { name: "新建文档集" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "新建文档集" });
    const input = within(dialog).getByPlaceholderText("文档集名称");
    await userEvent.type(input, "产品");
    expect(within(dialog).getByText("2/64")).toBeInTheDocument();
    await userEvent.clear(input);
    await userEvent.type(input, "a".repeat(65));
    expect(
      within(dialog).getByText("名称不能超过 64 个字"),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "创建" })).toBeDisabled();
    await userEvent.clear(input);
    await userEvent.type(input, "产品团队");
    await userEvent.click(within(dialog).getByRole("button", { name: "创建" }));
    await waitFor(() =>
      expect(server.find("POST", "/doc-sets")).toHaveLength(1),
    );
    expect(server.find("POST", "/doc-sets")[0]!.body).toEqual({
      name: "产品团队",
      icon: { kind: "icon", value: "book-open" },
    });
    // 创建成功后关闭并展开该集合
    expect(
      await screen.findByRole("button", { name: /产品团队/ }),
    ).toBeInTheDocument();
  });

  it("编辑：预填名称，保存带 version；删除：确认后带 version 调用 DELETE", async () => {
    const { server } = renderDocs(base, {
      nodes: [],
      sets: [docSet("set1", "产品团队")],
    });
    const name = await screen.findByRole("button", { name: /产品团队/ });
    const row = name.parentElement!;
    await userEvent.click(
      within(row).getByRole("button", { name: "更多操作" }),
    );
    await userEvent.click(screen.getByRole("menuitem", { name: "编辑" }));
    const dialog = await screen.findByRole("dialog", { name: "编辑文档集" });
    const input = within(dialog).getByDisplayValue("产品团队");
    await userEvent.clear(input);
    await userEvent.type(input, "研发");
    await userEvent.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/doc-sets/set1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/doc-sets/set1")[0]!.body).toEqual({
      name: "研发",
      icon: { kind: "icon", value: "library-big" },
      version: 1,
    });

    await userEvent.click(
      within(name.parentElement!).getByRole("button", { name: "更多操作" }),
    );
    await userEvent.click(screen.getByRole("menuitem", { name: "删除" }));
    const confirm = await screen.findByRole("dialog", { name: "删除文档集" });
    await userEvent.click(
      within(confirm).getByRole("button", { name: "删除" }),
    );
    await waitFor(() =>
      expect(server.find("DELETE", "/doc-sets/set1")).toHaveLength(1),
    );
    // 保存一次后 version 已变为 2，删除必须带上最新版本
    expect(
      server.find("DELETE", "/doc-sets/set1")[0]!.query.get("version"),
    ).toBe("2");
  });
});

describe("导航分组空态", () => {
  it("我的文档为空显示「创建第一篇文档」并可点击新建；团队文档为空显示「还没有文档集」", async () => {
    const { server } = renderDocs(base, { nodes: [], sets: [] });
    expect(await screen.findByText("还没有文档集")).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "创建第一篇文档" }),
    );
    await waitFor(() => expect(server.find("POST", "/docs")).toHaveLength(1));
    expect(server.find("POST", "/docs")[0]!.body).toMatchObject({
      kind: "doc",
      container: { kind: "personal" },
    });
  });
});

describe("文档集的快速添加", () => {
  it("文档集行的「+」在文档集根新建文档：container 为该文档集，没有父级", async () => {
    const { server } = renderDocs(base, {
      nodes: [],
      sets: [docSet("set1", "产品团队")],
    });
    const name = await screen.findByRole("button", { name: /产品团队/ });
    const row = name.parentElement!;
    await userEvent.click(
      within(row).getByRole("button", { name: "在此文档集中新建" }),
    );
    await userEvent.click(
      screen.getByRole("menuitem", { name: "新建文档 (.md)" }),
    );
    await waitFor(() => expect(server.find("POST", "/docs")).toHaveLength(1));
    const body = server.find("POST", "/docs")[0]!.body as Record<
      string,
      unknown
    >;
    expect(body.container).toEqual({ kind: "set", set_id: "set1" });
    // 文档集根没有父级：请求里不带 parent_id（契约中 parent_id 为字符串，不发送 null）
    expect(body.parent_id ?? null).toBeNull();
    expect(body.kind).toBe("doc");
  });

  it("只读文档集没有「+」", async () => {
    renderDocs(base, {
      nodes: [],
      sets: [docSet("set1", "只读集", { can_edit: false })],
    });
    const name = await screen.findByRole("button", { name: /只读集/ });
    expect(
      within(name.parentElement!).queryByRole("button", {
        name: "在此文档集中新建",
      }),
    ).not.toBeInTheDocument();
  });
});

describe("导航树", () => {
  it("文件夹展开后按需加载子节点，行菜单重命名保存带 version", async () => {
    const { server } = renderDocs(base, { nodes: [folder, doc, child] });
    await (await navOf()).findByRole("button", { name: "产品需求说明" });
    expect(screen.queryByText("子文档")).not.toBeInTheDocument();
    await userEvent.click(
      (await navOf()).getByRole("button", { name: "展开" }),
    );
    expect(
      await (await navOf()).findByRole("button", { name: /子文档/ }),
    ).toBeInTheDocument();
    expect(
      server.calls.some(
        (c) =>
          c.method === "GET" &&
          c.path === docs &&
          c.query.get("parent_id") === "f1",
      ),
    ).toBe(true);

    const docRow = (await navOf()).getByRole("button", {
      name: "产品需求说明",
    }).parentElement!;
    await userEvent.click(
      within(docRow).getByRole("button", { name: "更多操作" }),
    );
    expect(
      screen.getByRole("menuitem", { name: "添加到任务" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("menuitem", { name: "复制链接" }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("menuitem", { name: "重命名" }));
    const dialog = await screen.findByRole("dialog", { name: "重命名" });
    const input = within(dialog).getByDisplayValue("产品需求说明");
    await userEvent.clear(input);
    await userEvent.type(input, "新标题");
    await userEvent.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(server.find("PATCH", "/docs/d1")).toHaveLength(1),
    );
    expect(server.find("PATCH", "/docs/d1")[0]!.body).toEqual({
      version: 1,
      title: "新标题",
    });
  });

  it("复制链接写入剪贴板并提示；删除确认后请求 DELETE", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    const { server } = renderDocs(base, { nodes: [doc] });
    const docRow = (
      await (await navOf()).findByRole("button", { name: "产品需求说明" })
    ).parentElement!;
    await userEvent.click(
      within(docRow).getByRole("button", { name: "更多操作" }),
    );
    await userEvent.click(screen.getByRole("menuitem", { name: "复制链接" }));
    expect(await screen.findByText("链接已复制")).toBeInTheDocument();
    expect(writeText).toHaveBeenCalledWith(
      expect.stringMatching(/\/o\/o1\/spaces\/s1\/docs\/d1$/),
    );

    await userEvent.click(
      within(docRow).getByRole("button", { name: "更多操作" }),
    );
    await userEvent.click(screen.getByRole("menuitem", { name: "删除" }));
    const confirm = await screen.findByRole("dialog", { name: "删除文档" });
    expect(
      within(confirm).getByText(/它下面的文档会一起删除/),
    ).toBeInTheDocument();
    await userEvent.click(
      within(confirm).getByRole("button", { name: "删除" }),
    );
    await waitFor(() =>
      expect(server.find("DELETE", "/docs/d1")).toHaveLength(1),
    );
  });

  it("添加到任务：打开新建任务弹窗，描述里是文档链接", async () => {
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: vi.fn() },
      configurable: true,
    });
    renderDocs(base, { nodes: [doc] });
    const docRow = (
      await (await navOf()).findByRole("button", { name: "产品需求说明" })
    ).parentElement!;
    await userEvent.click(
      within(docRow).getByRole("button", { name: "更多操作" }),
    );
    await userEvent.click(screen.getByRole("menuitem", { name: "添加到任务" }));
    const dialog = await screen.findByRole("dialog", { name: "新建任务" });
    expect(
      within(dialog).getByDisplayValue(/\/o\/o1\/spaces\/s1\/docs\/d1$/),
    ).toBeInTheDocument();
  });
});

describe("文档编辑页", () => {
  it("标题保存带 version，正文 1.5 秒防抖后保存 Markdown", async () => {
    const { server } = renderDocs(`${base}/d1`, {
      nodes: [doc],
      bodies: { d1: "# 旧正文" },
    });
    const title = await screen.findByDisplayValue("产品需求说明");
    await userEvent.clear(title);
    await userEvent.type(title, "改名");
    await userEvent.click(
      await screen.findByRole("button", { name: "模拟编辑正文" }),
    );
    await waitFor(
      () => expect(server.find("PATCH", "/docs/d1").length).toBeGreaterThan(0),
      { timeout: 4000 },
    );
    const bodies = server
      .find("PATCH", "/docs/d1")
      .map((c) => c.body as Record<string, unknown>);
    expect(bodies.at(-1)).toEqual({
      version: 1,
      title: "改名",
      body: "# 新正文",
    });
  });

  it("版本冲突时提示文档已在别处修改，请刷新", async () => {
    renderDocs(`${base}/d1`, {
      nodes: [doc],
      bodies: { d1: "" },
      conflict: true,
    });
    await userEvent.click(
      await screen.findByRole("button", { name: "模拟编辑正文" }),
    );
    expect(
      await screen.findByText(
        "文档已在别处修改，请刷新",
        {},
        { timeout: 4000 },
      ),
    ).toBeInTheDocument();
  });

  it("只读文档显示可查看徽章，标题不可编辑", async () => {
    renderDocs(`${base}/d1`, {
      nodes: [node("d1", { title: "只读文档", can_edit: false })],
      bodies: { d1: "" },
    });
    expect(await screen.findByText("可查看")).toBeInTheDocument();
    expect(screen.getByDisplayValue("只读文档")).toHaveAttribute("readonly");
  });
});

describe("表格", () => {
  it("编辑单元格后 1.5 秒自动保存 CSV", async () => {
    const sheet = node("s1", {
      kind: "sheet",
      title: "数据表",
      content_type: "text/csv",
    });
    const { server } = renderDocs(`${base}/s1`, {
      nodes: [sheet],
      bodies: { s1: "a,b\n1,2" },
    });
    const cell = await screen.findByRole("textbox", {
      name: "第 2 行，第 2 列",
    });
    expect(cell).toHaveValue("2");
    await userEvent.clear(cell);
    await userEvent.type(cell, "9");
    await waitFor(
      () => expect(server.find("PATCH", "/docs/s1").length).toBeGreaterThan(0),
      {
        timeout: 4000,
      },
    );
    expect(server.find("PATCH", "/docs/s1").at(-1)!.body).toEqual({
      version: 1,
      body: "a,b\n1,9",
    });
  });
});

describe("上传", () => {
  it("上传文件：每个文件一次 upload 请求", async () => {
    const { server } = renderDocs(base, { nodes: [] });
    await screen.findByText("还没有文档");
    await userEvent.click(screen.getByRole("button", { name: "快速添加" }));
    await userEvent.click(screen.getByRole("button", { name: /上传和导入/ }));
    await userEvent.click(screen.getByRole("menuitem", { name: "上传文件" }));
    const input = document.querySelector<HTMLInputElement>(
      'input[type="file"]:not([webkitdirectory])',
    )!;
    await userEvent.upload(input, [
      new File(["# a"], "a.md", { type: "text/markdown" }),
      new File(["x"], "b.pdf", { type: "application/pdf" }),
    ]);
    await waitFor(() =>
      expect(server.find("POST", "/docs/upload")).toHaveLength(2),
    );
    expect(server.find("POST", "/docs/upload")[0]!.body).toBeInstanceOf(
      FormData,
    );
  });

  it("超过 20 MB 的文件在本地拦截，不发上传请求", async () => {
    const { server } = renderDocs(base, { nodes: [] });
    await screen.findByText("还没有文档");
    await userEvent.click(screen.getByRole("button", { name: "快速添加" }));
    await userEvent.click(screen.getByRole("button", { name: /上传和导入/ }));
    await userEvent.click(screen.getByRole("menuitem", { name: "上传文件" }));
    const input = document.querySelector<HTMLInputElement>(
      'input[type="file"]:not([webkitdirectory])',
    )!;
    const big = new File(["x"], "大文件.zip");
    Object.defineProperty(big, "size", { value: 21 * 1024 * 1024 });
    await userEvent.upload(input, big);
    expect(
      await screen.findByText("已跳过 1 个超过 20 MB 的文件"),
    ).toBeInTheDocument();
    expect(server.find("POST", "/docs/upload")).toHaveLength(0);
  });

  it("上传文件夹：子树里全部超过 20 MB 的文件夹不创建，汇总提示跳过数量", async () => {
    const { server } = renderDocs(base, { nodes: [] });
    await screen.findByText("还没有文档");
    await userEvent.click(screen.getByRole("button", { name: "快速添加" }));
    await userEvent.click(screen.getByRole("button", { name: /上传和导入/ }));
    await userEvent.click(
      screen.getByRole("menuitem", { name: "上传文件夹（保留目录结构）" }),
    );
    const input = document.querySelector<HTMLInputElement>(
      "input[webkitdirectory]",
    )!;
    const withPath = (file: File, path: string) => {
      Object.defineProperty(file, "webkitRelativePath", { value: path });
      return file;
    };
    const small = withPath(new File(["# a"], "a.md"), "包/a.md");
    // 「大」子树里只有超限文件，不应创建「大」
    const bigNested = withPath(new File(["x"], "大.zip"), "包/大/大.zip");
    Object.defineProperty(bigNested, "size", { value: 21 * 1024 * 1024 });
    // 「空」里只有超限文件，整个文件夹都不应创建
    const bigOnly = withPath(new File(["x"], "超.zip"), "空/子/超.zip");
    Object.defineProperty(bigOnly, "size", { value: 21 * 1024 * 1024 });
    await userEvent.upload(input, [small, bigNested, bigOnly]);
    await waitFor(() =>
      expect(server.find("POST", "/docs/upload")).toHaveLength(1),
    );
    expect(
      await screen.findByText("已跳过 2 个超过 20 MB 的文件"),
    ).toBeInTheDocument();
    const titles = server
      .find("POST", "/docs")
      .map((c) => (c.body as { title: string }).title);
    expect(titles).toEqual(["包"]);
  });

  it("上传文件夹：先按路径建目录，再把文件放进对应目录", async () => {
    const { server } = renderDocs(base, { nodes: [] });
    await screen.findByText("还没有文档");
    await userEvent.click(screen.getByRole("button", { name: "快速添加" }));
    await userEvent.click(screen.getByRole("button", { name: /上传和导入/ }));
    await userEvent.click(
      screen.getByRole("menuitem", { name: "上传文件夹（保留目录结构）" }),
    );
    const input = document.querySelector<HTMLInputElement>(
      "input[webkitdirectory]",
    )!;
    const top = new File(["# a"], "a.md");
    Object.defineProperty(top, "webkitRelativePath", { value: "包/a.md" });
    const nested = new File(["# b"], "b.md");
    Object.defineProperty(nested, "webkitRelativePath", {
      value: "包/子/b.md",
    });
    await userEvent.upload(input, [top, nested]);
    await waitFor(() =>
      expect(server.find("POST", "/docs/upload")).toHaveLength(2),
    );
    const folders = server.find("POST", "/docs").map((c) => c.body);
    expect(folders).toEqual([
      { container: { kind: "personal" }, kind: "folder", title: "包" },
      {
        container: { kind: "personal" },
        parent_id: expect.any(String),
        kind: "folder",
        title: "子",
      },
    ]);
    const [aUpload, bUpload] = server
      .find("POST", "/docs/upload")
      .map((c) => c.body as FormData);
    expect(aUpload!.get("parent_id")).toBeTruthy();
    expect(bUpload!.get("parent_id")).toBeTruthy();
    expect(bUpload!.get("parent_id")).not.toBe(aUpload!.get("parent_id"));
  });
});

describe("移动到", () => {
  it("把文档移入文件夹：请求体带目标容器与父级", async () => {
    const { server } = renderDocs(base, { nodes: [folder, doc] });
    const docRow = (
      await (await navOf()).findByRole("button", { name: "产品需求说明" })
    ).parentElement!;
    await userEvent.click(
      within(docRow).getByRole("button", { name: "更多操作" }),
    );
    await userEvent.click(screen.getByRole("menuitem", { name: "移动到" }));
    const dialog = await screen.findByRole("dialog", { name: "移动到" });
    expect(within(dialog).getByText("当前空间")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "展开" }));
    await userEvent.click(
      await within(dialog).findByRole("button", { name: /需求/ }),
    );
    await userEvent.click(within(dialog).getByRole("button", { name: "移动" }));
    await waitFor(() =>
      expect(server.find("POST", "/docs/d1/move")).toHaveLength(1),
    );
    expect(server.find("POST", "/docs/d1/move")[0]!.body).toEqual({
      version: 1,
      target_space_id: SPACE,
      container: { kind: "personal" },
      parent_id: "f1",
      before_id: null,
    });
  });

  it("移动文件夹时自身禁用并标「不能移入自身」", async () => {
    renderDocs(base, { nodes: [folder] });
    const row = (await (await navOf()).findByRole("button", { name: "需求" }))
      .parentElement!;
    await userEvent.click(
      within(row).getByRole("button", { name: "更多操作" }),
    );
    await userEvent.click(screen.getByRole("menuitem", { name: "移动到" }));
    const dialog = await screen.findByRole("dialog", { name: "移动到" });
    await userEvent.click(within(dialog).getByRole("button", { name: "展开" }));
    const self = await within(dialog).findByRole("button", {
      name: /需求.*不能移入自身/,
    });
    expect(self).toBeDisabled();
  });
});

describe("搜索", () => {
  it("输入关键词后防抖搜索，点击结果打开文档", async () => {
    const { server } = renderDocs(base, { nodes: [doc], bodies: { d1: "" } });
    await userEvent.click(await screen.findByRole("button", { name: "搜索" }));
    const input = await screen.findByRole("textbox", { name: "搜索文档" });
    expect(screen.getByText("输入关键词，按名称搜索文档")).toBeInTheDocument();
    await userEvent.type(input, "需求");
    const results = within(
      await screen.findByRole("dialog", { name: "搜索文档" }),
    );
    const result = await results.findByRole("button", { name: /产品需求说明/ });
    await userEvent.click(result);
    expect(await screen.findByDisplayValue("产品需求说明")).toBeInTheDocument();
    expect(
      server.calls.some(
        (c) => c.path === `${docs}/search` && c.query.get("q") === "需求",
      ),
    ).toBe(true);
  });
});

describe("doc.changed 实时失效", () => {
  it("带 space_id 的事件只失效该空间的文档查询", () => {
    vi.useFakeTimers();
    try {
      const queryClient = createQueryClient();
      const spy = vi.spyOn(queryClient, "invalidateQueries");
      const batcher = createEventBatcher(queryClient, ORG);
      batcher.push("doc.changed", { space_id: SPACE, entity: "doc" });
      vi.advanceTimersByTime(250);
      expect(spy).toHaveBeenCalledWith({ queryKey: keys.docs(ORG, SPACE) });
      batcher.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it("没有 space_id 时整体失效组织下的文档查询", () => {
    const pending: Parameters<typeof collectEvent>[0] = {
      all: false,
      taskSpaces: new Set<string>(),
      taskLists: false,
      taskIds: new Set<string>(),
      taskDetails: false,
      inbox: false,
      conversations: false,
      conversationIds: new Set<string>(),
      timelines: false,
      timelineTaskIds: new Set<string>(),
      messageConversations: new Set<string>(),
      spaces: false,
      iterationSpaces: new Set<string>(),
      iterationIds: new Set<string>(),
      projects: false,
      views: false,
      triageSpaces: new Set<string>(),
      organization: false,
      members: false,
      automationSpaces: new Set<string>(),
      docSpaces: new Set<string>(),
      skillSpaces: new Set<string>(),
    };
    collectEvent(pending, "doc.changed", {});
    expect(pending.docSpaces).toBeNull();
  });
});

describe("文档历史版本", () => {
  it("打开前完成防抖保存，预览旧版并恢复为新版本", async () => {
    let restored = false;
    const revision = (version: number) => ({
      revision: version,
      node_version: version,
      title: version === 1 ? "历史标题" : "产品需求说明",
      created_by: "u1",
      created_by_name: "林",
      created_at: NOW,
    });
    const { server, queryClient } = renderDocs(`${base}/d1`, {
      nodes: [doc],
      bodies: { d1: "原正文" },
      override: (call) => {
        if (call.path === `${docs}/d1/revisions`)
          return json(200, {
            revisions: [revision(restored ? 3 : 2), revision(1)],
            next_before: 0,
          });
        if (call.path === `${docs}/d1/revisions/1/restore`) {
          restored = true;
          return json(200, {
            ...doc,
            title: "历史标题",
            body: "历史正文",
            version: 3,
            breadcrumb: [],
          });
        }
        if (call.path.startsWith(`${docs}/d1/revisions/`))
          return json(200, {
            ...revision(Number(call.path.split("/").at(-1))),
            body: "历史正文",
          });
        return undefined;
      },
    });
    await userEvent.click(
      await screen.findByRole("button", { name: "模拟编辑正文" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "历史版本" }));
    const dialog = await screen.findByRole("dialog", { name: "历史版本" });
    expect(server.find("PATCH", "/docs/d1")).toHaveLength(1);
    await userEvent.click(
      await within(dialog).findByRole("option", { name: /^版本 v1/ }),
    );
    expect(await within(dialog).findByText("历史正文")).toBeInTheDocument();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "恢复此版本" }),
    );
    await within(dialog).findByRole("option", { name: /^版本 v3 当前版本/ });
    expect(server.find("POST", "/revisions/1/restore")[0]!.body).toEqual({
      version: 2,
    });
    expect(
      queryClient.getQueryData(keys.docNode(ORG, SPACE, "d1")),
    ).toMatchObject({ title: "历史标题", body: "历史正文", version: 3 });
    await userEvent.keyboard("{Escape}");
    expect(await screen.findByDisplayValue("历史标题")).toBeInTheDocument();
  });

  it("只读文档可预览历史但不显示恢复", async () => {
    const rev = (n: number) => ({
      revision: n,
      node_version: n,
      title: "只读历史",
      created_by: "u1",
      created_by_name: "林",
      created_at: NOW,
      body: "READ_ONLY",
    });
    renderDocs(`${base}/d1`, {
      nodes: [{ ...doc, can_edit: false }],
      override: (call) => {
        if (call.path.endsWith("/revisions"))
          return json(200, { revisions: [rev(2), rev(1)], next_before: 0 });
        if (call.path.includes("/revisions/")) return json(200, rev(1));
        return undefined;
      },
    });
    await userEvent.click(
      await screen.findByRole("button", { name: "历史版本" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "历史版本" });
    await userEvent.click(
      await within(dialog).findByRole("option", { name: /^版本 v1/ }),
    );
    expect(await within(dialog).findByText("READ_ONLY")).toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: "恢复此版本" }),
    ).not.toBeInTheDocument();
  });

  it("保存失败不打开历史，保留当前编辑和错误", async () => {
    renderDocs(`${base}/d1`, { nodes: [doc], conflict: true });
    await userEvent.click(
      await screen.findByRole("button", { name: "模拟编辑正文" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "历史版本" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "文档已在别处修改",
    );
    await userEvent.click(screen.getByRole("button", { name: "历史版本" }));
    expect(
      screen.queryByRole("dialog", { name: "历史版本" }),
    ).not.toBeInTheDocument();
  });
});
