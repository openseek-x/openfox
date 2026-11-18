import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import {
  apiError,
  json,
  renderSettings,
  ts,
  type Route,
} from "../settings/testing";
const base = "/v1/organizations/o1/integrations";
function route(manage = true, more?: Route): Route {
  return (call) =>
    more?.(call) ??
    (call.path === `${base}/yunxiao`
      ? json(200, {
          provider: {
            id: "yunxiao",
            name: "云效",
            category: "project",
            availability: "available",
            template: "C",
            configured: true,
            bindable: true,
            capabilities: [],
            summary: { connections: 1, status: "ok" },
          },
          can_manage: manage,
          connections: [
            {
              id: "c1",
              provider: "yunxiao",
              scope: "ORGANIZATION",
              display_name: "Cloud",
              external_id: "cloud",
              status: "ACTIVE",
              version: 3,
              created_at: ts,
              updated_at: ts,
            },
          ],
          personal_connections: [],
          sync_links: [],
        })
      : undefined);
}
it("member verifies identity separately and closing clears PAT", async () => {
  const calls = renderSettings(
    "/o/o1/settings/integrations/yunxiao",
    route(false),
    "MEMBER",
  );
  const user = userEvent.setup();
  await user.click(
    await screen.findByRole("button", { name: "验证我的云效身份" }),
  );
  expect(
    screen.queryByRole("button", { name: "更新云效组织 PAT" }),
  ).not.toBeInTheDocument();
  let dialog = screen.getByRole("dialog");
  await user.type(
    within(dialog).getByLabelText("访问令牌"),
    "synthetic-private",
  );
  await user.click(within(dialog).getByRole("button", { name: "取消" }));
  await user.click(screen.getByRole("button", { name: "验证我的云效身份" }));
  dialog = screen.getByRole("dialog");
  expect(within(dialog).getByLabelText("访问令牌")).toHaveValue("");
  expect(calls.some((c) => c.method !== "GET")).toBe(false);
});
it("same-holder credential update sends CAS and keeps a failed save visible", async () => {
  const calls = renderSettings(
    "/o/o1/settings/integrations/yunxiao",
    route(true, (c) =>
      c.method === "PUT" ? apiError(409, "VERSION_CONFLICT") : undefined,
    ),
  );
  const user = userEvent.setup();
  await user.click(
    await screen.findByRole("button", { name: "更新云效组织 PAT" }),
  );
  const d = within(screen.getByRole("dialog"));
  await user.click(d.getByLabelText("实例"));
  await user.click(screen.getByRole("option", { name: "Cloud" }));
  await user.type(d.getByLabelText("访问令牌"), "synthetic-new");
  await user.click(d.getByRole("button", { name: "保存" }));
  await waitFor(() =>
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      version: 3,
      token: "synthetic-new",
    }),
  );
  expect(screen.getByRole("dialog")).toBeVisible();
});
