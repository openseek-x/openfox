import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";

import { ApiProvider } from "../../context";
import {
  backend,
  json,
  ORG,
  SPACE,
  type Call,
  type Override,
} from "../../tasks/test-backend";
import BlockBody from "../BlockBody";

// 阶段 5b · 文档正文编辑器（jsdom）：只读不可编辑、斜杠菜单插入标题、附件大小校验与插入链接。

// jsdom 没有布局：编辑器用 elementFromPoint 定位，菜单用 scrollIntoView 跟随高亮，这里给出空实现
if (!document.elementFromPoint) document.elementFromPoint = () => null;
if (!Element.prototype.scrollIntoView)
  Element.prototype.scrollIntoView = () => undefined;
if (!Range.prototype.getClientRects) {
  Range.prototype.getClientRects = () => [] as unknown as DOMRectList;
  Range.prototype.getBoundingClientRect = () => new DOMRect();
}

const NODE = "n1";
const attachmentPath = `/v1/organizations/${ORG}/spaces/${SPACE}/docs/${NODE}/attachments`;

function renderBody(options: {
  editable: boolean;
  initialMarkdown?: string;
  onChange?: (markdown: string) => void;
  override?: Override;
}) {
  const server = backend((call) => options.override?.(call));
  const onChange = options.onChange ?? vi.fn();
  render(
    <I18nProvider initialLocale="zh-CN">
      <QueryClientProvider client={new QueryClient()}>
        <ApiProvider api={server.api}>
          <ToastProvider>
            <BlockBody
              orgId={ORG}
              spaceId={SPACE}
              nodeId={NODE}
              initialMarkdown={options.initialMarkdown ?? ""}
              editable={options.editable}
              onChange={onChange}
            />
          </ToastProvider>
        </ApiProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { server, onChange };
}

/** 正文的可编辑区域（TipTap 渲染为 .tiptap）。 */
const editorArea = () => document.querySelector<HTMLElement>(".tiptap")!;

/** 附件用的隐藏文件框：有 accept="image/*" 的是图片，另一个是附件。 */
const attachmentInput = () =>
  document.querySelector<HTMLInputElement>('input[type="file"]:not([accept])')!;

describe("正文编辑器", () => {
  it("只读时正文不可编辑，输入 / 不弹出斜杠菜单", async () => {
    renderBody({ editable: false, initialMarkdown: "只读正文" });
    await waitFor(() =>
      expect(editorArea()).toHaveAttribute("contenteditable", "false"),
    );
    expect(editorArea()).toHaveTextContent("只读正文");
    await userEvent.click(editorArea());
    await userEvent.keyboard("/");
    expect(
      screen.queryByRole("listbox", { name: "斜杠菜单" }),
    ).not.toBeInTheDocument();
  });

  it("斜杠菜单：输入 /标题 后回车插入一级标题，并回写 Markdown", async () => {
    const onChange = vi.fn();
    renderBody({ editable: true, onChange });
    await waitFor(() =>
      expect(editorArea()).toHaveAttribute("contenteditable", "true"),
    );
    await userEvent.click(editorArea());
    await userEvent.keyboard("/标题");
    const menu = await screen.findByRole("listbox", { name: "斜杠菜单" });
    expect(screen.getAllByRole("option")[0]).toHaveTextContent("标题 1");
    expect(menu).toBeInTheDocument();
    await userEvent.keyboard("{Enter}");
    await userEvent.keyboard("标题内容");
    await waitFor(() =>
      expect(onChange).toHaveBeenLastCalledWith("# 标题内容"),
    );
    expect(
      screen.queryByRole("listbox", { name: "斜杠菜单" }),
    ).not.toBeInTheDocument();
  });

  it("点击斜杠菜单外部会关闭菜单并保留已输入的文字", async () => {
    renderBody({ editable: true });
    await waitFor(() =>
      expect(editorArea()).toHaveAttribute("contenteditable", "true"),
    );
    await userEvent.click(editorArea());
    await userEvent.keyboard("/标题");
    const menu = await screen.findByRole("listbox", { name: "斜杠菜单" });
    await userEvent.click(menu);
    expect(menu).toBeInTheDocument();
    await userEvent.click(document.body);
    expect(menu).not.toBeInTheDocument();
    expect(editorArea()).toHaveTextContent("/标题");
  });

  it("附件超过 20 MB 时提示且不发出上传请求", async () => {
    const { server } = renderBody({ editable: true });
    await waitFor(() =>
      expect(editorArea()).toHaveAttribute("contenteditable", "true"),
    );
    const big = new File(["x"], "大文件.zip");
    Object.defineProperty(big, "size", { value: 21 * 1024 * 1024 });
    await userEvent.upload(attachmentInput(), big);
    expect(await screen.findByText("附件不能超过 20 MB")).toBeInTheDocument();
    expect(server.find("POST", "/attachments")).toHaveLength(0);
  });

  it("附件上传成功后插入链接文字，地址来自服务端", async () => {
    const onChange = vi.fn();
    const override: Override = (call: Call) =>
      call.path === attachmentPath && call.method === "POST"
        ? json(201, {
            id: "a1",
            node_id: NODE,
            name: "方案.pdf",
            content_type: "application/pdf",
            size_bytes: 3,
            url: "/api/v1/doc-attachments/a1",
          })
        : undefined;
    const { server } = renderBody({ editable: true, onChange, override });
    await waitFor(() =>
      expect(editorArea()).toHaveAttribute("contenteditable", "true"),
    );
    await userEvent.upload(
      attachmentInput(),
      new File(["pdf"], "方案.pdf", { type: "application/pdf" }),
    );
    await waitFor(() =>
      expect(server.find("POST", "/attachments")).toHaveLength(1),
    );
    await waitFor(() =>
      expect(onChange).toHaveBeenLastCalledWith(
        "[方案.pdf](/api/v1/doc-attachments/a1)",
      ),
    );
  });
});

describe("正文查找控件", () => {
  it("只读正文可查找、计数和循环；零结果禁用导航，Esc 退出且无保存", async () => {
    const { onChange } = renderBody({
      editable: false,
      initialMarkdown: "中文 Abc 中文 abc",
    });
    await userEvent.click(
      await screen.findByRole("button", { name: "文内查找" }),
    );
    const input = screen.getByRole("searchbox", { name: "查找正文" });
    await userEvent.type(input, "中文");
    expect(screen.getByRole("status")).toHaveTextContent("1 / 2");
    await userEvent.click(screen.getByRole("button", { name: "下一个匹配" }));
    expect(screen.getByRole("status")).toHaveTextContent("2 / 2");
    expect(document.querySelectorAll(".doc-find-match")).toHaveLength(2);
    await userEvent.clear(input);
    await userEvent.type(input, "abc");
    expect(screen.getByRole("status")).toHaveTextContent("1 / 2");
    await userEvent.click(screen.getByRole("checkbox", { name: "区分大小写" }));
    expect(screen.getByRole("status")).toHaveTextContent("1 / 1");
    await userEvent.clear(input);
    await userEvent.type(input, "不存在");
    expect(screen.getByRole("status")).toHaveTextContent("0 / 0");
    expect(screen.getByRole("button", { name: "下一个匹配" })).toBeDisabled();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("search")).not.toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
  });
});
