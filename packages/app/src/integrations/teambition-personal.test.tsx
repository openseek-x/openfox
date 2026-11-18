import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiError, json, renderSettings } from "../settings/testing";

const endpoint =
  "/v1/organizations/o1/me/account-bindings/teambition/sessions/s1";
const path =
  "/integrations/oauth/complete?provider=teambition&organization=o1&session=s1&status=ready";

describe("Teambition personal confirmation", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => vi.restoreAllMocks());
  it("requires the initiating member to explicitly confirm the verified identity", async () => {
    const calls = renderSettings(
      path,
      (call) => {
        if (call.path === endpoint && call.method === "GET")
          return json(200, {
            id: "s1",
            status: "SELECTING",
            error_code: "",
            expires_at: "2026-11-14T12:00:00Z",
            identity: { id: "remote", name: "本人", email: "lin@openfox.dev" },
          });
        if (call.path === `${endpoint}/complete`)
          return json(200, { id: "binding" });
        return undefined;
      },
      "MEMBER",
    );
    expect(await screen.findByText("本人 · lin@openfox.dev")).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    await userEvent.click(
      screen.getByRole("button", { name: "确认绑定此身份" }),
    );
    expect(await screen.findByText("已连接")).toBeVisible();
    expect(calls.filter((c) => c.path === `${endpoint}/complete`)).toHaveLength(
      1,
    );
    expect(screen.queryByRole("button", { name: "确认绑定此身份" })).toBeNull();
  });
  it("shows an already completed session after refresh without another write", async () => {
    const calls = renderSettings(path, (call) =>
      call.path === endpoint
        ? json(200, {
            id: "s1",
            status: "COMPLETED",
            binding_id: "binding",
            error_code: "",
          })
        : undefined,
    );
    expect(await screen.findByText("已连接")).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });
  it("reports actual failed identity status even when the return URL says ready", async () => {
    renderSettings(path, (call) =>
      call.path === endpoint
        ? json(200, {
            id: "s1",
            status: "FAILED",
            error_code: "account_mismatch",
          })
        : undefined,
    );
    expect(
      await screen.findByText(/企业成员邮箱必须与当前 OpenFox 本人邮箱一致/),
    ).toBeVisible();
    expect(screen.queryByRole("button", { name: "确认绑定此身份" })).toBeNull();
  });
  it("keeps failed final confirmation visible and does not report success to the opener", async () => {
    const postMessage = vi.fn();
    vi.stubGlobal("opener", { postMessage });
    renderSettings(path, (call) => {
      if (call.path === endpoint)
        return json(200, {
          id: "s1",
          status: "SELECTING",
          identity: { name: "本人", email: "lin@openfox.dev" },
        });
      if (call.path === `${endpoint}/complete`)
        return apiError(409, "VERSION_CONFLICT");
      return undefined;
    });
    await userEvent.click(
      await screen.findByRole("button", { name: "确认绑定此身份" }),
    );
    await waitFor(() =>
      expect(
        screen.getByText("授权未完成或已失效，请返回重新发起。"),
      ).toBeVisible(),
    );
    expect(postMessage).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });
});
