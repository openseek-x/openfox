import { createServer, type Server } from "node:http";
import { readFile } from "node:fs/promises";
import { readFileSync, mkdtempSync, rmSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { extname, resolve, sep } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import type { OfficeView, RunArtifact } from "../../../packages/api/src";
import { mockArtifactReview } from "./artifactReviewFixture";

// Use the built app and the actual deployment CSP: dev mode would hide worker,
// chunk URL, font asset, and WASM policy failures.
let server: Server;
let origin: string;
let candidates: string;
let downloadBytes: Buffer;
test.beforeAll(async () => {
  candidates = mkdtempSync(resolve(tmpdir(), "openfox-office-roundtrip-"));
  execFileSync(
    "go",
    ["run", "./tests/frontend/office-fidelity/roundtrip", candidates],
    { timeout: 30000 },
  );
  const root = resolve("apps/web/dist");
  const csp = readFileSync("deploy/nginx-web.example.conf", "utf8").match(
    /Content-Security-Policy "([^"]+)"/,
  )![1]!;
  server = createServer(async (req, res) => {
    try {
      const pathname = decodeURIComponent(
        new URL(req.url!, "http://test").pathname,
      );
      // Chromium downloads bypass page.route; serve the fixture at the real API path.
      if (pathname === "/api/v1/organizations/o1/runs/r1/artifacts/a1") {
        res.setHeader("Content-Type", "application/octet-stream");
        res.setHeader("Content-Disposition", "attachment");
        res.end(downloadBytes);
        return;
      }
      const file = resolve(root, pathname.replace(/^\//, ""));
      if (!file.startsWith(root + sep)) throw Error("Not a static asset");
      const target = extname(file) ? file : resolve(root, "index.html");
      const mime: Record<string, string> = {
        ".html": "text/html",
        ".js": "text/javascript",
        ".mjs": "text/javascript",
        ".css": "text/css",
        ".wasm": "application/wasm",
        ".otf": "font/otf",
        ".ttf": "font/ttf",
        ".svg": "image/svg+xml",
      };
      res.setHeader(
        "Content-Type",
        mime[extname(target)] ?? "application/octet-stream",
      );
      res.setHeader("Content-Security-Policy", csp);
      res.end(await readFile(target));
    } catch {
      res.writeHead(404);
      res.end();
    }
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  origin = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
});
test.afterAll(async () => {
  if (candidates) rmSync(candidates, { recursive: true, force: true });
  await new Promise<void>((resolve, reject) =>
    server.close((e) => (e ? reject(e) : resolve())),
  );
});

async function setup(
  page: Page,
  corrupt = false,
  kind: "docx" | "pptx" | "xlsx" = "docx",
  fixture?: string,
) {
  if (kind === "xlsx")
    await page.addInitScript(() => {
      const Original = window.Worker;
      window.Worker = class extends Original {
        constructor(url: string | URL, options?: WorkerOptions) {
          super(url, options);
          this.addEventListener("message", (event) => {
            if (event.data.kind === "sheet") {
              const { bitmap, ...frame } = event.data;
              (window as unknown as { sheetFrame: unknown }).sheetFrame = frame;
            }
          });
        }
      };
    });
  const raw = readFileSync(
    fixture ??
      `tests/frontend/e2e/fixtures/office-preview/${kind === "pptx" ? "styled.pptx" : `complex.${kind}`}`,
  );
  downloadBytes = raw;
  const view: OfficeView = JSON.parse(
    readFileSync(
      fixture
        ? `${fixture}.json`
        : `tests/frontend/e2e/fixtures/office-preview/${kind === "pptx" ? "styled.pptx" : `complex.${kind}`}.json`,
      "utf8",
    ),
  );
  const file: RunArtifact = {
    organization_id: "o1",
    id: "a1",
    content_id: "content-a1",
    content_revision: 1,
    head_artifact_id: "a1",
    run_id: "r1",
    name: `复杂中文报告.${kind}`,
    mime_type:
      kind === "docx"
        ? "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
        : kind === "xlsx"
          ? "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
          : "application/vnd.openxmlformats-officedocument.presentationml.presentation",
    size_bytes: raw.length,
    sha256: view.sha256,
    state: "AVAILABLE",
    pinned: false,
    version: 1,
    created_at: "2026-11-17T00:00:00Z",
    retention_until: "2099-01-01T00:00:00Z",
  };
  const state = await mockArtifactReview(
    page,
    file,
    corrupt ? Buffer.from("wrong file") : raw,
  );
  let conversions = 0;
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/office")) await route.fulfill({ json: view });
    else if (
      path.endsWith("/office-preview") &&
      new URL(route.request().url()).searchParams.get("format") === "xlsx"
    ) {
      await route.fulfill({
        contentType:
          "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        body: corrupt
          ? Buffer.from("bad")
          : readFileSync(
              fixture
                ? `${fixture}.render.xlsx`
                : "tests/frontend/e2e/fixtures/office-preview/complex.xlsx.render.xlsx",
            ),
      });
    } else if (path.endsWith("/office-preview")) {
      conversions++;
      await route.fulfill({
        contentType: "application/pdf",
        body: readFileSync(
          "tests/frontend/e2e/fixtures/office-preview/sample.docx.pdf",
        ),
      });
    } else await route.fallback();
  });
  await page.goto(`${origin}/o/o1/chat/c1?artifact=a1`);
  const previewCanvas = page
    .getByRole("region", { name: file.name, exact: true })
    .locator("canvas");
  await expect(previewCanvas).toHaveCount(1);
  if (kind === "xlsx" && !corrupt) {
    await expect(
      page.getByRole("button", { name: "选择单元格", exact: true }),
    ).toBeDisabled();
    await expect(
      page.getByRole("tab", { name: "第二张表", exact: true }),
    ).toBeEnabled();
  } else
    await expect
      .poll(() => previewCanvas.evaluate((c: HTMLCanvasElement) => c.width))
      .toBeGreaterThan(800);
  return { view, state, conversions: () => conversions };
}

async function drag(page: Page, target: string, start = 0, end?: number) {
  const spans = page.locator(`[data-office-target="${target}"]`);
  const first = await spans.nth(start).boundingBox();
  const last = await spans
    .nth((end ?? (await spans.count())) - 1)
    .boundingBox();
  expect(first).not.toBeNull();
  expect(last).not.toBeNull();
  await page.mouse.move(first!.x + 0.5, first!.y + first!.height / 2);
  await page.mouse.down();
  await page.mouse.move(
    last!.x + last!.width - 0.5,
    last!.y + last!.height / 2,
    { steps: 12 },
  );
  await page.mouse.up();
}

test("Word: native source selection preserves canvas and sends distinct duplicate-text anchors", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const { view, state, conversions } = await setup(page);
  const toggle = page.getByRole("button", { name: "批注", exact: true });
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await expect(page.locator("[data-office-target]")).toHaveCount(0);
  const canvas = page.locator("canvas");
  const before = await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL());
  const box = await canvas.boundingBox();
  await toggle.click();
  const duplicates = view.units.filter((u) => u.text === "重复文本");
  expect(duplicates).toHaveLength(3);
  await expect(
    page.locator(`[data-office-target="${duplicates[0]!.id}"]`),
  ).toHaveCount(4);
  expect(await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL())).toBe(
    before,
  );
  expect(await canvas.boundingBox()).toEqual(box);
  await drag(page, duplicates[0]!.id);
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill("改第一处，保留斜体");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await drag(page, duplicates[1]!.id);
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill("改表格里的同名文字");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  const title = view.units.find((u) => u.text === "中文报告 · 协作进展")!;
  await drag(page, title.id, 0, 2);
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill("只改这两个字");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await toggle.click();
  await expect(page.locator("[data-office-target]")).toHaveCount(0);
  expect(await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL())).toBe(
    before,
  );
  expect(await canvas.boundingBox()).toEqual(box);
  await page.screenshot({ path: test.info().outputPath("word-native.png") });
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect.poll(() => state.sent.length).toBe(1);
  const comments = state.sent[0]!.file_contexts![0]!.comments;
  expect(comments.map((c) => c.anchor)).toEqual([
    {
      kind: "office",
      target: duplicates[0]!.id,
      start: 0,
      end: 12,
      quote: "重复文本",
    },
    {
      kind: "office",
      target: duplicates[1]!.id,
      start: 0,
      end: 12,
      quote: "重复文本",
    },
    { kind: "office", target: title.id, start: 0, end: 6, quote: "中文" },
  ]);
  expect(conversions()).toBe(0);
  expect(errors).toEqual([]);
});

test("Word: source mismatch falls back to PDF and cannot offer exact text anchors", async ({
  page,
}) => {
  const { conversions } = await setup(page, true);
  await expect.poll(conversions).toBe(1);
  await page.getByRole("button", { name: "批注", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "选择整页", exact: true }),
  ).toBeEnabled();
  await expect(page.locator("[data-office-target]")).toHaveCount(0);
});

test("Word: page two, keyboard selection, zoom, region feedback and worker cleanup", async ({
  page,
}) => {
  const active = new Set();
  page.on("worker", (worker) => {
    active.add(worker);
    worker.on("close", () => active.delete(worker));
  });
  const { view, state } = await setup(page);
  await page.getByRole("button", { name: "批注", exact: true }).click();
  await page.getByRole("button", { name: "下一页", exact: true }).click();
  const target = view.units.filter((u) => u.text === "重复文本")[2]!;
  await expect(page.locator(`[data-office-target="${target.id}"]`)).toHaveCount(
    4,
  );
  const canvas = page.locator("canvas");
  const before = await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL());
  await page.getByRole("combobox", { name: "缩放" }).selectOption("1");
  expect((await canvas.boundingBox())!.width).toBe(816);
  expect(await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL())).toBe(
    before,
  );
  await page
    .getByRole("button", { name: "批注 重复文本", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill("修改第二页");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await page.getByRole("combobox", { name: "缩放" }).selectOption("fit");
  await page.getByRole("button", { name: "区域", exact: true }).click();
  const box = (await canvas.boundingBox())!;
  await page.mouse.move(box.x + box.width * 0.15, box.y + box.height * 0.15);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width * 0.55, box.y + box.height * 0.25, {
    steps: 10,
  });
  await page.mouse.up();
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill("调整这个区域");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect.poll(() => state.sent.length).toBe(1);
  const comments = state.sent[0]!.file_contexts![0]!.comments;
  expect(comments[0]!.anchor).toEqual({
    kind: "office",
    target: target.id,
    start: 0,
    end: 12,
    quote: "重复文本",
  });
  expect(comments[1]!.anchor).toMatchObject({
    kind: "region",
    page: 2,
    quote: "",
    start: 0,
    end: 0,
  });
  expect(comments[1]!.anchor.width).toBeCloseTo(0.4, 2);
  expect(comments[1]!.anchor.height).toBeCloseTo(0.1, 2);
  expect(active.size).toBeGreaterThan(0);
  await page.goto("about:blank");
  await expect.poll(() => active.size).toBe(0);
});

test("PPT: native slide keeps layout while selecting source text and chart objects", async ({
  page,
}) => {
  const { view, state, conversions } = await setup(page, false, "pptx");
  const canvas = page.locator("canvas");
  const toggle = page.getByRole("button", { name: "批注", exact: true });
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await expect(page.locator("[data-office-target]")).toHaveCount(0);
  const before = await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL());
  const box = await canvas.boundingBox();
  expect(
    await canvas.evaluate((c: HTMLCanvasElement) => [
      ...c.getContext("2d")!.getImageData(2, 2, 1, 1).data,
    ]),
  ).toEqual([19, 36, 58, 255]);
  await toggle.click();
  const title = view.units[0]!;
  await expect(page.locator(`[data-office-target="${title.id}"]`)).toHaveCount(
    [...title.text].length,
  );
  expect(await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL())).toBe(
    before,
  );
  expect(await canvas.boundingBox()).toEqual(box);
  await drag(page, title.id, 0, 2);
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill("调整标题");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await page.getByRole("button", { name: "选择对象", exact: true }).click();
  await expect(page.locator("[data-office-object]")).toHaveCount(3);
  await page.locator("[data-office-object]").last().click();
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill("突出这个图表");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await toggle.click();
  await expect(page.locator("[data-office-object]")).toHaveCount(0);
  expect(await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL())).toBe(
    before,
  );
  await page.screenshot({ path: test.info().outputPath("ppt-native.png") });
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect.poll(() => state.sent.length).toBe(1);
  const comments = state.sent[0]!.file_contexts![0]!.comments;
  expect(comments[0]!.anchor).toEqual({
    kind: "office",
    target: title.id,
    start: 0,
    end: Buffer.byteLength(title.text.slice(0, 2)),
    quote: title.text.slice(0, 2),
  });
  expect(comments[1]!.anchor).toMatchObject({ kind: "region", page: 1 });
  expect(comments[1]!.anchor.width).toBeGreaterThan(0.4);
  expect(conversions()).toBe(0);
});

if (process.env.OPENFOX_TEST_PPT_FIXTURE)
  test("PPT: original 11-slide deck source selection and rendering", async ({
    page,
  }) => {
    const { view, state, conversions } = await setup(
      page,
      false,
      "pptx",
      process.env.OPENFOX_TEST_PPT_FIXTURE,
    );
    const canvas = page.locator("canvas");
    const toggle = page.getByRole("button", { name: "批注", exact: true });
    for (let index = 0; index < 11; index++) {
      await expect(
        page.getByText(`${index + 1} / 11`, { exact: true }),
      ).toBeVisible();
      const before = await canvas.evaluate((c: HTMLCanvasElement) =>
        c.toDataURL(),
      );
      await toggle.click();
      const available = page.locator("[data-office-target]");
      await expect(available.first()).toBeVisible();
      expect(
        await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL()),
      ).toBe(before);
      const targetIDs = await available.evaluateAll((nodes) =>
        [
          ...new Set(nodes.map((n) => n.getAttribute("data-office-target"))),
        ].sort(),
      );
      expect(targetIDs).toEqual(
        view.units
          .filter(
            (u) =>
              u.section === `Slide ${index + 1}` &&
              u.editable &&
              u.slide_source &&
              u.text.trim(),
          )
          .map((u) => u.id)
          .sort(),
      );
      const id = await available.first().getAttribute("data-office-target");
      const unit = view.units.find((u) => u.id === id)!;
      expect(unit.section).toBe(`Slide ${index + 1}`);
      if (index === 10) {
        await available.first().focus();
        await page.keyboard.press("Enter");
        await page
          .getByRole("textbox", { name: "针对选区的修改意见" })
          .fill("调整最后一页的文字");
        await page
          .getByRole("button", { name: "添加到聊天", exact: true })
          .click();
      }
      await toggle.click();
      await page.screenshot({
        path: test.info().outputPath(`user-slide-${index + 1}.png`),
      });
      if (index < 10) {
        await page.getByRole("button", { name: "下一页", exact: true }).click();
        await expect(
          page.getByRole("button", { name: "上一页", exact: true }),
        ).toBeEnabled();
      }
    }
    await page.getByRole("button", { name: "发送", exact: true }).click();
    await expect.poll(() => state.sent.length).toBe(1);
    expect(
      state.sent[0]!.file_contexts![0]!.comments[0]!.anchor.target,
    ).toMatch(/^s11\./);
    expect(conversions()).toBe(0);
  });

test("Excel: source grid renders merge, image and chart with stable annotation mode", async ({
  page,
}) => {
  const { view, state, conversions } = await setup(page, false, "xlsx");
  const canvas = page.getByRole("grid");
  await page.getByRole("combobox", { name: "缩放" }).selectOption("fit");
  await page.screenshot({ path: test.info().outputPath("excel-native.png") });
  const before = await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL()),
    box = await canvas.boundingBox();
  const toggle = page.getByRole("button", { name: "批注", exact: true });
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await toggle.click();
  expect(await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL())).toBe(
    before,
  );
  expect(await canvas.boundingBox()).toEqual(box);
  // The source A1:D1 merge is one selectable range, never four guessed cells.
  await canvas.click({ position: { x: 60, y: 32 } });
  await expect(page.locator("output")).toHaveText("A1:D1");
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill("改工作表标题");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await page.getByRole("tab", { name: "第二张表", exact: true }).click();
  await expect(canvas).toHaveAttribute("data-sheet-index", "1");
  await expect(
    page.getByRole("button", { name: "选择单元格", exact: true }),
  ).toBeEnabled();
  await canvas.click({ position: { x: 50, y: 30 } });
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill("只改第二张表的同名文字");
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect.poll(() => state.sent.length).toBe(1);
  const comments = state.sent[0]!.file_contexts![0]!.comments;
  expect(comments[0]!.anchor).toEqual({
    kind: "office_range",
    target: "xl/worksheets/sheet1.xml",
    range: "A1:D1",
    start: 0,
    end: 0,
    quote: "中文工作表 · 项目进展\t\t\t",
  });
  expect(comments[1]!.anchor).toEqual({
    kind: "office",
    target: view.units.find(
      (u) => u.cell_source?.sheet === 1 && u.label === "A1",
    )!.id,
    start: 0,
    end: 0,
    quote: "重复文本",
  });
  expect(conversions()).toBe(0);
});

// Measure the real worker grid, then independently resolve visible fragments
// across the frozen/scrolling bands. Assertions use original OOXML addresses.
async function sheetCell(page: Page, row: number, col: number) {
  return page.getByRole("grid").evaluate(
    (canvas, { row, col }) => {
      const f = (
        window as unknown as {
          sheetFrame: import("../../../packages/app/src/conversations/artifacts/sheet/protocol").SheetFrame;
        }
      ).sheetFrame;
      const scroller = canvas.parentElement!.parentElement!;
      const zoom = (
        document.querySelector('select[aria-label="缩放"]') as HTMLSelectElement
      ).value;
      const scale =
        zoom === "fit"
          ? Math.max(0.1, Math.min(1, (canvas.clientWidth - 40) / f.width))
          : Number(zoom);
      const center = (
        indices: number[] | undefined,
        offsets: number[],
        start: number,
        index: number,
        frozen: number,
        scroll: number,
        viewport: number,
      ) => {
        const i = indices ? indices.indexOf(index) : index - start;
        if (i < 0) throw Error("Hidden cell");
        const a = offsets[i]!,
          b = offsets[i + 1]!;
        const fixed = a < frozen;
        const left = Math.max(
          fixed ? 0 : frozen * scale,
          a * scale - (fixed ? 0 : scroll),
        );
        const right = Math.min(viewport, b * scale - (fixed ? 0 : scroll));
        if (right <= left) throw Error("Cell outside viewport");
        return (left + right) / 2;
      };
      const rect = canvas.getBoundingClientRect();
      return {
        x:
          rect.x +
          40 +
          center(
            f.grid.colIndices,
            f.grid.colOffsets,
            f.grid.startCol,
            col,
            f.frozenWidth,
            scroller.scrollLeft,
            canvas.clientWidth - 40,
          ),
        y:
          rect.y +
          24 +
          center(
            f.grid.rowIndices,
            f.grid.rowOffsets,
            f.grid.startRow,
            row,
            f.frozenHeight,
            scroller.scrollTop,
            canvas.clientHeight - 24,
          ),
      };
    },
    { row, col },
  );
}
async function addSheetComment(page: Page, text: string) {
  await page.getByRole("textbox", { name: "针对选区的修改意见" }).fill(text);
  await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
  await expect(
    page.getByRole("textbox", { name: "输入消息", exact: true }),
  ).toBeFocused();
}

test("Excel: frozen panes, source ranges, hidden rows, keyboard and object feedback", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 540 });
  const workers = new Set();
  page.on("worker", (w) => {
    workers.add(w);
    w.on("close", () => workers.delete(w));
  });
  const { view, state } = await setup(page, false, "xlsx");
  const canvas = page.getByRole("grid");
  // At 100% the sheet actually overflows; at 50% it fits this workspace.
  const fixedPixels = () =>
    canvas.evaluate((c: HTMLCanvasElement) => {
      const ratio = c.width / c.clientWidth;
      return [
        ...c
          .getContext("2d")!
          .getImageData(42 * ratio, 26 * ratio, 40 * ratio, 15 * ratio).data,
      ];
    });
  const frozen = await fixedPixels();
  await canvas.evaluate((c) => c.parentElement!.parentElement!.scrollTo(30, 3));
  await expect
    .poll(() =>
      canvas.evaluate((c) => c.parentElement!.parentElement!.scrollLeft),
    )
    .toBe(30);
  expect(await fixedPixels()).toEqual(frozen);
  await page.getByRole("combobox", { name: "缩放" }).selectOption("0.5");
  await page.getByRole("button", { name: "批注", exact: true }).click();
  let a = await sheetCell(page, 2, 1),
    b = await sheetCell(page, 3, 3);
  await page.mouse.move(a.x, a.y);
  await page.mouse.down();
  await page.mouse.move(b.x, b.y, { steps: 8 });
  await page.mouse.up();
  await expect(page.locator("output")).toHaveText("B3:D4");
  await addSheetComment(page, "检查范围里的公式，保留格式");
  // The same source text in separate cells must retain distinct edit IDs.
  a = await sheetCell(page, 6, 0);
  await page.mouse.click(a.x, a.y);
  await addSheetComment(page, "第一处重复文本");
  b = await sheetCell(page, 6, 1);
  await page.mouse.click(b.x, b.y);
  await addSheetComment(page, "第二处重复文本");
  await canvas.focus();
  await page.keyboard.press("ArrowDown");
  await expect(page.locator("output")).toHaveText("B9"); // row 8 is hidden
  await page.keyboard.press("Shift+ArrowRight");
  await expect(page.locator("output")).toHaveText("B9:C9");
  await page.keyboard.press("Shift+ArrowLeft");
  await expect(page.locator("output")).toHaveText("B9");
  await page.keyboard.press("Shift+ArrowLeft");
  await expect(page.locator("output")).toHaveText("A9:B9");
  await page.keyboard.press("Enter");
  await addSheetComment(page, "空白范围也可以反馈");
  a = await sheetCell(page, 0, 0);
  await page.mouse.click(a.x, a.y);
  await addSheetComment(page, "合并标题");
  await canvas.focus();
  await page.keyboard.press("ArrowRight");
  await expect(page.locator("output")).toHaveText("E1");
  await page.keyboard.press("ArrowLeft");
  await expect(page.locator("output")).toHaveText("A1:D1");
  // A row header selects the source used columns, including formula cells.
  a = await sheetCell(page, 2, 0);
  const box = (await canvas.boundingBox())!;
  await page.mouse.click(box.x + 15, a.y);
  await expect(page.locator("output")).toHaveText("A3:D3");
  await addSheetComment(page, "第三行");
  // Column selection includes source hidden/blank cells but is not submitted twice.
  a = await sheetCell(page, 2, 2);
  await page.mouse.click(a.x, box.y + 12);
  await expect(page.locator("output")).toHaveText("A1:D8"); // intersects the merged title
  await page
    .getByRole("button", { name: "收起并保留批注草稿", exact: true })
    .click();
  await page.getByRole("button", { name: "选择对象", exact: true }).click();
  await page.getByRole("combobox", { name: "缩放" }).selectOption("1");
  await canvas.evaluate((c) =>
    c.parentElement!.parentElement!.scrollTo(190, 20),
  );
  await expect
    .poll(() =>
      canvas.evaluate((c) => c.parentElement!.parentElement!.scrollLeft),
    )
    .toBe(190);
  // Chart starts at source F3 and occupies its drawing rectangle even scrolled.
  const chart = await canvas.evaluate((c) => {
    const f = (
      window as unknown as {
        sheetFrame: import("../../../packages/app/src/conversations/artifacts/sheet/protocol").SheetFrame;
      }
    ).sheetFrame;
    const o = f.objects[0]!,
      s = c.parentElement!.parentElement!,
      b = c.getBoundingClientRect();
    return {
      x: b.x + 40 + o.x - s.scrollLeft + 20,
      y: b.y + 24 + f.frozenHeight + 20,
      expected: {
        x: o.x / f.width,
        y: o.y / f.height,
        width: o.w / f.width,
        height: o.h / f.height,
      },
    };
  });
  await page.mouse.click(chart.x, chart.y);
  await addSheetComment(page, "调整柱状图");
  await page.getByRole("button", { name: "批注", exact: true }).click();
  await page.screenshot({
    path: test.info().outputPath("excel-frozen-scroll.png"),
  });
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await expect.poll(() => state.sent.length).toBe(1);
  const anchors = state.sent[0]!.file_contexts![0]!.comments.map(
    (c) => c.anchor,
  );
  expect(anchors[0]).toEqual({
    kind: "office_range",
    target: "xl/worksheets/sheet1.xml",
    range: "B3:D4",
    start: 0,
    end: 0,
    quote: "10\t8\t=C3/B3\n20\t12\t=C4/B4",
  });
  for (const [i, label] of [
    [1, "A7"],
    [2, "B7"],
  ] as const)
    expect(anchors[i]).toMatchObject({
      kind: "office",
      target: view.units.find(
        (u) => u.label === label && u.cell_source?.sheet === 0,
      )!.id,
      quote: "重复文本",
    });
  expect(anchors[3]).toMatchObject({
    kind: "office_range",
    range: "A9:B9",
    quote: "\t",
  });
  expect(anchors[5]).toMatchObject({
    kind: "office_range",
    range: "A3:D3",
    quote: "第一阶段\t10\t8\t=C3/B3",
  });
  expect(anchors[6]).toMatchObject({ kind: "region", page: 1 });
  for (const key of ["x", "y", "width", "height"] as const)
    expect(anchors[6]![key]).toBeCloseTo(chart.expected[key], 5);
  await page.goto("about:blank");
  await expect.poll(() => workers.size).toBe(0);
});

test("Excel: render copy mismatch falls back to region-only PDF", async ({
  page,
}) => {
  const { conversions } = await setup(page, true, "xlsx");
  await expect.poll(conversions).toBe(1);
  await expect(page.getByRole("grid")).toHaveCount(0);
  await page.getByRole("button", { name: "批注", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "选择整页", exact: true }),
  ).toBeEnabled();
  await expect(
    page.getByRole("button", { name: "选择单元格", exact: true }),
  ).toHaveCount(0);
});

for (const kind of ["docx", "xlsx", "pptx"] as const) {
  test(`${kind}: edited candidate renders, retains annotation layout and downloads exact OOXML bytes`, async ({
    page,
  }) => {
    const path = resolve(
      candidates,
      kind === "pptx" ? "styled.pptx" : `complex.${kind}`,
    );
    const { view, conversions } = await setup(page, false, kind, path);
    const canvas = page.locator("canvas");
    const before = await canvas.evaluate((c: HTMLCanvasElement) =>
      c.toDataURL(),
    );
    const geometry = await canvas.boundingBox();
    await page.getByRole("button", { name: "批注", exact: true }).click();
    expect(await canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL())).toBe(
      before,
    );
    expect(await canvas.boundingBox()).toEqual(geometry);
    if (kind !== "xlsx") {
      const unit = view.units.find((u) => u.text === "中文报告 · 已核验")!;
      await expect(
        page.locator(`[data-office-target="${unit.id}"]`),
      ).toHaveCount([...unit.text].length);
    } else {
      const point = await sheetCell(page, 2, 1);
      await page.mouse.click(point.x, point.y);
      await expect(
        page.getByRole("textbox", { name: "针对选区的修改意见" }),
      ).toBeVisible();
      expect(
        view.units.find((u) => u.label === "B3" && u.cell_source?.sheet === 0)
          ?.text,
      ).toBe("42");
      await page.keyboard.press("Escape");
    }
    expect(conversions()).toBe(0);
    if (process.env.OPENFOX_STAGE14_EVIDENCE) {
      mkdirSync(process.env.OPENFOX_STAGE14_EVIDENCE, { recursive: true });
      await page.screenshot({
        path: resolve(
          process.env.OPENFOX_STAGE14_EVIDENCE,
          `candidate-${kind}.jpg`,
        ),
      });
    }
    const promise = page.waitForEvent("download");
    await page.getByRole("link", { name: "下载", exact: true }).click();
    const download = await promise;
    const downloaded = readFileSync((await download.path())!);
    expect(createHash("sha256").update(downloaded).digest("hex")).toBe(
      view.sha256,
    );
    expect(downloaded).toEqual(readFileSync(path));
  });
}
