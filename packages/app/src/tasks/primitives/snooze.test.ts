import { describe, expect, it } from "vitest";

import { parseSnooze, snoozePresets, triageSnoozePresets } from "./snooze";

// 2026-10-28 周三 10:00（本地时间）
const now = new Date(2026, 9, 28, 10, 0);
const local = (...args: [number, number, number, number?, number?]) =>
  new Date(args[0], args[1] - 1, args[2], args[3] ?? 0, args[4] ?? 0);

describe("snoozePresets", () => {
  it("分拣台的下周预设与月底进位", () => {
    expect(triageSnoozePresets(new Date(2026, 10, 1, 9))[2]!.at).toEqual(
      local(2026, 11, 9, 8),
    );
    expect(triageSnoozePresets(new Date(2026, 0, 31, 9))[3]!.at).toEqual(
      local(2026, 3, 3, 8),
    );
  });

  it("四个预设", () => {
    const presets = snoozePresets(now);
    expect(presets.map((p) => p.id)).toEqual([
      "oneHour",
      "tomorrow",
      "nextMonday",
      "nextMonth",
    ]);
    expect(presets[0]!.at).toEqual(local(2026, 10, 28, 11));
    expect(presets[1]!.at).toEqual(local(2026, 10, 29, 8));
    expect(presets[2]!.at).toEqual(local(2026, 11, 2, 8));
    expect(presets[3]!.at).toEqual(local(2026, 11, 28, 8));
  });

  it.each([
    ["10:00 → 今天 18:00", new Date(2026, 9, 28, 10, 0), true],
    [
      "17:30 距 18:00 不足一小时 → 不出现",
      new Date(2026, 9, 28, 17, 30),
      false,
    ],
    ["19:00 已过 18:00 → 不出现", new Date(2026, 9, 28, 19, 0), false],
  ])("今天稍晚预设：%s", (_name, at, shown) => {
    const ids = snoozePresets(at, true).map((p) => p.id);
    expect(ids.includes("laterToday")).toBe(shown);
    if (shown)
      expect(snoozePresets(at, true)[1]!.at).toEqual(local(2026, 10, 28, 18));
  });

  it("默认不含今天稍晚（收件箱）", () => {
    expect(snoozePresets(now).map((p) => p.id)).not.toContain("laterToday");
  });

  it("今天是周一时下周一在 7 天后；周日在次日", () => {
    expect(snoozePresets(new Date(2026, 9, 26, 9))[2]!.at).toEqual(
      local(2026, 11, 2, 8),
    );
    expect(snoozePresets(new Date(2026, 10, 1, 9))[2]!.at).toEqual(
      local(2026, 11, 2, 8),
    );
  });

  it("月末加一个月不溢出到下下个月", () => {
    expect(snoozePresets(new Date(2026, 0, 31, 9))[3]!.at).toEqual(
      local(2026, 2, 28, 8),
    );
  });
});

describe("parseSnooze", () => {
  it("N 分钟/小时/天/周后", () => {
    expect(parseSnooze("30分钟后", now)).toEqual(local(2026, 10, 28, 10, 30));
    expect(parseSnooze("3 小时后", now)).toEqual(local(2026, 10, 28, 13));
    expect(parseSnooze("3个小时后", now)).toEqual(local(2026, 10, 28, 13));
    expect(parseSnooze("2天后", now)).toEqual(local(2026, 10, 30, 10));
    expect(parseSnooze("1 周后", now)).toEqual(local(2026, 11, 4, 10));
  });

  it("明天 / 后天 + 时段 + 时间", () => {
    expect(parseSnooze("明天下午3点", now)).toEqual(local(2026, 10, 29, 15));
    expect(parseSnooze("明天 上午 9:30", now)).toEqual(
      local(2026, 10, 29, 9, 30),
    );
    expect(parseSnooze("后天晚上8点", now)).toEqual(local(2026, 10, 30, 20));
    expect(parseSnooze("明天14:30", now)).toEqual(local(2026, 10, 29, 14, 30));
    expect(parseSnooze("明天9点半", now)).toEqual(local(2026, 10, 29, 9, 30));
    expect(parseSnooze("明天", now)).toEqual(local(2026, 10, 29, 8));
    expect(parseSnooze("今天下午4点", now)).toEqual(local(2026, 10, 28, 16));
    expect(parseSnooze("今天上午9点", now)).toBeNull();
  });

  it("YYYY-MM-DD [HH:mm]", () => {
    expect(parseSnooze("2026-11-05", now)).toEqual(local(2026, 11, 5, 8));
    expect(parseSnooze("2026-11-05 16:45", now)).toEqual(
      local(2026, 11, 5, 16, 45),
    );
  });

  it("分拣时间搜索接受示例、组合时长和下个星期几", () => {
    expect(parseSnooze("下午 4 点", now)).toEqual(local(2026, 10, 28, 16));
    expect(parseSnooze("在2天后", now)).toEqual(local(2026, 10, 30, 10));
    expect(parseSnooze("5 周后", now)).toEqual(local(2026, 12, 2, 10));
    expect(parseSnooze("1d and 2h", now)).toEqual(local(2026, 10, 29, 12));
    expect(parseSnooze("Friday at 4pm", now)).toEqual(local(2026, 10, 30, 16));
  });

  it("英文格式", () => {
    expect(parseSnooze("in 15 minutes", now)).toEqual(
      local(2026, 10, 28, 10, 15),
    );
    expect(parseSnooze("In 2 hours", now)).toEqual(local(2026, 10, 28, 12));
    expect(parseSnooze("in 3 days", now)).toEqual(local(2026, 10, 31, 10));
    expect(parseSnooze("in 1 week", now)).toEqual(local(2026, 11, 4, 10));
    expect(parseSnooze("tomorrow", now)).toEqual(local(2026, 10, 29, 8));
    expect(parseSnooze("tomorrow 3pm", now)).toEqual(local(2026, 10, 29, 15));
    expect(parseSnooze("tomorrow at 9:15 am", now)).toEqual(
      local(2026, 10, 29, 9, 15),
    );
    expect(parseSnooze("tomorrow 12am", now)).toEqual(local(2026, 10, 29, 0));
  });

  it("无法识别、非法或已过去的输入返回 null", () => {
    for (const text of [
      "",
      "   ",
      "随便",
      "明天25点",
      "明天下午",
      "2026-02-30",
      "2026-10-27",
      "2026-11-05 25:00",
      "in 5 parsecs",
      "tomorrow 13pm",
      "0分钟后",
    ])
      expect(parseSnooze(text, now), text).toBeNull();
  });
});
