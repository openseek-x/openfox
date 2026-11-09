import type { Device } from "@openfox/api";
import { describe, expect, it } from "vitest";

import {
  argsToRows,
  filterDirectory,
  groupDirectory,
  hoursToSeconds,
  isSupportedRuntime,
  isValidEmail,
  mergeEmails,
  minutesToSeconds,
  parseContact,
  parseInRange,
  QUEUE_TIMEOUT_HOURS,
  RUN_TIMEOUT_MINUTES,
  rowsToArgs,
  runtimeChoices,
  runtimeLabel,
  secondsToHours,
  secondsToMinutes,
  splitArgs,
  splitEmails,
  validDirectoryRoot,
} from "./model";
import { device, entry } from "./fixtures";

describe("目录筛选与分组", () => {
  const entries = [
    entry("a1", { name: "熊孩子", title: "AI 工程师", pinned: true }),
    entry("a2", {
      name: "架构师",
      space_id: "s2",
      space_name: "OpenFox",
      mine: false,
    }),
    entry("c1", {
      type: "crew",
      name: "前端小分队",
      space_id: "s2",
      space_name: "OpenFox",
      members: [{ id: "x", name: "小橙", kind: "CODE", title: null }],
    }),
    entry("a3", { name: "草稿助手", space_id: "", space_name: "" }),
    entry("a4", { name: "测试", space_id: "s1" }),
  ];

  it("筛选胶囊：Agent / 小队 / 我的", () => {
    expect(filterDirectory(entries, "crew", "").map((e) => e.id)).toEqual([
      "c1",
    ]);
    expect(filterDirectory(entries, "agent", "").map((e) => e.id)).toEqual([
      "a1",
      "a2",
      "a3",
      "a4",
    ]);
    expect(filterDirectory(entries, "mine", "").map((e) => e.id)).not.toContain(
      "a2",
    );
  });

  it("搜索匹配名称、职位和小队成员名，不区分大小写", () => {
    expect(filterDirectory(entries, "all", "ai").map((e) => e.id)).toEqual([
      "a1",
    ]);
    expect(filterDirectory(entries, "all", "小橙").map((e) => e.id)).toEqual([
      "c1",
    ]);
    expect(filterDirectory(entries, "all", "架构").map((e) => e.id)).toEqual([
      "a2",
    ]);
  });

  it("置顶最前，按空间列表顺序分组，未归属空间最后", () => {
    const groups = groupDirectory(entries, [{ id: "s2" }, { id: "s1" }]);
    expect(groups.map((g) => [g.id, g.entries.map((e) => e.id)])).toEqual([
      ["pinned", ["a1"]],
      ["space:s2", ["a2", "c1"]],
      ["space:s1", ["a4"]],
      ["unassigned", ["a3"]],
    ]);
  });
});

describe("Runtime 可用性", () => {
  it("只有在线、授权允许空间与所有者、codex 可运行的电脑可选", () => {
    const choices = runtimeChoices(
      [
        device("d1"),
        device("d2", { online: false }),
        device("d3", { grant_scope: "SPACE", grant_space_id: "other" }),
        device("d4", { grant_scope: "PERSONAL", grant_user_id: "u9" }),
        device("d5", { runtimes: [] }),
        device("d6", {
          runtimes: [
            {
              provider: "codex",
              version: null,
              status: "NEEDS_LOGIN",
              runnable: false,
              reason: null,
              login_status: "LOGGED_OUT",
              models: [],
              approval_modes: ["FOLLOW_RUNTIME"],
            },
          ],
        }),
      ],
      "s1",
      "u1",
    );
    expect(choices.map((c) => [c.deviceId, c.block])).toEqual([
      ["d1", null],
      ["d2", "offline"],
      ["d3", "space"],
      ["d4", "personal"],
      ["d5", "missing"],
      ["d6", "login"],
    ]);
  });
});

describe("多 Runtime 可选项", () => {
  const runtime = (
    provider: string,
    extra: Partial<Device["runtimes"][number]> = {},
  ): Device["runtimes"][number] => ({
    provider,
    version: "1.0.0",
    status: "READY",
    runnable: true,
    reason: null,
    login_status: "UNKNOWN",
    models: [],
    approval_modes: ["FOLLOW_RUNTIME"],
    ...extra,
  });

  it("每台电脑上报的每个 Runtime 一项；Codex 总有一项，其他未上报的不出现", () => {
    const choices = runtimeChoices(
      [
        device("d1", {
          runtimes: [
            runtime("codex"),
            runtime("claude-code"),
            runtime("pi", { runnable: false, status: "UNAVAILABLE" }),
          ],
        }),
        device("d2", { runtimes: [runtime("opencode")] }),
      ],
      "s1",
      "u1",
    );
    expect(choices.map((c) => [c.value, c.block])).toEqual([
      ["d1:codex", null],
      ["d1:claude-code", null],
      ["d1:pi", "unavailable"],
      ["d2:codex", "missing"],
      ["d2:opencode", null],
    ]);
  });

  it("Runtime 显示名与支持判断", () => {
    expect(runtimeLabel("claude-code")).toBe("Claude Code");
    expect(runtimeLabel("opencode")).toBe("OpenCode");
    expect(runtimeLabel("pi")).toBe("pi");
    expect(isSupportedRuntime("Claude-Code")).toBe(true);
    expect(isSupportedRuntime("gemini-cli")).toBe(false);
  });
});

describe("运行设置单位换算", () => {
  it("分钟、小时换算为秒并能还原", () => {
    expect(minutesToSeconds(120)).toBe(7200);
    expect(hoursToSeconds(0.25)).toBe(900);
    expect(hoursToSeconds(24)).toBe(86400);
    expect(secondsToMinutes(3600)).toBe(60);
    expect(secondsToHours(5400)).toBe(1.5);
  });

  it("范围校验：分钟 1–480、小时 0.25–24", () => {
    expect(parseInRange("0", RUN_TIMEOUT_MINUTES)).toBeNull();
    expect(parseInRange("481", RUN_TIMEOUT_MINUTES)).toBeNull();
    expect(parseInRange("480", RUN_TIMEOUT_MINUTES)).toBe(480);
    expect(parseInRange("0.2", QUEUE_TIMEOUT_HOURS)).toBeNull();
    expect(parseInRange("0.25", QUEUE_TIMEOUT_HOURS)).toBe(0.25);
    expect(parseInRange("", QUEUE_TIMEOUT_HOURS)).toBeNull();
  });
});

describe("其他解析", () => {
  it("自定义参数：引号内空格保留，参数与值配对", () => {
    expect(splitArgs(`--model gpt-5 -c "a b"`)).toEqual([
      "--model",
      "gpt-5",
      "-c",
      "a b",
    ]);
    const rows = argsToRows(["--model", "gpt-5", "--verbose", "-c", "x=1"]);
    expect(rows).toEqual([
      { key: "--model", value: "gpt-5" },
      { key: "--verbose", value: "" },
      { key: "-c", value: "x=1" },
    ]);
    expect(rowsToArgs(rows)).toEqual([
      "--model",
      "gpt-5",
      "--verbose",
      "-c",
      "x=1",
    ]);
  });

  it("工作目录只接受相对路径", () => {
    expect(validDirectoryRoot("work/openfox")).toBe(true);
    expect(validDirectoryRoot("/abs")).toBe(false);
    expect(validDirectoryRoot("~/x")).toBe(false);
    expect(validDirectoryRoot("a/../b")).toBe(false);
  });

  it("邮箱切分、去重与校验", () => {
    expect(splitEmails("a@x.com, b@x.com  c@x")).toEqual([
      "a@x.com",
      "b@x.com",
      "c@x",
    ]);
    expect(mergeEmails(["A@x.com"], ["a@x.com", "b@x.com"])).toEqual([
      "A@x.com",
      "b@x.com",
    ]);
    expect(isValidEmail("c@x")).toBe(false);
    expect(isValidEmail("c@x.io")).toBe(true);
  });

  it("?contact= 解析", () => {
    expect(parseContact("agent:a1")).toEqual({ kind: "agent", id: "a1" });
    expect(parseContact("bad:a1")).toBeNull();
    expect(parseContact(null)).toBeNull();
  });
});
