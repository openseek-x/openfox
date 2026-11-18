import { test, expect } from "@playwright/test";
import { createHash } from "node:crypto";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { mockArtifactReview } from "./artifactReviewFixture";

function fixture(rotation: number) {
  const stream =
    "BT /F1 18 Tf 30 150 Td (First selected line) Tj 0 -30 Td (Second selected line) Tj ET\n";
  const objects = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    `<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Rotate ${rotation} /Resources << /Font << /F1 5 0 R >> >> /Annots [6 0 R] /Contents 4 0 R >>`,
    `<< /Length ${stream.length} >>\nstream\n${stream}endstream`,
    "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    "<< /Type /Annot /Subtype /Text /Rect [260 160 280 180] /Contents (Original note - read only) /Name /Comment >>",
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
for (const rotation of [0, 90])
  test(`PDF text: ${rotation} degree source, multiline user-space quads, zoom and read-only annotations`, async ({
    page,
  }) => {
    const data = fixture(rotation);
    const state = await mockArtifactReview(
      page,
      {
        organization_id: "o1",
        id: "a1",
        content_id: "a1",
        content_revision: 1,
        head_artifact_id: "a1",
        run_id: "r1",
        name: "text.pdf",
        mime_type: "application/pdf",
        size_bytes: data.length,
        sha256: createHash("sha256").update(data).digest("hex"),
        state: "AVAILABLE",
        pinned: false,
        version: 1,
        created_at: "2026-11-17T00:00:00Z",
        retention_until: "2099-01-01T00:00:00Z",
      },
      data,
    );
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.goto("/o/o1/chat/c1?artifact=a1");
    const layer = page.locator(".openfox-pdf-text");
    await expect(layer.locator("span")).toHaveCount(2);
    await page.evaluate(() => document.fonts.ready);
    const canvas = page.locator("[data-pdf-page] canvas"),
      box = await canvas.boundingBox(),
      before = await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL());
    const select = async () =>
      layer.evaluate((el) => {
        const spans = el.querySelectorAll("span"),
          range = document.createRange();
        range.setStart(spans[0]!.firstChild!, 0);
        range.setEnd(spans[1]!.firstChild!, spans[1]!.textContent!.length);
        const sel = window.getSelection()!;
        sel.removeAllRanges();
        sel.addRange(range);
        el.dispatchEvent(new MouseEvent("mouseup", { bubbles: true }));
      });
    await select();
    await expect(
      page.getByRole("textbox", { name: "针对选区的修改意见" }),
    ).toHaveCount(0);
    await page.getByRole("button", { name: "原文件批注（只读）" }).click();
    await expect(page.getByRole("note")).toContainText(
      "Original note - read only",
    );
    await page.getByRole("note").getByRole("button").click();
    await page.getByRole("button", { name: "批注", exact: true }).click();
    expect(await canvas.boundingBox()).toEqual(box);
    expect(
      await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL()),
    ).toEqual(before);
    await select();
    await page
      .getByRole("textbox", { name: "针对选区的修改意见" })
      .fill("核对这两行");
    await expect(page.locator("svg polygon")).toHaveCount(2);
    if (rotation === 0 && process.env.OPENFOX_STAGE13_EVIDENCE) {
      await mkdir(process.env.OPENFOX_STAGE13_EVIDENCE, { recursive: true });
      await page.screenshot({
        path: join(process.env.OPENFOX_STAGE13_EVIDENCE, "pdf-text.jpg"),
      });
    }
    await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
    await expect(page.locator("svg polygon")).toHaveCount(2);
    const marks = await page
      .locator("svg polygon")
      .evaluateAll((els) => els.map((el) => el.getAttribute("points")));
    await page.getByLabel("缩放", { exact: true }).selectOption("1.5");
    expect(
      await page
        .locator("svg polygon")
        .evaluateAll((els) => els.map((el) => el.getAttribute("points"))),
    ).toEqual(marks);
    await page.getByRole("button", { name: "旋转 90°" }).click();
    await expect(
      page.getByRole("button", { name: "选择整页", exact: true }),
    ).toBeEnabled();
    await expect
      .poll(() =>
        page
          .locator("svg polygon")
          .evaluateAll((els) => els.map((el) => el.getAttribute("points"))),
      )
      .not.toEqual(marks);
    // Text spans must remain on the canvas after rotation and responsive scaling.
    const rect = await canvas.boundingBox();
    const spans = await layer.locator("span").evaluateAll((els) =>
      els.map((el) => {
        const r = el.getBoundingClientRect();
        return { x: r.x, y: r.y, w: r.width, h: r.height };
      }),
    );
    for (const r of spans) {
      expect(r.x).toBeGreaterThanOrEqual(rect!.x - 1);
      expect(r.y).toBeGreaterThanOrEqual(rect!.y - 1);
      expect(r.x + r.w).toBeLessThanOrEqual(rect!.x + rect!.width + 1);
      expect(r.y + r.h).toBeLessThanOrEqual(rect!.y + rect!.height + 1);
    }
    await page.getByRole("button", { name: "发送", exact: true }).click();
    await expect.poll(() => state.sent.length).toBe(1);
    const a = state.sent[0]!.file_contexts![0]!.comments[0]!.anchor;
    expect(a.kind).toBe("pdf_text");
    expect(a.page).toBe(1);
    expect(a.view_box).toEqual([0, 0, 300, 200]);
    expect(a.quads).toHaveLength(2);
    expect(a.quote).toContain("First selected line");
    expect(a.quote).toContain("Second selected line");
    for (const q of a.quads!) {
      for (let i = 0; i < 8; i++) {
        expect(q[i]).toBeGreaterThanOrEqual(0);
        expect(q[i]).toBeLessThanOrEqual(i % 2 === 0 ? 300 : 200);
      }
    }
    expect(errors).toEqual([]);
  });
