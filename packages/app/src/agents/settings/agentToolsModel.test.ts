import { describe, expect, it } from "vitest";
import {
  parseToolJson,
  draftToJson,
  validToolName,
  validToolUrl,
} from "./agentToolsModel";

describe("MCP Server 配置", () => {
  it("保留 STDIO 参数顺序、别名和编辑时的额外 JSON 字段", () => {
    const parsed = parseToolJson(
      JSON.stringify({
        mcpServers: {
          first: {
            command: ["cmd", "one", "two"],
            environment: { API_KEY: "test-value" },
            options: { mode: "example" },
          },
          second: { url: "https://example.test/mcp" },
        },
      }),
      "",
    );
    expect(parsed).toMatchObject({
      name: "first",
      count: 2,
      draft: {
        transport: "stdio",
        command: "cmd",
        args: ["one", "two"],
        env: [{ key: "API_KEY", value: "test-value" }],
      },
    });
    if ("draft" in parsed)
      expect(JSON.parse(draftToJson(parsed.draft, parsed.extras))).toEqual({
        command: "cmd",
        args: ["one", "two"],
        env: { API_KEY: "test-value" },
        options: { mode: "example" },
      });
  });
  it.each([
    ["[]", "object"],
    ['{"mcpServers":{}}', "empty"],
    ['{"url":"https://example.test","type":"sse"}', "sse"],
    ['{"env":{}}', "target"],
    ["[", "invalidJson"],
  ])("拒绝 %s", (json, error) =>
    expect(parseToolJson(json!, "")).toEqual({ error }),
  );
  it("公开查询需 HTTPS，脱敏串仅允许保留在原地址，示例密钥不可提交", () => {
    expect(validToolUrl("https://example.test/mcp")).toBe(true);
    expect(validToolUrl("http://localhost:3000/mcp")).toBe(false);
    expect(
      validToolUrl("http://localhost:3000/mcp", { deviceOnly: true }),
    ).toBe(true);
    expect(validToolUrl("https://example.test/mcp?key=real")).toBe(true);
    expect(
      validToolUrl("https://example.test/mcp?key=real", { oauth: true }),
    ).toBe(false);
    expect(
      validToolUrl("http://localhost:3000/mcp?key=x", { deviceOnly: true }),
    ).toBe(false);
    const redacted = "https://example.test/mcp?••••••";
    expect(validToolUrl(redacted)).toBe(false);
    expect(validToolUrl(redacted, { savedUrl: redacted })).toBe(true);
    expect(
      validToolUrl("https://other.test/mcp?••••••", { savedUrl: redacted }),
    ).toBe(false);
    const example = "https://example.test/mcp?key=<YOUR_KEY>";
    expect(validToolUrl(example, { example })).toBe(false);
    expect(validToolUrl("https://example.test/mcp", { example })).toBe(false);
    expect(validToolUrl("https://example.test/mcp?key=", { example })).toBe(
      false,
    );
    expect(validToolUrl("https://example.test/mcp?key=test", { example })).toBe(
      true,
    );
    expect(validToolUrl("https://example.test/mcp?")).toBe(false);
    expect(validToolUrl("https://example.test/mcp#section")).toBe(false);
    expect(validToolUrl("file:///etc/hosts")).toBe(false);
    expect(validToolUrl("https://user:secret@example.test/mcp")).toBe(false);
    expect(validToolName("work-mcp-1")).toBe(true);
    expect(validToolName("Bad name")).toBe(false);
  });
});
