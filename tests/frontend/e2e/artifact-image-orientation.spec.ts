import { test, expect } from "@playwright/test";
import { createHash } from "node:crypto";
import { mockArtifactReview } from "./artifactReviewFixture";

test("image region uses the EXIF-oriented displayed image, including after zoom", async ({
  page,
}) => {
  const encoded = await page.evaluate(() => {
    const c = document.createElement("canvas");
    c.width = 80;
    c.height = 40;
    const x = c.getContext("2d")!;
    x.fillStyle = "red";
    x.fillRect(0, 0, 40, 40);
    x.fillStyle = "blue";
    x.fillRect(40, 0, 40, 40);
    return c.toDataURL("image/jpeg").split(",")[1]!;
  });
  const jpeg = Buffer.from(encoded, "base64");
  // APP1 Exif / little-endian TIFF / Orientation=6 (90 degrees clockwise).
  const exif = Buffer.from(
    "ffe1002245786966000049492a0008000000010012010300010000000600000000000000",
    "hex",
  );
  const data = Buffer.concat([jpeg.subarray(0, 2), exif, jpeg.subarray(2)]);
  const state = await mockArtifactReview(
    page,
    {
      organization_id: "o1",
      id: "a1",
      content_id: "a1",
      content_revision: 1,
      head_artifact_id: "a1",
      run_id: "r1",
      name: "oriented.jpg",
      mime_type: "image/jpeg",
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
  await page.goto("/o/o1/chat/c1?artifact=a1");
  const img = page.getByRole("img", { name: "oriented.jpg", exact: true });
  await expect
    .poll(() =>
      img.evaluate((e: HTMLImageElement) => [e.naturalWidth, e.naturalHeight]),
    )
    .toEqual([40, 80]);
  await page.getByLabel("缩放", { exact: true }).selectOption("0.5");
  const colors = await img.evaluate((e: HTMLImageElement) => {
    const c = document.createElement("canvas");
    c.width = 40;
    c.height = 80;
    const x = c.getContext("2d")!;
    x.drawImage(e, 0, 0);
    return [
      [...x.getImageData(20, 10, 1, 1).data],
      [...x.getImageData(20, 70, 1, 1).data],
    ];
  });
  expect(colors[0]![0]).toBeGreaterThan(240);
  expect(colors[1]![2]).toBeGreaterThan(240);
  await page.getByRole("button", { name: "批注", exact: true }).click();
  const b = (await img.boundingBox())!;
  await page.mouse.move(b.x + b.width * 0.1, b.y + b.height * 0.1);
  await page.mouse.down();
  await page.mouse.move(b.x + b.width * 0.4, b.y + b.height * 0.3);
  await page.mouse.up();
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill("顶部红色区域");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await page.getByLabel("缩放", { exact: true }).selectOption("1");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect.poll(() => state.sent.length).toBe(1);
  const a = state.sent[0]!.file_contexts![0]!.comments[0]!.anchor;
  expect(a.kind).toBe("region");
  expect(a.x).toBeCloseTo(0.1, 2);
  expect(a.y).toBeCloseTo(0.1, 2);
  expect(a.width).toBeCloseTo(0.3, 2);
  expect(a.height).toBeCloseTo(0.2, 2);
});
