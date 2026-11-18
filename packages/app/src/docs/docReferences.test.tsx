import { render, screen, within, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { expect, it, vi } from "vitest";
import type { DocNodeDetail } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";
import { createQueryClient } from "../App";
import { ApiProvider } from "../context";
import { CreateTaskProvider } from "../tasks/create/CreateTaskProvider";
import { backend, json } from "../tasks/test-backend";
import { message, summary } from "../conversations/fixtures";
import { DocActionsProvider, useDocActions } from "./DocActions";

it("does not flush or copy local drafts when opening, cancelling or confirming a document reference", async () => {
  const current: DocNodeDetail = {
    id: "d1",
    space_id: "s1",
    container: { kind: "personal" },
    parent_id: null,
    kind: "doc",
    title: "Local unsaved title",
    body: "Uncommitted private draft",
    position: 0,
    size_bytes: null,
    content_type: null,
    created_by: "u1",
    updated_by: "u1",
    updated_by_name: "user",
    created_at: "2026-11-13T00:00:00Z",
    updated_at: "2026-11-13T00:00:00Z",
    version: 1,
    has_children: false,
    can_edit: true,
    breadcrumb: [],
  };
  // A failed/dirty autosave must not be invoked by a passive pointer operation.
  const flush = vi.fn(async () => false);
  const server = backend((call) => {
    if (call.path.endsWith("/conversations"))
      return json(200, {
        conversations: [summary("c1", { offer_participant: true })],
        next_cursor: null,
      });
    if (call.path.endsWith("/references"))
      return json(
        201,
        message(1, { kind: "REFERENCE", conversation_id: "c1" }),
      );
    return undefined;
  });
  function Action() {
    const actions = useDocActions();
    return (
      <button onClick={() => actions.forward(current)}>
        Forward current document
      </button>
    );
  }
  render(
    <MemoryRouter>
      <I18nProvider initialLocale="zh-CN">
        <QueryClientProvider client={createQueryClient()}>
          <ApiProvider api={server.api}>
            <ToastProvider>
              <CreateTaskProvider orgId="o1">
                <DocActionsProvider
                  orgId="o1"
                  spaceId="s1"
                  current={current}
                  beforeCurrentChange={flush}
                >
                  <Action />
                </DocActionsProvider>
              </CreateTaskProvider>
            </ToastProvider>
          </ApiProvider>
        </QueryClientProvider>
      </I18nProvider>
    </MemoryRouter>,
  );
  const user = userEvent.setup();
  await user.click(
    screen.getByRole("button", { name: "Forward current document" }),
  );
  let dialog = await screen.findByRole("dialog", { name: "转发到会话" });
  await user.click(within(dialog).getByRole("button", { name: "取消" }));
  expect(server.find("POST", "/references")).toHaveLength(0);
  await user.click(
    screen.getByRole("button", { name: "Forward current document" }),
  );
  dialog = await screen.findByRole("dialog", { name: "转发到会话" });
  await user.click(
    await within(dialog).findByRole("radio", { name: "会话 c1" }),
  );
  await user.click(within(dialog).getByRole("button", { name: "确认转发" }));
  await waitFor(() =>
    expect(
      screen.queryByRole("dialog", { name: "转发到会话" }),
    ).not.toBeInTheDocument(),
  );
  expect(flush).not.toHaveBeenCalled();
  expect(server.find("POST", "/references")[0]!.body).toEqual({
    type: "DOC",
    doc_id: "d1",
    client_message_id: expect.any(String),
  });
  expect(
    server.calls.filter(
      (call) => call.method === "PATCH" || call.path.endsWith("/download"),
    ),
  ).toHaveLength(0);
});
