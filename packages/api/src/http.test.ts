import { expect, it } from "vitest";
import { HttpClient } from "./http";

it("retains the default JSON limit and bounds the larger review snapshot response", async () => {
  const transport = (size: number) =>
    (async () =>
      new Response(JSON.stringify({ body: "<".repeat(size) }))) as typeof fetch;
  const client = new HttpClient("/api", transport(3 * 1024 * 1024));
  await expect(client.request("/v1/review")).rejects.toMatchObject({
    code: "INVALID_RESPONSE",
  });
  await expect(
    client.request<{ body: string }>("/v1/review", {
      maxResponseBytes: 16 * 1024 * 1024,
    }),
  ).resolves.toHaveProperty("body.length", 3 * 1024 * 1024);
  const huge = new HttpClient("/api", transport(16 * 1024 * 1024));
  await expect(
    huge.request("/v1/review", { maxResponseBytes: 100 * 1024 * 1024 }),
  ).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
});
