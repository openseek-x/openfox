import { expect, test, type Locator, type Page } from "@playwright/test";
import { mockArtifactReview } from "./artifactReviewFixture";

async function dragText(page: Page, part: Locator) {
  const points = await part.evaluate((el) => {
    const range = document.createRange();
    range.selectNodeContents(el);
    const box = range.getBoundingClientRect();
    return { x: box.x, y: box.y + box.height / 2, end: box.right };
  });
  await page.mouse.move(points.x, points.y);
  await page.mouse.down();
  await page.mouse.move(points.end, points.y, { steps: 8 });
  await page.mouse.up();
}

for (const newline of ["\n", "\r\n"]) {
  test(`rendered Markdown selection opens a focused nearby popover (${JSON.stringify(newline)})`, async ({
    page,
  }) => {
    const source = [
      "---",
      "title: 验收🙂",
      "---",
      "# 报告",
      "",
      "原文",
      "",
      "前🙂**原文**后",
      "",
      "末尾保持。",
    ].join(newline);
    const state = await mockArtifactReview(
      page,
      {
        organization_id: "o1",
        id: "a1",
        content_id: "content-a1",
        content_revision: 1,
        head_artifact_id: "a1",
        run_id: "r1",
        name: "报告.md",
        mime_type: "text/markdown",
        size_bytes: Buffer.byteLength(source),
        sha256: "a".repeat(64),
        state: "AVAILABLE",
        pinned: false,
        version: 1,
        created_at: "2026-11-16T00:00:00Z",
        retention_until: "2099-01-01T00:00:00Z",
      },
      source,
    );
    await page.goto("/o/o1/chat/c1?artifact=a1");
    await page.getByRole("button", { name: "批注", exact: true }).click();
    const part = page.locator("strong [data-comment-start]");
    await expect(part).toHaveText("原文");
    await expect(
      page.getByRole("button", { name: "批注修改", exact: true }),
    ).toHaveCount(0);
    await dragText(page, part);
    const dialog = page.getByRole("dialog", { name: "针对选区的修改意见" });
    const input = dialog.getByRole("textbox");
    await expect(input).toBeFocused();
    const box = (await dialog.boundingBox())!,
      selected = (await part.boundingBox())!;
    expect(Math.abs(box.y - selected.y)).toBeLessThan(280);
    expect(box.x + box.width).toBeLessThanOrEqual(1440);
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    expect(state.sent).toHaveLength(0);
    await dragText(page, part);
    await expect(input).toBeFocused();
    await page.keyboard.type("修改第二处原文");
    await page.screenshot({
      path: test.info().outputPath("selection-popover.png"),
    });
    await page.keyboard.press("ControlOrMeta+Enter");
    await expect(dialog).toHaveCount(0);
    expect(state.sent).toHaveLength(0);
    await page.getByRole("button", { name: "发送", exact: true }).click();
    await expect.poll(() => state.sent.length).toBe(1);
    const start = source.lastIndexOf("原文");
    expect(state.sent[0]!.file_contexts![0]!.comments[0]!.anchor).toEqual({
      kind: "text",
      start: Buffer.byteLength(source.slice(0, start)),
      end: Buffer.byteLength(source.slice(0, start + 2)),
      quote: "原文",
    });
  });
}

test("HTML partial text selection works under the deployed CSP, including entities", async ({
  page,
}) => {
  // Use a public-origin fixture: opaque frames cannot request loopback assets
  // under Chromium's Local Network Access policy. The actual deployment is public HTTPS.
  const baseURL = test.info().project.use.baseURL!;
  await page.route("https://openfox.test/**", async (route) => {
    const url = new URL(route.request().url());
    const response = await route.fetch({
      url: baseURL + url.pathname + url.search,
    });
    await route.fulfill({ response });
  });
  const source =
    "<!doctype html><html><body><p>重复原文</p><p>前🙂<strong>原文 &amp; 中文</strong>后文</p></body></html>";
  const state = await mockArtifactReview(
    page,
    {
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
    },
    source,
  );
  await page.route("**/o/o1/chat/c1?artifact=a1", async (route) => {
    const response = await route.fetch({
      url: baseURL + "/o/o1/chat/c1?artifact=a1",
    });
    await route.fulfill({
      response,
      headers: {
        ...response.headers(),
        "Content-Security-Policy":
          "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'",
      },
    });
  });
  await page.goto("https://openfox.test/o/o1/chat/c1?artifact=a1");
  await page.getByRole("button", { name: "批注", exact: true }).click();
  const iframe = page.locator("iframe");
  const part = iframe.contentFrame().locator("strong");
  await expect(part).toHaveText("原文 & 中文");
  // Locator coordinates are in the parent viewport, including iframe placement.
  const box = (await part.boundingBox())!;
  await page.mouse.move(box.x + 1, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width - 1, box.y + box.height / 2, {
    steps: 8,
  });
  await page.mouse.up();
  const input = page.getByRole("textbox", { name: "针对选区的修改意见" });
  await expect(input).toBeFocused();
  await input.fill("精简所选文字");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect.poll(() => state.sent.length).toBe(1);
  const quote = "原文 &amp; 中文",
    start = source.indexOf(quote);
  expect(state.sent[0]!.file_contexts![0]!.comments[0]!.anchor).toEqual({
    kind: "text",
    quote,
    start: Buffer.byteLength(source.slice(0, start)),
    end: Buffer.byteLength(source.slice(0, start + quote.length)),
  });
});
