import type { Page } from "@playwright/test";
import {
  agent,
  device,
  entry,
} from "../../../packages/app/src/agents/fixtures";
import type {
  MessageCreateInput,
  RunArtifact,
} from "../../../packages/api/src";

export async function mockArtifactReview(
  page: Page,
  file: RunArtifact,
  data: Buffer | string,
) {
  const base = "/api/v1/organizations/o1";
  const state: {
    sent: MessageCreateInput[];
    artifacts: RunArtifact[];
    onSend?: () => void;
  } = { sent: [], artifacts: [file] };
  const chat = {
    organization_id: "o1",
    id: "c1",
    space_id: "s1",
    agent_id: "a1",
    owner_user_id: "u1",
    visibility: "PRIVATE",
    lifecycle: "OPEN",
    handoff: "NONE",
    origin: "USER",
    version: 1,
    created_at: file.created_at,
    updated_at: file.created_at,
    title: "Review",
    archived_at: null,
    pinned: false,
    last_read_seq: 0,
    unread: false,
    last_message: null,
    agent: {
      id: "a1",
      name: "熊猫",
      title: "产品经理",
      avatar: null,
      online: true,
    },
    task: null,
    run: null,
    status: "IDLE",
    overrides: { runtime: null, model: null, approval_mode: null, version: 0 },
    effective: {
      runtime: "codex",
      model: "default",
      approval_mode: "READ_ONLY",
    },
    agent_defaults: {
      runtime: "codex",
      model: "default",
      approval_mode: "READ_ONLY",
    },
  };
  await page.addInitScript(() => {
    if (window !== window.top) return;
    localStorage.setItem(
      "openfox:onboarding:o1",
      JSON.stringify({ step: "done" }),
    );
  });
  await page.route("**/api/**", async (route) => {
    const req = route.request(),
      path = new URL(req.url()).pathname;
    let body: unknown = {},
      status = 200;
    if (path === `${base}/runs/r1/artifacts/a1`) {
      await route.fulfill({ contentType: file.mime_type, body: data });
      return;
    }
    if (path === "/api/v1/me") body = { id: "u1", email: "test@example.test" };
    else if (path === "/api/v1/me/organizations")
      body = {
        organizations: [{ id: "o1", slug: "qa", name: "QA", role: "OWNER" }],
      };
    else if (path === "/api/v1/me/preferences")
      body = {
        locale: "zh-CN",
        theme: "light",
        outline_color: 0,
        send_shortcut: "enter",
        command_shortcuts: {},
        version: 1,
      };
    else if (path === `${base}/spaces`)
      body = {
        spaces: [
          {
            id: "s1",
            name: "研发",
            prefix: "DEV",
            visibility: "shared",
            created_at: file.created_at,
          },
        ],
      };
    else if (path === `${base}/agent-directory`)
      body = {
        entries: [entry("a1", { name: "熊猫" })],
        archived_count: 0,
        truncated: false,
      };
    else if (path === `${base}/devices`)
      body = { devices: [device("d1")], next_cursor: null };
    else if (path === `${base}/spaces/s1/agents/a1`) body = agent("a1");
    else if (path === `${base}/conversations/c1`) body = chat;
    else if (path === `${base}/conversations`)
      body = { conversations: [chat], next_cursor: null };
    else if (path === `${base}/conversations/c1/proposals`)
      body = { proposals: [], next_cursor: "" };
    else if (path === `${base}/conversations/c1/messages`) {
      if (req.method() === "POST") {
        const input = req.postDataJSON() as MessageCreateInput;
        state.sent.push(input);
        state.onSend?.();
        status = 201;
        body = {
          id: "m1",
          seq: 1,
          kind: "TEXT",
          author_kind: "USER",
          author_user_id: "u1",
          conversation_id: "c1",
          body: input.body,
          attachments: [],
          created_at: file.created_at,
          run_id: null,
        };
      } else body = { messages: [], next_cursor: 0 };
    } else if (path === `${base}/conversations/c1/artifacts`)
      body = { artifacts: state.artifacts };
    else if (path.endsWith("/direct-chats"))
      body = { chats: [], next_before: "" };
    else if (path.endsWith("/inbox/stats"))
      body = {
        counts: {
          inbox: 0,
          inbox_unread: 0,
          inbox_important_unread: 0,
          inbox_other_unread: 0,
          later: 0,
          cleared: 0,
        },
      };
    else {
      status = 404;
      body = {
        error: { code: "NOT_FOUND", message: "fixture", request_id: "qa" },
      };
    }
    await route.fulfill({ status, json: body });
  });
  return state;
}
