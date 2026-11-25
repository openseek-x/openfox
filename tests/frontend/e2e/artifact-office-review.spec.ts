import { readFileSync } from "node:fs";
import { expect, test } from "@playwright/test";
import type { OfficeView, RunArtifact } from "../../../packages/api/src";
import { mockArtifactReview } from "./artifactReviewFixture";

for (const [kind, mime] of Object.entries({
  xlsx: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
})) {
  test(`${kind}: real Office layout stays identical when annotation is toggled`, async ({
    page,
  }) => {
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const file: RunArtifact = {
      organization_id: "o1",
      id: "a1",
      content_id: "content-a1",
      content_revision: 1,
      head_artifact_id: "a1",
      run_id: "r1",
      name: `报告.${kind}`,
      mime_type: mime,
      size_bytes: 10000,
      sha256: "a".repeat(64),
      state: "AVAILABLE",
      pinned: false,
      version: 1,
      created_at: "2026-11-16T00:00:00Z",
      retention_until: "2099-01-01T00:00:00Z",
    };
    const root = new URL(
      `./fixtures/office-preview/sample.${kind}`,
      import.meta.url,
    );
    const view: OfficeView = {
      ...JSON.parse(readFileSync(`${root.pathname}.json`, "utf8")),
      sha256: file.sha256,
    };
    const pdf = readFileSync(`${root.pathname}.pdf`);
    const state = await mockArtifactReview(page, file, "unused binary");
    let conversions = 0;
    await page.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path.endsWith("/office")) await route.fulfill({ json: view });
      else if (path.endsWith("/office-preview")) {
        conversions++;
        await route.fulfill({ contentType: "application/pdf", body: pdf });
      } else await route.fallback();
    });
    await page.goto("/o/o1/chat/c1?artifact=a1");
    const toggle = page.getByRole("button", { name: "批注", exact: true });
    await expect(toggle).toHaveAttribute("aria-pressed", "false");
    const canvas = page.locator("[data-pdf-page] canvas");
    // 转换预览由独立的 PDF worker 绘制，冷启动时要等待第一页就绪。
    await expect(canvas).toHaveCount(1, { timeout: 20000 });
    // 等待 PDF 真正绘制出文字或色块，而不只验证 canvas 已创建。
    await expect
      .poll(async () =>
        canvas.evaluate((c: HTMLCanvasElement) => {
          const d = c
            .getContext("2d")!
            .getImageData(0, 0, c.width, c.height).data;
          let count = 0;
          for (let i = 0; i < d.length; i += 4)
            if (d[i + 3] && d[i]! < 180) count++;
          return count;
        }),
      )
      .toBeGreaterThan(100);
    const before = await canvas.evaluate((c: HTMLCanvasElement) =>
      c.toDataURL(),
    );
    const box = await canvas.boundingBox();
    await page.mouse.click(box!.x + 30, box!.y + 30);
    await expect(
      page.getByRole("textbox", { name: "针对选区的修改意见" }),
    ).toHaveCount(0);
    await toggle.click();
    await expect(
      page.getByRole("button", { name: "选择整页", exact: true }),
    ).toBeEnabled();
    expect(await canvas.boundingBox()).toEqual(box);
    expect(
      await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL()),
    ).toEqual(before);
    await page.getByRole("button", { name: "选择文字", exact: true }).click();
    const targetUnit = view.units.find(
      (u) => u.text === (kind === "xlsx" ? "12.5" : "Original text"),
    )!;
    const target = page.locator(`[data-office-target="${targetUnit.id}"]`);
    await expect(target).toBeVisible();
    const readonly = view.units.find((u) => !u.editable);
    if (readonly)
      await expect(
        page.locator(`[data-office-target="${readonly.id}"]`),
      ).toBeDisabled();
    await target.focus();
    await target.press("Enter");
    await page
      .getByRole("textbox", { name: "针对选区的修改意见" })
      .fill("调整这段内容，保留格式");
    await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
    await toggle.click();
    await expect(page.locator("[data-office-target]")).toHaveCount(0);
    expect(
      await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL()),
    ).toEqual(before);
    expect(await canvas.boundingBox()).toEqual(box);
    expect(conversions).toBe(1);
    await page.screenshot({
      path: test.info().outputPath(`${kind}-layout.png`),
    });
    await page.getByRole("button", { name: "发送", exact: true }).click();
    await expect.poll(() => state.sent.length).toBe(1);
    expect(state.sent[0]!.file_contexts![0]!.comments[0]!.anchor).toEqual({
      kind: "office",
      target: targetUnit.id,
      start: 0,
      end: 0,
      quote: targetUnit.text,
    });
    expect(errors).toEqual([]);
  });
}
