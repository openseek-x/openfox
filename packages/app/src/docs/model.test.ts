import { describe, expect, it } from "vitest";

import {
  beforeIdFor,
  clampNavWidth,
  containerKey,
  docLink,
  docsPath,
  dropZone,
  isDocSetNameValid,
  isMoveTargetBlocked,
  nameLength,
  planFolderUpload,
  PERSONAL_CONTAINER,
  setContainer,
} from "./model";
import { runLimited } from "./upload";

// 文档库纯函数：名称长度、导航宽度、拖拽落点、移动禁用、同级插入、上传目录规划与并发上限。

function fileAt(path: string): File {
  const file = new File(["x"], path.split("/").pop()!);
  Object.defineProperty(file, "webkitRelativePath", { value: path });
  return file;
}

describe("名称与导航宽度", () => {
  it("名称按字符计数，代理对按一个字计", () => {
    expect(nameLength("产品")).toBe(2);
    expect(nameLength("𠀀a")).toBe(2);
  });

  it("文档集名称去掉空白后不能为空，且不超过 64 个字", () => {
    expect(isDocSetNameValid("   ")).toBe(false);
    expect(isDocSetNameValid("a".repeat(64))).toBe(true);
    expect(isDocSetNameValid("a".repeat(65))).toBe(false);
  });

  it("导航宽度限制在 240–480，非数值回到默认 300", () => {
    expect(clampNavWidth(100)).toBe(240);
    expect(clampNavWidth(999)).toBe(480);
    expect(clampNavWidth(301.4)).toBe(301);
    expect(clampNavWidth(Number.NaN)).toBe(300);
  });
});

describe("拖拽落点", () => {
  it("上下 30% 为前后插入，中间为放入", () => {
    expect(dropZone(10, 100, true)).toBe("before");
    expect(dropZone(50, 100, true)).toBe("inside");
    expect(dropZone(90, 100, true)).toBe("after");
  });

  it("不能放入的目标（文档）中间区域无效", () => {
    expect(dropZone(50, 100, false)).toBeNull();
    expect(dropZone(10, 100, false)).toBe("before");
  });

  it("高度为 0 时无落点", () => {
    expect(dropZone(0, 0, true)).toBeNull();
  });
});

describe("移动到的禁用规则", () => {
  it("自身与路径上经过被移动对象的子孙都禁用", () => {
    expect(isMoveTargetBlocked("a", "a", [])).toBe(true);
    expect(isMoveTargetBlocked("a", "c", ["a", "b"])).toBe(true);
    expect(isMoveTargetBlocked("a", "c", ["b"])).toBe(false);
  });
});

describe("同级前后插入", () => {
  const siblings = [{ id: "1" }, { id: "2" }, { id: "3" }];

  it("插在目标之前：before_id 就是目标", () => {
    expect(beforeIdFor(siblings, "2", "before", "9")).toBe("2");
  });

  it("插在目标之后：取下一个非被拖拽的兄弟，没有则为空（放到最后）", () => {
    expect(beforeIdFor(siblings, "1", "after", "9")).toBe("2");
    expect(beforeIdFor(siblings, "1", "after", "2")).toBe("3");
    expect(beforeIdFor(siblings, "3", "after", "9")).toBeNull();
  });

  it("目标不在兄弟列表里时放到最后", () => {
    expect(beforeIdFor(siblings, "x", "after", "9")).toBeNull();
  });
});

describe("上传目录规划", () => {
  it("目录按层级排序，父目录在前，文件带所在目录", () => {
    const plan = planFolderUpload([
      fileAt("包/子/b.md"),
      fileAt("包/a.md"),
      fileAt("包/子/深/c.csv"),
    ]);
    expect(plan.directories).toEqual([
      { path: "包", name: "包", parent: "" },
      { path: "包/子", name: "子", parent: "包" },
      { path: "包/子/深", name: "深", parent: "包/子" },
    ]);
    expect(plan.files.map((item) => [item.file.name, item.dir])).toEqual([
      ["b.md", "包/子"],
      ["a.md", "包"],
      ["c.csv", "包/子/深"],
    ]);
  });

  it("没有相对路径时按普通文件处理（根目录）", () => {
    const plan = planFolderUpload([new File(["x"], "solo.md")]);
    expect(plan.directories).toEqual([]);
    expect(plan.files).toEqual([{ file: expect.any(File), dir: "" }]);
  });
});

describe("runLimited", () => {
  it("同时运行的任务不超过上限，并统计失败数量", async () => {
    let running = 0;
    let peak = 0;
    const failed = await runLimited([1, 2, 3, 4, 5, 6], 4, async (item) => {
      running++;
      peak = Math.max(peak, running);
      await new Promise((resolve) => setTimeout(resolve, 5));
      running--;
      if (item % 3 === 0) throw new Error("boom");
    });
    expect(peak).toBeLessThanOrEqual(4);
    expect(failed).toBe(2);
  });

  it("没有任务时直接返回 0", async () => {
    expect(await runLimited([], 4, async () => undefined)).toBe(0);
  });
});

describe("路由与链接", () => {
  it("文档库路由与文档链接", () => {
    expect(docsPath("o1", "s1")).toBe("/o/o1/spaces/s1/docs");
    expect(docsPath("o1", "s1", "d1")).toBe("/o/o1/spaces/s1/docs/d1");
    expect(docLink("https://x.test", "o1", "s1", "d1")).toBe(
      "https://x.test/o/o1/spaces/s1/docs/d1",
    );
  });

  it("容器键：我的文档与文档集区分", () => {
    expect(containerKey(PERSONAL_CONTAINER)).toBe("personal");
    expect(containerKey(setContainer("set1"))).toBe("set:set1");
  });
});
