import { test, expect, type Page } from "@playwright/test";
import type { selectCanvasRange } from "../../../packages/app/src/docs/editor/annotationSelection";
type Editor = Parameters<typeof selectCanvasRange>[0];
import { mkdir } from "node:fs/promises";
import { mockArtifactReview } from "./artifactReviewFixture";
async function fixture(page: Page) {
  const state = await mockArtifactReview(
    page,
    {
      organization_id: "o1",
      id: "a1",
      run_id: "r1",
      name: "notes.md",
      mime_type: "text/markdown",
      size_bytes: 10,
      sha256: "a".repeat(64),
      state: "AVAILABLE",
      pinned: false,
      version: 1,
      content_id: "a1",
      content_revision: 1,
      head_artifact_id: "a1",
      created_at: "2026-11-17T00:00:00Z",
    },
    "file",
  );
  let node = {
    id: "n1",
    space_id: "s1",
    kind: "doc",
    title: "工作区方案",
    body: "# 中文画布\n\n前🙂 **重复**\n\n第二段 **重复**",
    version: 1,
    can_edit: true,
    container: { kind: "personal" },
    parent_id: null,
  };
  const writes: { body: string; version: number }[] = [];
  let fail = false;
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/docs/n1")) {
      if (route.request().method() === "PATCH") {
        const input = route.request().postDataJSON();
        writes.push(input);
        if (fail) {
          await route.fulfill({
            status: 503,
            json: { error: { code: "FAILED", message: "offline" } },
          });
          return;
        }
        if (input.version !== node.version) {
          await route.fulfill({
            status: 409,
            json: { error: { code: "VERSION_CONFLICT", message: "changed" } },
          });
          return;
        }
        node = { ...node, body: input.body, version: node.version + 1 };
      }
      await route.fulfill({ json: node });
    } else if (path.endsWith("/docs/search"))
      await route.fulfill({ json: { nodes: [node] } });
    else if (path.endsWith("/docs/n1/revisions"))
      await route.fulfill({
        json: {
          revisions: [
            {
              revision: 1,
              title: node.title,
              created_at: "2026-11-17T00:00:00Z",
              created_by_name: "QA",
            },
          ],
          next_before: null,
        },
      });
    else if (path.endsWith("/docs/n1/revisions/1"))
      await route.fulfill({
        json: { revision: 1, title: node.title, body: "历史正文" },
      });
    else await route.fallback();
  });
  return {
    state,
    writes,
    node: () => node,
    fail: (value: boolean) => {
      fail = value;
    },
    remote: (body: string) => {
      node = { ...node, body, version: node.version + 1 };
    },
  };
}
async function pick(page: Page, last = true) {
  await page.locator(".doc-body").evaluate((el, last) => {
    const editor = (el as HTMLElement & { editor: Editor }).editor;
    const matches: number[] = [];
    editor.state.doc.descendants((node, pos) => {
      if (node.isText && node.text?.includes("重复"))
        matches.push(pos + node.text.indexOf("重复"));
    });
    const from = last ? matches.at(-1)! : matches[0]!;
    editor.commands.setTextSelection({ from, to: from + 2 });
    el.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
  }, last);
}
test("canvas keeps layout, flushes exact edited Markdown before annotating and opens history on demand", async ({
  page,
}) => {
  const f = await fixture(page);
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/o/o1/chat/c1?artifact=new");
  await page.getByRole("textbox", { name: "搜索本空间文档" }).fill("方案");
  await page.getByRole("button", { name: "工作区方案", exact: true }).click();
  const body = page.locator(".doc-body");
  await expect(body).toContainText("中文画布");
  expect(f.writes).toHaveLength(0);
  await pick(page);
  await expect(
    page.getByRole("textbox", { name: "针对选区的修改意见" }),
  ).toHaveCount(0);
  await body.evaluate((el) =>
    (el as HTMLElement & { editor: Editor }).editor.commands.insertContentAt(
      1,
      "已编辑🙂 ",
    ),
  );
  const geometry = await body.boundingBox();
  await page.getByRole("button", { name: "批注", exact: true }).click();
  expect(await body.boundingBox()).toEqual(geometry);
  await pick(page);
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill("只改第二处重复文字");
  if (process.env.OPENFOX_STAGE14_EVIDENCE) {
    await mkdir(process.env.OPENFOX_STAGE14_EVIDENCE, { recursive: true });
    await page.screenshot({
      path: process.env.OPENFOX_STAGE14_EVIDENCE + "/canvas.jpg",
    });
  }
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await expect(
    page.getByRole("textbox", { name: "针对选区的修改意见" }),
  ).toHaveCount(0);
  expect(f.writes.length).toBeGreaterThan(0);
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect.poll(() => f.state.sent.length).toBe(1);
  const source = f.state.sent[0]!.file_contexts![0]!;
  expect(source.node_id).toBe("n1");
  expect(source.node_version).toBe(f.node().version);
  const a = source.comments[0]!.anchor;
  const raw = Buffer.from(f.node().body);
  expect(raw.subarray(a.start, a.end).toString()).toBe("重复");
  expect(a.start).toBe(
    Buffer.byteLength(
      f.node().body.slice(0, f.node().body.lastIndexOf("重复")),
    ),
  );
  await page.getByRole("button", { name: "历史版本", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "历史版本" })).toBeVisible();
  await expect(page.getByRole("dialog")).toContainText("历史正文");
  expect(errors).toEqual([]);
});
test("canvas restores failed edits after reload and preserves local text on remote conflict", async ({
  page,
}) => {
  const f = await fixture(page);
  f.fail(true);
  await page.goto("/o/o1/chat/c1?artifact=doc:s1:n1");
  const body = page.locator(".doc-body");
  await expect(body).toContainText("中文画布");
  await body.evaluate((el) =>
    (el as HTMLElement & { editor: Editor }).editor.commands.insertContentAt(
      1,
      "断网草稿 ",
    ),
  );
  await page.getByRole("button", { name: "保存文档" }).click();
  await expect(page.getByRole("region", { name: "文档画布" })).toContainText(
    "保存失败",
  );
  await page.reload();
  await expect(body).toContainText("断网草稿");
  f.remote("远端新正文");
  f.fail(false);
  await page.getByRole("button", { name: "保存文档" }).click();
  await expect(page.getByLabel("远端正文")).toHaveValue("远端新正文");
  await expect(body).toContainText("断网草稿");
  expect(f.node().body).toBe("远端新正文");
});

test("canvas does not interrupt composition or submit a selection whose text changed", async ({
  page,
}) => {
  const f = await fixture(page);
  await page.goto("/o/o1/chat/c1?artifact=doc:s1:n1");
  const body = page.locator(".doc-body");
  await expect(body).toContainText("中文画布");
  await page.getByRole("button", { name: "批注", exact: true }).click();
  await body.focus();
  await body.dispatchEvent("compositionstart", { data: "中" });
  await pick(page);
  const input = page.getByRole("textbox", { name: "针对选区的修改意见" });
  await expect(input).toHaveCount(0);
  await body.dispatchEvent("compositionend", { data: "中" });
  await expect
    .poll(() =>
      body.evaluate(
        (el) => (el as HTMLElement & { editor: Editor }).editor.view.composing,
      ),
    )
    .toBe(false);
  await pick(page);
  await input.fill("保留这段意见");
  await body.evaluate((el) => {
    const editor = (el as HTMLElement & { editor: Editor }).editor;
    const from = editor.state.selection.from;
    editor.commands.insertContentAt(from + 1, "改变");
  });
  await expect(
    page.getByRole("dialog", { name: "针对选区的修改意见" }),
  ).toContainText("请重新选择");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await expect(input).toHaveValue("保留这段意见");
  expect(f.state.sent).toHaveLength(0);
  await expect(
    page.getByRole("button", { name: "发送", exact: true }),
  ).toBeDisabled();
});
