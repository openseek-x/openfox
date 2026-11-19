import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { createApi, HttpClient } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ApiProvider } from "../context";
import { RuntimeDefaultsEditor } from "./RuntimeDefaultsEditor";

function mount(secretsAvailable = true, conflict = false) {
  const calls: Record<string, unknown>[] = [];
  let version = 2;
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const transport = async (_input: RequestInfo | URL, init?: RequestInit) => {
    if (init?.method === "PATCH") {
      const body = JSON.parse(String(init.body));
      calls.push(body);
      if (conflict) {
        version = 3;
        return new Response(
          JSON.stringify({
            error: { code: "VERSION_CONFLICT", message: "conflict" },
          }),
          { status: 409 },
        );
      }
    }
    return new Response(
      JSON.stringify({
        version,
        model: "machine-model",
        secrets_available: secretsAvailable,
        env_vars: [
          {
            name: "SAVED",
            has_value: true,
            version: 2,
            updated_at: "2026-11-13T01:00:00Z",
          },
        ],
      }),
      { status: 200 },
    );
  };
  render(
    <QueryClientProvider client={client}>
      <I18nProvider initialLocale="zh-CN">
        <ApiProvider api={createApi(new HttpClient("/api", transport))}>
          <RuntimeDefaultsEditor orgId="o" deviceId="d" runtime="pi" />
        </ApiProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );
  return calls;
}
describe("machine Runtime defaults", () => {
  it("edits masked metadata, keeps empty values explicit, and sends one revisioned atomic batch", async () => {
    const calls = mount();
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Runtime 默认配置" }));
    const dialog = await screen.findByRole("dialog");
    await waitFor(() =>
      expect(within(dialog).getByLabelText("机器默认模型")).toHaveValue(
        "machine-model",
      ),
    );
    expect(within(dialog).getByText("••••••••")).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "替换" }));
    expect(within(dialog).getByLabelText("新秘密值")).toHaveValue("");
    await user.click(within(dialog).getByRole("button", { name: "应用变量" }));
    await user.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0]).toMatchObject({
      expected_version: 2,
      model: "machine-model",
      set_env: { SAVED: "" },
      remove_env: [],
    });
  });
  it("retains draft after conflict and reloads a fresh revision when reopened", async () => {
    const calls = mount(true, true);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Runtime 默认配置" }));
    await user.clear(await screen.findByLabelText("机器默认模型"));
    await user.type(screen.getByLabelText("机器默认模型"), "new-model");
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(await screen.findByText(/配置已被其他请求修改/)).toBeVisible();
    expect(screen.getByLabelText("机器默认模型")).toHaveValue("new-model");
    await user.click(screen.getByRole("button", { name: "取消" }));
    await user.click(screen.getByRole("button", { name: "Runtime 默认配置" }));
    await waitFor(() =>
      expect(screen.getByLabelText("机器默认模型")).toHaveValue(
        "machine-model",
      ),
    );
    await user.clear(screen.getByLabelText("机器默认模型"));
    await user.type(screen.getByLabelText("机器默认模型"), "retry-model");
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[1]?.expected_version).toBe(3);
  });
  it("allows model edits without a key but disables new secret input", async () => {
    mount(false);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Runtime 默认配置" }));
    expect(await screen.findByLabelText("变量名称")).toBeDisabled();
    expect(screen.getByLabelText("新秘密值")).toBeDisabled();
    expect(screen.getByLabelText("机器默认模型")).toBeEnabled();
  });
});
