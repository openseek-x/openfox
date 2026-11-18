import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createApi, HttpClient, type Run } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";
import { describe, expect, it, vi } from "vitest";
import { ApiProvider, keys } from "../../context";
import { run } from "../fixtures";
import { ScheduledRun } from "./ScheduledRun";
import { applyRunState } from "./queries";
function setup(value: Run, transport: typeof fetch = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const api = createApi(new HttpClient("/api", transport));
  render(
    <I18nProvider initialLocale="en-US">
      <ApiProvider api={api}>
        <QueryClientProvider client={client}>
          <ToastProvider>
            <ScheduledRun orgId="o1" run={value} />
          </ToastProvider>
        </QueryClientProvider>
      </ApiProvider>
    </I18nProvider>,
  );
  return client;
}
const scheduled = () =>
  run("r1", {
    state: "QUEUED",
    schedule_pending: true,
    not_before: null,
    can_trigger_now: true,
  });
describe("scheduled run consumer", () => {
  it("shows an indefinite wait without a permitted action", () => {
    setup({ ...scheduled(), can_trigger_now: false });
    expect(
      screen.getByText("No eligible window; waiting indefinitely"),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Trigger this run now" }),
    ).toBeNull();
  });
  it("confirms time bypass and keeps one idempotency key on a retry", async () => {
    const calls: RequestInit[] = [];
    let fail = true;
    const transport = vi.fn<typeof fetch>(async (_url, init) => {
      calls.push(init!);
      return new Response(
        JSON.stringify(
          fail
            ? { error: { code: "RETRY", message: "retry" } }
            : {
                run: { ...scheduled(), schedule_pending: false },
                replayed: false,
              },
        ),
        {
          status: fail ? 503 : 200,
          headers: { "Content-Type": "application/json" },
        },
      );
    });
    setup(scheduled(), transport);
    const user = userEvent.setup();
    await user.click(
      screen.getByRole("button", { name: "Trigger this run now" }),
    );
    expect(calls).toHaveLength(0);
    const dialog = screen.getByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: "Trigger this run now" }),
    );
    await screen.findByText(
      "Could not remove the deferral. Refresh and try again.",
    );
    fail = false;
    await user.click(
      within(dialog).getByRole("button", { name: "Trigger this run now" }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(calls).toHaveLength(2);
    const first = new Headers(calls[0]!.headers).get("Idempotency-Key");
    expect(first).toBeTruthy();
    expect(new Headers(calls[1]!.headers).get("Idempotency-Key")).toBe(first);
  });
  it("invalidates the same run for scheduled timestamp events and admission", () => {
    const client = new QueryClient();
    const key = keys.run("o1", "r1");
    client.setQueryData(key, scheduled());
    const invalidate = vi.spyOn(client, "invalidateQueries");
    applyRunState(client, "o1", {
      id: "r1",
      version: 2,
      payload: { state: "QUEUED", wait_reason: "SCHEDULED" },
    });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: key, exact: true });
    invalidate.mockClear();
    applyRunState(client, "o1", {
      id: "r1",
      version: 3,
      payload: { state: "QUEUED", wait_reason: "COMPUTER_OFFLINE" },
    });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: key, exact: true });
  });
});
