import { describe, expect, it } from "vitest";

import {
  formatDuration,
  formatEstimate,
  isValidEstimate,
  parseDuration,
  planConversion,
  resolveConversion,
  scaleSequence,
  snapToScale,
  type EstimationSettings,
} from "./estimation";
import {
  findIdentifierOwner,
  isValidIdentifier,
  normalizeIdentifier,
  suggestIdentifier,
  validateSpaceName,
} from "./identifier";

const settings = (
  scale: EstimationSettings["scale"],
  extra: Partial<EstimationSettings> = {},
): EstimationSettings => ({
  scale,
  extended: false,
  allow_zero: false,
  count_unestimated: false,
  ...extra,
});

describe("估时刻度", () => {
  it("固定档位序列随扩展与允许零变化", () => {
    expect(scaleSequence("fibonacci")).toBe("1, 2, 3, 5, 8");
    expect(scaleSequence("fibonacci", { extended: true })).toBe(
      "1, 2, 3, 5, 8, 13, 21",
    );
    expect(scaleSequence("exponential", { extended: true })).toBe(
      "1, 2, 4, 8, 16, 32, 64",
    );
    expect(scaleSequence("linear", { allowZero: true })).toBe(
      "0, 1, 2, 3, 4, 5",
    );
    // T 恤的 0 显示为 -
    expect(scaleSequence("tShirt", { allowZero: true, extended: true })).toBe(
      "-, XS, S, M, L, XL, XXL, XXXL",
    );
    expect(scaleSequence("storyPoints")).toBe("");
  });

  it("按刻度显示任务估时", () => {
    expect(formatEstimate(5, settings("tShirt"))).toBe("L");
    expect(formatEstimate(13, settings("fibonacci"))).toBe("13");
    expect(formatEstimate(3600 * 8 + 1800, settings("time"))).toBe("1d 30m");
    expect(formatEstimate(3, settings("notUsed"))).toBe("");
    expect(formatEstimate(null, settings("linear"))).toBe("");
  });

  it("校验估时值是否在刻度内", () => {
    expect(isValidEstimate(5, settings("fibonacci"))).toBe(true);
    expect(isValidEstimate(4, settings("fibonacci"))).toBe(false);
    expect(isValidEstimate(13, settings("fibonacci"))).toBe(false);
    expect(isValidEstimate(13, settings("fibonacci", { extended: true }))).toBe(
      true,
    );
    expect(isValidEstimate(0, settings("linear"))).toBe(false);
    expect(isValidEstimate(0, settings("linear", { allow_zero: true }))).toBe(
      true,
    );
    expect(isValidEstimate(2.5, settings("storyPoints"))).toBe(true);
    expect(isValidEstimate(2.555, settings("storyPoints"))).toBe(false);
    expect(isValidEstimate(-1, settings("storyPoints"))).toBe(false);
    expect(isValidEstimate(1, settings("notUsed"))).toBe(false);
  });
});

describe("时间输入", () => {
  it("解析周 / 天 / 时 / 分（每天 8 小时、每周 5 天）", () => {
    expect(parseDuration("2w 3d 4h 30m")).toBe(
      2 * 5 * 8 * 3600 + 3 * 8 * 3600 + 4 * 3600 + 30 * 60,
    );
    expect(parseDuration("1.5h")).toBe(5400);
    expect(parseDuration("90M")).toBe(5400);
    expect(parseDuration(" 1d4h ")).toBe(12 * 3600);
  });

  it("非法输入返回 null", () => {
    expect(parseDuration("")).toBeNull();
    expect(parseDuration("   ")).toBeNull();
    expect(parseDuration("3")).toBeNull();
    expect(parseDuration("3x")).toBeNull();
    expect(parseDuration("abc 2h")).toBeNull();
    expect(parseDuration("2h foo")).toBeNull();
    expect(parseDuration("-2h")).toBeNull();
  });

  it("格式化与解析互逆", () => {
    expect(formatDuration(0)).toBe("0m");
    expect(formatDuration(59)).toBe("0m");
    expect(formatDuration(5 * 8 * 3600)).toBe("1w");
    const seconds = parseDuration("2w 3d 4h 30m")!;
    expect(formatDuration(seconds)).toBe("2w 3d 4h 30m");
  });
});

describe("转换已有估时", () => {
  it("固定档位之间按档位序号对齐", () => {
    const plan = planConversion({
      from: settings("fibonacci"),
      to: settings("exponential"),
      values: [1, 3, 8],
    });
    expect(plan.kind).toBe("convert");
    expect(plan.manual).toEqual([]);
    expect(plan.auto).toEqual([
      { from: 1, to: 1 },
      { from: 3, to: 4 },
      { from: 8, to: 16 },
    ]);
  });

  it("关闭扩展时超出的档位落到最高档", () => {
    const plan = planConversion({
      from: settings("fibonacci", { extended: true }),
      to: settings("fibonacci"),
      values: [21],
    });
    expect(plan.auto).toEqual([{ from: 21, to: 8 }]);
  });

  it("目标不允许零时 0 被清空", () => {
    const plan = planConversion({
      from: settings("linear", { allow_zero: true }),
      to: settings("linear"),
      values: [0, 2],
    });
    expect(plan.auto).toEqual([
      { from: 0, to: null },
      { from: 2, to: 2 },
    ]);
    expect(plan.kind).toBe("convert");
  });

  it("值都不变时无需转换", () => {
    const plan = planConversion({
      from: settings("fibonacci"),
      to: settings("fibonacci", { extended: true }),
      values: [1, 5],
    });
    expect(plan.kind).toBe("noop");
  });

  it("切到未启用不改任务", () => {
    expect(
      planConversion({
        from: settings("linear"),
        to: settings("notUsed"),
        values: [1],
      }).kind,
    ).toBe("disable");
  });

  it("自由点数到固定档位需要选择取整方式", () => {
    const plan = planConversion({
      from: settings("storyPoints"),
      to: settings("fibonacci"),
      values: [4, 7],
    });
    expect(plan.method).toBe("round");
    expect(plan.manual).toEqual([4, 7]);
    expect(
      resolveConversion(plan, settings("fibonacci"), { rounding: "floor" }),
    ).toEqual([
      { from: 4, to: 3 },
      { from: 7, to: 5 },
    ]);
    expect(
      resolveConversion(plan, settings("fibonacci"), { rounding: "ceil" }),
    ).toEqual([
      { from: 4, to: 5 },
      { from: 7, to: 8 },
    ]);
  });

  it("点数与时间之间按「1 点 =」换算", () => {
    const toTime = planConversion({
      from: settings("fibonacci"),
      to: settings("time"),
      values: [2, 5],
    });
    expect(toTime.method).toBe("pointsToTime");
    expect(
      resolveConversion(toTime, settings("time"), { secondsPerPoint: 3600 }),
    ).toEqual([
      { from: 2, to: 7200 },
      { from: 5, to: 18000 },
    ]);
    const fromTime = planConversion({
      from: settings("time"),
      to: settings("linear"),
      values: [7200, 36000],
    });
    expect(fromTime.method).toBe("timeToPoints");
    expect(
      resolveConversion(fromTime, settings("linear"), {
        secondsPerPoint: 3600,
        rounding: "nearest",
      }),
    ).toEqual([
      { from: 7200, to: 2 },
      { from: 36000, to: 5 },
    ]);
  });

  it("就近取整距离相同时取较大档", () => {
    expect(snapToScale("fibonacci", 4, "nearest")).toBe(5);
    expect(snapToScale("fibonacci", 100, "floor")).toBe(8);
    expect(snapToScale("fibonacci", 0.2, "ceil")).toBe(1);
  });
});

describe("空间标识符与名称", () => {
  it("输入时转大写并过滤非字母数字", () => {
    expect(normalizeIdentifier("eng-01x")).toBe("ENG01X");
    expect(normalizeIdentifier("abcdefghij")).toBe("ABCDEFG");
    expect(normalizeIdentifier("中文")).toBe("");
  });

  it("只接受 2–7 位字母数字", () => {
    expect(isValidIdentifier("EN")).toBe(true);
    expect(isValidIdentifier("ENG1234")).toBe(true);
    expect(isValidIdentifier("E")).toBe(false);
    expect(isValidIdentifier("eng")).toBe(false);
    expect(isValidIdentifier("ENG_1")).toBe(false);
  });

  it("名称 1..32 字符（按字符计数）", () => {
    expect(validateSpaceName("  ")).toBe("empty");
    expect(validateSpaceName("a".repeat(32))).toBeNull();
    expect(validateSpaceName("a".repeat(33))).toBe("tooLong");
    expect(validateSpaceName("空".repeat(32))).toBeNull();
  });

  it("由名称推荐标识符", () => {
    expect(suggestIdentifier("Engineering")).toBe("ENGI");
    expect(suggestIdentifier("Open Fox Team")).toBe("OFT");
    expect(suggestIdentifier("设计中心")).toBe("");
  });

  it("查找与其他空间的标识符冲突（忽略自己、不区分大小写）", () => {
    const spaces = [
      { id: "a", prefix: "OPEN" },
      { id: "b", prefix: "eng" },
    ];
    expect(findIdentifierOwner(spaces, "ENG")?.id).toBe("b");
    expect(findIdentifierOwner(spaces, "OPEN", "a")).toBeUndefined();
  });
});
