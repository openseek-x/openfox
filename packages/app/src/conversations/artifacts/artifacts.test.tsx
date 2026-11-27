import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RunArtifact } from "@openfox/api";
import type { DesktopBrowserState, DesktopBrowserCommand } from "../../desktop";

import { keys } from "../../context";
import {
  EVENT_COALESCE_MS,
  createEventBatcher,
} from "../../realtime/useOrgEvents";
import { json, renderSettings, type Call } from "../../settings/testing";
import { ORG, chatBase, message, run, summary } from "../fixtures";
import {
  artifactAnnotatable,
  artifactPreviewKind,
  artifactPreviewable,
} from "./model";

// 阶段 10d · 会话产物：回复里的「产物：<文件名>」按钮、右侧标签面板（列表 + 文件标签）、?artifact= 恢复、
// 各类型预览（Markdown / 图片 / 文本 / CSV / 二进制 / 已失效）、「更多」菜单入口与实时失效。

const LIST = `${ORG}/conversations/c1/artifacts`;
const RUN_ARTIFACTS = `${ORG}/runs/r1/artifacts`;

const artifact = (
  id: string,
  name: string,
  mime: string,
  extra: Partial<RunArtifact> = {},
): RunArtifact => ({
  content_id: `content-${id}`,
  content_revision: 1,
  head_artifact_id: id,
  organization_id: "o1",
  id,
  run_id: "r1",
  name,
  size_bytes: 12,
  sha256: "0".repeat(64),
  mime_type: mime,
  state: "AVAILABLE",
  retention_until: "2099-01-01T00:00:00Z",
  pinned: false,
  version: 1,
  created_at: "2026-11-04T08:00:00Z",
  ...extra,
});

const MD = artifact("a-md", "报告.md", "text/markdown");
const PNG = artifact("a-png", "图表.png", "image/png");
const TXT = artifact("a-txt", "main.go", "text/plain");
const HTML = artifact("a-html", "页面.html", "text/plain");
const CSV = artifact("a-csv", "数据.csv", "text/csv");
const WEBM = artifact("a-webm", "TC-01-登录.webm", "video/webm", {
  size_bytes: 4096,
});
const ZIP = artifact("a-zip", "产物.zip", "application/octet-stream", {
  size_bytes: 2048,
});
const OLD = artifact("a-old", "旧报告.md", "text/markdown", {
  retention_until: "2020-01-01T00:00:00Z",
});
const ALL = [MD, PNG, TXT, CSV, ZIP, OLD];

const CONTENT: Record<string, () => Response> = {
  "a-md": () => new Response("# 周报标题\n\n正文段落", { status: 200 }),
  "a-png": () =>
    new Response(new Uint8Array([137, 80, 78, 71]), {
      status: 200,
    }),
  "a-txt": () => new Response("package main\n", { status: 200 }),
  "a-csv": () => new Response("城市,人数\n上海,3\n北京,5\n", { status: 200 }),
  "a-html": () =>
    new Response(
      "<!doctype html><style>h1{color:red}</style><h1>个人简历</h1><script>window.test = true</script>",
    ),
};

beforeEach(() => {
  localStorage.clear();
  URL.createObjectURL = vi.fn(() => "blob:artifact");
  URL.revokeObjectURL = vi.fn();
});
afterEach(() => {
  localStorage.clear();
  vi.unstubAllGlobals();
});

function open(
  path = "/o/o1/chat/c1",
  options: { list?: RunArtifact[]; runArtifacts?: RunArtifact[] } = {},
) {
  const list = options.list ?? ALL;
  const runArtifacts = options.runArtifacts ?? list;
  return renderSettings(
    path,
    chatBase((call: Call) => {
      if (call.method !== "GET") return undefined;
      if (call.path === LIST) return json(200, { artifacts: list });
      if (call.path === RUN_ARTIFACTS)
        return json(200, { artifacts: runArtifacts });
      const content = /\/runs\/r1\/artifacts\/([^/]+)$/.exec(call.path);
      if (content) return CONTENT[content[1]!]?.() ?? json(404, {});
      if (call.path === `${ORG}/runs/r1`)
        return json(
          200,
          run("r1", {
            state: "SUCCEEDED",
            finished_at: "2026-11-04T08:01:00Z",
          }),
        );
      if (call.path === `${ORG}/conversations/c1/messages`)
        return json(200, {
          messages: [
            message(1, { body: "帮我写周报" }),
            message(2, { body: "周报已经写好。", run_id: "r1" }),
          ],
          next_cursor: 0,
        });
      if (call.path === `${ORG}/conversations/c1`)
        return json(200, summary("c1"));
      if (call.path === `${ORG}/conversations`)
        return json(200, { conversations: [summary("c1")], next_cursor: null });
      return undefined;
    }),
  );
}

const panel = () => screen.findByRole("complementary", { name: "会话产物" });

describe("会话产物 · 按钮与面板", () => {
  it("文件与网页共用可关闭、可排序的动态标签；打开文件不预先创建网页", async () => {
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        disconnect() {}
      },
    );
    let listener: ((state: DesktopBrowserState) => void) | undefined;
    let state: DesktopBrowserState = {
      lease: "browser-lease",
      activeTab: "web-one",
      downloads: [],
      tabs: [
        {
          id: "web-one",
          title: "网页一",
          url: "https://example.com/",
          loading: false,
          error: null,
          canBack: false,
          canForward: false,
        },
      ],
    };
    const mount = vi.fn(async () => state);
    vi.stubGlobal("openfoxDesktop", {
      platform: "darwin",
      browserMount: mount,
      onBrowserState: (callback: typeof listener) => {
        listener = callback;
        return () => {
          listener = undefined;
        };
      },
      browserCommand: vi.fn(async (input: DesktopBrowserCommand) => {
        if (input.action === "close") {
          const tabs = state.tabs.filter((tab) => tab.id !== input.tabId);
          state = {
            ...state,
            tabs,
            activeTab:
              state.activeTab === input.tabId
                ? (tabs.at(-1)?.id ?? null)
                : state.activeTab,
          };
        }
        return state;
      }),
    });
    open("/o/o1/chat/c1?artifact=a-md", { list: [MD] });
    const aside = await panel();
    await within(aside).findByRole("tab", { name: "报告.md" });
    expect(mount).not.toHaveBeenCalled();
    await userEvent.click(
      within(aside).getByRole("button", { name: "打开新标签页" }),
    );
    await userEvent.click(
      within(aside).getByRole("button", { name: "打开网页" }),
    );
    await within(aside).findByRole("tab", { name: "网页一" });
    expect(within(aside).getAllByRole("tablist")).toHaveLength(1);
    expect(within(aside).queryByRole("tab", { name: "浏览器" })).toBeNull();
    expect(within(aside).queryByRole("tab", { name: "产物 1 个" })).toBeNull();
    await waitFor(() =>
      expect(within(aside).queryByRole("tab", { name: "新标签页" })).toBeNull(),
    );
    act(() => {
      state = {
        ...state,
        tabs: [{ ...state.tabs[0]!, title: "新的网页标题" }],
      };
      listener?.(state);
    });
    const web = within(aside).getByRole("tab", { name: "新的网页标题" });
    const file = within(aside).getByRole("tab", { name: "报告.md" });
    const transfer = {
      getData: () => "browser:web-one",
      types: ["application/openfox-panel-tab"],
    };
    fireEvent.drop(file.parentElement!, { dataTransfer: transfer });
    expect(
      within(aside)
        .getAllByRole("tab")
        .map((tab) => tab.textContent),
    ).toEqual(["新的网页标题", "报告.mdv1"]);
    act(() => {
      state = {
        ...state,
        activeTab: "web-two",
        tabs: [
          ...state.tabs,
          { ...state.tabs[0]!, id: "web-two", title: "网页二" },
        ],
      };
      listener?.(state);
    });
    await waitFor(() =>
      expect(
        within(aside).getByRole("tab", { name: "网页二" }),
      ).toHaveAttribute("aria-selected", "true"),
    );
    await userEvent.click(file);
    await userEvent.click(
      within(aside).getByRole("button", { name: "关闭 网页二" }),
    );
    await waitFor(() =>
      expect(within(aside).queryByRole("tab", { name: "网页二" })).toBeNull(),
    );
    expect(file).toHaveAttribute("aria-selected", "true");
    await userEvent.click(
      within(aside).getByRole("button", { name: "关闭 新的网页标题" }),
    );
    await waitFor(() => expect(web).not.toBeInTheDocument());
    await waitFor(() => expect(file).toHaveAttribute("aria-selected", "true"));
    expect(mount).toHaveBeenCalledTimes(1);
  });

  it("回复里显示「产物：<文件名>」；点击打开面板并预览 Markdown；只显示打开的标签，关闭最后一个标签收起面板，之后可再次打开", async () => {
    open("/o/o1/chat/c1", { list: [MD], runArtifacts: [MD] });
    const button = await screen.findByRole("button", {
      name: "产物：报告.md",
    });
    // 按钮在 Agent 回复里，只出现一次（时间线末尾不重复）
    expect(
      screen.getAllByRole("button", { name: "产物：报告.md" }),
    ).toHaveLength(1);
    await userEvent.click(button);
    const aside = await panel();
    expect(
      await within(aside).findByRole("heading", { name: "周报标题" }),
    ).toBeInTheDocument();
    expect(within(aside).queryByRole("tab", { name: "产物 1 个" })).toBeNull();
    expect(within(aside).getByRole("tab", { name: "报告.md" })).toHaveAttribute(
      "aria-selected",
      "true",
    );

    await userEvent.click(
      within(aside).getByRole("button", { name: "关闭 报告.md" }),
    );
    await waitFor(() =>
      expect(
        screen.queryByRole("complementary", { name: "会话产物" }),
      ).not.toBeInTheDocument(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "产物：报告.md" }),
    );
    expect(
      await within(await panel()).findByRole("heading", { name: "周报标题" }),
    ).toBeInTheDocument();
  });

  it("刷新后按 ?artifact= 恢复面板并选中该产物（图片用 blob URL 显示）", async () => {
    open("/o/o1/chat/c1?artifact=a-png");
    const aside = await panel();
    const image = await within(aside).findByRole("img", { name: "图表.png" });
    expect(image).toHaveAttribute("src", "blob:artifact");
    expect(
      within(aside).getByRole("tab", { name: "图表.png" }),
    ).toHaveAttribute("aria-selected", "true");
    expect(within(aside).queryByRole("tab", { name: "产物 6 个" })).toBeNull();
  });

  it("文本按代码显示、CSV 显示表格", async () => {
    open("/o/o1/chat/c1?artifact=a-txt");
    let aside = await panel();
    expect(await within(aside).findByText(/package main/)).toBeInTheDocument();
    await userEvent.click(
      within(aside).getByRole("button", { name: "打开新标签页" }),
    );
    await userEvent.click(
      within(aside).getByRole("button", { name: "浏览产物（6）" }),
    );
    await userEvent.click(
      within(aside).getByRole("button", { name: "打开 数据.csv" }),
    );
    aside = await panel();
    const table = await within(aside).findByRole("table");
    expect(within(table).getByText("上海")).toBeInTheDocument();
    expect(within(table).getByText("人数")).toBeInTheDocument();
    // 两个文件标签都在
    expect(
      within(aside).getByRole("tab", { name: "main.go" }),
    ).toBeInTheDocument();
    expect(
      within(aside).getByRole("tab", { name: "数据.csv" }),
    ).toBeInTheDocument();
  });

  it("二进制只显示信息与下载；已失效显示「产物已失效」且不请求内容", async () => {
    const calls = open("/o/o1/chat/c1?artifact=a-zip");
    let aside = await panel();
    expect(
      await within(aside).findByText(/无法预览这种文件/),
    ).toBeInTheDocument();
    expect(
      within(aside).getByRole("link", { name: "下载文件" }),
    ).toHaveAttribute("href", `/api${ORG}/runs/r1/artifacts/a-zip`);
    await userEvent.click(
      within(aside).getByRole("button", { name: "打开新标签页" }),
    );
    await userEvent.click(
      within(aside).getByRole("button", { name: "浏览产物（6）" }),
    );
    await userEvent.click(
      within(aside).getByRole("button", { name: "打开 旧报告.md" }),
    );
    aside = await panel();
    expect(await within(aside).findByText("产物已失效")).toBeInTheDocument();
    expect(calls.some((call) => call.path.endsWith("/artifacts/a-old"))).toBe(
      false,
    );
    expect(calls.some((call) => call.path.endsWith("/artifacts/a-zip"))).toBe(
      false,
    );
  });

  it("地址里的产物不属于这个会话：重拉一次列表后静默关闭面板", async () => {
    open("/o/o1/chat/c1?artifact=zzz", { list: [MD] });
    await waitFor(() =>
      expect(
        screen.queryByRole("complementary", { name: "会话产物" }),
      ).not.toBeInTheDocument(),
    );
  });
});

describe("会话产物 · 更多菜单", () => {
  it("有产物时「更多」里有「产物」，打开列表标签", async () => {
    open("/o/o1/chat/c1", { list: [MD] });
    await screen.findByRole("button", { name: "产物：报告.md" });
    await userEvent.click(screen.getByRole("button", { name: "更多操作" }));
    await userEvent.click(
      await screen.findByRole("menuitem", { name: "产物" }),
    );
    const aside = await panel();
    expect(
      within(aside).getByRole("tab", { name: "产物 1 个" }),
    ).toHaveAttribute("aria-selected", "true");
    expect(
      within(aside).getByRole("button", { name: "打开 报告.md" }),
    ).toBeInTheDocument();
  });

  it("没有产物时不显示「产物」", async () => {
    const calls = open("/o/o1/chat/c1", { list: [], runArtifacts: [] });
    await screen.findByText("周报已经写好。");
    await waitFor(() =>
      expect(calls.some((call) => call.path === LIST)).toBe(true),
    );
    await userEvent.click(screen.getByRole("button", { name: "更多操作" }));
    await screen.findByRole("menuitem", { name: "复制链接" });
    expect(screen.queryByRole("menuitem", { name: "产物" })).toBeNull();
    expect(screen.queryByRole("button", { name: /^产物：/ })).toBeNull();
  });
});

describe("会话产物 · 预览规则", () => {
  it("仅已验证的文本 HTML 按后缀静态预览；日志和二进制不猜格式；超过上限只能下载", () => {
    expect(artifactPreviewKind(MD)).toBe("markdown");
    expect(artifactPreviewKind(CSV)).toBe("table");
    expect(artifactPreviewKind(TXT)).toBe("code");
    expect(artifactPreviewKind(PNG)).toBe("image");
    expect(artifactPreviewKind(ZIP)).toBe("binary");
    expect(artifactPreviewKind(HTML)).toBe("html");
    expect(artifactPreviewKind({ ...HTML, name: "PAGE.HTM" })).toBe("html");
    expect(artifactPreviewKind({ ...HTML, name: "执行输出.txt" })).toBe("code");
    expect(
      artifactPreviewKind({ ...HTML, mime_type: "application/octet-stream" }),
    ).toBe("binary");
    expect(artifactPreviewKind(WEBM)).toBe("video");
    expect(artifactPreviewKind({ ...WEBM, mime_type: "video/mp4" })).toBe(
      "video",
    );
    // 扩展名是视频但中心按 octet-stream 存（未通过嗅探）：只能下载。
    expect(
      artifactPreviewKind({ ...WEBM, mime_type: "application/octet-stream" }),
    ).toBe("binary");
    // SVG 仍按源码显示，不当作可执行图像。
    expect(artifactPreviewKind(artifact("x", "a.svg", "text/plain"))).toBe(
      "code",
    );
    expect(artifactPreviewable(ZIP)).toBe(false);
    expect(
      artifactPreviewable({ ...PNG, size_bytes: 10 * 1024 * 1024 + 1 }),
    ).toBe(false);
    expect(
      artifactPreviewable({ ...MD, size_bytes: 5 * 1024 * 1024 + 1 }),
    ).toBe(false);
    // 测试报告与录像：HTML 渲染上限 10 MB，视频播放上限与单个产物上限一致（20 MB）。
    expect(artifactPreviewable({ ...HTML, size_bytes: 7_447_186 })).toBe(true);
    expect(
      artifactPreviewable({ ...HTML, size_bytes: 10 * 1024 * 1024 + 1 }),
    ).toBe(false);
    expect(artifactPreviewable({ ...WEBM, size_bytes: 20 * 1024 * 1024 })).toBe(
      true,
    );
    expect(
      artifactPreviewable({ ...WEBM, size_bytes: 20 * 1024 * 1024 + 1 }),
    ).toBe(false);
    // 后端批注只支持 5 MB 以内文本；更大的 HTML 与视频只能查看。
    expect(artifactAnnotatable({ ...HTML, size_bytes: 5 * 1024 * 1024 })).toBe(
      true,
    );
    expect(
      artifactAnnotatable({ ...HTML, size_bytes: 5 * 1024 * 1024 + 1 }),
    ).toBe(false);
    expect(artifactAnnotatable(WEBM)).toBe(false);
  });

  it("视频产物在面板内播放，可下载原文件", async () => {
    const calls = open("/o/o1/chat/c1?artifact=a-webm", { list: [WEBM] });
    const aside = await panel();
    const video = await within(aside).findByLabelText("视频：TC-01-登录.webm");
    expect(video.tagName).toBe("VIDEO");
    // 视频直接指向下载地址由浏览器按 Range 分段播放，面板不再取回整段内容。
    expect(video).toHaveAttribute(
      "src",
      "/api/v1/organizations/o1/runs/r1/artifacts/a-webm",
    );
    expect(video).toHaveAttribute("controls");
    expect(calls.some((call) => call.path.endsWith("/artifacts/a-webm"))).toBe(
      false,
    );
    expect(within(aside).getByRole("link", { name: "下载" })).toHaveAttribute(
      "download",
      "TC-01-登录.webm",
    );
    expect(within(aside).queryByRole("button", { name: "批注" })).toBeNull();
  });

  it("HTML 默认隔离预览；源码切换保留原文件，切回重新创建沙箱", async () => {
    open("/o/o1/chat/c1?artifact=a-html", { list: [HTML] });
    const aside = await panel();
    const iframe = await within(aside).findByTitle("HTML 预览：页面.html");
    expect(iframe).toHaveAttribute("sandbox", "allow-scripts");
    const preview = new DOMParser().parseFromString(
      iframe.getAttribute("srcdoc")!,
      "text/html",
    );
    expect(preview.querySelectorAll("script")).toHaveLength(1);
    expect(preview.querySelector("script")?.src).toContain(
      "htmlSelectionBridge",
    );
    expect(iframe).toHaveAttribute("referrerpolicy", "no-referrer");
    expect(iframe.getAttribute("srcdoc")).toContain("个人简历");
    expect(iframe.getAttribute("srcdoc")).not.toContain("window.test");
    expect(
      within(aside).queryByRole("heading", { name: "个人简历" }),
    ).toBeNull();
    const modes = within(
      within(aside).getByRole("group", { name: "查看方式" }),
    );
    await userEvent.click(modes.getByRole("button", { name: "源码" }));
    expect(within(aside).queryByTitle("HTML 预览：页面.html")).toBeNull();
    expect(aside.textContent).toContain("window.test = true");
    expect(modes.getByRole("button", { name: "源码" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await userEvent.click(modes.getByRole("button", { name: "预览" }));
    expect(
      within(aside).getByTitle("HTML 预览：页面.html"),
    ).toBeInTheDocument();
  });

  it("Markdown 默认渲染 GFM 表格并保留 frontmatter，可切回完整源码", async () => {
    CONTENT["a-rich"] = () =>
      new Response(
        "---\ntitle: 周报\n---\n# 阅读模式\n\n| 项目 | 结果 |\n|---|---|\n| 预览 | 通过 |\n\n<script>window.test = true</script>",
      );
    open("/o/o1/chat/c1?artifact=a-rich", { list: [{ ...MD, id: "a-rich" }] });
    const aside = await panel();
    expect(
      await within(aside).findByRole("heading", { name: "阅读模式" }),
    ).toBeInTheDocument();
    expect(within(aside).getByRole("table")).toHaveTextContent("通过");
    expect(within(aside).getByText("周报")).toBeInTheDocument();
    expect(aside.querySelector("script")).toBeNull();
    await userEvent.click(within(aside).getByRole("button", { name: "源码" }));
    expect(within(aside).queryByRole("table")).toBeNull();
    expect(aside.textContent).toContain("title: 周报");
  });

  it("大 HTML 报告仍渲染且可切源码，超 5 MB 不能批注，超上限不请求内容，拒绝下载不绕过鉴权", async () => {
    CONTENT["a-denied"] = () =>
      json(403, { error: { code: "FORBIDDEN", message: "denied" } });
    CONTENT["a-large"] = CONTENT["a-html"]!;
    const list = [
      { ...HTML, id: "a-large", size_bytes: 5 * 1024 * 1024 + 1 },
      {
        ...HTML,
        id: "a-huge",
        name: "大文件.html",
        size_bytes: 10 * 1024 * 1024 + 1,
      },
      { ...HTML, id: "a-denied", name: "不可读.html" },
    ];
    for (const file of list) {
      file.content_id = `content-${file.id}`;
      file.head_artifact_id = file.id;
    }
    const calls = open("/o/o1/chat/c1?artifact=a-large", { list });
    const aside = await panel();
    expect(
      await within(aside).findByTitle("HTML 预览：页面.html"),
    ).toBeInTheDocument();
    expect(
      within(aside).getByRole("group", { name: "查看方式" }),
    ).toBeInTheDocument();
    expect(within(aside).queryByRole("button", { name: "批注" })).toBeNull();
    await userEvent.click(
      within(aside).getByRole("button", { name: "打开新标签页" }),
    );
    await userEvent.click(
      within(aside).getByRole("button", { name: "浏览产物（3）" }),
    );
    await userEvent.click(
      within(aside).getByRole("button", { name: "打开 大文件.html" }),
    );
    await within(aside).findByText(/文件较大/);
    expect(calls.some((call) => call.path.endsWith("/artifacts/a-huge"))).toBe(
      false,
    );
    await userEvent.click(
      within(aside).getByRole("button", { name: "打开新标签页" }),
    );
    await userEvent.click(
      within(aside).getByRole("button", { name: "浏览产物（3）" }),
    );
    await userEvent.click(
      within(aside).getByRole("button", { name: "打开 不可读.html" }),
    );
    expect(await within(aside).findByRole("alert")).toHaveTextContent(
      "加载文件失败",
    );
    expect(aside.querySelector("iframe")).toBeNull();
  });
});

describe("会话产物 · 实时", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("run.artifact 失效该会话的产物列表与对应 Run 的产物，不动其他会话", () => {
    const client = new QueryClient();
    const seed = (key: readonly unknown[]) => client.setQueryData(key, []);
    seed(keys.conversationArtifacts("o1", "c1"));
    seed(keys.conversationArtifacts("o1", "c2"));
    seed(keys.runSub("o1", "r1", "artifacts"));
    seed(keys.runSub("o1", "r2", "artifacts"));
    const batcher = createEventBatcher(client, "o1");
    batcher.push("run.artifact", {
      type: "run.artifact",
      conversation_id: "c1",
      payload: { run_id: "r1" },
    });
    vi.advanceTimersByTime(EVENT_COALESCE_MS);
    const stale = client
      .getQueryCache()
      .getAll()
      .filter((query) => query.state.isInvalidated)
      .map((query) => JSON.stringify(query.queryKey))
      .sort();
    expect(stale).toEqual(
      [
        JSON.stringify(keys.conversationArtifacts("o1", "c1")),
        JSON.stringify(keys.runSub("o1", "r1", "artifacts")),
      ].sort(),
    );
  });
});
