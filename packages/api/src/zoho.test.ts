import { expect, it, vi } from "vitest";
import { HttpClient } from "./http";
import { zohoApi, zohoAuthorizeURL } from "./zoho";
it("accepts only the fixed US authorize endpoint", () => {
  expect(
    zohoAuthorizeURL(
      "https://accounts.zoho.com/oauth/v2/auth?state=controlled",
    ),
  ).toBe(true);
  for (const raw of [
    "http://accounts.zoho.com/oauth/v2/auth",
    "https://accounts.zoho.com.evil.test/oauth/v2/auth",
    "https://u:p@accounts.zoho.com/oauth/v2/auth",
    "https://accounts.zoho.eu/oauth/v2/auth",
    "https://accounts.zoho.com/oauth/v2/auth#x",
    "javascript:alert(1)",
  ])
    expect(zohoAuthorizeURL(raw)).toBe(false);
});
it("sends exact methods, pagination and decimal project IDs with a stable import key", async () => {
  const calls: { url: string; init?: RequestInit }[] = [];
  const transport = vi.fn(
    async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: String(input), init });
      return new Response("{}", {
        headers: { "Content-Type": "application/json" },
      });
    },
  );
  const api = zohoApi(new HttpClient("/api", transport));
  await api.start("o", { purpose: "personal", connection_id: "c", version: 0 });
  await api.complete("o", "session");
  await api.complete("o", "session", "9007199254740993123");
  await api.projects("o", "c", 101);
  await api.createImport(
    "o",
    "c",
    "9007199254740993123",
    "s",
    "stable_test_key",
  );
  expect(calls[0]?.url).toContain("/integrations/zoho_bugtracker/oauth/start");
  expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({
    purpose: "personal",
    connection_id: "c",
    version: 0,
  });
  expect(JSON.parse(String(calls[1]?.init?.body))).toEqual({});
  expect(JSON.parse(String(calls[2]?.init?.body))).toEqual({
    portal_id: "9007199254740993123",
  });
  expect(calls[3]?.url).toContain("index=101");
  expect(JSON.parse(String(calls[4]?.init?.body))).toEqual({
    connection_id: "c",
    project_id: "9007199254740993123",
    space_id: "s",
  });
  expect(new Headers(calls[4]?.init?.headers).get("Idempotency-Key")).toBe(
    "stable_test_key",
  );
});
