import { expect, test, type Page } from "@playwright/test";

test.skip(
  process.env.OPENFOX_HIFOX_BATCH6_E2E !== "1",
  "requires isolated document and conversation fixtures",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const recipient = "00000000-0000-0000-0000-0000000000c3";
const conversation = "00000000-0000-0000-0000-000000003c03";
const base = `/api/v1/organizations/${org}`;
const targetPath = `${base}/conversations/${conversation}`;

async function login(page: Page, email: string) {
  await page.goto("/login");
  await page.getByLabel("邮箱", { exact: true }).fill(email);
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

async function headers(page: Page): Promise<Record<string, string>> {
  const csrf = (await page.context().cookies()).find(
    (c) => c.name === "openfox_csrf",
  )?.value;
  return csrf ? { "X-CSRF-Token": csrf } : {};
}

async function request(
  page: Page,
  path: string,
  method = "GET",
  data?: unknown,
) {
  const response = await page.request.fetch(path, {
    method,
    data,
    headers: await headers(page),
  });
  expect(response.ok(), `${method} ${path}: ${response.status()}`).toBeTruthy();
  return response.status() === 204 ? null : response.json();
}

test("D04 doc sheet and file references follow live reader access without copying source contents", async ({
  page,
  browser,
}) => {
  await login(page, "alice@example.test");
  const space = await request(page, `${base}/spaces`, "POST", {
    name: "D04 private document source",
    visibility: "private",
  });
  const spacePath = `${base}/spaces/${space.id}`;
  const set = await request(page, `${spacePath}/doc-sets`, "POST", {
    name: "D04 team documents",
  });
  let folder = await request(page, `${spacePath}/docs`, "POST", {
    kind: "folder",
    title: "D04 source folder",
    container: { kind: "set", set_id: set.id },
  });
  const nodes = [];
  for (const kind of ["doc", "sheet"])
    nodes.push(
      await request(page, `${spacePath}/docs`, "POST", {
        kind,
        title: `D04 ${kind} source`,
        body:
          kind === "doc"
            ? "D04 confidential body marker"
            : "Name,Value\nSecret,D04 confidential sheet marker",
        parent_id: folder.id,
        container: { kind: "set", set_id: set.id },
      }),
    );
  const upload = await page.request.post(`${spacePath}/docs/upload`, {
    headers: await headers(page),
    multipart: {
      container: "set",
      set_id: set.id,
      parent_id: folder.id,
      file: {
        name: "D04-file.bin",
        mimeType: "application/octet-stream",
        buffer: Buffer.from("D04 confidential file marker"),
      },
    },
  });
  expect(upload.status()).toBe(201);
  nodes.push(await upload.json());
  await request(page, `${targetPath}/members/${recipient}`, "PUT", {
    access_level: "COMMENTER",
  });
  const before = await request(page, `${targetPath}/messages`);
  const sentMessages: {
    id: string;
    seq: number;
    title: string;
    nodeId: string;
    role: "button" | "link";
  }[] = [];
  for (const node of nodes) {
    await page.goto(`/o/${org}/spaces/${space.id}/docs/${node.id}`);
    const openForward = async () => {
      await page
        .locator("section > header")
        .getByRole("button", { name: "更多操作", exact: true })
        .click();
      await page
        .getByRole("menuitem", { name: "转发到会话", exact: true })
        .click();
      return page.getByRole("dialog", { name: "转发到会话", exact: true });
    };
    let dialog = await openForward();
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    expect(
      (await request(page, `${targetPath}/messages`)).messages,
    ).toHaveLength(before.messages.length + sentMessages.length);
    dialog = await openForward();
    await dialog
      .getByRole("radio", { name: "C03 private source", exact: true })
      .check();
    const response = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().endsWith(`${conversation}/references`),
    );
    await dialog.getByRole("button", { name: "确认转发", exact: true }).click();
    const sent = await response;
    expect(sent.status()).toBe(201);
    const message = await sent.json();
    expect(message.run_id).toBeNull();
    expect(message.payload).toBeUndefined();
    expect(message.reference).toMatchObject({
      type: "DOC",
      available: true,
      doc_id: node.id,
      doc_kind: node.kind,
    });
    expect(JSON.stringify(message)).not.toContain("confidential");
    expect(JSON.stringify(message)).not.toContain("download");
    sentMessages.push({
      id: message.id,
      seq: message.seq,
      title: node.title,
      nodeId: node.id,
      role: node.kind === "doc" ? "button" : "link",
    });
    await expect(dialog).toBeHidden();
  }
  await page.goto(`/o/${org}/chat/${conversation}`);
  await page.reload();
  for (const message of sentMessages)
    await expect(
      page.locator(`[data-seq="${message.seq}"]`).getByRole(message.role, {
        name: `文档引用 ${message.title}`,
        exact: true,
      }),
    ).toContainText(message.title);

  const context = await browser.newContext({
    baseURL: page.url().split("/o/")[0],
    locale: "zh-CN",
  });
  try {
    const reader = await context.newPage();
    await login(reader, "carol@example.test");
    await reader.goto(`/o/${org}/chat/${conversation}`);
    const assertCards = async (visible: boolean) => {
      for (const message of sentMessages) {
        const card = reader.locator(`[data-seq="${message.seq}"]`);
        if (visible)
          await expect(
            card.getByRole(message.role, {
              name: `文档引用 ${message.title}`,
              exact: true,
            }),
          ).toContainText(message.title);
        else {
          await expect(
            card.getByText("文档不可用", { exact: true }),
          ).toBeVisible();
          await expect(
            card.getByRole(message.role, {
              name: `文档引用 ${message.title}`,
              exact: true,
            }),
          ).toHaveCount(0);
        }
      }
    };
    await assertCards(false);
    const hidden = (
      await request(reader, `${targetPath}/messages`)
    ).messages.filter((m: { id: string }) =>
      sentMessages.some((item) => item.id === m.id),
    );
    for (const node of nodes) {
      expect(JSON.stringify(hidden)).not.toContain(node.id);
      expect(JSON.stringify(hidden)).not.toContain(node.title);
    }
    expect(JSON.stringify(hidden)).not.toContain(space.id);
    await request(page, `${spacePath}/members/${recipient}`, "PUT", {
      role: "MEMBER",
      status: "ACTIVE",
    });
    await reader.reload();
    await assertCards(true);
    const doc = await request(page, `${spacePath}/docs/${nodes[0].id}`);
    await request(page, `${spacePath}/docs/${doc.id}`, "PATCH", {
      version: doc.version,
      title: "D04 renamed current document",
    });
    sentMessages[0]!.title = "D04 renamed current document";
    await assertCards(true);
    // Moving the ancestor must invalidate every open child card for old readers.
    folder = await request(
      page,
      `${spacePath}/docs/${folder.id}/move`,
      "POST",
      {
        version: folder.version,
        target_space_id: space.id,
        container: { kind: "personal" },
      },
    );
    await assertCards(false);
    await reader.reload();
    await assertCards(false);
    folder = await request(
      page,
      `${spacePath}/docs/${folder.id}/move`,
      "POST",
      {
        version: folder.version,
        target_space_id: space.id,
        container: { kind: "set", set_id: set.id },
      },
    );
    await assertCards(true);
    await request(page, `${spacePath}/members/${recipient}`, "DELETE");
    await assertCards(false);
    await request(
      page,
      `${spacePath}/docs/${folder.id}?version=${folder.version}`,
      "DELETE",
    );
    await page.reload();
    for (const message of sentMessages)
      await expect(
        page
          .locator(`[data-seq="${message.seq}"]`)
          .getByText("文档不可用", { exact: true }),
      ).toBeVisible();
  } finally {
    await context.close();
  }
});
