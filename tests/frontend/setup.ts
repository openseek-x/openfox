import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach } from "vitest";

import { resetDrafts } from "../../packages/app/src/conversations/drafts";

afterEach(cleanup);
beforeEach(() => {
  globalThis.indexedDB = new IDBFactory();
});
// 阶段 10a：输入框草稿存在模块级内存里，每个用例结束后清空，避免同一文件里的用例互相带草稿
afterEach(resetDrafts);
