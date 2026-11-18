// Shared real-backend acceptance: verify the source is intact before the user
// accepts through the production UI. No API interception or model mocks.
import { expect } from "@playwright/test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
export async function acceptProposal(page, context, f) {
  const base = `${f.origin}/api/v1/organizations/${f.organization}`;
  let proposal;
  await expect
    .poll(
      async () => {
        const r = await context.request.get(
          `${base}/conversations/${f.conversation}/proposals`,
        );
        assert.equal(r.status(), 200);
        proposal = (await r.json()).proposals.find(
          (p) => p.status === "PENDING",
        );
        return Boolean(proposal);
      },
      { timeout: 300000, intervals: [500, 1000, 2000] },
    )
    .toBe(true);
  const original = await context.request.get(
    proposal.source.node_id
      ? `${base}/spaces/${f.space}/docs/${f.node}`
      : `${base}/runs/${proposal.base.run_id}/artifacts/${proposal.source.artifact_id}`,
  );
  assert.equal(original.status(), 200);
  const originalBytes = proposal.source.node_id
    ? Buffer.from((await original.json()).body)
    : await original.body();
  assert.equal(
    createHash("sha256").update(originalBytes).digest("hex"),
    proposal.source.sha256,
  );
  await page.getByRole("button", { name: "更多操作", exact: true }).click();
  await page.getByRole("menuitem", { name: "修改提案", exact: true }).click();
  await page
    .getByRole("button", { name: `${proposal.base.name} 待审阅`, exact: true })
    .click({ timeout: 15000 });
  await expect(
    page.getByText("原件尚未修改，审阅后决定是否采用。", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "原件", exact: true }).click();
  await page.getByRole("button", { name: "候选", exact: true }).click();
  const response = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/proposals/${proposal.id}/decision`) &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "接受修改", exact: true }).click();
  assert.equal((await response).status(), 200);
  await expect(
    page.getByText("修改已作为新版本应用。", { exact: true }),
  ).toBeVisible();
  return proposal;
}
