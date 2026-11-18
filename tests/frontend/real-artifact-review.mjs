// Opt-in live acceptance driven by the Go temporary stack. No route mocks.
import { readFile, mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import assert from "node:assert/strict";
import { chromium, expect } from "@playwright/test";
import { acceptProposal } from "./real-proposal-review.mjs";

const fixture = JSON.parse(await readFile(process.argv[2], "utf8"));
const f = fixture;
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({
  locale: "zh-CN",
  viewport: { width: 1440, height: 1000 },
});
await context.addCookies(f.cookies);
const page = await context.newPage();
page.setDefaultTimeout(20000);
const pageErrors = [],
  failedAPI = [];
page.on("pageerror", (e) => pageErrors.push(e.message));
page.on("response", (response) => {
  if (response.url().includes("/api/") && response.status() >= 400)
    failedAPI.push({
      method: response.request().method(),
      path: new URL(response.url()).pathname,
      status: response.status(),
    });
});
const base = `${f.origin}/api/v1/organizations/${f.organization}`;
const button = (name) => page.getByRole("button", { name, exact: true });
const saveEvidence = async (phase) => {
  if (!f.evidence) return;
  await mkdir(f.evidence, { recursive: true });
  await page.screenshot({
    path: path.join(f.evidence, `${f.kind}-${phase}.png`),
    fullPage: false,
  });
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
    `${f.origin}/o/${f.organization}/chat/${f.conversation}?artifact=${f.kind === "native" ? `doc:${f.space}:${f.node}` : f.artifact}`,
  );
  if (f.kind === "native") {
    const body = page.locator(".doc-body");
    await expect(body).toContainText("末尾保持。");
    await body.locator("p").last().click();
    await page.keyboard.press("End");
    await page.keyboard.insertText("（画布编辑）");
    await button("保存文档").click();
    await expect(page.getByRole("region", { name: "文档画布" })).toContainText(
      "已保存",
    );
  } else {
    await button("更多预览操作").click();
    await button("打开当前版").click();
  }
  await button("批注").click();
  if (["docx", "xlsx", "pptx"].includes(f.kind)) {
    const target = page
      .locator("[data-office-target]")
      .filter({ hasText: f.quote })
      .first();
    await target.focus();
    await target.press("Enter");
  } else if (f.kind === "html") {
    const heading = page
      .locator("iframe")
      .first()
      .contentFrame()
      .getByRole("heading", { name: "原文", exact: true });
    await heading.focus();
    await heading.press("Enter");
  } else if (f.kind === "csv") {
    await page.locator('[data-cell="1:1"]').click();
  } else {
    const part =
      f.kind === "native"
        ? page.locator(".doc-body p").filter({ hasText: f.quote }).first()
        : page
            .locator("[data-comment-start]")
            .filter({ hasText: f.quote })
            .first();
    await expect(part).toBeVisible();
    const points = await part.evaluate((el, quote) => {
      const node = el.firstChild;
      const start = node.textContent.indexOf(quote);
      if (start < 0) throw new Error("quote not visible in preview");
      const range = document.createRange();
      range.setStart(node, start);
      range.setEnd(node, start + quote.length);
      const box = range.getBoundingClientRect();
      return { x: box.left, y: box.top + box.height / 2, end: box.right };
    }, f.quote);
    await page.mouse.move(points.x, points.y);
    await page.mouse.down();
    await page.mouse.move(points.end, points.y, { steps: 8 });
    await page.mouse.up();
  }

  const opinion = `请把选中内容精确替换成「${f.replacement}」，其余内容保持不变。`;
  await page.getByRole("textbox", { name: "针对选区的修改意见" }).fill(opinion);
  await button("添加到聊天").click();

  await expect(page.getByLabel("文件批注", { exact: true })).toContainText(
    "待发送批注（1）",
  );
  const send = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/conversations/${f.conversation}/messages`) &&
      r.request().method() === "POST",
  );
  await button("发送").click();
  assert.equal((await send).status(), 201);
  await expect(page.getByLabel("文件批注", { exact: true })).toHaveCount(0);
  await acceptProposal(page, context, f);
  if (f.kind !== "native")
    await page.getByRole("tab").filter({ hasText: "当前" }).click();
  if (f.kind === "native") {
    await expect
      .poll(
        async () => {
          const r = await context.request.get(
            `${base}/spaces/${f.space}/docs/${f.node}`,
          );
          return (await r.json()).body;
        },
        { timeout: 300000 },
      )
      .toContain(f.replacement);
    await page.goto(
      `${f.origin}/o/${f.organization}/chat/${f.conversation}?artifact=doc:${f.space}:${f.node}`,
    );
    await expect(
      page.locator("p").filter({ hasText: new RegExp(`^${f.replacement}$`) }),
    ).toBeVisible();
  } else if (f.kind === "html") {
    await expect(
      page
        .locator("iframe")
        .first()
        .contentFrame()
        .getByRole("heading", { name: "新文", exact: true }),
    ).toBeVisible({ timeout: 300000 });
  } else {
    await expect(
      page.getByText(f.replacement, { exact: true }).last(),
    ).toBeVisible({ timeout: 300000 });
  }

  await saveEvidence("updated");
  assert.deepEqual(pageErrors, []);
  assert.deepEqual(failedAPI, []);
  console.log(
    `${f.kind}: browser → ordinary chat → real Codex → explicit proposal acceptance → updated document passed`,
  );
} finally {
  await browser.close();
}
