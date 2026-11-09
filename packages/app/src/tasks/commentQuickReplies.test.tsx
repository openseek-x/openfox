import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import { backend, json, ORG, SPACE, type Override } from "./test-backend";

const reply = {
  id: "q1",
  scope: "PERSONAL",
  owner_user_id: "u1",
  title: "验收回复",
  body: "中文快捷回复已送达",
  position: 0,
  version: 1,
  can_edit: true,
  created_at: "2026-11-05T00:00:00Z",
  updated_at: "2026-11-05T00:00:00Z",
};
const org = `/v1/organizations/${ORG}`;

function open(override?: Override) {
  localStorage.setItem(`openfox:onboarding:${ORG}`, "{}");
  const server = backend((call) => {
    const custom = override?.(call);
    if (custom) return custom;
    if (call.path === `${org}/quick-replies`)
      return json(200, { personal: [reply], organization: [] });
    return undefined;
  });
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter
      initialEntries={[`/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`]}
    >
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

afterEach(() => localStorage.clear());

describe("任务评论快捷回复", () => {
  it("紧凑工具栏、标题菜单和新增入口；切换弹层时只显示一个", async () => {
    const server = open();
    const user = userEvent.setup();
    const box = await screen.findByRole("textbox", { name: "评论..." });
    expect(box).toHaveAttribute("data-slot", "prompt-input-textarea");
    expect(screen.queryByRole("button", { name: "提及成员" })).toBeNull();
    const quick = screen.getByRole("button", { name: "快捷回复" });
    await user.click(quick);
    const menu = within(
      await screen.findByRole("dialog", { name: "快捷回复" }),
    );
    expect(await menu.findByRole("button", { name: "验收回复" })).toBeVisible();
    expect(menu.queryByText(reply.body)).toBeNull();
    expect(menu.queryByRole("combobox")).toBeNull();
    await waitFor(() =>
      expect(menu.getByRole("button", { name: "验收回复" })).toHaveFocus(),
    );
    await user.keyboard("{End}");
    expect(menu.getByRole("button", { name: "管理快捷回复…" })).toHaveFocus();
    await user.keyboard("{Home}{ArrowDown}");
    expect(
      menu.getByRole("button", { name: "把「验收回复」插入到输入框" }),
    ).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(quick).toHaveFocus();
    await user.click(quick);
    await user.click(screen.getByRole("button", { name: "插入附件链接" }));
    expect(screen.queryByRole("dialog", { name: "快捷回复" })).toBeNull();
    expect(screen.getByRole("dialog", { name: "插入附件链接" })).toBeVisible();
    await user.click(quick);
    expect(screen.queryByRole("dialog", { name: "插入附件链接" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "添加快捷回复" }));
    const manager = within(
      await screen.findByRole("dialog", { name: "管理快捷回复" }),
    );
    expect(manager.getByRole("textbox", { name: "标题" })).toBeVisible();
    expect(manager.getByRole("textbox", { name: "内容" })).toBeVisible();
    expect(server.find("POST", "/comments")).toHaveLength(0);
  });

  it("@ 在编辑区筛选邮箱，键盘选择替换词并保留光标后的正文", async () => {
    const server = open();
    const user = userEvent.setup();
    const box = (await screen.findByRole("textbox", {
      name: "评论...",
    })) as HTMLTextAreaElement;
    await user.type(box, "开头 结尾");
    box.setSelectionRange(3, 3);
    await user.keyboard("@wa");
    expect(box).toHaveFocus();
    const options = within(screen.getByRole("listbox", { name: "提及成员" }));
    expect(options.getAllByRole("option")).toHaveLength(1);
    expect(options.getByRole("option")).toHaveTextContent("wang@openfox.dev");
    expect(
      within(screen.getByRole("dialog", { name: "提及成员" })).queryByRole(
        "combobox",
      ),
    ).toBeNull();
    await user.keyboard("{Enter}谢谢");
    expect(box).toHaveValue("开头 @wang@openfox.dev 谢谢结尾");
    expect(server.find("POST", "/comments")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "发送评论" }));
    await waitFor(() =>
      expect(server.find("POST", "/comments")).toHaveLength(1),
    );
    expect(server.find("POST", "/comments")[0]!.body).toMatchObject({
      mentions: [{ user_id: "u2" }],
    });
  });

  it("@ 空结果 Enter 不发送，Escape 不关闭任务，输入法确认不误发", async () => {
    const server = open();
    const user = userEvent.setup();
    const box = await screen.findByRole("textbox", { name: "评论..." });
    await user.type(box, "@nobody");
    expect(screen.queryByRole("option")).toBeNull();
    await user.keyboard("{Enter}");
    expect(server.find("POST", "/comments")).toHaveLength(0);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox", { name: "提及成员" })).toBeNull();
    expect(box).toHaveFocus();
    await user.clear(box);
    fireEvent.compositionStart(box);
    fireEvent.change(box, {
      target: { value: "@wa", selectionStart: 3, selectionEnd: 3 },
    });
    expect(screen.queryByRole("listbox", { name: "提及成员" })).toBeNull();
    fireEvent.keyDown(box, { key: "Enter", keyCode: 229, isComposing: true });
    expect(server.find("POST", "/comments")).toHaveLength(0);
    fireEvent.compositionEnd(box);
    expect(
      screen.getByRole("option", { name: /wang@openfox.dev/ }),
    ).toBeVisible();
    await user.keyboard("{Escape}");
    await user.clear(box);
    await user.type(box, "email@example.test");
    expect(screen.queryByRole("listbox", { name: "提及成员" })).toBeNull();
  });

  it("插入替换选区但不发送，用户确认发送后提交插入的正文", async () => {
    const server = open();
    const user = userEvent.setup();
    const box = await screen.findByRole("textbox", { name: "评论..." });
    await user.type(box, "开头旧内容结尾");
    (box as HTMLTextAreaElement).setSelectionRange(2, 5);
    await user.click(screen.getByRole("button", { name: "快捷回复" }));
    await user.click(
      await screen.findByRole("button", { name: "把「验收回复」插入到输入框" }),
    );
    expect(box).toHaveValue("开头中文快捷回复已送达结尾");
    expect(server.find("POST", "/comments")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "发送评论" }));
    await waitFor(() =>
      expect(server.find("POST", "/comments")).toHaveLength(1),
    );
    expect(server.find("POST", "/comments")[0]!.body).toMatchObject({
      body: "开头中文快捷回复已送达结尾",
    });
    await waitFor(() => expect(box).toHaveValue(""));
  });

  it("直接回复保留原草稿，失败重试复用幂等 ID，成功后新发送使用新 ID", async () => {
    let fail = true;
    const server = open((call) =>
      call.method === "POST" && call.path.endsWith("/comments") && fail
        ? json(503, {
            error: {
              code: "NOT_READY",
              message: "unavailable",
              request_id: "r",
            },
          })
        : undefined,
    );
    const user = userEvent.setup();
    const box = await screen.findByRole("textbox", { name: "评论..." });
    await user.type(box, "未发送的草稿");
    const sendQuick = async () => {
      await user.click(screen.getByRole("button", { name: "快捷回复" }));
      await user.click(await screen.findByText("验收回复"));
    };
    await sendQuick();
    await waitFor(() =>
      expect(server.find("POST", "/comments")).toHaveLength(1),
    );
    expect(box).toHaveValue("未发送的草稿");
    fail = false;
    await sendQuick();
    await waitFor(() =>
      expect(server.find("POST", "/comments")).toHaveLength(2),
    );
    const first = server.find("POST", "/comments")[0]!.body;
    expect(first).toMatchObject({ body: reply.body });
    expect(server.find("POST", "/comments")[1]!.body).toEqual(first);
    expect(box).toHaveValue("未发送的草稿");
    await sendQuick();
    await waitFor(() =>
      expect(server.find("POST", "/comments")).toHaveLength(3),
    );
    expect(server.find("POST", "/comments")[2]!.body).not.toEqual(first);
  });

  it("评论入口可以打开现有快捷回复管理", async () => {
    open();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "快捷回复" }));
    await user.click(screen.getByRole("button", { name: "管理快捷回复…" }));
    expect(
      await screen.findByRole("dialog", { name: "管理快捷回复" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "新建快捷回复" })).toBeEnabled();
  });

  it("已停用空间不允许快捷发表评论", async () => {
    const server = open((call) => {
      if (call.path === `${org}/spaces`) return json(200, { spaces: [] });
      if (call.path === `${org}/spaces/${SPACE}`)
        return json(200, {
          id: SPACE,
          prefix: "OPEN",
          name: "默认空间",
          visibility: "shared",
          version: 2,
          status: "retired",
          features: {
            views: false,
            projects: false,
            iterations: false,
            triage: false,
          },
          estimation: {
            scale: "notUsed",
            extended: false,
            allow_zero: false,
            count_unestimated: false,
          },
          my_role: "OWNER",
          can_manage: true,
        });
      return undefined;
    });
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "快捷回复" })).toBeDisabled(),
    );
    await userEvent.click(screen.getByRole("button", { name: "快捷回复" }));
    expect(server.find("POST", "/comments")).toHaveLength(0);
  });
});

describe("评论话题", () => {
  const comment = (id: string, body: string) => ({
    id,
    body,
    author_name: "测试成员",
    author_kind: "USER",
    author_user_id: "u1",
    reactions: [],
    created_at: "2026-11-05T00:00:00Z",
  });
  it("跨页总数不依赖可见条数；失败可重试，回复留在原评论，两个草稿互不影响", async () => {
    const notes: ReturnType<typeof comment>[] = [];
    let fail = true;
    const server = open((call) => {
      if (call.path.endsWith("/timeline")) {
        const thread = call.query.get("thread");
        if (thread === "r1")
          return json(200, {
            items: notes
              .slice()
              .reverse()
              .map((c) => ({ type: "comment", comment: c })),
            next_cursor: null,
          });
        return json(200, {
          total: 101 + notes.length,
          items: [
            {
              type: "comment",
              comment: {
                ...comment("r1", "第一条主评论"),
                reply_count: notes.length,
                recent_replies: notes,
              },
            },
            { type: "comment", comment: comment("r2", "第二条主评论") },
          ],
          next_cursor: "more",
        });
      }
      if (call.method === "POST" && call.path.endsWith("/comments")) {
        if (fail)
          return json(503, {
            error: { code: "NOT_READY", message: "offline" },
          });
        const note = comment(
          "reply-" + notes.length,
          (call.body as { body: string }).body,
        );
        notes.push(note);
        return json(201, note);
      }
      return undefined;
    });
    const user = userEvent.setup();
    const card = within(
      (await screen.findByText("第一条主评论")).closest("li")!,
    );
    const other = within(screen.getByText("第二条主评论").closest("li")!);
    const main = screen.getByRole("textbox", { name: "评论..." });
    const replyBox = () => card.getByRole("textbox", { name: "回复..." });
    expect(
      await screen.findByRole("tab", { name: /动态\s*101/ }),
    ).toBeVisible();
    expect(card.getByRole("button", { name: "提交回复" })).toBeDisabled();
    await user.type(main, "任务草稿");
    await user.type(
      other.getByRole("textbox", { name: "回复..." }),
      "独立草稿",
    );
    await user.type(replyBox(), "第一条回复");
    await user.click(card.getByRole("button", { name: "提交回复" }));
    await waitFor(() =>
      expect(server.find("POST", "/comments")).toHaveLength(1),
    );
    expect(replyBox()).toHaveValue("第一条回复");
    fail = false;
    await user.click(replyBox());
    await user.keyboard("{Control>}{Enter}{/Control}");
    await card.findByRole("button", { name: "查看完整话题" });
    const [failed, saved] = server.find("POST", "/comments");
    expect(saved!.body).toEqual(failed!.body);
    expect(saved!.body).toMatchObject({
      body: "第一条回复",
      reply_to_id: "r1",
    });
    expect(card.getByText("1 条回复")).toBeVisible();
    expect(card.queryByRole("textbox")).toBeNull();
    expect(main).toHaveValue("任务草稿");
    expect(other.getByRole("textbox")).toHaveValue("独立草稿");
    await user.click(card.getByRole("button", { name: "回复..." }));
    const topic = within(await screen.findByRole("dialog", { name: "话题" }));
    const box = topic.getByRole("textbox", { name: "回复..." });
    await waitFor(() => expect(box).toHaveFocus());
    expect(await topic.findByText("第一条回复")).toBeVisible();
    expect(topic.queryByText("第二条主评论")).toBeNull();
    await user.type(box, "面板草稿");
    await user.click(topic.getByRole("button", { name: "关闭" }));
    await user.click(card.getByRole("button", { name: "查看完整话题" }));
    const reopened = within(
      await screen.findByRole("dialog", { name: "话题" }),
    );
    expect(reopened.getByRole("textbox")).toHaveValue("面板草稿");
    await user.click(reopened.getByRole("button", { name: "提交评论" }));
    expect(await reopened.findByText("面板草稿")).toBeVisible();
    expect(await card.findByText("2 条回复")).toBeVisible();
    expect(server.find("POST", "/comments")[2]!.body).toMatchObject({
      reply_to_id: "r1",
    });
    expect(screen.getByRole("tab", { name: /动态\s*101/ })).toBeVisible();
  });
  it("快捷回复直接发送到所属话题，附件链接仅插入该评论的草稿", async () => {
    const server = open((call) => {
      if (call.path.endsWith("/timeline"))
        return json(200, {
          total: 2,
          items: [{ type: "comment", comment: comment("root", "附件评论") }],
          next_cursor: null,
        });
      if (call.path.endsWith("/attachments") && call.method === "POST")
        return json(201, { id: "file-7", name: "notes.txt" });
      return undefined;
    });
    const user = userEvent.setup();
    const card = within((await screen.findByText("附件评论")).closest("li")!);
    await user.upload(
      card.getByTestId("reply-attachment-input"),
      new File(["abc"], "notes.txt", { type: "text/plain" }),
    );
    const box = card.getByRole("textbox");
    await waitFor(() =>
      expect((box as HTMLTextAreaElement).value).toContain(
        "/attachments/file-7",
      ),
    );
    await user.click(card.getByRole("button", { name: "快捷回复" }));
    await user.click(await screen.findByText("验收回复"));
    await waitFor(() =>
      expect(server.find("POST", "/comments")[0]?.body).toMatchObject({
        reply_to_id: "root",
        body: reply.body,
      }),
    );
    await user.click(await card.findByRole("button", { name: "回复..." }));
    const topic = within(await screen.findByRole("dialog", { name: "话题" }));
    expect((topic.getByRole("textbox") as HTMLTextAreaElement).value).toContain(
      "/attachments/file-7",
    );
    expect(screen.getByRole("textbox", { name: "评论..." })).toHaveValue("");
  });
});
