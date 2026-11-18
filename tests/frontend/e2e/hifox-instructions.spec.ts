import { expect, test, type Page } from "@playwright/test";

// Opt in only against the disposable local DB seeded by tests/frontend/fixture.sql.
test.skip(
  process.env.OPENFOX_HIFOX_INSTRUCTIONS_E2E !== "1",
  "requires an isolated local API and fixture database",
);
const org = "00000000-0000-0000-0000-0000000000a1";
const space = "00000000-0000-0000-0000-0000000000aa";
const base = `/api/v1/organizations/${org}/spaces/${space}`;

async function request(
  page: Page,
  path: string,
  method = "GET",
  body?: unknown,
) {
  const csrf = (await page.context().cookies()).find(
    (cookie) => cookie.name === "openfox_csrf",
  )?.value;
  const response = await page.request.fetch(path, {
    method,
    data: body,
    headers: csrf ? { "X-CSRF-Token": csrf } : {},
  });
  expect(
    response.ok(),
    `${method} ${path}: ${response.status()} ${await response.text()}`,
  ).toBeTruthy();
  return response.json();
}

async function setupInstructions(page: Page) {
  await page.goto("/login");
  await page.getByLabel("邮箱", { exact: true }).fill("alice@example.test");
  await page.getByRole("button", { name: "继续" }).click();
  await page
    .getByLabel("第 1 位")
    .pressSequentially(process.env.OPENFOX_LIVE_OTP_CODE!);
  await page.waitForURL(/\/o\//);
  await page.evaluate(
    (orgId) =>
      localStorage.setItem(
        `openfox:onboarding:${orgId}`,
        JSON.stringify({ step: "done" }),
      ),
    org,
  );
  const devices = await request(page, `/api/v1/organizations/${org}/devices`);
  const device = devices.devices.find(
    (item: { id: string }) =>
      item.id === "00000000-0000-0000-0000-000000000011",
  );
  expect(device).toBeTruthy();
  const agent = await request(page, `${base}/agents`, "POST", {
    name: `Instructions E2E ${Date.now()}`,
    kind: "CODE",
    visibility: "PRIVATE",
    config: {
      computer_id: device.id,
      grant_id: device.grant_id,
      runtime: "codex",
      model: "current-model",
      instructions: "initial",
    },
  });
  const crew = await request(page, `${base}/crews`, "POST", {
    name: `Instructions crew ${Date.now()}`,
    visibility: "PRIVATE",
    leader_agent_id: agent.id,
    agent_ids: [agent.id],
  });
  return { agent, crew };
}

async function verifyHistoryGuard(
  page: Page,
  entity: { id: string; name: string },
  kind: "agent" | "crew",
  rapid: boolean,
) {
  const label = kind === "agent" ? "Agent 指令" : "小队指令";
  const parameter = kind === "agent" ? "agentSettings" : "crewSettings";
  const openInstructions = async () => {
    await page.getByRole("button", { name: new RegExp(`^${label}`) }).click();
  };
  const input = page.getByRole("textbox", { name: label, exact: true });
  // Keep two prior entries in this SPA document, including rapid Back.
  await page.goto(`/o/${org}/inbox`);
  await page.getByRole("link", { name: /^全部 Agent/ }).click();
  await page.getByRole("button", { name: entity.name, exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`${parameter}=${entity.id}`));
  const drawerURL = page.url();
  await page
    .getByRole("link", { name: "我的任务", exact: true })
    .first()
    .click();
  const nextURL = page.url();
  await page.evaluate(() => history.back());
  await expect(page).toHaveURL(drawerURL);
  await openInstructions();
  await input.fill(`${kind} history draft`);
  const length = await page.evaluate(() => history.length);
  const entry = await page.evaluate(() => ({
    idx: history.state.idx,
    key: history.state.key,
  }));
  for (const direction of ["back", "forward", "back"] as const) {
    await page.evaluate((direction) => history[direction](), direction);
    await page.getByRole("button", { name: "继续编辑", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "继续编辑", exact: true }),
    ).toHaveCount(0);
    await expect(page).toHaveURL(drawerURL);
    await expect(input).toHaveValue(`${kind} history draft`);
    expect(await page.evaluate(() => history.length)).toBe(length);
    expect(
      await page.evaluate(() => ({
        idx: history.state.idx,
        key: history.state.key,
      })),
    ).toEqual(entry);
  }
  if (rapid) {
    // Rapid repeated back requests must still leave a single recoverable prompt.
    await page.evaluate(
      () =>
        new Promise<void>((resolve) => {
          history.back();
          setTimeout(() => {
            history.back();
            resolve();
          }, 0);
        }),
    );
    await expect(
      page.getByRole("button", { name: "继续编辑", exact: true }),
    ).toHaveCount(1);
    await page.getByRole("button", { name: "继续编辑", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "继续编辑", exact: true }),
    ).toHaveCount(0);
    await expect(page).toHaveURL(drawerURL);
    await expect(input).toHaveValue(`${kind} history draft`);
    expect(await page.evaluate(() => history.length)).toBe(length);
    expect(
      await page.evaluate(() => ({
        idx: history.state.idx,
        key: history.state.key,
      })),
    ).toEqual(entry);
    // The next entity starts with a full-document navigation. Finish this
    // scenario explicitly; an unsaved draft correctly blocks that navigation.
    await page.getByRole("button", { name: "关闭", exact: true }).click();
    await page.getByRole("button", { name: "放弃修改", exact: true }).click();
    await expect(input).toHaveCount(0);
    return;
  }
  await page.evaluate(() => history.back());
  await page.getByRole("button", { name: "放弃修改", exact: true }).click();
  await expect(page).toHaveURL(`/o/${org}/agents`);
  await expect(input).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "放弃修改", exact: true }),
  ).toHaveCount(0);
  await page.evaluate(() => history.forward());
  await expect(page).toHaveURL(drawerURL);
  await openInstructions();
  await input.fill(`${kind} forward draft`);
  await page.evaluate(() => history.forward());
  await page.getByRole("button", { name: "放弃修改", exact: true }).click();
  await expect(page).toHaveURL(nextURL);
  await expect(input).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "放弃修改", exact: true }),
  ).toHaveCount(0);
  await page.evaluate(() => history.back());
  await expect(page).toHaveURL(drawerURL);
  await openInstructions();
  await input.fill(`${kind} close draft`);
  if (kind === "agent") {
    await page.getByRole("button", { name: "展开", exact: true }).click();
    await expect(input).toHaveValue(`${kind} close draft`);
    await expect(
      page.getByRole("button", { name: "放弃修改", exact: true }),
    ).toHaveCount(0);
  }
  // The existing drawer confirmation must not trigger a second router prompt.
  await page.getByRole("button", { name: "关闭", exact: true }).click();
  await page.getByRole("button", { name: "放弃修改", exact: true }).click();
  await expect(page).toHaveURL(`/o/${org}/agents`);
  await expect(input).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "放弃修改", exact: true }),
  ).toHaveCount(0);
}

for (const rapid of [false, true]) {
  test(`real Agent/Crew instructions with ${rapid ? "rapid consecutive Back" : "ordinary navigation"}: save, rich/source, expansion and history restore`, async ({
    page,
  }) => {
    const nativeDialogs: string[] = [];
    page.on("dialog", (dialog) => {
      nativeDialogs.push(dialog.type());
      void dialog.dismiss();
    });
    const { agent, crew } = await setupInstructions(page);
    await page.goto(`/o/${org}/agents?agentSettings=${agent.id}`);
    await page.getByRole("button", { name: /^Agent 指令/ }).click();
    const input = page.getByRole("textbox", {
      name: "Agent 指令",
      exact: true,
    });
    const markdown =
      "# 中文标题\n\n- 第一项\n- 第二项\n\n```js\nconst hello = '世界';\n```";
    await input.fill(markdown);
    await page.getByRole("button", { name: "富文本", exact: true }).click();
    await expect(page.getByRole("heading", { name: "中文标题" })).toBeVisible();
    await page.getByRole("button", { name: "源码", exact: true }).click();
    await expect(input).toHaveValue(markdown);
    await page.getByRole("button", { name: "展开编辑器" }).click();
    await expect(
      page.getByRole("dialog", { name: "Agent 指令" }),
    ).toBeVisible();
    await input.evaluate((node) => {
      const area = node as HTMLTextAreaElement;
      area.focus();
      area.setSelectionRange(4, 6);
    });
    await input.press("Escape");
    await expect(page.getByRole("dialog", { name: "Agent 指令" })).toHaveCount(
      0,
    );
    expect(
      await input.evaluate((node) => [
        (node as HTMLTextAreaElement).selectionStart,
        (node as HTMLTextAreaElement).selectionEnd,
      ]),
    ).toEqual([4, 6]);
    await page.getByRole("button", { name: "保存", exact: true }).click();
    await expect
      .poll(
        async () =>
          (await request(page, `${base}/agents/${agent.id}`)).config
            .instructions,
      )
      .toBe(markdown);
    await page.getByRole("button", { name: "指令历史" }).click();
    await page.getByRole("button", { name: /^版本 1 / }).click();
    await page.getByRole("button", { name: "恢复此版本" }).click();
    await expect
      .poll(
        async () =>
          (await request(page, `${base}/agents/${agent.id}`)).config
            .instructions,
      )
      .toBe("initial");
    const restored = await request(page, `${base}/agents/${agent.id}`);
    expect(restored.config.model).toBe("current-model");
    expect(restored.current_config_version).toBe(3);
    await expect(input).toHaveValue("initial");
    await input.fill("unsaved navigation draft");
    await page
      .getByRole("link", { name: "我的任务", exact: true })
      .first()
      .click();
    await page.getByRole("button", { name: "继续编辑", exact: true }).click();
    await expect(input).toHaveValue("unsaved navigation draft");
    expect(page.url()).toContain(`agentSettings=${agent.id}`);
    await input.fill("initial");
    await page.goto(`/o/${org}/agents?crewSettings=${crew.id}`);
    await page.getByRole("button", { name: /^小队指令/ }).click();
    const crewInput = page.getByRole("textbox", {
      name: "小队指令",
      exact: true,
    });
    await crewInput.fill(markdown);
    await page.getByRole("button", { name: "富文本", exact: true }).click();
    await expect(page.getByRole("heading", { name: "中文标题" })).toBeVisible();
    await page.getByRole("button", { name: "展开编辑器" }).click();
    const rich = page.getByRole("textbox", { name: "小队指令", exact: true });
    await rich.press("Escape");
    await page.getByRole("button", { name: "源码", exact: true }).click();
    await expect(crewInput).toHaveValue(markdown);
    await page.getByRole("button", { name: "保存", exact: true }).click();
    await expect
      .poll(
        async () =>
          (await request(page, `${base}/crews/${crew.id}`)).instructions,
      )
      .toBe(markdown);
    await page.reload();
    await page.getByRole("button", { name: /^小队指令/ }).click();
    await expect(crewInput).toHaveValue(markdown);
    await verifyHistoryGuard(page, agent, "agent", rapid);
    await verifyHistoryGuard(page, crew, "crew", rapid);
    expect(nativeDialogs).toEqual([]);
  });
}

test("real Agent/Crew native consecutive history keeps drafts and restores exact entries", async ({
  page,
}) => {
  const { agent, crew } = await setupInstructions(page);
  const cdp = await page.context().newCDPSession(page);
  const native = async (direction: -1 | 1) => {
    const history = await cdp.send("Page.getNavigationHistory");
    const entry = history.entries[history.currentIndex + direction];
    expect(entry).toBeTruthy();
    await cdp.send("Page.navigateToHistoryEntry", { entryId: entry!.id });
    await expect(page).toHaveURL(entry!.url);
  };
  const snapshot = () =>
    page.evaluate(() => {
      const navigation = (
        window as unknown as Window & {
          navigation: { currentEntry: { key: string; index: number } };
        }
      ).navigation;
      return {
        url: location.href,
        state: history.state,
        length: history.length,
        nativeKey: navigation.currentEntry.key,
        nativeIndex: navigation.currentEntry.index,
      };
    });
  for (const kind of ["agent", "crew"] as const) {
    const entity = kind === "agent" ? agent : crew;
    const label = kind === "agent" ? "Agent 指令" : "小队指令";
    const parameter = kind === "agent" ? "agentSettings" : "crewSettings";
    const input = page.getByRole("textbox", { name: label, exact: true });
    for (const mode of [
      "back-cancel",
      "forward-cancel",
      "back-proceed",
      "forward-proceed",
      "mixed-proceed",
    ] as const) {
      // Five natural same-document entries: inbox → list → drawer → tasks → inbox.
      await page.goto(`/o/${org}/inbox`);
      await page.getByRole("link", { name: /^全部 Agent/ }).click();
      await page
        .getByRole("button", { name: entity.name, exact: true })
        .click();
      await expect(page).toHaveURL(new RegExp(`${parameter}=${entity.id}`));
      await page
        .getByRole("link", { name: "我的任务", exact: true })
        .first()
        .click();
      await page
        .getByRole("link", { name: "收件箱", exact: true })
        .first()
        .click();
      await native(-1);
      await native(-1);
      await page.getByRole("button", { name: new RegExp(`^${label}`) }).click();
      const draft = `${kind} ${mode} 中文草稿`;
      await input.fill(draft);
      if (kind === "agent") {
        await page.getByRole("button", { name: /^运行设置/ }).click();
        await page
          .getByRole("spinbutton", { name: "单任务最大运行时长" })
          .fill("73");
      }
      await input.evaluate((node) =>
        node.setAttribute("data-a01-mount", "retained"),
      );
      const original = await snapshot();
      const direction = mode.startsWith("forward") ? 1 : -1;
      await native(direction);
      await expect(
        page.getByRole("button", { name: "继续编辑", exact: true }),
      ).toBeVisible();
      const first = await snapshot();
      await native(mode === "mixed-proceed" ? 1 : direction);
      if (mode === "mixed-proceed") await native(1);
      expect((await snapshot()).nativeKey).not.toBe(first.nativeKey);
      await expect(input).toHaveValue(draft);
      await expect(input).toHaveAttribute("data-a01-mount", "retained");
      await expect(
        page.getByRole("button", { name: "继续编辑", exact: true }),
      ).toHaveCount(1);
      const cancel = mode.endsWith("cancel");
      await page
        .getByRole("button", {
          name: cancel ? "继续编辑" : "放弃修改",
          exact: true,
        })
        .click();
      await expect(
        page.getByRole("button", { name: "继续编辑", exact: true }),
      ).toHaveCount(0);
      await expect.poll(snapshot).toEqual(cancel ? original : first);
      if (cancel) {
        await expect(input).toHaveValue(draft);
        if (kind === "agent")
          await expect(
            page.getByRole("spinbutton", { name: "单任务最大运行时长" }),
          ).toHaveValue("73");
        await expect(input).toHaveAttribute("data-a01-mount", "retained");
        // Explicitly discard before the next fresh-document scenario.
        await page.getByRole("button", { name: "关闭", exact: true }).click();
        await page
          .getByRole("button", { name: "放弃修改", exact: true })
          .click();
        await expect(input).toHaveCount(0);
      } else await expect(input).toHaveCount(0);
    }
  }
});

for (const action of ["reload", "navigate", "close"] as const) {
  test(`held drafts retain actual beforeunload protection: ${action}`, async ({
    page,
  }) => {
    const { agent } = await setupInstructions(page);
    await page.goto(`/o/${org}/inbox`);
    await page.getByRole("link", { name: /^全部 Agent/ }).click();
    await page.getByRole("button", { name: agent.name, exact: true }).click();
    const original = page.url();
    await page.getByRole("button", { name: /^Agent 指令/ }).click();
    const input = page.getByRole("textbox", {
      name: "Agent 指令",
      exact: true,
    });
    await input.fill("protected on hard unload");
    await page.evaluate(() => history.back());
    await expect(
      page.getByRole("button", { name: "继续编辑", exact: true }),
    ).toBeVisible();
    const cdp = await page.context().newCDPSession(page);
    const dialog = page.waitForEvent("dialog", { timeout: 5000 });
    // CDP acknowledges the command. Do not wait for a load that cancellation
    // intentionally prevents, as page.reload()/goto() would do.
    const request =
      action === "reload"
        ? cdp.send("Page.reload")
        : action === "close"
          ? cdp.send("Page.close")
          : cdp.send("Page.navigate", {
              url: new URL("/login", page.url()).href,
            });
    const event = await dialog;
    expect(event.type()).toBe("beforeunload");
    await event.dismiss();
    await request;
    expect(page.isClosed()).toBe(false);
    await expect(input).toHaveValue("protected on hard unload");
    await page.getByRole("button", { name: "继续编辑", exact: true }).click();
    await expect(page).toHaveURL(original);
    const leavingDialog = page.waitForEvent("dialog", { timeout: 5000 });
    const leaving = cdp.send("Page.navigate", {
      url: new URL("/login", page.url()).href,
    });
    await (await leavingDialog).accept();
    await leaving;
    await expect(page).toHaveURL(/\/login$/);
  });
}

test("actual logout and expired authentication release dirty or held settings before the security redirect", async ({
  page,
}) => {
  for (const action of ["logout", "expired"] as const) {
    const { agent } = await setupInstructions(page);
    await page.goto(`/o/${org}/inbox`);
    await page.getByRole("link", { name: /^全部 Agent/ }).click();
    await page.getByRole("button", { name: agent.name, exact: true }).click();
    await page.getByRole("button", { name: /^Agent 指令/ }).click();
    const input = page.getByRole("textbox", {
      name: "Agent 指令",
      exact: true,
    });
    await input.fill(`security ${action}`);
    if (action === "logout") {
      await page
        .getByRole("button", { name: "alice@example.test", exact: true })
        .click();
      await page
        .getByRole("menuitem", { name: "退出登录", exact: true })
        .click();
    } else {
      await page.evaluate(() => history.back());
      await expect(
        page.getByRole("button", { name: "继续编辑", exact: true }),
      ).toBeVisible();
      await page.context().clearCookies();
      // The real me query has a 30-second stale time. Reconnect after that
      // boundary exercises a real 401, without replacing API responses.
      await page.waitForTimeout(31_000);
      const unauthorized = page.waitForResponse(
        (response) =>
          response.url().endsWith("/api/v1/me") && response.status() === 401,
      );
      await page.evaluate(() => {
        window.dispatchEvent(new Event("offline"));
        window.dispatchEvent(new Event("online"));
      });
      await unauthorized;
    }
    await expect(page).toHaveURL(/\/login(?:\?|$)/);
    await expect(input).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "继续编辑", exact: true }),
    ).toHaveCount(0);
  }
});

test("real Agent and Crew persist empty instructions after rich/source editing", async ({
  page,
}) => {
  const { agent, crew } = await setupInstructions(page);
  for (const kind of ["agent", "crew"] as const) {
    const entity = kind === "agent" ? agent : crew;
    const label = kind === "agent" ? "Agent 指令" : "小队指令";
    const parameter = kind === "agent" ? "agentSettings" : "crewSettings";
    const endpoint = `${base}/${kind === "agent" ? "agents" : "crews"}/${entity.id}`;
    const instructions = async () => {
      const saved = await request(page, endpoint);
      return kind === "agent" ? saved.config.instructions : saved.instructions;
    };
    await page.goto(`/o/${org}/agents?${parameter}=${entity.id}`);
    await page.getByRole("button", { name: new RegExp(`^${label}`) }).click();
    const input = page.getByRole("textbox", { name: label, exact: true });
    await input.fill("# Clear this 中文\n\n- one");
    await page.getByRole("button", { name: "富文本", exact: true }).click();
    await page.getByRole("button", { name: "源码", exact: true }).click();
    await page.getByRole("button", { name: "保存", exact: true }).click();
    await expect.poll(instructions).toContain("Clear this");
    await input.fill("");
    await page.getByRole("button", { name: "保存", exact: true }).click();
    await expect.poll(instructions).toBe("");
    await page.reload();
    await page.getByRole("button", { name: new RegExp(`^${label}`) }).click();
    await expect(input).toHaveValue("");
  }
});
