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

import { App, createQueryClient } from "../App";
import { backend, json, ORG, SPACE, type Override } from "./test-backend";

// 任务详情的「附件」区：列表、上传（按钮 / 拖拽 / 粘贴）、预览、下载、删除，以及评论里插入附件链接。

function renderAt(path: string, override?: Override) {
  const server = backend(override);
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter initialEntries={[path]}>
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
    </MemoryRouter>,
  );
  return server;
}

const detailPath = `/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`;
const LIST = "/tasks/task-1/attachments";

const attachment = (extra: Record<string, unknown> = {}) => ({
  id: "a1",
  owner_kind: "TASK",
  task_id: "task-1",
  name: "设计稿.pdf",
  content_type: "application/pdf",
  size_bytes: 1536,
  uploaded_by: "u1",
  uploader_name: "林",
  created_at: "2026-10-30T00:00:00Z",
  previewable: false,
  can_delete: true,
  ...extra,
});

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
});

const region = () => screen.findByRole("region", { name: "附件" });

describe("任务详情 · 附件", () => {
  it("列出文件名、大小、上传人；下载走附件地址；只有 can_delete 的行有删除按钮", async () => {
    renderAt(detailPath, (call) =>
      call.path.endsWith(LIST)
        ? json(200, {
            attachments: [
              attachment(),
              attachment({ id: "a2", name: "别人的.txt", can_delete: false }),
            ],
          })
        : undefined,
    );
    const section = await region();
    const link = await within(section).findByRole("link", {
      name: "设计稿.pdf",
    });
    expect(link.getAttribute("href")).toContain(
      "/v1/organizations/o1/attachments/a1",
    );
    expect(within(section).getAllByText("1.5 KB")).toHaveLength(2);
    expect(within(section).getAllByText(/林/).length).toBeGreaterThan(0);
    expect(
      within(section).getByRole("button", { name: "删除 设计稿.pdf" }),
    ).toBeVisible();
    expect(
      within(section).queryByRole("button", { name: "删除 别人的.txt" }),
    ).not.toBeInTheDocument();
  });

  it("没有附件时不显示空区块，工具栏仍可添加附件", async () => {
    renderAt(detailPath, (call) =>
      call.path.endsWith(LIST) ? json(200, { attachments: [] }) : undefined,
    );
    expect(
      await screen.findByRole("button", { name: "添加附件" }),
    ).toBeEnabled();
    expect(
      screen.queryByRole("region", { name: "附件" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/还没有附件/)).not.toBeInTheDocument();
  });

  it("按钮上传：multipart 发到任务附件接口，成功后刷新列表", async () => {
    let uploaded = false;
    const server = renderAt(detailPath, (call) => {
      if (call.path.endsWith(LIST) && call.method === "POST") {
        uploaded = true;
        return json(201, attachment({ id: "a9", name: "notes.txt" }));
      }
      if (call.path.endsWith(LIST))
        return json(200, {
          attachments: uploaded
            ? [attachment({ id: "a9", name: "notes.txt" })]
            : [],
        });
      return undefined;
    });
    const input = await screen.findByTestId("attachment-input");
    const file = new File(["hello"], "notes.txt", { type: "text/plain" });
    await userEvent.upload(input, file);
    await within(await region()).findByRole("link", { name: "notes.txt" });
    const post = server.calls.find(
      (c) => c.method === "POST" && c.path.endsWith(LIST),
    );
    expect(post?.body).toBeInstanceOf(FormData);
    expect((post!.body as FormData).get("file")).toBeInstanceOf(File);
  });

  it("超过 20 MB 的文件不发请求并提示", async () => {
    const server = renderAt(detailPath, (call) =>
      call.path.endsWith(LIST) && call.method === "GET"
        ? json(200, { attachments: [] })
        : undefined,
    );
    const input = await screen.findByTestId("attachment-input");
    const big = new File(["x"], "big.zip", { type: "application/zip" });
    Object.defineProperty(big, "size", { value: 21 * 1024 * 1024 });
    await userEvent.upload(input, big);
    expect(
      await screen.findByText("big.zip 超过 20 MB，无法上传"),
    ).toBeInTheDocument();
    expect(
      server.calls.some((c) => c.method === "POST" && c.path.endsWith(LIST)),
    ).toBe(false);
  });

  it("服务端拒绝类型时提示具体文件", async () => {
    renderAt(detailPath, (call) => {
      if (call.path.endsWith(LIST) && call.method === "POST")
        return json(415, {
          error: { code: "ATTACHMENT_TYPE_NOT_ALLOWED", message: "x" },
        });
      if (call.path.endsWith(LIST)) return json(200, { attachments: [] });
      return undefined;
    });
    await userEvent.upload(
      await screen.findByTestId("attachment-input"),
      new File(["MZ"], "run.exe"),
    );
    expect(
      await screen.findByText("不支持 run.exe 这种文件类型"),
    ).toBeInTheDocument();
  });

  it("拖入文件与粘贴图片都会上传", async () => {
    const server = renderAt(detailPath, (call) => {
      if (call.path.endsWith(LIST) && call.method === "POST")
        return json(201, attachment({ id: "a7", name: "x" }));
      if (call.path.endsWith(LIST)) return json(200, { attachments: [] });
      return undefined;
    });
    const zone = await screen.findByTestId("attachment-drop");
    const dropped = new File(["d"], "dropped.txt", { type: "text/plain" });
    fireEvent.drop(zone, { dataTransfer: { files: [dropped] } });
    await waitFor(() =>
      expect(
        server.calls.filter(
          (c) => c.method === "POST" && c.path.endsWith(LIST),
        ),
      ).toHaveLength(1),
    );
    const pasted = new File(["p"], "image.png", { type: "image/png" });
    fireEvent.paste(zone, { clipboardData: { files: [pasted] } });
    await waitFor(() =>
      expect(
        server.calls.filter(
          (c) => c.method === "POST" && c.path.endsWith(LIST),
        ),
      ).toHaveLength(2),
    );
    const second = server.calls.filter((c) => c.method === "POST")[1]!
      .body as FormData;
    // 粘贴的图片补上时间戳文件名，避免多张同名
    expect((second.get("file") as File).name).toMatch(
      /^粘贴的图片-\d{14}\.png$/,
    );
  });

  it("删除需要确认，确认后 DELETE 并刷新", async () => {
    let removed = false;
    const server = renderAt(detailPath, (call) => {
      if (call.method === "DELETE" && call.path.endsWith("/attachments/a1")) {
        removed = true;
        return new Response(null, { status: 204 });
      }
      if (call.path.endsWith(LIST))
        return json(200, { attachments: removed ? [] : [attachment()] });
      return undefined;
    });
    const section = await region();
    await userEvent.click(
      await within(section).findByRole("button", { name: "删除 设计稿.pdf" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "删除附件" });
    expect(
      within(dialog).getByText(/确定删除「设计稿.pdf」吗/),
    ).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "删除" }));
    await waitFor(() =>
      expect(
        server.calls.some(
          (c) => c.method === "DELETE" && c.path.endsWith("/attachments/a1"),
        ),
      ).toBe(true),
    );
    await waitFor(() =>
      expect(
        within(section).queryByRole("link", { name: "设计稿.pdf" }),
      ).not.toBeInTheDocument(),
    );
  });

  it("图片可预览：点击文件名打开预览，地址带 disposition=inline", async () => {
    renderAt(detailPath, (call) =>
      call.path.endsWith(LIST)
        ? json(200, {
            attachments: [
              attachment({
                name: "截图.png",
                content_type: "image/png",
                previewable: true,
              }),
            ],
          })
        : undefined,
    );
    const section = await region();
    await userEvent.click(
      await within(section).findByRole("button", { name: "预览 截图.png" }),
    );
    const image = await screen.findByRole("img", { name: "截图.png" });
    expect(image.getAttribute("src")).toContain("disposition=inline");
  });

  it("评论框可以插入已有附件的链接", async () => {
    renderAt(detailPath, (call) =>
      call.path.endsWith(LIST)
        ? json(200, { attachments: [attachment()] })
        : undefined,
    );
    await region();
    await userEvent.click(
      await screen.findByRole("button", { name: "插入附件链接" }),
    );
    await userEvent.click(
      await screen.findByText("设计稿.pdf", {
        selector: "[role=option] *, [role=option]",
      }),
    );
    const box = screen.getByRole("textbox", { name: /评论/ });
    await waitFor(() =>
      expect((box as HTMLTextAreaElement).value).toMatch(
        /^\[设计稿\.pdf\]\(https?:\/\/[^)]+\/attachments\/a1\)$/,
      ),
    );
  });
});
