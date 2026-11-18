import { expect, test, type Page } from "@playwright/test";
import type { RunArtifact } from "../../../packages/api/src";
import { mockArtifactReview } from "./artifactReviewFixture";

// Deliberately tiny but real PDF: the browser must parse it and render in its
// actual worker. API requests are mocked; this test does not contact an AI.
function pdfFixture() {
  const stream = "0.1 0.4 0.8 rg 40 50 100 70 re f\n";
  const objects = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>",
    "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Resources << >> /Contents 4 0 R >>",
    `<< /Length ${stream.length} >>\nstream\n${stream}endstream`,
    "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 300] /Rotate 90 /Resources << >> /Contents 4 0 R >>",
  ];
  let text = "%PDF-1.7\n";
  const offsets = [0];
  objects.forEach((o, i) => {
    offsets.push(Buffer.byteLength(text));
    text += `${i + 1} 0 obj\n${o}\nendobj\n`;
  });
  const xref = Buffer.byteLength(text);
  text += `xref\n0 ${objects.length + 1}\n0000000000 65535 f \n`;
  for (const n of offsets.slice(1))
    text += `${String(n).padStart(10, "0")} 00000 n \n`;
  return Buffer.from(
    text +
      `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`,
  );
}
const png = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a8ZkAAAAASUVORK5CYII=",
  "base64",
);
async function mock(page: Page, pdf: boolean) {
  const data = pdf ? pdfFixture() : png;
  const file: RunArtifact = {
    organization_id: "o1",
    id: "a1",
    content_id: "content-a1",
    content_revision: 1,
    head_artifact_id: "a1",
    run_id: "r1",
    name: pdf ? "layout.pdf" : "image.png",
    mime_type: pdf ? "application/pdf" : "image/png",
    size_bytes: data.length,
    sha256: "a".repeat(64),
    state: "AVAILABLE",
    pinned: false,
    version: 1,
    created_at: "2026-11-16T00:00:00Z",
    retention_until: "2099-01-01T00:00:00Z",
  };
  return mockArtifactReview(page, file, data);
}

for (const pdf of [true, false])
  test(`${pdf ? "PDF worker" : "image"}: region feedback stays in the draft through zoom and navigation`, async ({
    page,
  }) => {
    const state = await mock(page, pdf);
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.goto("/o/o1/chat/c1?artifact=a1");
    await page.getByRole("button", { name: "批注", exact: true }).click();
    await page.evaluate(() => document.fonts.ready);
    const whole = page.getByRole("button", { name: "选择整页", exact: true });
    await expect(whole).toBeEnabled();
    if (pdf) {
      await expect(page.locator("[data-pdf-page] canvas")).toHaveCount(1);
      const painted = await page
        .locator("[data-pdf-page] canvas")
        .evaluate((c: HTMLCanvasElement) => {
          const bytes = c
            .getContext("2d")!
            .getImageData(0, 0, c.width, c.height).data;
          for (let i = 0; i < bytes.length; i += 4)
            if (bytes[i]! < 180 && bytes[i + 2]! > 180) return true;
          return false;
        });
      expect(painted).toBe(true);
    }
    const visual = pdf
      ? page.locator("[data-pdf-page] canvas")
      : page.locator('img[alt="image.png"]');
    const before = pdf
      ? await visual.evaluate((c: HTMLCanvasElement) => c.toDataURL())
      : await visual.screenshot();
    const box = (await visual.boundingBox())!;
    const toggle = page.getByRole("button", { name: "批注", exact: true });
    await toggle.click();
    expect(await visual.boundingBox()).toEqual(box);
    if (pdf)
      expect(
        await visual.evaluate((c: HTMLCanvasElement) => c.toDataURL()),
      ).toBe(before);
    else
      expect((await visual.screenshot()).equals(before as Buffer)).toBe(true);
    await page.mouse.move(box.x + box.width * 0.1, box.y + box.height * 0.2);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width * 0.4, box.y + box.height * 0.6);
    await page.mouse.up();
    await expect(
      page.getByRole("textbox", { name: "针对选区的修改意见" }),
    ).toHaveCount(0);
    await toggle.click();
    if (pdf)
      await page.getByRole("button", { name: "选择区域", exact: true }).click();
    await page.mouse.move(box.x + box.width * 0.1, box.y + box.height * 0.2);
    await page.mouse.down();
    await page.mouse.move(box.x + box.width * 0.4, box.y + box.height * 0.6);
    await page.mouse.up();
    await page
      .getByRole("textbox", { name: "针对选区的修改意见", exact: true })
      .fill("这里需要更突出");
    await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
    expect(state.sent).toHaveLength(0);
    if (pdf) {
      await page.getByRole("button", { name: "旋转 90°" }).click();
      await expect(whole).toBeEnabled();
      await page.getByRole("button", { name: "下一页", exact: true }).click();
      await expect(whole).toBeEnabled();
      await page.getByRole("button", { name: "上一页", exact: true }).click();
      await expect(whole).toBeEnabled();
    }
    await page.getByLabel("缩放", { exact: true }).selectOption("1.5");
    await page
      .getByLabel("文件批注", { exact: true })
      .locator("summary")
      .click();
    await expect(
      page.getByText("这里需要更突出", { exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "发送", exact: true }).click();
    await expect.poll(() => state.sent.length).toBe(1);
    const anchor = state.sent[0]!.file_contexts![0]!.comments[0]!.anchor;
    expect(anchor).toMatchObject({ kind: "region", page: 1 });
    expect(anchor.x).toBeCloseTo(0.1, 2);
    expect(errors).toEqual([]);
  });
