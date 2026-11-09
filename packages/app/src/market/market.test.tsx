import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  MarketSkill,
  MarketSkillDetail,
  MarketTemplate,
  MarketTemplateDetail,
  Skill,
  SkillDetail,
} from "@openfox/api";

import { agent, device } from "../agents/fixtures";
import {
  apiError,
  findCall,
  json,
  renderSettings,
  type Call,
  type Route,
} from "../settings/testing";
import { initialOf, pickText } from "./model";

// 市场：空目录、技能 / 模板切换与筛选请求、加载更多、精选包翻页、技能详情与各类文件预览、
// 添加到空间、模板详情、使用模板创建 Agent、技能页的「可更新」。

const o = "/v1/organizations/o1";
const NOW = "2026-10-30T08:00:00Z";

function space(id: string, name: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    prefix: id.toUpperCase(),
    name,
    visibility: "shared",
    version: 1,
    created_at: NOW,
    icon: null,
    purpose: "GENERAL",
    features: {
      views: false,
      projects: false,
      iterations: false,
      triage: false,
    },
    multi_assignee: false,
    estimation: "NONE",
    status: "active",
    retired_at: null,
    position: null,
    my_role: "OWNER",
    can_manage: true,
    member_count: 1,
    task_type_count: 1,
    label_count: 0,
    task_count: 0,
    updated_at: NOW,
    ...extra,
  };
}

// s1、s2 可写；s3 无写权限；s4 已停用
const spaces = [
  space("s1", "默认空间"),
  space("s2", "OpenFox"),
  space("s3", "只读空间", { my_role: null, can_manage: false }),
  space("s4", "已停用空间", { status: "retired" }),
];

function mskill(id: string, extra: Partial<MarketSkill> = {}): MarketSkill {
  return {
    market_skill_id: id,
    version: "1.0.0",
    name: { "zh-Hans": `技能 ${id}`, en: `Skill ${id}` },
    description: { "zh-Hans": `${id} 的描述`, en: `${id} description` },
    icon: null,
    category_ids: ["dev"],
    tags: [{ "zh-Hans": "标签", en: "tag" }],
    publisher: { name: "官方", verified: true },
    origin: "skillhub",
    featured: false,
    downloads: 1200,
    stars: 30,
    file_count: 3,
    total_bytes: 2048,
    ...extra,
  };
}

function mtemplate(
  id: string,
  extra: Partial<MarketTemplate> = {},
): MarketTemplate {
  return {
    market_template_id: id,
    version: "1",
    name: { "zh-Hans": `模板 ${id}`, en: `Template ${id}` },
    job_title: { "zh-Hans": "前端工程师", en: "Frontend" },
    tagline: { "zh-Hans": "写界面", en: "builds UI" },
    description: { "zh-Hans": "负责界面开发", en: "UI dev" },
    category_ids: ["dev"],
    keywords: ["react"],
    avatar: {},
    skill_count: 2,
    origin: "builtin",
    ...extra,
  };
}

const catalog = {
  source: "builtin",
  catalog_version: "1",
  imported_at: NOW,
  categories: [
    {
      id: "dev",
      name: { "zh-Hans": "开发", en: "Dev" },
      order: 1,
      skill_count: 2,
      template_count: 1,
    },
    {
      id: "writing",
      name: { "zh-Hans": "写作", en: "Writing" },
      order: 2,
      skill_count: 1,
      template_count: 0,
    },
  ],
  packs: ["p1", "p2", "p3", "p4"].map((id) => ({
    id,
    name: { "zh-Hans": `精选包 ${id}`, en: `Pack ${id}` },
    tint: "#33bbff",
    members: [
      {
        market_skill_id: "a1",
        name: { "zh-Hans": "技能 a1" },
        icon: null,
        available: true,
      },
      {
        market_skill_id: "gone",
        name: { "zh-Hans": "消失的技能" },
        icon: null,
        available: false,
      },
    ],
  })),
  counts: { skills: 3, templates: 1 },
};

const SKILL_MD = "---\nname: demo\nlicense: MIT\n---\n\n# 标题\n\n正文";

interface Files {
  [path: string]: {
    content?: string;
    binary?: boolean;
    truncated?: boolean;
    size?: number;
    mime?: string;
  };
}

const detailFiles = [
  { path: "SKILL.md", size_bytes: 60, mime: "text/markdown" },
  { path: "scripts/run.ts", size_bytes: 20, mime: "text/plain" },
  { path: "scripts/lib/util.ts", size_bytes: 20, mime: "text/plain" },
  { path: "data.csv", size_bytes: 500, mime: "text/csv" },
  { path: "pack.bin", size_bytes: 2048, mime: "application/octet-stream" },
  { path: "big.txt", size_bytes: 2_000_000, mime: "text/plain" },
  { path: ".env", size_bytes: 30, mime: "text/plain" },
  { path: "logo.png", size_bytes: 100, mime: "image/png" },
  { path: "icon.svg", size_bytes: 100, mime: "image/svg+xml" },
];

function mdetail(id: string): MarketSkillDetail {
  return { ...mskill(id), files: detailFiles };
}

const csvRows = ["id,name"];
for (let i = 1; i <= 150; i++) csvRows.push(`${i},row${i}`);

const files: Files = {
  "SKILL.md": { content: SKILL_MD },
  "scripts/run.ts": { content: "const a = 1;\nconst b = 2;\n" },
  "data.csv": { content: csvRows.join("\n") },
  "big.txt": {
    content: Array.from({ length: 800 }, (_, i) => `line ${i + 1}`).join("\n"),
    truncated: true,
  },
  ".env": { content: "# 注释\nAPI_KEY=secret123\nPORT=8080" },
  "icon.svg": { content: "<svg></svg>" },
};

interface Options {
  catalog?: typeof catalog;
  skills?: MarketSkill[];
  /** 第二页（next_cursor 指向） */
  page2?: MarketSkill[];
  templates?: MarketTemplate[];
  route?: Route;
  mySkills?: Skill[];
}

function skill(id: string, extra: Partial<Skill> = {}): Skill {
  return {
    id,
    space_id: "s1",
    name: `skill-${id}`,
    description: "",
    source_kind: "manual",
    source_label: "",
    source_url: null,
    market_skill_id: null,
    market_version: null,
    market_latest_version: null,
    file_count: 1,
    total_bytes: 10,
    created_by: { id: "u1", name: "林" },
    used_by: [],
    used_count: 0,
    can_edit: true,
    version: 3,
    created_at: NOW,
    updated_at: NOW,
    ...extra,
  };
}

function skillDetail(item: Skill): SkillDetail {
  return {
    ...item,
    files: [{ path: "SKILL.md", size_bytes: 10 }],
    agents: [],
  };
}

function setup(path: string, options: Options = {}) {
  const items = options.skills ?? [mskill("a1"), mskill("a2")];
  let mySkills = [...(options.mySkills ?? [])];
  const route: Route = (call) => {
    const custom = options.route?.(call);
    if (custom) return custom;
    const { method, path: p } = call;
    if (p === `${o}/spaces` && method === "GET") return json(200, { spaces });
    if (p === `${o}/devices`)
      return json(200, { devices: [device("d1")], next_cursor: null });
    if (p === `${o}/market/catalog`)
      return json(200, options.catalog ?? catalog);
    if (p === `${o}/market/skills` && method === "GET") {
      if (call.query.get("cursor") === "c2")
        return json(200, {
          items: options.page2 ?? [],
          next_cursor: null,
          total: 3,
        });
      return json(200, {
        items,
        next_cursor: options.page2 ? "c2" : null,
        total: options.page2 ? 3 : items.length,
      });
    }
    if (p === `${o}/market/templates` && method === "GET") {
      const list = options.templates ?? [mtemplate("t1")];
      return json(200, { items: list, next_cursor: null, total: list.length });
    }
    const detail = /^\/v1\/organizations\/o1\/market\/skills\/([^/]+)$/.exec(p);
    if (detail && method === "GET") return json(200, mdetail(detail[1]!));
    const file =
      /^\/v1\/organizations\/o1\/market\/skills\/([^/]+)\/file$/.exec(p);
    if (file) {
      const name = call.query.get("path") ?? "";
      if (name === "pack.bin")
        return json(200, {
          path: name,
          size_bytes: 2048,
          mime: "application/octet-stream",
          binary: true,
          truncated: false,
          content: "",
        });
      const entry = files[name];
      if (!entry) return apiError(404, "NOT_FOUND");
      return json(200, {
        path: name,
        size_bytes: entry.content?.length ?? 0,
        mime: "text/plain",
        binary: false,
        truncated: entry.truncated ?? false,
        content: entry.content ?? "",
      });
    }
    if (/\/market\/skills\/[^/]+\/download$/.test(p))
      return new Response(new Uint8Array([80, 78, 71]), {
        status: 200,
        headers: { "Content-Type": "image/png" },
      });
    const tpl = /^\/v1\/organizations\/o1\/market\/templates\/([^/]+)$/.exec(p);
    if (tpl && method === "GET") return json(200, templateDetail(tpl[1]!));
    // 空间技能
    if (p === `${o}/spaces/s1/skills` && method === "GET")
      return json(200, { skills: mySkills, can_create: true });
    if (p === `${o}/spaces/s1/skills/market` && method === "POST") {
      const created = skill("new1", { source_kind: "market" });
      mySkills = [...mySkills, created];
      return json(201, skillDetail(created));
    }
    const mine = /^\/v1\/organizations\/o1\/spaces\/s1\/skills\/([^/]+)$/.exec(
      p,
    );
    if (mine && method === "GET") {
      const found = mySkills.find((s) => s.id === mine[1]);
      return found ? json(200, skillDetail(found)) : apiError(404, "NOT_FOUND");
    }
    if (/\/skills\/[^/]+\/files$/.test(p) && method === "GET")
      return json(200, {
        path: "SKILL.md",
        size_bytes: 3,
        binary: false,
        content: "abc",
      });
    return undefined;
  };
  const calls = renderSettings(path, route);
  return calls;
}

function templateDetail(id: string): MarketTemplateDetail {
  return {
    ...mtemplate(id),
    instructions: {
      "zh-Hans": "# 你是前端工程师\n\n负责界面",
      en: "# You are a frontend engineer",
    },
    quick_prompts: [{ "zh-Hans": "帮我写一个组件", en: "write a component" }],
    skills: [
      {
        market_skill_id: "a1",
        version: "1.0.0",
        available: true,
        skill: mskill("a1"),
      },
      {
        market_skill_id: "gone",
        version: "1.0.0",
        available: false,
        skill: null,
      },
    ],
  };
}

const marketCalls = (calls: Call[], suffix: string) =>
  calls.filter((c) => c.method === "GET" && c.path === `${o}/market/${suffix}`);

beforeEach(() => {
  localStorage.clear();
  URL.createObjectURL = vi.fn(() => "blob:preview");
  URL.revokeObjectURL = vi.fn();
});
afterEach(() => {
  localStorage.clear();
});

/** 代码预览被语法高亮拆成多个 span，按 <code> 的完整文本匹配。 */
function codeText(pattern: string | RegExp) {
  return (_: string, element: Element | null) =>
    element?.tagName === "CODE" &&
    (typeof pattern === "string"
      ? element.textContent === pattern
      : pattern.test(element.textContent ?? ""));
}

describe("纯函数", () => {
  it("多语言字段按界面语言取，缺失取另一种", () => {
    expect(pickText({ "zh-Hans": "中", en: "En" }, "zh-CN")).toBe("中");
    expect(pickText({ "zh-Hans": "中", en: "En" }, "en-US")).toBe("En");
    expect(pickText({ en: "Only" }, "zh-CN")).toBe("Only");
    expect(pickText({ "zh-Hans": "仅中文" }, "en-US")).toBe("仅中文");
    expect(pickText({}, "zh-CN")).toBe("");
    expect(initialOf(" abc")).toBe("A");
  });
});

describe("市场列表", () => {
  it("目录为空时显示「市场还没有内容」", async () => {
    setup("/o/o1/market", {
      catalog: {
        ...catalog,
        categories: [],
        packs: [],
        counts: { skills: 0, templates: 0 },
      },
    });
    expect(await screen.findByText("市场还没有内容")).toBeInTheDocument();
  });

  it("默认请求技能（limit=30，无 sort / category / q），卡片显示名称、发布者与下载量；多语言缺失取另一种", async () => {
    const calls = setup("/o/o1/market", {
      skills: [mskill("a1"), mskill("a2", { name: { en: "Only English" } })],
    });
    expect(
      await screen.findByRole("button", { name: "查看技能 技能 a1" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Only English")).toBeInTheDocument();
    expect(screen.getAllByText("官方").length).toBeGreaterThan(0);
    const first = marketCalls(calls, "skills")[0]!;
    expect(first.query.get("limit")).toBe("30");
    expect(first.query.get("sort")).toBeNull();
    expect(first.query.get("category")).toBeNull();
    expect(first.query.get("q")).toBeNull();
  });

  it("图标加载失败时显示首字母块", async () => {
    setup("/o/o1/market", {
      skills: [mskill("a1", { icon: "https://cdn.example.com/a1.png" })],
    });
    const card = await screen.findByRole("button", {
      name: "查看技能 技能 a1",
    });
    const img = card.querySelector("img")!;
    expect(img).toHaveAttribute("referrerpolicy", "no-referrer");
    expect(img).toHaveAttribute("loading", "lazy");
    fireEvent.error(img);
    expect(card.querySelector("img")).toBeNull();
    expect(within(card).getByText("技")).toBeInTheDocument();
  });

  it("切换到 Agent 模板：请求模板接口并显示模板卡片", async () => {
    const user = userEvent.setup();
    const calls = setup("/o/o1/market");
    await screen.findByRole("button", { name: "查看技能 技能 a1" });
    await user.click(screen.getByRole("radio", { name: "Agent 模板" }));
    expect(await screen.findByText("模板 t1")).toBeInTheDocument();
    expect(screen.getByText("2 个技能")).toBeInTheDocument();
    expect(marketCalls(calls, "templates")).toHaveLength(1);
    // 精选包只在技能页显示；模板页没有排序按钮
    expect(
      screen.queryByRole("region", { name: "精选包" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /排序/ }),
    ).not.toBeInTheDocument();
  });

  it("选了分类后精选包仍然显示；排序按钮直接显示当前排序名", async () => {
    const user = userEvent.setup();
    setup("/o/o1/market");
    await screen.findByRole("button", { name: "查看技能 技能 a1" });
    const sort = screen.getByRole("button", { name: "排序：综合评分" });
    expect(sort).toHaveTextContent(/^综合评分$/);
    await user.click(screen.getByRole("button", { name: "开发" }));
    expect(screen.getByRole("region", { name: "精选包" })).toBeInTheDocument();
    await user.click(sort);
    await user.click(screen.getByRole("menuitemradio", { name: "名称" }));
    expect(
      screen.getByRole("button", { name: "排序：名称" }),
    ).toHaveTextContent(/^名称$/);
  });

  it("分类、排序、搜索都带进请求参数", async () => {
    const user = userEvent.setup();
    const calls = setup("/o/o1/market");
    await screen.findByRole("button", { name: "查看技能 技能 a1" });
    await user.click(screen.getByRole("button", { name: "开发" }));
    await waitFor(() =>
      expect(marketCalls(calls, "skills").at(-1)!.query.get("category")).toBe(
        "dev",
      ),
    );
    await user.click(screen.getByRole("button", { name: /排序/ }));
    await user.click(screen.getByRole("menuitemradio", { name: "下载量" }));
    await waitFor(() =>
      expect(marketCalls(calls, "skills").at(-1)!.query.get("sort")).toBe(
        "downloads",
      ),
    );
    await user.type(
      screen.getByRole("textbox", { name: "搜索市场" }),
      "review",
    );
    await waitFor(() =>
      expect(marketCalls(calls, "skills").at(-1)!.query.get("q")).toBe(
        "review",
      ),
    );
    const last = marketCalls(calls, "skills").at(-1)!;
    expect(last.query.get("category")).toBe("dev");
    expect(last.query.get("sort")).toBe("downloads");
  });

  it("搜索没有结果时显示无结果文案", async () => {
    const user = userEvent.setup();
    setup("/o/o1/market", {
      route: (call) =>
        call.path === `${o}/market/skills` && call.query.get("q") === "zzz"
          ? json(200, { items: [], next_cursor: null, total: 0 })
          : undefined,
    });
    await screen.findByRole("button", { name: "查看技能 技能 a1" });
    await user.type(screen.getByRole("textbox", { name: "搜索市场" }), "zzz");
    expect(
      await screen.findByText("没有找到与「zzz」相关的内容"),
    ).toBeInTheDocument();
  });

  it("加载更多用 next_cursor 取下一页并追加", async () => {
    const user = userEvent.setup();
    const calls = setup("/o/o1/market", {
      skills: [mskill("a1")],
      page2: [mskill("a3")],
    });
    await screen.findByRole("button", { name: "查看技能 技能 a1" });
    await user.click(screen.getByRole("button", { name: "加载更多" }));
    expect(await screen.findByText("技能 a3")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "查看技能 技能 a1" }),
    ).toBeInTheDocument();
    expect(marketCalls(calls, "skills").at(-1)!.query.get("cursor")).toBe("c2");
    expect(
      screen.queryByRole("button", { name: "加载更多" }),
    ).not.toBeInTheDocument();
  });

  it("精选包每页 3 张，箭头翻页；不可用成员置灰", async () => {
    const user = userEvent.setup();
    setup("/o/o1/market");
    const rail = await screen.findByRole("region", { name: "精选包" });
    expect(within(rail).getAllByRole("article")).toHaveLength(3);
    expect(within(rail).getByRole("button", { name: "上一页" })).toBeDisabled();
    expect(
      within(rail).getAllByRole("button", { name: "消失的技能" })[0],
    ).toBeDisabled();
    await user.click(within(rail).getByRole("button", { name: "下一页" }));
    expect(within(rail).getAllByRole("article")).toHaveLength(1);
    expect(
      within(rail).getByRole("article", { name: "精选包 p4" }),
    ).toBeInTheDocument();
    expect(within(rail).getByRole("button", { name: "下一页" })).toBeDisabled();
  });
});

describe("技能详情与文件预览", () => {
  const open = "/o/o1/market?skill=a1";

  it("默认选中 SKILL.md，文件夹在前；frontmatter 以键值表展示", async () => {
    setup(open);
    const dialog = await screen.findByRole("dialog", { name: "技能 a1" });
    const tree = await within(dialog).findByRole("navigation", {
      name: "文件",
    });
    const names = within(tree)
      .getAllByRole("button")
      .map((b) => b.textContent);
    expect(names[0]).toBe("SKILL.md"); // SKILL.md 置顶
    expect(names[1]).toBe("scripts"); // 其后文件夹在前
    expect(names.indexOf("SKILL.md")).toBeLessThan(names.indexOf("data.csv"));
    expect(
      within(tree).getByRole("button", { name: "SKILL.md" }),
    ).toHaveAttribute("aria-current", "true");
    const meta = await within(dialog).findByLabelText("文件头信息");
    expect(meta).toHaveTextContent("name");
    expect(meta).toHaveTextContent("license");
    expect(within(dialog).getByText("MIT")).toBeInTheDocument();
    expect(await within(dialog).findByText("正文")).toBeInTheDocument();
  });

  it("文件树默认只展开一级目录，嵌套目录收起，点击后展开", async () => {
    const user = userEvent.setup();
    setup(open);
    const dialog = await screen.findByRole("dialog", { name: "技能 a1" });
    const tree = await within(dialog).findByRole("navigation", {
      name: "文件",
    });
    expect(
      within(tree).getByRole("button", { name: "run.ts" }),
    ).toBeInTheDocument();
    const lib = within(tree).getByRole("button", { name: "lib" });
    expect(lib).toHaveAttribute("aria-expanded", "false");
    expect(
      within(tree).queryByRole("button", { name: "util.ts" }),
    ).not.toBeInTheDocument();
    await user.click(lib);
    expect(
      within(tree).getByRole("button", { name: "util.ts" }),
    ).toBeInTheDocument();
  });

  it("信息条是四列信息块：来源 / 下载量 / 评分 / 版本", async () => {
    setup(open);
    const dialog = await screen.findByRole("dialog", { name: "技能 a1" });
    const block = (await within(dialog).findAllByText("来源"))[0]!.closest(
      "dl",
    )!;
    expect(block.className).toContain("grid-cols-4");
    expect(
      within(block)
        .getAllByRole("definition")
        .map((d) => d.textContent),
    ).toEqual(["skillhub", "1.2k", "30", "1.0.0"]);
  });

  it("Markdown 可在预览与源码间切换，源码按 Code 显示", async () => {
    const user = userEvent.setup();
    setup(open);
    const dialog = await screen.findByRole("dialog", { name: "技能 a1" });
    await within(dialog).findByText("正文");
    await user.click(within(dialog).getByRole("radio", { name: "源码" }));
    expect(dialog.textContent).toContain("---\nname: demo");
    expect(
      within(dialog).queryByLabelText("文件头信息"),
    ).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("radio", { name: "预览" }));
    expect(await within(dialog).findByText("正文")).toBeInTheDocument();
  });

  it("Code 显示行号与语言标签", async () => {
    const user = userEvent.setup();
    setup(open);
    const dialog = await screen.findByRole("dialog", { name: "技能 a1" });
    await user.click(
      await within(dialog).findByRole("button", { name: "run.ts" }),
    );
    const preview = await screen.findByRole("region", {
      name: "文件预览 scripts/run.ts",
    });
    expect(within(preview).getByText("TypeScript")).toBeInTheDocument();
    await within(preview).findByText(codeText(/const a = 1;/));
    expect(preview.textContent).toContain("1\n2");
    expect(within(preview).getByText(/2 行/)).toBeInTheDocument();
  });

  it("复制按钮写入剪贴板", async () => {
    const user = userEvent.setup();
    setup(open);
    const dialog = await screen.findByRole("dialog", { name: "技能 a1" });
    await user.click(
      await within(dialog).findByRole("button", { name: "run.ts" }),
    );
    await within(dialog).findByText(codeText(/const a = 1;/));
    await user.click(within(dialog).getByRole("button", { name: "复制内容" }));
    expect(await screen.findByText("已复制")).toBeInTheDocument();
    expect(await navigator.clipboard.readText()).toContain("const a = 1;");
  });

  it("CSV 只渲染前 100 行并标注总行数", async () => {
    const user = userEvent.setup();
    setup(open);
    const dialog = await screen.findByRole("dialog", { name: "技能 a1" });
    await user.click(
      await within(dialog).findByRole("button", { name: "data.csv" }),
    );
    const table = await within(dialog).findByRole("table");
    expect(within(table).getAllByRole("row")).toHaveLength(101); // 表头 + 100 行
    expect(within(table).getByText("row100")).toBeInTheDocument();
    expect(within(table).queryByText("row101")).not.toBeInTheDocument();
    expect(
      within(dialog).getByText("共 150 行，只显示前 100 行"),
    ).toBeInTheDocument();
  });

  it("二进制文件不渲染，只给下载按钮（不请求内容）", async () => {
    const user = userEvent.setup();
    const calls = setup(open);
    const dialog = await screen.findByRole("dialog", { name: "技能 a1" });
    await user.click(
      await within(dialog).findByRole("button", { name: "pack.bin" }),
    );
    const link = await within(dialog)
      .findByRole("link", { name: "下载文件", description: "" })
      .catch(() => null);
    const links = within(dialog).getAllByRole("link", { name: "下载文件" });
    expect(links.length).toBeGreaterThan(0);
    void link;
    expect(links[0]!.getAttribute("href")).toContain(
      "/market/skills/a1/download?path=pack.bin",
    );
    expect(within(dialog).getByText(/无法预览这种文件/)).toBeInTheDocument();
    expect(
      calls.filter(
        (c) => c.path.endsWith("/file") && c.query.get("path") === "pack.bin",
      ),
    ).toHaveLength(0);
  });

  it("文本被截断时只显示前 500 行并提示下载完整文件", async () => {
    const user = userEvent.setup();
    setup(open);
    const dialog = await screen.findByRole("dialog", { name: "技能 a1" });
    await user.click(
      await within(dialog).findByRole("button", { name: "big.txt" }),
    );
    expect(
      await within(dialog).findByText("文件较大，只显示前 500 行。"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("link", { name: "下载完整文件" }),
    ).toBeInTheDocument();
    expect(dialog.textContent).toContain("line 500");
    expect(dialog.textContent).not.toContain("line 501");
  });

  it(".env 的值默认遮挡，点击后显示", async () => {
    const user = userEvent.setup();
    setup(open);
    const dialog = await screen.findByRole("dialog", { name: "技能 a1" });
    await user.click(
      await within(dialog).findByRole("button", { name: ".env" }),
    );
    const reveal = await within(dialog).findAllByRole("button", {
      name: "显示值",
    });
    expect(reveal).toHaveLength(2);
    expect(dialog.textContent).not.toContain("secret123");
    await user.click(reveal[0]!);
    expect(dialog.textContent).toContain("API_KEY=secret123");
    expect(dialog.textContent).not.toContain("8080");
  });

  it("图片经下载接口取 blob 显示；SVG 可切换到源码", async () => {
    const user = userEvent.setup();
    const calls = setup(open);
    const dialog = await screen.findByRole("dialog", { name: "技能 a1" });
    await user.click(
      await within(dialog).findByRole("button", { name: "logo.png" }),
    );
    const img = await within(dialog).findByRole("img", { name: "logo.png" });
    expect(img).toHaveAttribute("src", "blob:preview");
    expect(
      calls.some(
        (c) =>
          c.path.endsWith("/download") && c.query.get("path") === "logo.png",
      ),
    ).toBe(true);
    await user.click(within(dialog).getByRole("button", { name: "icon.svg" }));
    expect(
      await within(dialog).findByRole("img", { name: "icon.svg" }),
    ).toBeInTheDocument();
    await user.click(within(dialog).getByRole("radio", { name: "源码" }));
    expect(
      await within(dialog).findByText(codeText("<svg></svg>")),
    ).toBeInTheDocument();
  });

  it("读取失败显示「重新加载」", async () => {
    const user = userEvent.setup();
    let fail = true;
    setup(open, {
      route: (call) =>
        call.path.endsWith("/market/skills/a1/file") &&
        call.query.get("path") === "scripts/run.ts" &&
        fail
          ? apiError(503, "STORAGE_UNAVAILABLE")
          : undefined,
    });
    const dialog = await screen.findByRole("dialog", { name: "技能 a1" });
    await user.click(
      await within(dialog).findByRole("button", { name: "run.ts" }),
    );
    const reload = await within(dialog).findByRole("button", {
      name: "重新加载",
    });
    fail = false;
    await user.click(reload);
    expect(
      await within(dialog).findByText(codeText(/const a = 1;/)),
    ).toBeInTheDocument();
  });
});

describe("市场安装目标", () => {
  it("多选 Agent：搜索保留选择，部分失败保留面板，重试成功后关闭", async () => {
    const user = userEvent.setup();
    const agents = [
      {
        id: "a",
        name: "Writer",
        space_id: "s1",
        space_name: "第一空间",
        avatar: null,
      },
      {
        id: "b",
        name: "Tester",
        space_id: "s2",
        space_name: "第二空间",
        avatar: null,
      },
    ];
    let rejectSecond = true;
    const calls = setup("/o/o1/market?skill=a1", {
      route: (call) => {
        if (call.path === o + "/market/agent-targets") {
          const q = call.query.get("q") ?? "";
          return json(200, {
            items: agents.filter((a) => (a.name + a.space_name).includes(q)),
            next_cursor: "",
          });
        }
        if (call.method === "PUT" && call.path.endsWith("/market-skills/a1")) {
          if (call.path.includes("/agents/b/") && rejectSecond)
            return apiError(409, "AGENT_SKILL_LIMIT");
          return new Response(null, { status: 204 });
        }
        return undefined;
      },
    });
    const open = async () => {
      await user.click(
        await screen.findByRole("button", { name: "添加到 Agent" }),
      );
      return within(
        await screen.findByRole("dialog", { name: "为 Agent 添加技能" }),
      );
    };
    let picker = await open();
    expect(picker.getByRole("button", { name: "添加技能" })).toBeDisabled();
    await user.click(picker.getByRole("button", { name: "返回" }));
    expect(
      await screen.findByRole("dialog", { name: "技能 a1" }),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
    picker = await open();
    await user.click(await picker.findByRole("checkbox", { name: "Writer" }));
    const search = picker.getByRole("textbox", { name: "搜索 Agent" });
    await user.type(search, "第二");
    await user.click(await picker.findByRole("checkbox", { name: "Tester" }));
    await user.clear(search);
    await user.type(search, "xyz");
    expect(await picker.findByText("没有匹配的 Agent")).toBeInTheDocument();
    await user.clear(search);
    expect(
      await picker.findByRole("checkbox", { name: "Writer" }),
    ).toBeChecked();
    expect(
      await picker.findByRole("checkbox", { name: "Tester" }),
    ).toBeChecked();
    await user.click(picker.getByRole("button", { name: "添加技能" }));
    expect(
      await screen.findByText("已把「技能 a1」添加给 1 个 Agent"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("dialog", { name: "为 Agent 添加技能" }),
    ).toBeInTheDocument();
    rejectSecond = false;
    await user.click(picker.getByRole("button", { name: "添加技能" }));
    expect(
      await screen.findByText("已把「技能 a1」添加给 2 个 Agent"),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    for (const a of agents)
      expect(
        calls.filter(
          (c) =>
            c.method === "PUT" &&
            c.path ===
              o +
                "/spaces/" +
                a.space_id +
                "/agents/" +
                a.id +
                "/market-skills/a1",
        ),
      ).toHaveLength(2);
  });

  it("从空间技能页的市场选择弹窗打开：直接加到当前空间并打开该技能", async () => {
    const user = userEvent.setup();
    const calls = setup("/o/o1/spaces/s1/skills");
    await user.click(await screen.findByRole("button", { name: "添加技能" }));
    const item = screen.getByRole("menuitem", { name: /从技能市场添加/ });
    expect(item).not.toHaveAttribute("aria-disabled", "true");
    await user.click(item);
    const picker = await screen.findByRole("dialog", {
      name: "从技能市场添加",
    });
    await user.click(
      await within(picker).findByRole("button", { name: "查看技能 技能 a1" }),
    );
    const detail = await screen.findByRole("dialog", { name: "技能 a1" });
    await user.click(
      await within(detail).findByRole("button", { name: "添加到空间" }),
    );
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${o}/spaces/s1/skills/market`),
      ).toBeTruthy(),
    );
    expect(
      screen.queryByRole("dialog", { name: "选择要添加到的空间" }),
    ).not.toBeInTheDocument();
    // 成功后打开空间里的这个技能
    expect(
      await screen.findByRole("dialog", { name: "skill-new1" }),
    ).toBeInTheDocument();
  });

  it("409 SKILL_MARKET_EXISTS：提示空间里已有，并给出打开入口", async () => {
    const user = userEvent.setup();
    setup("/o/o1/spaces/s1/skills", {
      mySkills: [skill("old1")],
      route: (call) =>
        call.method === "POST" && call.path === `${o}/spaces/s1/skills/market`
          ? json(409, {
              error: {
                code: "SKILL_MARKET_EXISTS",
                message: "exists",
                request_id: "r",
                details: { skill_id: "old1" },
              },
            })
          : undefined,
    });
    await user.click(await screen.findByRole("button", { name: "添加技能" }));
    await user.click(screen.getByRole("menuitem", { name: /从技能市场添加/ }));
    await user.click(
      await screen.findByRole("button", { name: "查看技能 技能 a1" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "技能 a1" });
    await user.click(
      await within(dialog).findByRole("button", { name: "添加到空间" }),
    );
    expect(await screen.findByText("空间里已有这个技能")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "打开它" }));
    expect(
      await screen.findByRole("dialog", { name: "skill-old1" }),
    ).toBeInTheDocument();
  });
});

describe("Agent 模板", () => {
  const open = "/o/o1/market?template=t1";

  it("详情：快速提示、包含技能（不可用的置灰并标「市场中暂无」）、Agent 指令页签", async () => {
    const user = userEvent.setup();
    setup(open);
    const dialog = await screen.findByRole("dialog", { name: "模板 t1" });
    expect(
      await within(dialog).findByText("帮我写一个组件"),
    ).toBeInTheDocument();
    expect(within(dialog).getByText("react")).toBeInTheDocument();
    const items = within(dialog)
      .getAllByRole("listitem")
      .filter((li) => li.hasAttribute("data-available"));
    expect(items).toHaveLength(2);
    expect(items[0]).toHaveAttribute("data-available", "true");
    expect(items[1]).toHaveAttribute("data-available", "false");
    expect(within(items[1]!).getByText("市场中暂无")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("tab", { name: "Agent 指令" }));
    expect(await within(dialog).findByText("负责界面")).toBeInTheDocument();
  });

  it("没有快速提示时不显示该区块", async () => {
    setup(open, {
      route: (call) =>
        call.path === `${o}/market/templates/t1`
          ? json(200, { ...templateDetail("t1"), quick_prompts: [] })
          : undefined,
    });
    const dialog = await screen.findByRole("dialog", { name: "模板 t1" });
    await within(dialog).findByText("react");
    expect(within(dialog).queryByText("快速提示")).not.toBeInTheDocument();
  });

  it("使用模板创建 Agent：选空间 → 预填 → 创建后调用 applyMarketTemplate → 汇总 toast", async () => {
    const user = userEvent.setup();
    const created: Call[] = [];
    const calls = setup(open, {
      route: (call) => {
        if (call.method === "POST" && call.path === `${o}/spaces/s2/agents`) {
          created.push(call);
          return json(201, agent("new1", { space_id: "s2", name: "模板 t1" }));
        }
        if (
          call.method === "POST" &&
          call.path === `${o}/spaces/s2/agents/new1/market-template`
        )
          return json(200, {
            items: [
              {
                market_skill_id: "a1",
                status: "added",
                reason: "",
                skill_id: "k1",
              },
              {
                market_skill_id: "a2",
                status: "reused",
                reason: "",
                skill_id: "k2",
              },
              {
                market_skill_id: "gone",
                status: "missing",
                reason: "",
                skill_id: null,
              },
              {
                market_skill_id: "a3",
                status: "skipped",
                reason: "SKILL_NAME_TAKEN",
                skill_id: null,
              },
            ],
          });
        return undefined;
      },
    });
    const dialog = await screen.findByRole("dialog", { name: "模板 t1" });
    await user.click(
      await within(dialog).findByRole("button", { name: "使用模板创建 Agent" }),
    );
    const picker = await screen.findByRole("dialog", {
      name: "选择 Agent 所在的空间",
    });
    await user.click(within(picker).getByRole("radio", { name: "OpenFox" }));
    await user.click(within(picker).getByRole("button", { name: "继续" }));
    const create = await screen.findByRole("dialog", { name: "创建 Agent" });
    expect(within(create).getByRole("textbox", { name: "名称" })).toHaveValue(
      "模板 t1",
    );
    expect(within(create).getByRole("textbox", { name: "职位" })).toHaveValue(
      "前端工程师",
    );
    expect(
      within(create).getByRole("textbox", { name: "Agent 指令" }),
    ).toHaveValue("# 你是前端工程师\n\n负责界面");
    await waitFor(() =>
      expect(
        within(create).getByRole("button", { name: "创建" }),
      ).toBeEnabled(),
    );
    await user.click(within(create).getByRole("button", { name: "创建" }));
    expect(
      await screen.findByText(
        "Agent 已创建。已添加 2 个技能，1 个市场中暂无，1 个跳过。",
      ),
    ).toBeInTheDocument();
    expect(created).toHaveLength(1);
    expect((created[0]!.body as { name: string; title: string }).title).toBe(
      "前端工程师",
    );
    expect(
      findCall(calls, "POST", `${o}/spaces/s2/agents/new1/market-template`)
        ?.body,
    ).toEqual({ market_template_id: "t1" });
  });
});

describe("技能页的市场技能", () => {
  const market = (extra: Partial<Skill> = {}) =>
    skill("m1", {
      name: "market-skill",
      source_kind: "market",
      market_skill_id: "a1",
      market_version: "1.0.0",
      market_latest_version: "1.1.0",
      ...extra,
    });

  it("卡片显示「市场」与「可更新」徽标；版本相同时不显示「可更新」", async () => {
    setup("/o/o1/spaces/s1/skills", {
      mySkills: [
        market(),
        market({
          id: "m2",
          name: "same-skill",
          market_latest_version: "1.0.0",
        }),
      ],
    });
    const card = await screen
      .findByRole("button", { name: "查看技能 market-skill" })
      .catch(() => null);
    void card;
    const first = (await screen.findAllByText("market-skill"))[0]!.closest(
      "button",
    )!;
    expect(within(first).getByText("市场")).toBeInTheDocument();
    expect(within(first).getByText("可更新")).toBeInTheDocument();
    const second = screen.getByText("same-skill").closest("button")!;
    expect(within(second).getByText("市场")).toBeInTheDocument();
    expect(within(second).queryByText("可更新")).not.toBeInTheDocument();
  });

  it("详情里「更新到 x.y.z」带空间技能的 version 请求", async () => {
    const user = userEvent.setup();
    const calls = setup("/o/o1/spaces/s1/skills?skill=m1", {
      mySkills: [market()],
      route: (call) =>
        call.method === "POST" &&
        call.path === `${o}/spaces/s1/skills/m1/market-update`
          ? json(
              200,
              skillDetail(market({ market_version: "1.1.0", version: 4 })),
            )
          : undefined,
    });
    const dialog = await screen.findByRole("dialog", { name: "market-skill" });
    await user.click(
      await within(dialog).findByRole("button", { name: "更新到 1.1.0" }),
    );
    expect(await screen.findByText("已更新到 1.1.0")).toBeInTheDocument();
    expect(
      findCall(calls, "POST", `${o}/spaces/s1/skills/m1/market-update`)?.body,
    ).toEqual({
      version: 3,
    });
  });

  it("只读时不显示更新按钮；SKILL_UP_TO_DATE 本地化", async () => {
    const user = userEvent.setup();
    setup("/o/o1/spaces/s1/skills?skill=m1", {
      mySkills: [market({ can_edit: false })],
    });
    const dialog = await screen.findByRole("dialog", { name: "market-skill" });
    await within(dialog).findByText("可更新");
    expect(
      within(dialog).queryByRole("button", { name: /更新到/ }),
    ).not.toBeInTheDocument();
    void user;
  });

  it("已是最新版本（409 SKILL_UP_TO_DATE）显示中文提示", async () => {
    const user = userEvent.setup();
    setup("/o/o1/spaces/s1/skills?skill=m1", {
      mySkills: [market()],
      route: (call) =>
        call.method === "POST" &&
        call.path === `${o}/spaces/s1/skills/m1/market-update`
          ? apiError(409, "SKILL_UP_TO_DATE")
          : undefined,
    });
    const dialog = await screen.findByRole("dialog", { name: "market-skill" });
    await user.click(
      await within(dialog).findByRole("button", { name: "更新到 1.1.0" }),
    );
    expect(
      await within(dialog).findByText("已经是最新版本"),
    ).toBeInTheDocument();
  });
});
