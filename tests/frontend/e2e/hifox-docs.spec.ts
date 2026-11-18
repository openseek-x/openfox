import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";

// Run only against a disposable local database using the existing live fixture.
const org = "00000000-0000-0000-0000-0000000000a1";
const space = "00000000-0000-0000-0000-0000000000aa";
const apiBase = `/api/v1/organizations/${org}`;
async function login(page: Page) {
  await page.goto("/login");
  await page.getByLabel("邮箱", { exact: true }).fill("alice@example.test");
  await page.getByRole("button", { name: "继续" }).click();
  await page
    .getByLabel("第 1 位")
    .pressSequentially(process.env.OPENFOX_LIVE_OTP_CODE!);
  await page.waitForURL(/\/o\//);
  await page.evaluate(
    (id) =>
      localStorage.setItem(
        `openfox:onboarding:${id}`,
        JSON.stringify({ step: "done" }),
      ),
    org,
  );
}
async function request(
  page: Page,
  path: string,
  method = "GET",
  body?: unknown,
) {
  return page.evaluate(
    async ({ path, method, body }) => {
      const csrf =
        document.cookie
          .split("; ")
          .find((value) => value.startsWith("openfox_csrf="))
          ?.split("=")[1] ?? "";
      const response = await fetch(path, {
        method,
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": decodeURIComponent(csrf),
        },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const value = await response.json();
      if (!response.ok)
        throw new Error(
          `${method} ${path}: ${response.status} ${JSON.stringify(value)}`,
        );
      return value;
    },
    { path, method, body },
  );
}
async function menu(page: Page) {
  await page
    .locator("section > header")
    .getByRole("button", { name: "更多操作" })
    .click();
}

test("D01 D02 D03: real document actions, search and personal page widths", async ({
  page,
}) => {
  test.skip(
    !process.env.OPENFOX_LIVE_OTP_CODE,
    "Requires a disposable local live fixture",
  );
  await login(page);
  const stamp = Date.now();
  const body = `# 中文标题\n\n中文 **内容** Abc abc\n\n\`\`\`ts\n中文 ${"long_code_".repeat(35)}\n\`\`\`\n\n| 名称 | 值 |\n| --- | --- |\n| 中文 | ${"long_cell_".repeat(25)} |`;
  const doc = await request(page, `${apiBase}/spaces/${space}/docs`, "POST", {
    container: { kind: "personal" },
    kind: "doc",
    title: `对齐文档 ${stamp}`,
    body,
  });
  const target = await request(page, `${apiBase}/spaces`, "POST", {
    name: `移动目标 ${stamp}`,
    visibility: "private",
  });
  const nodePath = `${apiBase}/spaces/${space}/docs/${doc.id}`;
  await page.goto(`/o/${org}/spaces/${space}/docs/${doc.id}`);
  await expect(page.locator(".doc-body")).toContainText("中文标题");
  const before = await request(page, nodePath);
  const editorJSON = await page
    .locator(".doc-body")
    .evaluate((element) =>
      (
        element as HTMLElement & { editor: { getJSON: () => unknown } }
      ).editor.getJSON(),
    );

  // Search never sends a body mutation, including closing with Escape.
  const writes: string[] = [];
  page.on("request", (req) => {
    if (req.method() === "PATCH" && req.url().endsWith(`/docs/${doc.id}`))
      writes.push(req.postData() ?? "");
  });
  await page.getByRole("button", { name: "文内查找" }).click();
  await page.getByRole("searchbox", { name: "查找正文" }).fill("中文");
  await expect(page.getByRole("search").getByRole("status")).toHaveText(
    "1 / 4",
  );
  await page.getByRole("button", { name: "下一个匹配" }).click();
  await expect(page.getByRole("search").getByRole("status")).toHaveText(
    "2 / 4",
  );
  await page.getByRole("searchbox").fill("abc");
  await expect(page.getByRole("search").getByRole("status")).toHaveText(
    "1 / 2",
  );
  await page.getByRole("checkbox", { name: "区分大小写" }).check();
  await expect(page.getByRole("search").getByRole("status")).toHaveText(
    "1 / 1",
  );
  await page.getByRole("searchbox").press("Escape");
  await expect(page.getByRole("search")).toHaveCount(0);
  expect(
    await page
      .locator(".doc-body")
      .evaluate((element) =>
        (
          element as HTMLElement & { editor: { getJSON: () => unknown } }
        ).editor.getJSON(),
      ),
  ).toEqual(editorJSON);

  // Width is per-user local UI data and does not change the saved document.
  for (const [label, width] of [
    ["窄", 600],
    ["标准", 768],
    ["宽", 1120],
  ] as const) {
    await menu(page);
    await page.getByRole("menuitemradio", { name: label, exact: true }).click();
    await expect(page.locator("[data-doc-page-width]")).toHaveCSS(
      "max-width",
      `${width}px`,
    );
    const overflow = await page
      .locator("[data-doc-page-width]")
      .evaluate((el) => el.scrollWidth > el.clientWidth + 1);
    expect(overflow).toBe(false);
    await expect(page.locator(".doc-body pre")).toHaveCSS("overflow-x", "auto");
    await expect(page.locator(".doc-body table")).toHaveCSS(
      "overflow-x",
      "auto",
    );
  }
  await page.reload();
  await expect(page.locator("[data-doc-page-width]")).toHaveAttribute(
    "data-doc-page-width",
    "wide",
  );
  const after = await request(page, nodePath);
  expect(after.version).toBe(before.version);
  expect(after.body).toBe(before.body);
  expect(writes).toEqual([]);

  // Editing then immediately renaming flushes the new body and uses its version.
  await page.locator(".doc-body").press("ControlOrMeta+End");
  await page.keyboard.type(" 文档新增内容");
  await menu(page);
  await page.getByRole("menuitem", { name: "重命名", exact: true }).click();
  const rename = page.getByRole("dialog", { name: "重命名" });
  await rename.getByRole("textbox").fill(`改名 ${stamp}`);
  await rename.getByRole("button", { name: "保存", exact: true }).click();
  await expect(rename).toHaveCount(0);
  await expect(page.getByRole("textbox", { name: "未命名文档" })).toHaveValue(
    `改名 ${stamp}`,
  );
  const saved = await request(page, nodePath);
  expect(saved.body).toContain("文档新增内容");
  expect(saved.title).toBe(`改名 ${stamp}`);

  await menu(page);
  await page.getByRole("menuitem", { name: "历史版本" }).click();
  await expect(page.getByRole("dialog", { name: "历史版本" })).toBeVisible();
  await expect(page.getByRole("option").first()).toContainText("当前版本");
  await page.keyboard.press("Escape");
  await menu(page);
  const downloaded = page.waitForEvent("download");
  await page.getByRole("menuitem", { name: "下载", exact: true }).click();
  const download = await downloaded;
  expect(await readFile((await download.path())!, "utf8")).toContain(
    "文档新增内容",
  );

  // Cross-space move navigates the open document to its new canonical route.
  await menu(page);
  await page.getByRole("menuitem", { name: "移动到" }).click();
  const move = page.getByRole("dialog", { name: "移动到" });
  const targetGroup = move.locator("section").filter({ hasText: target.name });
  await targetGroup
    .getByRole("button", { name: "我的文档", exact: true })
    .click();
  await move.getByRole("button", { name: "移动", exact: true }).click();
  await page.waitForURL(`**/spaces/${target.id}/docs/${doc.id}`);
  await expect(page.getByRole("textbox", { name: "未命名文档" })).toHaveValue(
    `改名 ${stamp}`,
  );
  const moved = await request(
    page,
    `${apiBase}/spaces/${target.id}/docs/${doc.id}`,
  );
  expect(moved.space_id).toBe(target.id);
  await menu(page);
  await page.getByRole("menuitem", { name: "删除", exact: true }).click();
  const deleted = page.waitForResponse(
    (res) =>
      res.request().method() === "DELETE" &&
      res.url().includes(`/docs/${doc.id}`),
  );
  await page
    .getByRole("dialog", { name: "删除文档" })
    .getByRole("button", { name: "删除", exact: true })
    .click();
  expect((await deleted).status()).toBe(204);
  await page.waitForURL(`**/spaces/${target.id}/docs`);

  // A retired test space is read-only at the server boundary. The same menu
  // hides writes and search remains available without changing persisted data.
  const readonly = await request(
    page,
    `${apiBase}/spaces/${target.id}/docs`,
    "POST",
    {
      container: { kind: "personal" },
      kind: "doc",
      title: "只读样本",
      body: "中文只读内容",
    },
  );
  const targetNow = await request(page, `${apiBase}/spaces/${target.id}`);
  await request(page, `${apiBase}/spaces/${target.id}/retire`, "POST", {
    version: targetNow.version,
    open_tasks: "keep",
  });
  await page.goto(`/o/${org}/spaces/${target.id}/docs/${readonly.id}`);
  await expect(page.getByText("可查看", { exact: true })).toBeVisible();
  await menu(page);
  for (const name of ["重命名", "移动到", "删除"])
    await expect(page.getByRole("menuitem", { name, exact: true })).toHaveCount(
      0,
    );
  await expect(
    page.getByRole("menuitem", { name: "下载", exact: true }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "文内查找" }).click();
  await page.getByRole("searchbox").fill("中文");
  await expect(page.getByRole("search").getByRole("status")).toHaveText(
    "1 / 1",
  );
  const readonlyAfter = await request(
    page,
    `${apiBase}/spaces/${target.id}/docs/${readonly.id}`,
  );
  expect(readonlyAfter.body).toBe(readonly.body);
  expect(readonlyAfter.version).toBe(readonly.version);
});
