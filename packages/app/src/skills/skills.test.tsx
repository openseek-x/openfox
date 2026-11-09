import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  ApiError,
  ORGANIZATION_EVENT_TYPES,
  type Device,
  type DeviceCommand,
  type DeviceSkillScanResult,
  type Skill,
  type SkillDetail,
  type SkillImportItem,
  type SkillRuntimeImport,
} from "@openfox/api";

import { agent, device, entry } from "../agents/fixtures";
import { App, createQueryClient } from "../App";
import { keys } from "../context";
import { createEventBatcher } from "../realtime/useOrgEvents";
import {
  backend,
  json,
  ORG,
  SPACE,
  type Call,
  type Override,
} from "../tasks/test-backend";

// 技能：空态与只读、添加菜单、手动创建、上传、URL 安装、Runtime 导入（扫描、选择、进度、汇总、取消）、
// 列表的排序 / 筛选 / 搜索、详情（编辑、保存、删除）、Agent 技能区块、skill.changed 失效。

// 导入进度的轮询在测试里加快，其余常量保持原值
vi.mock("./model", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./model")>()),
  IMPORT_POLL_MS: 20,
}));

const o = `/v1/organizations/${ORG}`;
const sp = `${o}/spaces/${SPACE}`;
const skillsPath = `${sp}/skills`;
const base = `/o/${ORG}/spaces/${SPACE}/skills`;
const NOW = "2026-10-30T08:00:00Z";

function skill(id: string, extra: Partial<Skill> = {}): Skill {
  return {
    id,
    space_id: SPACE,
    name: `skill-${id}`,
    description: "",
    source_kind: "manual",
    source_label: "",
    source_url: null,
    market_skill_id: null,
    market_version: null,
    market_latest_version: null,
    file_count: 2,
    total_bytes: 100,
    created_by: { id: "u1", name: "林" },
    used_by: [],
    used_count: 0,
    can_edit: true,
    version: 1,
    created_at: NOW,
    updated_at: NOW,
    ...extra,
  };
}

function detailOf(item: Skill, extra: Partial<SkillDetail> = {}): SkillDetail {
  return {
    ...item,
    files: [
      { path: "SKILL.md", size_bytes: 60 },
      { path: "refs/guide.md", size_bytes: 40 },
      { path: "logo.png", size_bytes: 10 },
    ],
    agents: [],
    ...extra,
  };
}

const SKILL_MD =
  "---\nname: skill-s1\ndescription: 做代码审查\n---\n\n先读 diff，再给出意见。";

interface Options {
  skills?: Skill[];
  canCreate?: boolean;
  /** 详情接口里的文件内容 */
  files?: Record<string, string>;
  override?: Override;
}

/** 内存版技能接口：记录请求，并按请求改写数据。 */
function skillBackend(options: Options = {}) {
  let skills = [...(options.skills ?? [])];
  const files: Record<string, string> = {
    "SKILL.md": SKILL_MD,
    "refs/guide.md": "# 指南",
    ...options.files,
  };
  let seq = 0;
  const override: Override = (call: Call) => {
    const { method, path } = call;
    const custom = options.override?.(call);
    if (custom) return custom;
    if (path === skillsPath && method === "GET")
      return json(200, {
        skills,
        can_create: options.canCreate ?? true,
      });
    if (path === skillsPath && method === "POST") {
      const body = call.body as {
        name: string;
        description?: string;
        instructions?: string;
      };
      if (body.name === "taken")
        return json(409, {
          error: {
            code: "SKILL_NAME_TAKEN",
            message: "技能名称已存在",
            request_id: "r",
          },
        });
      const created = skill(`new-${++seq}`, {
        name: body.name,
        description: body.description ?? "",
      });
      skills = [...skills, created];
      return json(201, detailOf(created));
    }
    if (path === `${skillsPath}/upload` && method === "POST") {
      const created = skill(`up-${++seq}`, { name: "uploaded" });
      skills = [...skills, created];
      return json(201, detailOf(created));
    }
    const fileMatch = new RegExp(`^${skillsPath}/([^/]+)/files$`).exec(path);
    if (fileMatch && method === "GET") {
      const filePath = call.query.get("path") ?? "";
      if (filePath === "logo.png")
        return json(200, {
          path: filePath,
          size_bytes: 10,
          binary: true,
          content: "",
        });
      const content = files[filePath] ?? "";
      return json(200, {
        path: filePath,
        size_bytes: content.length,
        binary: false,
        content,
      });
    }
    if (fileMatch && method === "PUT") {
      const current = skills.find((s) => s.id === fileMatch[1])!;
      const next = { ...current, version: current.version + 1 };
      skills = skills.map((s) => (s.id === next.id ? next : s));
      return json(200, detailOf(next));
    }
    const skillMatch = new RegExp(`^${skillsPath}/([^/]+)$`).exec(path);
    if (skillMatch) {
      const current = skills.find((s) => s.id === skillMatch[1]);
      if (!current)
        return json(404, {
          error: { code: "NOT_FOUND", message: "x", request_id: "r" },
        });
      if (method === "GET") return json(200, detailOf(current));
      if (method === "DELETE") {
        skills = skills.filter((s) => s.id !== current.id);
        return new Response(null, { status: 204 });
      }
    }
    return undefined;
  };
  return { override };
}

function renderSkills(path = base, options: Options = {}) {
  const store = skillBackend(options);
  const server = backend(store.override);
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return { server, queryClient };
}

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

const addButton = () => screen.findByRole("button", { name: /添加技能/ });

describe("技能列表", () => {
  it("空态显示还没有技能与添加技能按钮", async () => {
    renderSkills(base, { skills: [] });
    expect(await screen.findByText("还没有技能")).toBeInTheDocument();
    expect(await addButton()).toBeInTheDocument();
  });

  it("只读（不可创建）时不显示添加技能", async () => {
    renderSkills(base, { skills: [], canCreate: false });
    expect(await screen.findByText("还没有技能")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /添加技能/ })).toBeNull();
  });

  it("添加菜单列出五项，技能市场入口已开放", async () => {
    renderSkills(base, { skills: [] });
    await userEvent.click(await addButton());
    const menu = within(screen.getByRole("menu"));
    expect(
      menu.getByRole("menuitem", { name: /手动创建/ }),
    ).toBeInTheDocument();
    expect(
      menu.getByRole("menuitem", { name: /上传技能文件/ }),
    ).toBeInTheDocument();
    expect(
      menu.getByRole("menuitem", { name: /通过 URL 导入/ }),
    ).toBeInTheDocument();
    expect(
      menu.getByRole("menuitem", { name: /从 Runtime 导入/ }),
    ).toBeInTheDocument();
    const market = menu.getByRole("menuitem", { name: /从技能市场添加/ });
    expect(market).not.toHaveAttribute("aria-disabled", "true");
    expect(within(market).queryByText("即将开放")).not.toBeInTheDocument();
  });

  it("卡片显示名称、文件数、无描述、未使用与创建者；容器带两列样式", async () => {
    renderSkills(base, {
      skills: [
        skill("s1", { description: "做代码审查" }),
        skill("s2", { file_count: 5 }),
      ],
    });
    const card = await screen.findByRole("button", {
      name: "打开技能 skill-s1",
    });
    expect(within(card).getByText("做代码审查")).toBeInTheDocument();
    expect(within(card).getByText("— 未使用")).toBeInTheDocument();
    expect(within(card).getByText(/由 林 创建/)).toBeInTheDocument();
    const second = screen.getByRole("button", { name: "打开技能 skill-s2" });
    expect(within(second).getByText("5 个文件")).toBeInTheDocument();
    expect(within(second).getByText("无描述")).toBeInTheDocument();
    expect(card.parentElement!.className).toContain("@[1000px]:grid-cols-2");
  });

  it("排序、添加者与搜索都会带进列表请求参数", async () => {
    const { server } = renderSkills(base, { skills: [skill("s1")] });
    await screen.findByRole("button", { name: "打开技能 skill-s1" });
    const lastList = () =>
      server
        .find("GET", "/skills")
        .filter((c) => c.path === skillsPath)
        .at(-1)!.query;
    expect(lastList().get("sort")).toBe("updated");

    await userEvent.click(screen.getByRole("button", { name: "排序" }));
    await userEvent.click(screen.getByRole("option", { name: "使用量" }));
    await waitFor(() => expect(lastList().get("sort")).toBe("usage"));

    await userEvent.click(screen.getByRole("button", { name: "添加者" }));
    await userEvent.click(
      await screen.findByRole("option", { name: "wang@openfox.dev" }),
    );
    await waitFor(() => expect(lastList().get("created_by")).toBe("u2"));

    await userEvent.type(
      screen.getByRole("textbox", { name: "搜索技能" }),
      "审查",
    );
    await waitFor(() => expect(lastList().get("q")).toBe("审查"));
  });
});

describe("手动创建", () => {
  const openCreate = async () => {
    await userEvent.click(await addButton());
    await userEvent.click(screen.getByRole("menuitem", { name: /手动创建/ }));
    return within(await screen.findByRole("dialog", { name: "编写技能指令" }));
  };

  it("名称为空时保存禁用，填写后可保存，成功后提示并打开详情", async () => {
    const { server } = renderSkills(base, { skills: [] });
    const dialog = await openCreate();
    expect(dialog.getByRole("button", { name: "保 存" })).toBeDisabled();
    await userEvent.type(dialog.getByLabelText("技能名称"), "code-review");
    await userEvent.type(dialog.getByLabelText("描述"), "审查代码");
    await userEvent.type(dialog.getByLabelText("指令"), "先读 diff");
    await userEvent.click(dialog.getByRole("button", { name: "保 存" }));
    await waitFor(() => expect(server.find("POST", "/skills")).toHaveLength(1));
    expect(server.find("POST", "/skills")[0]!.body).toEqual({
      name: "code-review",
      description: "审查代码",
      instructions: "先读 diff",
    });
    expect(await screen.findByText("已创建技能")).toBeInTheDocument();
    expect(
      await screen.findByRole("dialog", { name: "code-review" }),
    ).toBeInTheDocument();
  });

  it("名称不合规时提示并禁用保存", async () => {
    renderSkills(base, { skills: [] });
    const dialog = await openCreate();
    await userEvent.type(dialog.getByLabelText("技能名称"), "Bad Name");
    expect(
      dialog.getByText(/名称只能包含小写字母、数字和连字符/),
    ).toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "保 存" })).toBeDisabled();
  });

  it("重名时错误条追加「请换一个技能名称。」", async () => {
    renderSkills(base, { skills: [] });
    const dialog = await openCreate();
    await userEvent.type(dialog.getByLabelText("技能名称"), "taken");
    await userEvent.click(dialog.getByRole("button", { name: "保 存" }));
    expect(
      await dialog.findByText("技能名称已存在 请换一个技能名称。"),
    ).toBeInTheDocument();
  });
});

describe("上传技能文件", () => {
  const openUpload = async () => {
    await userEvent.click(await addButton());
    await userEvent.click(
      screen.getByRole("menuitem", { name: /上传技能文件/ }),
    );
    await screen.findByRole("dialog", { name: "上传技能文件" });
  };
  const folderFile = (name: string, size = 10) => {
    const file = new File(["x".repeat(size)], name.split("/").pop()!);
    Object.defineProperty(file, "webkitRelativePath", {
      value: `pack/${name}`,
    });
    return file;
  };

  it("文件超过 100 个时本地拦截，不发请求", async () => {
    const { server } = renderSkills(base, { skills: [] });
    await openUpload();
    const files = Array.from({ length: 101 }, (_, i) => folderFile(`f${i}.md`));
    fireEvent.change(screen.getByTestId("skill-folder-input"), {
      target: { files },
    });
    expect(
      await screen.findByText("文件超过 100 个，请精简后再上传。"),
    ).toBeInTheDocument();
    expect(server.find("POST", "/skills/upload")).toHaveLength(0);
  });

  it("合计超过 5 MB 时本地拦截", async () => {
    const { server } = renderSkills(base, { skills: [] });
    await openUpload();
    const big = new File(["x"], "big.zip");
    Object.defineProperty(big, "size", { value: 5 * 1024 * 1024 + 1 });
    fireEvent.change(screen.getByTestId("skill-file-input"), {
      target: { files: [big] },
    });
    expect(
      await screen.findByText("文件合计超过 5 MB，请精简后再上传。"),
    ).toBeInTheDocument();
    expect(server.find("POST", "/skills/upload")).toHaveLength(0);
  });

  it("文件夹上传：每个 file 后跟一个去掉顶层目录的 file_path，成功后打开详情", async () => {
    const { server } = renderSkills(base, { skills: [] });
    await openUpload();
    fireEvent.change(screen.getByTestId("skill-folder-input"), {
      target: {
        files: [folderFile("SKILL.md"), folderFile("refs/guide.md")],
      },
    });
    await waitFor(() =>
      expect(server.find("POST", "/skills/upload")).toHaveLength(1),
    );
    const form = server.find("POST", "/skills/upload")[0]!.body as FormData;
    expect([...form.keys()]).toEqual([
      "file",
      "file_path",
      "file",
      "file_path",
    ]);
    expect(form.getAll("file_path")).toEqual(["SKILL.md", "refs/guide.md"]);
    expect(await screen.findByText("已上传技能")).toBeInTheDocument();
    expect(
      await screen.findByRole("dialog", { name: "uploaded" }),
    ).toBeInTheDocument();
  });

  it("文件选择器的 accept 包含 .md、.markdown 和 .zip", async () => {
    renderSkills(base, { skills: [] });
    await openUpload();
    const accept = screen
      .getByTestId("skill-file-input")
      .getAttribute("accept");
    expect(accept).toContain(".md");
    expect(accept).toContain(".markdown");
    expect(accept).toContain(".zip");
  });

  it("上传失败按错误码显示中文", async () => {
    renderSkills(base, {
      skills: [],
      override: (call) =>
        call.path === `${skillsPath}/upload`
          ? json(400, {
              error: {
                code: "SKILL_INVALID",
                message: "SKILL.md is missing",
                request_id: "r",
              },
            })
          : undefined,
    });
    await openUpload();
    fireEvent.change(screen.getByTestId("skill-file-input"), {
      target: { files: [new File(["x"], "pack.zip")] },
    });
    expect(
      await screen.findByText("不是有效的技能（缺 SKILL.md 或格式不对）"),
    ).toBeInTheDocument();
  });

  it("单个 .zip 只带 file 字段", async () => {
    const { server } = renderSkills(base, { skills: [] });
    await openUpload();
    fireEvent.change(screen.getByTestId("skill-file-input"), {
      target: { files: [new File(["zip"], "pack.zip")] },
    });
    await waitFor(() =>
      expect(server.find("POST", "/skills/upload")).toHaveLength(1),
    );
    const form = server.find("POST", "/skills/upload")[0]!.body as FormData;
    expect([...form.keys()]).toEqual(["file"]);
  });
});

describe("从 URL 安装", () => {
  const openUrl = async () => {
    await userEvent.click(await addButton());
    await userEvent.click(
      screen.getByRole("menuitem", { name: /通过 URL 导入/ }),
    );
    return within(await screen.findByRole("dialog", { name: "通过 URL 导入" }));
  };

  it("来源行是五个新窗口外链", async () => {
    renderSkills(base, { skills: [] });
    const dialog = await openUrl();
    for (const label of [
      "skillhub.club",
      "skillhub.cn",
      "ClawHub",
      "Hermes Agent",
      "Skills.sh",
    ]) {
      const link = dialog.getByRole("link", { name: label });
      expect(link).toHaveAttribute("target", "_blank");
      expect(link).toHaveAttribute("rel", "noopener noreferrer");
    }
  });

  it("回车提交；安装中按钮显示来源；成功后提示并打开详情", async () => {
    const { server } = renderSkills(base, {
      skills: [skill("u1", { name: "installed" })],
    });
    // 列表里先放一个同 id 的技能，安装完成后详情接口能读到它
    let finish!: (value: SkillDetail) => void;
    server.api.skills.importFromUrl = vi.fn(
      () => new Promise<SkillDetail>((resolve) => (finish = resolve)),
    );
    const dialog = await openUrl();
    await userEvent.type(
      dialog.getByLabelText("技能地址"),
      "https://skillhub.club/s/review{Enter}",
    );
    expect(
      await dialog.findByRole("button", { name: /正在从 skillhub\.club 安装/ }),
    ).toBeInTheDocument();
    finish(detailOf(skill("u1", { name: "installed" })));
    expect(await screen.findByText("已安装技能")).toBeInTheDocument();
    expect(
      await screen.findByRole("dialog", { name: "installed" }),
    ).toBeInTheDocument();
  });

  it("失败显示服务端消息；冲突追加换来源的建议", async () => {
    renderSkills(base, {
      skills: [],
      override: (call) => {
        if (call.path !== `${skillsPath}/url-imports`) return undefined;
        const url = (call.body as { url: string }).url;
        return url.includes("conflict")
          ? json(409, {
              error: {
                code: "SKILL_NAME_TAKEN",
                message: "同名技能已存在",
                request_id: "r",
              },
            })
          : json(422, {
              error: {
                code: "SKILL_URL_FETCH_FAILED",
                message: "下载超时",
                request_id: "r",
              },
            });
      },
    });
    const dialog = await openUrl();
    const input = dialog.getByLabelText("技能地址");
    await userEvent.type(input, "https://example.com/a.zip{Enter}");
    expect(await dialog.findByText("下载失败：下载超时")).toBeInTheDocument();
    await userEvent.clear(input);
    await userEvent.type(input, "https://example.com/conflict.zip{Enter}");
    expect(
      await dialog.findByText("同名技能已存在 请换一个来源或技能名称。"),
    ).toBeInTheDocument();
  });

  it("按错误码本地化：被拒绝的地址、地址格式、超限、技能不合规", async () => {
    const codes: Record<string, [string, string, number]> = {
      forbidden: ["SKILL_URL_FORBIDDEN", "the URL target is not allowed", 400],
      invalid: ["SKILL_URL_INVALID", "bad url", 400],
      large: ["SKILL_TOO_LARGE", "too large", 413],
      bad: ["SKILL_INVALID", "bad skill", 400],
    };
    renderSkills(base, {
      skills: [],
      override: (call) => {
        if (call.path !== `${skillsPath}/url-imports`) return undefined;
        const url = (call.body as { url: string }).url;
        const key = Object.keys(codes).find((name) => url.includes(name))!;
        const [code, message, status] = codes[key]!;
        return json(status, { error: { code, message, request_id: "r" } });
      },
    });
    const dialog = await openUrl();
    const input = dialog.getByLabelText("技能地址");
    const expectations: [string, string][] = [
      ["forbidden", "不能从这个地址安装（只允许公网 https 地址）"],
      ["invalid", "地址格式不对"],
      ["large", "超过 100 个文件或 5 MB"],
      ["bad", "不是有效的技能（缺 SKILL.md 或格式不对）"],
    ];
    for (const [name, text] of expectations) {
      await userEvent.clear(input);
      await userEvent.type(input, `https://example.com/${name}{Enter}`);
      expect(await dialog.findByText(text)).toBeInTheDocument();
    }
    expect(dialog.queryByText("the URL target is not allowed")).toBeNull();
  });

  it("没有服务端消息时显示「安装失败」", async () => {
    const { server } = renderSkills(base, { skills: [] });
    server.api.skills.importFromUrl = vi.fn(() =>
      Promise.reject(
        new ApiError({ status: 500, code: "HTTP_ERROR", message: "" }),
      ),
    );
    const dialog = await openUrl();
    await userEvent.type(
      dialog.getByLabelText("技能地址"),
      "https://a.b/c{Enter}",
    );
    expect(await dialog.findByText("安装失败")).toBeInTheDocument();
  });
});

describe("从 Runtime 导入", () => {
  const scan = (
    result: DeviceSkillScanResult | null,
    extra: Partial<DeviceCommand> = {},
  ): DeviceCommand => ({
    id: "c1",
    device_id: "d1",
    kind: "skill_scan",
    params: {},
    status: "SUCCEEDED",
    result: result as DeviceCommand["result"],
    error_code: "",
    created_at: NOW,
    delivered_at: NOW,
    completed_at: NOW,
    expires_at: NOW,
    ...extra,
  });
  const scanned: DeviceSkillScanResult = {
    supported: true,
    runtime: "codex",
    skills: [
      {
        name: "review",
        description: "代码审查",
        file_count: 2,
        total_bytes: 100,
        importable: true,
      },
      {
        name: "deploy",
        description: "",
        file_count: 1,
        total_bytes: 10,
        importable: true,
      },
      {
        name: "huge",
        description: "",
        file_count: 300,
        total_bytes: 9_000_000,
        importable: false,
      },
    ],
  };

  function importState(
    items: SkillImportItem[],
    status: SkillRuntimeImport["status"] = "running",
  ): SkillRuntimeImport {
    return { command_id: "imp1", device_id: "d1", status, items };
  }
  const pending = (name: string): SkillImportItem => ({
    name,
    status: "pending",
    reason: "",
    skill_id: null,
  });
  const imported = (name: string, id: string): SkillImportItem => ({
    name,
    status: "imported",
    reason: "",
    skill_id: id,
  });

  /** 电脑、扫描命令与导入进度的假后端。 */
  function runtimeOptions(config: {
    devices?: Device[];
    scan?: DeviceCommand;
    getScan?: DeviceCommand[];
    start?: Response;
    progress?: SkillRuntimeImport[];
    cancel?: SkillRuntimeImport;
    skills?: Skill[];
  }): Options {
    const progress = [...(config.progress ?? [])];
    const getScan = [...(config.getScan ?? [])];
    return {
      skills: config.skills ?? [],
      override: (call) => {
        if (call.path === `${o}/devices`)
          return json(200, {
            devices: config.devices ?? [
              device("d1", { name: "我的 Mac", online: true }),
            ],
            next_cursor: null,
          });
        if (call.path === `${o}/devices/d1/commands` && call.method === "POST")
          return json(200, { command: config.scan ?? scan(scanned) });
        if (call.path === `${o}/devices/d1/commands/c1`)
          return json(200, {
            command:
              getScan.length > 1
                ? getScan.shift()
                : (getScan[0] ?? config.scan ?? scan(scanned)),
          });
        if (
          call.path === `${skillsPath}/runtime-imports` &&
          call.method === "POST"
        )
          return (
            config.start ??
            json(202, importState([pending("review"), pending("deploy")]))
          );
        if (call.path === `${skillsPath}/runtime-imports/imp1/cancel`)
          return json(
            200,
            config.cancel ?? importState([pending("review")], "canceled"),
          );
        if (call.path === `${skillsPath}/runtime-imports/imp1`) {
          const next = progress.length > 1 ? progress.shift() : progress[0];
          return json(200, next);
        }
        return undefined;
      },
    };
  }

  const openRuntime = async () => {
    await userEvent.click(await addButton());
    await userEvent.click(
      screen.getByRole("menuitem", { name: /从 Runtime 导入/ }),
    );
    return within(
      await screen.findByRole("dialog", { name: "从 Runtime 导入" }),
    );
  };
  const pickDevice = async (dialog: ReturnType<typeof within>) => {
    await userEvent.click(
      dialog.getByRole("button", { name: "电脑 / Runtime" }),
    );
    await userEvent.click(await screen.findByRole("option", { name: /codex/ }));
  };

  it("未选电脑时提示先选择；只列自己的电脑", async () => {
    renderSkills(
      base,
      runtimeOptions({
        devices: [
          device("d1", { name: "我的 Mac" }),
          device("d2", { name: "别人的电脑", owner_user_id: "u2" }),
        ],
      }),
    );
    const dialog = await openRuntime();
    expect(dialog.getByText("请先选择一台电脑。")).toBeInTheDocument();
    expect(dialog.getByText("请选择一个技能继续。")).toBeInTheDocument();
    await userEvent.click(
      dialog.getByRole("button", { name: "电脑 / Runtime" }),
    );
    const list = within(await screen.findByRole("listbox"));
    expect(list.getByText("我的 Mac")).toBeInTheDocument();
    expect(list.queryByText("别人的电脑")).toBeNull();
  });

  it("选中后下发 skill_scan 并轮询到结果，全选后显示已选择数量", async () => {
    const { server } = renderSkills(
      base,
      runtimeOptions({
        scan: scan(null, { status: "PENDING", result: null }),
        getScan: [
          scan(null, { status: "DELIVERED", result: null }),
          scan(scanned),
        ],
      }),
    );
    const dialog = await openRuntime();
    await pickDevice(dialog);
    expect(await dialog.findByText("正在扫描技能...")).toBeInTheDocument();
    expect(
      await dialog.findByText("代码审查", {}, { timeout: 4000 }),
    ).toBeInTheDocument();
    expect(server.find("POST", "/devices/d1/commands")[0]!.body).toEqual({
      kind: "skill_scan",
    });
    await userEvent.click(dialog.getByLabelText("全选（2）"));
    expect(dialog.getByText("已选择 2 个技能")).toBeInTheDocument();
    expect(dialog.getByRole("button", { name: "导入 2 个技能" })).toBeEnabled();
    expect(dialog.getByText("无法导入")).toBeInTheDocument();
  });

  it("电脑离线时提示且不下发命令", async () => {
    const { server } = renderSkills(
      base,
      runtimeOptions({
        devices: [device("d1", { name: "我的 Mac", online: false })],
      }),
    );
    const dialog = await openRuntime();
    await pickDevice(dialog);
    expect(
      await dialog.findByText("这台电脑当前离线，无法扫描技能。"),
    ).toBeInTheDocument();
    expect(server.find("POST", "/devices/d1/commands")).toHaveLength(0);
  });

  it("Runtime 不支持与没有本地技能各有提示", async () => {
    renderSkills(
      base,
      runtimeOptions({ scan: scan({ supported: false, skills: [] }) }),
    );
    const dialog = await openRuntime();
    await pickDevice(dialog);
    expect(
      await dialog.findByText("这台电脑的 Runtime 暂不支持技能导入。"),
    ).toBeInTheDocument();
  });

  it("没有本地技能时显示空态", async () => {
    renderSkills(
      base,
      runtimeOptions({
        scan: scan({ supported: true, runtime: "codex", skills: [] }),
      }),
    );
    const dialog = await openRuntime();
    await pickDevice(dialog);
    expect(
      await dialog.findByText("这台电脑上没有本地技能。"),
    ).toBeInTheDocument();
  });

  it("导入：请求带电脑与名称，进度「正在导入 x / N...」，结束后汇总与明细", async () => {
    const { server } = renderSkills(
      base,
      runtimeOptions({
        skills: [skill("k1")],
        progress: [
          importState([imported("review", "k1"), pending("deploy")]),
          importState([imported("review", "k1"), pending("deploy")]),
          importState([imported("review", "k1"), pending("deploy")]),
          importState(
            [
              imported("review", "k1"),
              {
                name: "deploy",
                status: "skipped",
                reason: "SKILL_NAME_TAKEN",
                skill_id: null,
              },
            ],
            "done",
          ),
        ],
      }),
    );
    const dialog = await openRuntime();
    await pickDevice(dialog);
    await userEvent.click(await dialog.findByLabelText("全选（2）"));
    await userEvent.click(
      dialog.getByRole("button", { name: "导入 2 个技能" }),
    );
    await waitFor(() =>
      expect(server.find("POST", "/skills/runtime-imports")).toHaveLength(1),
    );
    expect(server.find("POST", "/skills/runtime-imports")[0]!.body).toEqual({
      device_id: "d1",
      names: ["review", "deploy"],
    });
    expect(await dialog.findByText("正在导入 1 / 2...")).toBeInTheDocument();
    expect(await dialog.findByText("导入完成。")).toBeInTheDocument();
    expect(dialog.getByTestId("summary-已导入")).toHaveTextContent("1");
    expect(dialog.getByTestId("summary-已跳过")).toHaveTextContent("1");
    expect(dialog.getByTestId("summary-失败")).toHaveTextContent("0");
    expect(dialog.getByText("已有同名技能")).toBeInTheDocument();
    // 成功 1 个、跳过 1 个：「完成」打开成功的那个技能
    await userEvent.click(dialog.getByRole("button", { name: "完成" }));
    expect(
      await screen.findByRole("dialog", { name: "skill-k1" }),
    ).toBeInTheDocument();
  });

  it("明细里的原因码翻译成中文，未知的码显示原文", async () => {
    const failed = (name: string, reason: string): SkillImportItem => ({
      name,
      status: "failed",
      reason,
      skill_id: null,
    });
    renderSkills(
      base,
      runtimeOptions({
        progress: [
          importState(
            [
              failed("a", "SKILL_LIMIT"),
              failed("b", "REQUESTER_INACTIVE"),
              failed("c", "SPACE_RETIRED"),
              failed("d", "UPLOAD_REJECTED"),
              failed("e", "SKILL_TOO_LARGE"),
              failed("f", "WEIRD_CODE"),
            ],
            "done",
          ),
        ],
      }),
    );
    const dialog = await openRuntime();
    await pickDevice(dialog);
    await userEvent.click(await dialog.findByLabelText("全选（2）"));
    await userEvent.click(
      dialog.getByRole("button", { name: "导入 2 个技能" }),
    );
    expect(await dialog.findByText("空间技能数已达上限")).toBeInTheDocument();
    expect(dialog.getByText("你已没有这个空间的写权限")).toBeInTheDocument();
    expect(dialog.getByText("空间已停用")).toBeInTheDocument();
    expect(dialog.getByText("电脑没有完成上传")).toBeInTheDocument();
    expect(dialog.getByText("超过 100 个文件或 5 MB")).toBeInTheDocument();
    expect(dialog.getByText("WEIRD_CODE")).toBeInTheDocument();
    expect(dialog.getByTestId("summary-失败")).toHaveTextContent("6");
  });

  it("导入多个成功时「完成」只关闭弹窗", async () => {
    renderSkills(
      base,
      runtimeOptions({
        progress: [
          importState(
            [imported("review", "k1"), imported("deploy", "k2")],
            "done",
          ),
        ],
      }),
    );
    const dialog = await openRuntime();
    await pickDevice(dialog);
    await userEvent.click(await dialog.findByLabelText("全选（2）"));
    await userEvent.click(
      dialog.getByRole("button", { name: "导入 2 个技能" }),
    );
    await userEvent.click(await dialog.findByRole("button", { name: "完成" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("取消：调用取消接口并显示导入已取消", async () => {
    const { server } = renderSkills(
      base,
      runtimeOptions({
        progress: [importState([pending("review"), pending("deploy")])],
        cancel: importState(
          [
            { name: "review", status: "canceled", reason: "", skill_id: null },
            { name: "deploy", status: "canceled", reason: "", skill_id: null },
          ],
          "canceled",
        ),
      }),
    );
    const dialog = await openRuntime();
    await pickDevice(dialog);
    await userEvent.click(await dialog.findByLabelText("全选（2）"));
    await userEvent.click(
      dialog.getByRole("button", { name: "导入 2 个技能" }),
    );
    await userEvent.click(await dialog.findByRole("button", { name: "取消" }));
    expect(await dialog.findByText("导入已取消。")).toBeInTheDocument();
    expect(
      server.find("POST", "/skills/runtime-imports/imp1/cancel"),
    ).toHaveLength(1);
  });

  it("同一台电脑已有导入时提示稍后再试", async () => {
    renderSkills(
      base,
      runtimeOptions({
        start: json(409, {
          error: { code: "COMMAND_BUSY", message: "x", request_id: "r" },
        }),
      }),
    );
    const dialog = await openRuntime();
    await pickDevice(dialog);
    await userEvent.click(await dialog.findByLabelText("全选（2）"));
    await userEvent.click(
      dialog.getByRole("button", { name: "导入 2 个技能" }),
    );
    expect(
      await dialog.findByText("这台电脑正在导入技能，请稍后再试。"),
    ).toBeInTheDocument();
  });
});

describe("技能详情", () => {
  const detailPath = `${base}?skill=s1`;
  const withSkill = (extra: Partial<Skill> = {}): Options => ({
    skills: [skill("s1", extra)],
  });

  it("从卡片打开详情；文件树 SKILL.md 置顶；预览分名称 / 描述 / 指令", async () => {
    renderSkills(base, withSkill());
    await userEvent.click(
      await screen.findByRole("button", { name: "打开技能 skill-s1" }),
    );
    const dialog = within(
      await screen.findByRole("dialog", { name: "skill-s1" }),
    );
    const tree = within(dialog.getByRole("navigation", { name: "文件" }));
    const names = tree.getAllByRole("button").map((b) => b.textContent);
    expect(names[0]).toBe("SKILL.md");
    expect(await dialog.findByText("名称")).toBeInTheDocument();
    expect(dialog.getByText("做代码审查")).toBeInTheDocument();
    expect(dialog.getByText("先读 diff，再给出意见。")).toBeInTheDocument();
  });

  it("地址里的 ?skill= 刷新后仍打开详情", async () => {
    renderSkills(detailPath, withSkill());
    expect(
      await screen.findByRole("dialog", { name: "skill-s1" }),
    ).toBeInTheDocument();
  });

  it("二进制文件显示无法预览", async () => {
    renderSkills(detailPath, withSkill());
    const dialog = within(
      await screen.findByRole("dialog", { name: "skill-s1" }),
    );
    await userEvent.click(
      await dialog.findByRole("button", { name: "logo.png" }),
    );
    expect(await dialog.findByText("无法预览二进制文件")).toBeInTheDocument();
  });

  it("编辑后出现未保存提示，保存带 version 与变更内容", async () => {
    const { server } = renderSkills(detailPath, withSkill({ version: 3 }));
    const dialog = within(
      await screen.findByRole("dialog", { name: "skill-s1" }),
    );
    await userEvent.click(await dialog.findByRole("radio", { name: "编辑" }));
    const editor = await dialog.findByLabelText("文件内容");
    expect(dialog.queryByText(/未保存的修改/)).toBeNull();
    fireEvent.change(editor, {
      target: { value: "---\nname: skill-s1\n---\n新指令" },
    });
    expect(
      dialog.getByText("未保存的修改——保存会覆盖当前的技能"),
    ).toBeInTheDocument();
    await userEvent.click(dialog.getByRole("button", { name: "保存修改" }));
    await waitFor(() =>
      expect(server.find("PUT", "/skills/s1/files")).toHaveLength(1),
    );
    expect(server.find("PUT", "/skills/s1/files")[0]!.body).toEqual({
      version: 3,
      changes: [
        { path: "SKILL.md", content: "---\nname: skill-s1\n---\n新指令" },
      ],
    });
    expect(await screen.findByText("已保存修改")).toBeInTheDocument();
    await waitFor(() => expect(dialog.queryByText(/未保存的修改/)).toBeNull());
  });

  it("有未保存修改时关闭会先确认", async () => {
    renderSkills(detailPath, withSkill());
    const dialog = within(
      await screen.findByRole("dialog", { name: "skill-s1" }),
    );
    await userEvent.click(await dialog.findByRole("radio", { name: "编辑" }));
    fireEvent.change(await dialog.findByLabelText("文件内容"), {
      target: { value: "改了" },
    });
    await userEvent.click(dialog.getByRole("button", { name: "关闭" }));
    expect(
      await screen.findByRole("dialog", { name: "放弃未保存的修改？" }),
    ).toBeInTheDocument();
  });

  it("删除技能：菜单进入确认，请求带 version", async () => {
    const { server } = renderSkills(detailPath, withSkill({ version: 4 }));
    const dialog = within(
      await screen.findByRole("dialog", { name: "skill-s1" }),
    );
    await userEvent.click(
      await dialog.findByRole("button", { name: "更多操作" }),
    );
    await userEvent.click(screen.getByRole("menuitem", { name: "删除技能" }));
    const confirm = within(
      await screen.findByRole("dialog", { name: "删除技能？" }),
    );
    await userEvent.click(confirm.getByRole("button", { name: "删除技能" }));
    await waitFor(() =>
      expect(server.find("DELETE", "/skills/s1")).toHaveLength(1),
    );
    expect(server.find("DELETE", "/skills/s1")[0]!.query.get("version")).toBe(
      "4",
    );
    expect(await screen.findByText("已删除技能")).toBeInTheDocument();
  });

  it("删除文件：确认后以 delete 变更保存", async () => {
    const { server } = renderSkills(detailPath, withSkill());
    const dialog = within(
      await screen.findByRole("dialog", { name: "skill-s1" }),
    );
    await userEvent.click(
      await dialog.findByRole("button", { name: "guide.md" }),
    );
    await userEvent.click(
      await dialog.findByRole("button", { name: "删除文件" }),
    );
    const confirm = within(
      await screen.findByRole("dialog", { name: "删除文件 refs/guide.md？" }),
    );
    await userEvent.click(confirm.getByRole("button", { name: "删除文件" }));
    await waitFor(() =>
      expect(server.find("PUT", "/skills/s1/files")).toHaveLength(1),
    );
    expect(server.find("PUT", "/skills/s1/files")[0]!.body).toEqual({
      version: 1,
      changes: [{ path: "refs/guide.md", delete: true }],
    });
  });

  it("只读：没有编辑切换、删除文件与更多菜单", async () => {
    renderSkills(detailPath, withSkill({ can_edit: false }));
    const dialog = within(
      await screen.findByRole("dialog", { name: "skill-s1" }),
    );
    expect(await dialog.findByText("只读")).toBeInTheDocument();
    expect(dialog.queryByRole("radio", { name: "编辑" })).toBeNull();
    expect(dialog.queryByRole("button", { name: "更多操作" })).toBeNull();
    expect(dialog.queryByRole("button", { name: "删除文件" })).toBeNull();
  });
});

describe("Agent 设置里的技能区块", () => {
  const url = `/o/${ORG}/spaces/${SPACE}/skills?agentSettings=a1`;
  const spaceSkills = [skill("s1"), skill("s2"), skill("s3")];

  function agentOptions(config: {
    attached: Skill[];
    ownerId?: string;
    role?: string;
    skills?: Skill[];
  }): Options {
    let attached = config.attached;
    return {
      skills: config.skills ?? spaceSkills,
      override: (call) => {
        if (call.path === `${o}/agent-directory`)
          return json(200, {
            entries: [entry("a1", { name: "熊孩子" })],
            archived_count: 0,
            truncated: false,
          });
        if (call.path === `${sp}/agents/a1` && call.method === "GET")
          return json(
            200,
            agent("a1", {
              name: "熊孩子",
              owner_user_id: config.ownerId ?? "u1",
            }),
          );
        if (call.path === `${sp}/agents/a1/skills` && call.method === "GET")
          return json(200, { skills: attached });
        if (call.path === `${sp}/agents/a1/skills` && call.method === "PUT") {
          const ids = (call.body as { skill_ids: string[] }).skill_ids;
          attached = ids.map(
            (id) =>
              (config.skills ?? spaceSkills).find((s) => s.id === id) ??
              skill(id),
          );
          return json(200, { skills: attached });
        }
        if (config.role && call.path === "/v1/me/organizations")
          return json(200, {
            organizations: [
              {
                id: ORG,
                slug: "lin",
                name: "林的团队",
                role: config.role,
                created_at: NOW,
              },
            ],
          });
        return undefined;
      },
    };
  }

  const openSection = async () => {
    const drawer = within(
      await screen.findByRole("complementary", { name: /熊孩子/ }),
    );
    await userEvent.click(await drawer.findByRole("button", { name: /^技能/ }));
    return drawer;
  };

  it("列出已装配技能，可从本空间添加并整体替换", async () => {
    const { server } = renderSkills(
      url,
      agentOptions({ attached: [spaceSkills[0]!] }),
    );
    const drawer = await openSection();
    expect(await drawer.findByText("skill-s1")).toBeInTheDocument();
    expect(
      drawer.getByText(
        "这里装配的技能会在 Agent 每次运行时下发到电脑。电脑上 ~/.agents/skills 里的技能，Codex 也会自动读取。",
      ),
    ).toBeInTheDocument();
    await userEvent.click(drawer.getByRole("button", { name: "添加技能" }));
    // 空间技能列表移到来源菜单的下一级
    await userEvent.click(
      await screen.findByRole("menuitem", { name: /从空间技能里选择/ }),
    );
    await userEvent.click(
      await screen.findByRole("menuitem", { name: "skill-s2" }),
    );
    await waitFor(() =>
      expect(server.find("PUT", "/agents/a1/skills")).toHaveLength(1),
    );
    expect(server.find("PUT", "/agents/a1/skills")[0]!.body).toEqual({
      skill_ids: ["s1", "s2"],
    });
    expect(await drawer.findByText("skill-s2")).toBeInTheDocument();
  });

  it("移除：用剩下的 id 整体替换", async () => {
    const { server } = renderSkills(
      url,
      agentOptions({ attached: [spaceSkills[0]!, spaceSkills[1]!] }),
    );
    const drawer = await openSection();
    await userEvent.click(
      await drawer.findByRole("button", { name: "移除 skill-s1" }),
    );
    await waitFor(() =>
      expect(server.find("PUT", "/agents/a1/skills")).toHaveLength(1),
    );
    expect(server.find("PUT", "/agents/a1/skills")[0]!.body).toEqual({
      skill_ids: ["s2"],
    });
  });

  it("已装配 20 个时不能再添加", async () => {
    const twenty = Array.from({ length: 20 }, (_, i) => skill(`t${i}`));
    renderSkills(
      url,
      agentOptions({ attached: twenty, skills: [...twenty, skill("extra")] }),
    );
    const drawer = await openSection();
    expect(await drawer.findByText("最多装配 20 个技能。")).toBeInTheDocument();
    expect(drawer.getByRole("button", { name: "添加技能" })).toBeDisabled();
  });

  it("没有编辑权限时只读：不显示添加与移除", async () => {
    renderSkills(
      url,
      agentOptions({
        attached: [spaceSkills[0]!],
        ownerId: "u9",
        role: "MEMBER",
      }),
    );
    const drawer = await openSection();
    expect(await drawer.findByText("skill-s1")).toBeInTheDocument();
    expect(
      drawer.getByText("你没有编辑这个 Agent 的权限，只能查看。"),
    ).toBeInTheDocument();
    expect(drawer.queryByRole("button", { name: "添加技能" })).toBeNull();
    expect(drawer.queryByRole("button", { name: "移除 skill-s1" })).toBeNull();
  });

  // 添加技能的来源菜单（D30）
  const openSources = async (drawer: ReturnType<typeof within>) => {
    await userEvent.click(drawer.getByRole("button", { name: "添加技能" }));
    return within(await screen.findByRole("menu", { name: "添加技能" }));
  };

  it("来源菜单列出六项；从市场添加跳到市场页", async () => {
    renderSkills(url, agentOptions({ attached: [] }));
    const drawer = await openSection();
    await drawer.findByText("还没有装配技能");
    const menu = await openSources(drawer);
    const names = menu
      .getAllByRole("menuitem")
      .map((item) => item.textContent ?? "");
    expect(names.map((name) => name.slice(0, 4))).toEqual([
      "从技能市",
      "从空间技",
      "手动创建",
      "上传技能",
      "通过 U",
      "从 Ru",
    ]);
    await userEvent.click(
      menu.getByRole("menuitem", { name: /从技能市场添加/ }),
    );
    expect(
      await screen.findByRole("heading", { name: "市场" }),
    ).toBeInTheDocument();
  });

  it("空间里没有可添加的技能时「从空间技能里选择」禁用并说明原因", async () => {
    renderSkills(url, agentOptions({ attached: [], skills: [] }));
    const drawer = await openSection();
    await drawer.findByText("还没有装配技能");
    const menu = await openSources(drawer);
    const item = menu.getByRole("menuitem", { name: /从空间技能里选择/ });
    expect(item).toHaveAttribute("aria-disabled", "true");
    expect(item).toHaveTextContent("空间里没有可添加的技能");
    // 禁用项点了不进入下一级
    await userEvent.click(item);
    expect(menu.queryByText("选择要添加的技能")).toBeNull();
  });
  it("手动创建：技能进入空间技能库后自动装配到当前 Agent", async () => {
    const { server } = renderSkills(
      url,
      agentOptions({ attached: [spaceSkills[0]!] }),
    );
    const drawer = await openSection();
    await drawer.findByText("skill-s1");
    const menu = await openSources(drawer);
    await userEvent.click(menu.getByRole("menuitem", { name: /手动创建/ }));
    const dialog = within(
      await screen.findByRole("dialog", { name: "编写技能指令" }),
    );
    await userEvent.type(dialog.getByLabelText("技能名称"), "code-review");
    await userEvent.click(dialog.getByRole("button", { name: "保 存" }));
    await waitFor(() =>
      expect(server.find("PUT", "/agents/a1/skills")).toHaveLength(1),
    );
    expect(server.find("POST", "/skills")).toHaveLength(1);
    expect(server.find("PUT", "/agents/a1/skills")[0]!.body).toEqual({
      skill_ids: ["s1", "new-1"],
    });
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "编写技能指令" }),
      ).not.toBeInTheDocument(),
    );
    // 不像空间技能页那样打开技能详情
    expect(screen.queryByRole("dialog", { name: "code-review" })).toBeNull();
  });

  it("上传：成功后自动装配", async () => {
    const { server } = renderSkills(url, agentOptions({ attached: [] }));
    const drawer = await openSection();
    await drawer.findByText("还没有装配技能");
    const menu = await openSources(drawer);
    await userEvent.click(menu.getByRole("menuitem", { name: /上传技能文件/ }));
    await screen.findByRole("dialog", { name: "上传技能文件" });
    fireEvent.change(screen.getByTestId("skill-file-input"), {
      target: { files: [new File(["# x"], "SKILL.md")] },
    });
    await waitFor(() =>
      expect(server.find("PUT", "/agents/a1/skills")).toHaveLength(1),
    );
    expect(server.find("PUT", "/agents/a1/skills")[0]!.body).toEqual({
      skill_ids: ["up-1"],
    });
  });

  it("没有在空间创建技能的权限时，手动创建 / 上传 / URL / Runtime 禁用并说明原因", async () => {
    renderSkills(url, {
      ...agentOptions({ attached: [] }),
      canCreate: false,
    });
    const drawer = await openSection();
    await drawer.findByText("还没有装配技能");
    const menu = await openSources(drawer);
    for (const name of [
      /手动创建/,
      /上传技能文件/,
      /通过 URL 导入/,
      /从 Runtime 导入/,
    ]) {
      const item = menu.getByRole("menuitem", { name });
      expect(item).toHaveAttribute("aria-disabled", "true");
      expect(item).toHaveTextContent("你没有在这个空间创建技能的权限");
    }
    // 市场与空间技能不受影响
    expect(
      menu.getByRole("menuitem", { name: /从技能市场添加/ }),
    ).not.toHaveAttribute("aria-disabled");
    await userEvent.click(menu.getByRole("menuitem", { name: /手动创建/ }));
    expect(screen.queryByRole("dialog", { name: "编写技能指令" })).toBeNull();
  });
  it("从 Runtime 批量导入：成功的全部装配，超过 20 个上限只加到上限并提示", async () => {
    const nineteen = Array.from({ length: 19 }, (_, i) => skill(`t${i}`));
    const names = ["r1", "r2", "r3"];
    const done: SkillRuntimeImport = {
      command_id: "imp1",
      device_id: "d1",
      status: "done",
      items: names.map((name) => ({
        name,
        status: "imported" as const,
        reason: "",
        skill_id: `k-${name}`,
      })),
    };
    const command: DeviceCommand = {
      id: "c1",
      device_id: "d1",
      kind: "skill_scan",
      params: {},
      status: "SUCCEEDED",
      result: {
        supported: true,
        runtime: "codex",
        skills: names.map((name) => ({
          name,
          description: "",
          file_count: 1,
          total_bytes: 10,
          importable: true,
        })),
      } as unknown as DeviceCommand["result"],
      error_code: "",
      created_at: NOW,
      delivered_at: NOW,
      completed_at: NOW,
      expires_at: NOW,
    };
    const agentSide = agentOptions({ attached: nineteen, skills: nineteen });
    const { server } = renderSkills(url, {
      ...agentSide,
      override: (call) => {
        if (call.path === `${o}/devices`)
          return json(200, {
            devices: [device("d1", { name: "我的 Mac", online: true })],
            next_cursor: null,
          });
        if (call.path.startsWith(`${o}/devices/d1/commands`))
          return json(200, { command });
        if (call.path === `${skillsPath}/runtime-imports`)
          return json(202, { ...done, status: "running" });
        if (call.path === `${skillsPath}/runtime-imports/imp1`)
          return json(200, done);
        return agentSide.override?.(call);
      },
    });
    const drawer = await openSection();
    await drawer.findByText("skill-t0");
    const menu = await openSources(drawer);
    await userEvent.click(
      menu.getByRole("menuitem", { name: /从 Runtime 导入/ }),
    );
    const dialog = within(
      await screen.findByRole("dialog", { name: "从 Runtime 导入" }),
    );
    await userEvent.click(
      dialog.getByRole("button", { name: "电脑 / Runtime" }),
    );
    await userEvent.click(await screen.findByRole("option", { name: /codex/ }));
    await userEvent.click(await dialog.findByLabelText("全选（3）"));
    await userEvent.click(
      dialog.getByRole("button", { name: "导入 3 个技能" }),
    );
    await waitFor(() =>
      expect(server.find("PUT", "/agents/a1/skills")).toHaveLength(1),
    );
    expect(server.find("PUT", "/agents/a1/skills")[0]!.body).toEqual({
      skill_ids: [...nineteen.map((s) => s.id), "k-r1"],
    });
    expect(
      await screen.findByText(
        "已达 20 个技能上限，有 2 个技能没有装配，它们仍在空间技能库里。",
      ),
    ).toBeInTheDocument();
    // 「完成」只关闭弹窗；不会重复装配
    await userEvent.click(await dialog.findByRole("button", { name: "完成" }));
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "从 Runtime 导入" }),
      ).not.toBeInTheDocument(),
    );
    expect(server.find("PUT", "/agents/a1/skills")).toHaveLength(1);
  });
});

describe("skill.changed 实时失效", () => {
  it("事件类型已订阅", () => {
    expect(ORGANIZATION_EVENT_TYPES).toContain("skill.changed");
  });

  it("带 space_id 的事件只失效该空间的技能查询（含 Agent 装配）", () => {
    vi.useFakeTimers();
    try {
      const queryClient = createQueryClient();
      const spy = vi.spyOn(queryClient, "invalidateQueries");
      const batcher = createEventBatcher(queryClient, ORG);
      batcher.push("skill.changed", { space_id: SPACE });
      vi.advanceTimersByTime(250);
      expect(spy).toHaveBeenCalledWith({ queryKey: keys.skills(ORG, SPACE) });
      expect(keys.agentSkills(ORG, SPACE, "a1").slice(0, 4)).toEqual(
        keys.skills(ORG, SPACE),
      );
      batcher.dispose();
    } finally {
      vi.useRealTimers();
    }
  });

  it("没有 space_id 时整体失效组织下的技能查询", () => {
    vi.useFakeTimers();
    try {
      const queryClient = createQueryClient();
      const spy = vi.spyOn(queryClient, "invalidateQueries");
      const batcher = createEventBatcher(queryClient, ORG);
      batcher.push("skill.changed", {});
      vi.advanceTimersByTime(250);
      expect(spy).toHaveBeenCalledWith({ queryKey: ["org", ORG, "skills"] });
      batcher.dispose();
    } finally {
      vi.useRealTimers();
    }
  });
});
