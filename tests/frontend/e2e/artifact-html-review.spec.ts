import { expect, test } from "@playwright/test";
import type { RunArtifact } from "../../../packages/api/src";
import { mockArtifactReview } from "./artifactReviewFixture";

const quote = '<h1 id="target" data-openfox-review="e999">原文 &amp; 🙂</h1>';
const replacement = '<h1 id="target">新标题 &amp; 🙂</h1>';
const source =
  '\ufeff<!doctype html><html><head><style>h1{color:rgb(10,20,30)}body{background-image:url(https://blocked.invalid/css)}</style><link rel="stylesheet" href="https://blocked.invalid/style"><meta http-equiv="refresh" content="0;url=https://blocked.invalid/nav"><script>parent.postMessage({pwn:true},"*");fetch("https://blocked.invalid/js")</script></head><body>前🙂\r\n' +
  quote +
  '\r\n<p>保持不变</p><img src="https://blocked.invalid/image" onerror="parent.postMessage({pwn:true},\'*\')"><a href="https://blocked.invalid/link" target="_top">外部链接</a><iframe src="https://blocked.invalid/frame"></iframe><form action="https://blocked.invalid/form"><button>提交</button></form></body></html>';
const file: RunArtifact = {
  organization_id: "o1",
  id: "a1",
  content_id: "content-a1",
  content_revision: 1,
  head_artifact_id: "a1",
  run_id: "r1",
  name: "page.html",
  mime_type: "text/plain",
  size_bytes: Buffer.byteLength(source),
  sha256: "a".repeat(64),
  state: "AVAILABLE",
  pinned: false,
  version: 1,
  created_at: "2026-11-16T00:00:00Z",
  retention_until: "2099-01-01T00:00:00Z",
};

test("HTML element comments map original bytes and update directly through ordinary chat in opaque frames", async ({
  page,
}) => {
  const outbound: string[] = [],
    errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route(/https?:\/\/blocked\.invalid\//, async (route) => {
    outbound.push(route.request().url());
    await route.fulfill({ body: "blocked" });
  });
  await page.addInitScript(() => {
    window.addEventListener("message", (event) => {
      if (event.data?.pwn) document.documentElement.dataset.pwn = "true";
    });
  });
  const state = await mockArtifactReview(page, file, source);
  const updated = {
    ...file,
    id: "a2",
    content_revision: 2,
    head_artifact_id: "a2",
    run_id: "r2",
    sha256: "b".repeat(64),
    replaces_artifact_id: file.id,
  };
  state.onSend = () => {
    state.artifacts = [updated, { ...file, head_artifact_id: updated.id }];
  };
  await page.route("**/api/**", async (route) => {
    if (
      new URL(route.request().url()).pathname.endsWith("/runs/r2/artifacts/a2")
    )
      await route.fulfill({
        contentType: "text/plain",
        body: source.replace(quote, replacement),
      });
    else await route.fallback();
  });
  await page.goto("/o/o1/chat/c1?artifact=current:content-a1");
  await page.getByRole("button", { name: "批注", exact: true }).click();
  const first = page.locator("iframe").first();
  await expect(
    first.contentFrame().getByRole("heading", { name: "原文 & 🙂" }),
  ).toBeVisible();
  await expect(first).toHaveAttribute("sandbox", "allow-scripts");
  const heading = first
    .contentFrame()
    .getByRole("heading", { name: "原文 & 🙂" });
  await expect(heading).toHaveCSS("color", "rgb(10, 20, 30)");
  await expect(first.contentFrame().locator("script")).toHaveCount(1);
  const blocked = await heading.evaluate(() => {
    try {
      void parent.document.body;
      return false;
    } catch {
      return true;
    }
  });
  expect(blocked).toBe(true);
  // Host-origin and stale/forged messages must not create a selection.
  await page.evaluate(() =>
    window.postMessage(
      { type: "openfox-html-select", session: "forged", id: "e999" },
      "*",
    ),
  );
  await expect(
    page.getByRole("textbox", { name: "针对选区的修改意见" }),
  ).toHaveCount(0);
  await heading.focus();
  await heading.press("Enter");
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill("将标题改为新标题，保留其他内容");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  expect(state.sent).toHaveLength(0);
  await page.screenshot({ path: test.info().outputPath("html-review.png") });
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect.poll(() => state.sent.length).toBe(1);
  expect(state.sent[0]!.file_contexts![0]!.comments[0]!.anchor).toEqual({
    kind: "text",
    quote,
    start: Buffer.byteLength(source.slice(0, source.indexOf(quote))),
    end: Buffer.byteLength(
      source.slice(0, source.indexOf(quote) + quote.length),
    ),
  });
  await expect(
    first.contentFrame().getByRole("heading", { name: "新标题 & 🙂" }),
  ).toBeVisible();
  await expect(page.getByText(/当前版本|候选版|采纳/)).toHaveCount(0);
  expect(outbound).toEqual([]);
  expect(await page.locator("html").getAttribute("data-pwn")).toBeNull();
  expect(errors).toEqual([]);
});
