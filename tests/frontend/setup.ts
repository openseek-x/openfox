import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

import { resetDrafts } from "../../packages/app/src/conversations/drafts";

afterEach(cleanup);
// 输入框草稿存在模块级内存里，每个用例结束后清空，避免同一文件里的用例互相带草稿。
afterEach(resetDrafts);
