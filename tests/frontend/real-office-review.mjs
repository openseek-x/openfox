// Opt-in stage 8: real API, real Codex, production assets/CSP. No route mocks.
import { readFile, mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import assert from "node:assert/strict";
import { chromium, expect } from "@playwright/test";
import { acceptProposal } from "./real-proposal-review.mjs";
const f = JSON.parse(await readFile(process.argv[2], "utf8"));
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({
  locale: "zh-CN",
  viewport: { width: 1440, height: 900 },
});
await context.addCookies(f.cookies);
const page = await context.newPage();
page.setDefaultTimeout(20000);
const errors = [],
  requests = [];
page.on("pageerror", (e) => errors.push(e.message));
page.on("response", (r) => {
  if (r.url().endsWith("/office"))
    requests.push({ url: r.url(), status: r.status() });
});
const button = (name) => page.getByRole("button", { name, exact: true });
const base = `${f.origin}/api/v1/organizations/${f.organization}`;
const canvas = page.locator("canvas");
const save = async (phase) => {
  if (f.evidence) {
    await mkdir(f.evidence, { recursive: true });
    await page.screenshot({
      path: path.join(f.evidence, `${f.kind}-${phase}.png`),
    });
  }
};
try {
  await page.addInitScript(
    (org) =>
      localStorage.setItem(
        `openfox:onboarding:${org}`,
        JSON.stringify({ step: "done" }),
      ),
    f.organization,
  );
  await page.goto(
    `${f.origin}/o/${f.organization}/chat/${f.conversation}?artifact=${f.artifact}`,
  );
  await page.getByRole("button", { name: "更多预览操作" }).click();
  await page.getByRole("button", { name: "打开当前版" }).click();
  await expect(canvas).toHaveCount(1);
  if (f.extension === "xlsx")
    await expect(
      page.getByRole("tab", { name: "第二张表", exact: true }),
    ).toBeEnabled();
  else
    await expect
      .poll(() => canvas.evaluate((c) => c.width))
      .toBeGreaterThan(800);
  await expect(button("批注")).toHaveAttribute("aria-pressed", "false");
  const before = await canvas.evaluate((c) => c.toDataURL());
  await button("批注").click();
  assert.equal(await canvas.evaluate((c) => c.toDataURL()), before);
  if (f.extension === "xlsx") {
    await page.getByRole("combobox", { name: "缩放" }).selectOption("0.5");
    await page.getByRole("grid").focus();
    await page.keyboard.press("ArrowDown");
    await page.keyboard.press("ArrowDown");
    for (let i = 0; i < (f.kind === "xlsx_formula" ? 3 : 1); i++)
      await page.keyboard.press("ArrowRight");
    await expect(page.locator("output")).toHaveText(
      f.kind === "xlsx_formula" ? "D3" : "B3",
    );
    await page.keyboard.press("Enter");
  } else if (f.kind === "pptx_chart") {
    await button("选择对象").click();
    await expect(page.locator("[data-office-object]")).toHaveCount(3);
    await page.locator("[data-office-object]").last().click();
  } else {
    const target = page
      .locator(`[data-office-target="${f.anchor.target}"]`)
      .first();
    await expect(target).toBeVisible();
    await target.focus();
    await page.keyboard.press("Enter");
  }
  await page
    .getByRole("textbox", { name: "针对选区的修改意见" })
    .fill(f.instruction);
  await button("添加到聊天").click();
  await expect(page.getByLabel("文件批注", { exact: true })).toContainText(
    "待发送批注（1）",
  );
  await save("selected");
  const sent = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/conversations/${f.conversation}/messages`) &&
      r.request().method() === "POST",
  );
  await button("发送").click();
  const response = await sent;
  assert.equal(response.status(), 201);
  const payload = response.request().postDataJSON().file_contexts[0];
  assert.equal(payload.comments[0].anchor.kind, f.anchor.kind);
  if (f.anchor.kind !== "region")
    assert.equal(payload.comments[0].anchor.quote, f.anchor.quote);
  await acceptProposal(page, context, f);
  await page.getByRole("tab").filter({ hasText: "当前" }).click();
  let updated;
  await expect
    .poll(
      async () => {
        const r = await context.request.get(
          `${base}/conversations/${f.conversation}/artifacts`,
        );
        assert.equal(r.status(), 200);
        const body = await r.json();
        const files = Array.isArray(body) ? body : body.artifacts;
        updated = files.find((a) => a.replaces_artifact_id === f.artifact);
        return Boolean(updated);
      },
      { timeout: 300000, intervals: [500, 1000, 2000] },
    )
    .toBe(true);
  await expect
    .poll(
      () =>
        requests.some(
          (r) =>
            r.url.includes(`/artifacts/${updated.id}/office`) &&
            r.status === 200,
        ),
      { timeout: 30000 },
    )
    .toBe(true);
  // The current-version tab follows the accepted result; no manual reload.
  await expect(button("批注")).toHaveAttribute("aria-pressed", "false");
  await expect(canvas).toHaveCount(1);
  if (f.extension === "xlsx")
    await expect(
      page.getByRole("tab", { name: "第二张表", exact: true }),
    ).toBeEnabled();
  else
    await expect
      .poll(() => canvas.evaluate((c) => c.width))
      .toBeGreaterThan(800);
  const projection = await context.request.get(
    `${base}/runs/${updated.run_id}/artifacts/${updated.id}/office`,
  );
  const view = await projection.json();
  assert.equal(view.sha256, updated.sha256);
  if (f.kind === "xlsx_formula")
    assert.equal(
      view.units.find((u) => u.cell_source?.sheet === 0 && u.label === "D3")
        .text,
      f.replacement,
    );
  else if (f.kind !== "pptx_chart")
    assert.ok(view.units.some((u) => u.text === f.replacement));
  if (f.kind === "pptx_chart") {
    await expect
      .poll(() =>
        canvas.evaluate((c) => {
          let red = 0;
          const data = c
            .getContext("2d")
            .getImageData(0, 0, c.width, c.height).data;
          for (let i = 0; i < data.length; i += 4)
            if (
              data[i] > 200 &&
              data[i + 1] > 45 &&
              data[i + 1] < 100 &&
              data[i + 2] > 45 &&
              data[i + 2] < 110
            )
              red++;
          return red;
        }),
      )
      .toBeGreaterThan(500);
  }
  await save("updated");
  assert.deepEqual(errors, []);
  if (f.evidence)
    await writeFile(
      path.join(f.evidence, `${f.kind}-browser.json`),
      JSON.stringify(
        {
          kind: f.kind,
          source: f.artifact,
          result: updated.id,
          source_sha256: payload.sha256,
          result_sha256: updated.sha256,
          anchor: payload.comments[0].anchor,
          requestCount: requests.length,
          pageErrors: errors,
        },
        null,
        2,
      ),
    );
  console.log(
    `${f.kind}: visible selection → ordinary message → real model → explicit proposal acceptance → new Office preview passed`,
  );
} finally {
  await browser.close();
}
