import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import type { Attachment } from "@openfox/api";

import { apiError, json, renderSettings } from "../settings/testing";
import { ORG, chatBase, message, summary } from "./fixtures";

// 对话附件：输入框选择文件、先上传再发送、失败保留草稿、消息里的附件卡片。

afterEach(() => localStorage.clear());

const UPLOAD = `${ORG}/conversations/c1/attachments`;
const MESSAGES = `${ORG}/conversations/c1/messages`;

const attachment = (extra: Partial<Attachment> = {}): Attachment => ({
  id: "att-1",
  owner_kind: "CONVERSATION",
  conversation_id: "c1",
  name: "notes.txt",
  content_type: "text/plain; charset=utf-8",
  size_bytes: 5,
  uploaded_by: "u1",
  uploader_name: "林",
  created_at: "2026-10-30T00:00:00Z",
  previewable: false,
  can_delete: true,
  ...extra,
});

function open(
  route: (call: {
    method: string;
    path: string;
    body?: unknown;
  }) => Response | undefined,
  messages: unknown[] = [],
) {
  return renderSettings(
    "/o/o1/chat/c1",
    chatBase((call) => {
      const custom = route(call);
      if (custom) return custom;
      if (call.method === "GET" && call.path === MESSAGES)
        return json(200, { messages, next_cursor: 0 });
      if (call.path === `${ORG}/conversations/c1`)
        return json(200, summary("c1"));
      if (call.path === `${ORG}/conversations`)
        return json(200, { conversations: [summary("c1")], next_cursor: null });
      return undefined;
    }),
  );
}

describe("对话 · 附件", () => {
  it("添加附件按钮可用（不再是「即将开放」）", async () => {
    open(() => undefined);
    const attach = await screen.findByRole("button", { name: "添加附件" });
    expect(attach).toBeEnabled();
    expect(
      screen.queryByRole("button", { name: /添加附件.*即将开放/ }),
    ).not.toBeInTheDocument();
  });

  it("选择文件后先上传再发送，消息带 attachment_ids；成功后清空待发送列表", async () => {
    const calls = open((call) => {
      if (call.method === "POST" && call.path === UPLOAD)
        return json(201, attachment());
      if (call.method === "POST" && call.path === MESSAGES) {
        const body = call.body as { body: string; attachment_ids?: string[] };
        return json(
          201,
          message(10, {
            body: body.body || "请查看附件。",
            attachments: [attachment({ message_id: "m10" })],
          }),
        );
      }
      return undefined;
    });
    const box = await screen.findByRole("textbox", { name: "输入消息" });
    await userEvent.upload(
      screen.getByTestId("composer-file-input"),
      new File(["hello"], "notes.txt", { type: "text/plain" }),
    );
    const pending = await screen.findByRole("list", { name: "待发送的附件" });
    expect(within(pending).getByText("notes.txt")).toBeInTheDocument();
    // 只有附件、没有文字也可以发送
    await userEvent.type(box, "{Enter}");
    await waitFor(() =>
      expect(
        screen.queryByRole("list", { name: "待发送的附件" }),
      ).not.toBeInTheDocument(),
    );
    const upload = calls.findIndex((c) => c.path === UPLOAD);
    const post = calls.findIndex(
      (c) => c.method === "POST" && c.path === MESSAGES,
    );
    expect(upload).toBeGreaterThanOrEqual(0);
    expect(post).toBeGreaterThan(upload);
    expect(calls[post]!.body).toMatchObject({
      body: "",
      attachment_ids: ["att-1"],
    });
    expect(calls[upload]!.method).toBe("POST");
  });

  it("上传失败：提示具体原因，不发消息，草稿与文件都保留；重试只上传一次", async () => {
    let uploads = 0;
    let posts = 0;
    open((call) => {
      if (call.method === "POST" && call.path === UPLOAD) {
        uploads += 1;
        return uploads === 1
          ? apiError(415, "ATTACHMENT_TYPE_NOT_ALLOWED")
          : json(201, attachment());
      }
      if (call.method === "POST" && call.path === MESSAGES) {
        posts += 1;
        return json(201, message(10, { body: "看看" }));
      }
      return undefined;
    });
    const box = await screen.findByRole("textbox", { name: "输入消息" });
    await userEvent.upload(
      screen.getByTestId("composer-file-input"),
      new File(["MZ"], "run.exe"),
    );
    await userEvent.type(box, "看看{Enter}");
    expect(
      await screen.findByText("不支持 run.exe 这种文件类型"),
    ).toBeInTheDocument();
    expect(posts).toBe(0);
    expect(box).toHaveValue("看看");
    expect(
      screen.getByRole("list", { name: "待发送的附件" }),
    ).toBeInTheDocument();
    // 重试：上传成功后发送；已上传的文件不会再传一次
    await userEvent.type(box, "{Enter}");
    await waitFor(() => expect(box).toHaveValue(""));
    expect(uploads).toBe(2);
    expect(posts).toBe(1);
  });

  it("超过 20 MB 的文件不进入待发送列表并提示", async () => {
    open(() => undefined);
    await screen.findByRole("textbox", { name: "输入消息" });
    const big = new File(["x"], "huge.mov");
    Object.defineProperty(big, "size", { value: 21 * 1024 * 1024 });
    await userEvent.upload(screen.getByTestId("composer-file-input"), big);
    expect(
      await screen.findByText("huge.mov 超过 20 MB，无法上传"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("list", { name: "待发送的附件" }),
    ).not.toBeInTheDocument();
  });

  it("一条消息最多 10 个附件", async () => {
    open(() => undefined);
    await screen.findByRole("textbox", { name: "输入消息" });
    const files = Array.from(
      { length: 11 },
      (_, i) => new File(["a"], `f${i}.txt`),
    );
    await userEvent.upload(screen.getByTestId("composer-file-input"), files);
    expect(
      await screen.findByText("一条消息最多 10 个附件"),
    ).toBeInTheDocument();
    const pending = screen.getByRole("list", { name: "待发送的附件" });
    expect(within(pending).getAllByRole("listitem")).toHaveLength(10);
  });

  it("可以移除待发送的附件", async () => {
    open(() => undefined);
    await screen.findByRole("textbox", { name: "输入消息" });
    await userEvent.upload(
      screen.getByTestId("composer-file-input"),
      new File(["a"], "a.txt"),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "移除 a.txt" }),
    );
    expect(
      screen.queryByRole("list", { name: "待发送的附件" }),
    ).not.toBeInTheDocument();
  });

  it("消息里显示附件卡片：类型徽标、文件名、「类型 • 大小」、打开与下载；图片显示缩略图", async () => {
    open(
      () => undefined,
      [
        message(1, {
          body: "请看这几个文件",
          attachments: [
            attachment({ id: "att-1", message_id: "m1" }),
            attachment({
              id: "att-2",
              message_id: "m1",
              name: "截图.png",
              content_type: "image/png",
              size_bytes: 2048,
              previewable: true,
            }),
            attachment({
              id: "att-3",
              message_id: "m1",
              name: "方案.pdf",
              content_type: "application/pdf",
              size_bytes: 1_258_291,
            }),
          ],
        }),
      ],
    );
    const list = await screen.findByRole("list", { name: "附件" });
    const [text, image, pdf] = within(list).getAllByRole("listitem");
    // 文本文件：中性底色加文件图标，副标题「文本文件 • 5 B」
    expect(within(text!).getByText("notes.txt")).toBeInTheDocument();
    expect(within(text!).getByText(/文本文件 • 5 B/)).toBeInTheDocument();
    expect(
      within(text!)
        .getByRole("link", { name: "下载 notes.txt" })
        .getAttribute("href"),
    ).toContain("/v1/organizations/o1/attachments/att-1");
    // 非图片的「打开」直接指向下载地址
    expect(
      within(text!)
        .getByRole("link", { name: "打开 notes.txt" })
        .getAttribute("href"),
    ).not.toContain("disposition=inline");
    // 图片：缩略图与新标签页预览
    expect(image!.querySelector("img")?.getAttribute("src")).toContain(
      "disposition=inline",
    );
    expect(
      within(image!)
        .getByRole("link", { name: "打开 截图.png" })
        .getAttribute("href"),
    ).toContain("disposition=inline");
    expect(within(image!).getByText(/图片 • 2 KB/)).toBeInTheDocument();
    // PDF：红色 PDF 徽标
    expect(within(pdf!).getByText("PDF")).toBeInTheDocument();
    expect(within(pdf!).getByText(/PDF 文档 • 1.2 MB/)).toBeInTheDocument();
  });
});
