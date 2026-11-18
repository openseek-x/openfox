import { expect, test } from "@playwright/test";
import type { RunArtifact } from "../../../packages/api/src";
import { mockArtifactReview } from "./artifactReviewFixture";

for (const [name, mime, text, selector] of [
  ["data.csv", "text/csv", "name,value\nA,中文", "table"],
  ["notes.txt", "text/plain", "普通文本 中文", "pre[data-language]"],
  ["notes.md", "text/markdown", "# 标题\n\n**中文** 内容", ".chat-markdown"],
  [
    "page.html",
    "text/plain",
    '<!doctype html><html><body style="background:#314159;color:white"><h1>标题</h1><p>中文内容</p></body></html>',
    "iframe",
  ],
]) {
  test(`${name}: annotation mode preserves the same content and DOM`, async ({
    page,
  }) => {
    const file: RunArtifact = {
      organization_id: "o1",
      id: "a1",
      content_id: "content-a1",
      content_revision: 1,
      head_artifact_id: "a1",
      run_id: "r1",
      name: name!,
      mime_type: mime!,
      size_bytes: 100,
      sha256: "a".repeat(64),
      state: "AVAILABLE",
      pinned: false,
      version: 1,
      created_at: "2026-11-16T00:00:00Z",
      retention_until: "2099-01-01T00:00:00Z",
    };
    await mockArtifactReview(page, file, text!);
    await page.goto("/o/o1/chat/c1?artifact=a1");
    const panel = page.getByRole("region", { name: name!, exact: true });
    const content =
      selector === ".chat-markdown"
        ? panel.getByRole("heading")
        : panel.locator(selector!);
    await expect(content).toBeVisible();
    const toggle = page.getByRole("button", { name: "批注", exact: true });
    await expect(toggle).toHaveAttribute("aria-pressed", "false");
    await content.evaluate((el) =>
      el.setAttribute("data-test-instance", "same"),
    );
    if (selector === "iframe")
      await content.contentFrame().locator("h1").waitFor();
    const before = await content.screenshot();
    const bounds = await content.boundingBox();
    if (selector === "table")
      await panel
        .getByRole("button", { name: "第 2 行，第 2 列" })
        .click({ force: true });
    else if (selector !== "iframe")
      await content.evaluate((el) => {
        const range = document.createRange();
        range.selectNodeContents(el);
        getSelection()!.removeAllRanges();
        getSelection()!.addRange(range);
        el.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
      });
    await expect(
      page.getByRole("textbox", { name: "针对选区的修改意见" }),
    ).toHaveCount(0);
    await page.evaluate(() => getSelection()?.removeAllRanges());
    await toggle.click();
    await expect(content).toHaveAttribute("data-test-instance", "same");
    expect(await content.boundingBox()).toEqual(bounds);
    expect(await content.screenshot()).toEqual(before);
    await toggle.click();
    expect(await content.screenshot()).toEqual(before);
  });
}
