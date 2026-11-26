import { afterEach, describe, expect, it, vi } from "vitest";

import {
  clearSettings,
  loadSettings,
  originPermissionPattern,
  parseServerUrl,
  saveSettings,
  watchSettings,
} from "./config";

describe("parseServerUrl", () => {
  it("接受 https 地址并返回规范化 origin", () => {
    expect(parseServerUrl(" https://openfox.example.com ")).toEqual({
      ok: true,
      origin: "https://openfox.example.com",
    });
    expect(parseServerUrl("https://openfox.example.com:8443/")).toEqual({
      ok: true,
      origin: "https://openfox.example.com:8443",
    });
  });

  it("本机开发允许 http 的 localhost 与 127.0.0.1", () => {
    expect(parseServerUrl("http://localhost:8080").ok).toBe(true);
    expect(parseServerUrl("http://127.0.0.1:8080").ok).toBe(true);
  });

  it("拒绝其他 http 地址、非 http 协议与空值", () => {
    expect(parseServerUrl("http://openfox.example.com").ok).toBe(false);
    expect(parseServerUrl("http://192.168.1.2:8080").ok).toBe(false);
    expect(parseServerUrl("ftp://openfox.example.com").ok).toBe(false);
    expect(parseServerUrl("javascript:alert(1)").ok).toBe(false);
    expect(parseServerUrl("").ok).toBe(false);
    expect(parseServerUrl("不是地址").ok).toBe(false);
  });

  it("拒绝带账号、路径、查询串或片段的地址", () => {
    expect(parseServerUrl("https://user:pass@openfox.example.com").ok).toBe(
      false,
    );
    expect(parseServerUrl("https://openfox.example.com/app").ok).toBe(false);
    expect(parseServerUrl("https://openfox.example.com/?a=1").ok).toBe(false);
    expect(parseServerUrl("https://openfox.example.com/#x").ok).toBe(false);
  });
});

describe("originPermissionPattern", () => {
  it("生成不含端口的 host 权限匹配模式", () => {
    expect(originPermissionPattern("https://openfox.example.com:8443")).toBe(
      "https://openfox.example.com/*",
    );
    expect(originPermissionPattern("http://localhost:8080")).toBe(
      "http://localhost/*",
    );
  });
});

describe("连接配置存储", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  function stubStorage(initial: Record<string, unknown> = {}) {
    const data = { ...initial };
    vi.stubGlobal("chrome", {
      storage: {
        local: {
          get: async (key: string) => (key in data ? { [key]: data[key] } : {}),
          set: async (items: Record<string, unknown>) => {
            Object.assign(data, items);
          },
          remove: async (key: string) => {
            delete data[key];
          },
        },
      },
    });
    return data;
  }

  it("保存后能读回，清除后为空", async () => {
    stubStorage();
    expect(await loadSettings()).toBeNull();
    await saveSettings({ serverOrigin: "https://a.example.com", token: "t" });
    expect(await loadSettings()).toEqual({
      serverOrigin: "https://a.example.com",
      token: "t",
    });
    await clearSettings();
    expect(await loadSettings()).toBeNull();
  });

  it("存储内容结构不对时视为未配置", async () => {
    stubStorage({ "openfox.connection": { serverOrigin: 1, token: null } });
    expect(await loadSettings()).toBeNull();
    stubStorage({ "openfox.connection": { serverOrigin: "", token: "t" } });
    expect(await loadSettings()).toBeNull();
  });
  it("另一窗口清除令牌后，旧的初次读取结果不能恢复已断开的连接", async () => {
    let listener!: chrome.storage.ChangeListener;
    let resolveRead!: (value: unknown) => void;
    const removeListener = vi.fn();
    vi.stubGlobal("chrome", {
      storage: {
        local: {
          get: () =>
            new Promise((resolve) => {
              resolveRead = resolve;
            }),
        },
        onChanged: {
          addListener: (value: chrome.storage.ChangeListener) => {
            listener = value;
          },
          removeListener,
        },
      },
    });
    const change = vi.fn();
    const stop = watchSettings(change);
    listener({ "openfox.connection": {} }, "local");
    resolveRead({
      "openfox.connection": {
        serverOrigin: "https://test.example",
        token: "test",
      },
    });
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(change.mock.calls).toEqual([[null]]);
    stop();
    expect(removeListener).toHaveBeenCalledWith(listener);
  });
});
