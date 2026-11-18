import { afterEach, describe, expect, it } from "vitest";
import { docPageWidthKey, readDocPageWidth } from "./pagePreferences";

afterEach(() => localStorage.clear());
describe("文档页宽个人偏好", () => {
  it("旧数据和非法值保持标准宽度，按用户隔离", () => {
    expect(readDocPageWidth("u1")).toBe("standard");
    localStorage.setItem(docPageWidthKey("u1"), "wide");
    expect(readDocPageWidth("u1")).toBe("wide");
    expect(readDocPageWidth("u2")).toBe("standard");
    localStorage.setItem(docPageWidthKey("u1"), "unexpected");
    expect(readDocPageWidth("u1")).toBe("standard");
  });
});
