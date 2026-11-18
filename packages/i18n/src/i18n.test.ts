import { describe, expect, it } from "vitest";

import {
  createTranslator,
  dictionaries,
  interpolate,
  matchLocale,
} from "./index";

function leafPaths(node: unknown, prefix = ""): string[] {
  if (typeof node === "string") return [prefix];
  return Object.entries(node as Record<string, unknown>).flatMap(
    ([key, value]) => leafPaths(value, prefix ? `${prefix}.${key}` : key),
  );
}

describe("i18n", () => {
  it("每种语言的键与简体中文完全一致且没有空文案", () => {
    const base = leafPaths(dictionaries["zh-CN"]).sort();
    for (const [locale, messages] of Object.entries(dictionaries)) {
      expect(leafPaths(messages).sort(), locale).toEqual(base);
      for (const path of base)
        expect(
          createTranslator(locale as never)(path as never).trim(),
          `${locale}:${path}`,
        ).not.toBe("");
    }
  });

  it("占位符的个数在各语言之间一致", () => {
    const zh = createTranslator("zh-CN");
    const en = createTranslator("en-US");
    for (const path of leafPaths(dictionaries["zh-CN"])) {
      const names = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort();
      expect(names(en(path as never)), path).toEqual(names(zh(path as never)));
    }
  });

  it("替换占位符，缺参数时保留原样", () => {
    expect(interpolate("已发送验证码至 {email}", { email: "a@b.c" })).toBe(
      "已发送验证码至 a@b.c",
    );
    expect(interpolate("{seconds}s 后", {})).toBe("{seconds}s 后");
  });

  it("按语言返回文案，未知键回退为键名", () => {
    expect(createTranslator("en-US")("auth.login")).toBe("Sign in");
    expect(createTranslator("zh-CN")("auth.login")).toBe("登录");
    expect(createTranslator("en-US")("missing.key" as never)).toBe(
      "missing.key",
    );
  });

  it("把浏览器语言映射到支持的语言", () => {
    expect(matchLocale("zh-Hans-CN")).toBe("zh-CN");
    expect(matchLocale("zh-TW")).toBe("zh-CN");
    expect(matchLocale("en-GB")).toBe("en-US");
    expect(matchLocale("ja-JP")).toBeNull();
  });
});
