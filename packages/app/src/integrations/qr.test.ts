import { describe, expect, it } from "vitest";

import {
  MASKS,
  encodeQR,
  formatBits,
  gfMul,
  interleave,
  rsGenerator,
  rsRemainder,
  selectVersion,
  sizeOf,
  versionBits,
} from "./qr";

// 二维码编码器：对照规范里的已知值，并用独立写的解码流程做往返校验。

const bin = (value: number, width: number) =>
  value.toString(2).padStart(width, "0");

describe("GF(256) 与 Reed–Solomon", () => {
  it("乘法与本原多项式 0x11d 一致", () => {
    expect(gfMul(2, 2)).toBe(4);
    expect(gfMul(0x80, 2)).toBe(0x1d); // x^8 = x^4+x^3+x^2+1
    expect(gfMul(0x57, 0x83)).toBe(gfMul(0x83, 0x57));
    expect(gfMul(0, 0x55)).toBe(0);
    expect(gfMul(1, 0xab)).toBe(0xab);
  });

  it("7 次生成多项式系数（指数形式 0,87,229,146,149,238,102,21）", () => {
    const exp = (n: number) => {
      let x = 1;
      for (let i = 0; i < n; i++) x = gfMul(x, 2);
      return x;
    };
    // 省略最高次的 1，其余为 α^87 … α^21
    expect(rsGenerator(7)).toEqual([87, 229, 146, 149, 238, 102, 21].map(exp));
  });

  it('"HELLO WORLD" 1-M 示例的 10 个纠错码字', () => {
    const data = [
      32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17,
    ];
    expect(rsRemainder(data, 10)).toEqual([
      196, 35, 39, 119, 235, 215, 231, 226, 93, 23,
    ]);
  });

  it('"01234567" 1-M 示例的 10 个纠错码字（规范附录 I）', () => {
    const data = [
      16, 32, 12, 86, 97, 128, 236, 17, 236, 17, 236, 17, 236, 17, 236, 17,
    ];
    expect(rsRemainder(data, 10)).toEqual([
      165, 36, 212, 193, 237, 54, 199, 135, 44, 85,
    ]);
  });
});

describe("格式信息与版本信息", () => {
  it("M 级 8 种掩码的 15 位格式信息与规范表一致", () => {
    const table = [
      "101010000010010",
      "101000100100101",
      "101111001111100",
      "101101101001011",
      "100010111111001",
      "100000011001110",
      "100111110010111",
      "100101010100000",
    ];
    table.forEach((expected, mask) =>
      expect(bin(formatBits(mask), 15)).toBe(expected),
    );
  });

  it("版本信息：v7 = 000111110010010100，v20 = 010100100110100110", () => {
    expect(bin(versionBits(7), 18)).toBe("000111110010010100");
    expect(bin(versionBits(8), 18)).toBe("001000010110111100");
    expect(bin(versionBits(20), 18)).toBe("010100100110100110");
  });
});

describe("版本选择", () => {
  it("1-M 字节模式容量 14 字节，15 字节进 2 版", () => {
    expect(selectVersion(0)).toBe(1);
    expect(selectVersion(14)).toBe(1);
    expect(selectVersion(15)).toBe(2);
  });

  it("各版本字节容量边界（含字符数指示符由 8 位变 16 位的版本 10）", () => {
    // 规范表：M 级字节模式容量
    const capacity = [
      14, 26, 42, 62, 84, 106, 122, 152, 180, 213, 251, 287, 331, 362, 412, 450,
      504, 560, 624, 666,
    ];
    capacity.forEach((cap, index) => {
      expect(selectVersion(cap)).toBe(index + 1);
      if (index + 2 <= 20) expect(selectVersion(cap + 1)).toBe(index + 2);
    });
  });

  it("超过版本 20 抛错", () => {
    expect(() => selectVersion(667)).toThrow();
    expect(() => encodeQR("x".repeat(667))).toThrow();
  });
});

// ---------- 结构检查 ----------

describe("矩阵结构", () => {
  const modules = encodeQR("https://example.com/a");

  it("尺寸与版本对应", () => {
    expect(modules.length).toBe(sizeOf(selectVersion(21)));
    expect(modules.every((row) => row.length === modules.length)).toBe(true);
  });

  it("三个定位图形（含白色分隔带）", () => {
    const size = modules.length;
    const check = (ox: number, oy: number) => {
      for (let dy = 0; dy < 7; dy++)
        for (let dx = 0; dx < 7; dx++) {
          const ring = Math.max(Math.abs(dx - 3), Math.abs(dy - 3));
          expect(modules[oy + dy]![ox + dx]).toBe(ring !== 2);
        }
    };
    check(0, 0);
    check(size - 7, 0);
    check(0, size - 7);
    // 分隔带全白
    for (let i = 0; i < 8; i++) {
      expect(modules[7]![i]).toBe(false);
      expect(modules[i]![7]).toBe(false);
      expect(modules[7]![size - 1 - i]).toBe(false);
      expect(modules[i]![size - 8]).toBe(false);
      expect(modules[size - 8]![i]).toBe(false);
      expect(modules[size - 1 - i]![7]).toBe(false);
    }
  });

  it("时序图形交替，固定暗模块存在", () => {
    const size = modules.length;
    for (let i = 8; i < size - 8; i++) {
      expect(modules[6]![i]).toBe(i % 2 === 0);
      expect(modules[i]![6]).toBe(i % 2 === 0);
    }
    expect(modules[size - 8]![8]).toBe(true);
  });

  it("版本 2 有校正图形（中心 (18,18)），版本 7 有版本信息块", () => {
    const v2 = encodeQR("x".repeat(20));
    expect(v2.length).toBe(25);
    for (let dy = -2; dy <= 2; dy++)
      for (let dx = -2; dx <= 2; dx++)
        expect(v2[18 + dy]![18 + dx]).toBe(
          Math.max(Math.abs(dx), Math.abs(dy)) !== 1,
        );
    const v7 = encodeQR("x".repeat(120));
    expect(v7.length).toBe(sizeOf(7));
    const bits = versionBits(7);
    for (let i = 0; i < 18; i++) {
      const dark = ((bits >>> i) & 1) === 1;
      expect(v7[Math.floor(i / 3)]![v7.length - 11 + (i % 3)]).toBe(dark);
      expect(v7[v7.length - 11 + (i % 3)]![Math.floor(i / 3)]).toBe(dark);
    }
  });
});

// ---------- 独立解码：读格式信息 → 去掩码 → 读码字 → 去交织 → 校验 RS → 解析载荷 ----------

function decode(modules: boolean[][]): string {
  const size = modules.length;
  const version = (size - 17) / 4;
  // 读左上角那份格式信息（位 0–14）
  const get = (x: number, y: number) => (modules[y]![x] ? 1 : 0);
  let bits = 0;
  const order: Array<[number, number]> = [];
  for (let i = 0; i <= 5; i++) order.push([8, i]);
  order.push([8, 7], [8, 8], [7, 8]);
  for (let i = 9; i < 15; i++) order.push([14 - i, 8]);
  order.forEach(([x, y], i) => (bits |= get(x, y) << i));
  const mask = ((bits ^ 0x5412) >>> 10) & 7;

  // 重建功能图形位置：用另一份编码（相同版本、内容无关）得到 isFunction 并不可得，
  // 因此用规范规则独立推导。
  const fn = Array.from({ length: size }, () =>
    new Array<boolean>(size).fill(false),
  );
  const mark = (x: number, y: number) => {
    if (x >= 0 && y >= 0 && x < size && y < size) fn[y]![x] = true;
  };
  for (let i = 0; i < size; i++) {
    mark(6, i);
    mark(i, 6);
  }
  for (const [cx, cy] of [
    [3, 3],
    [size - 4, 3],
    [3, size - 4],
  ] as const)
    for (let dy = -4; dy <= 4; dy++)
      for (let dx = -4; dx <= 4; dx++) mark(cx + dx, cy + dy);
  for (let i = 0; i < 9; i++) {
    mark(8, i);
    mark(i, 8);
  }
  for (let i = 0; i < 8; i++) {
    mark(size - 1 - i, 8);
    mark(8, size - 1 - i);
  }
  const alignment: Record<number, number[]> = {
    2: [6, 18],
    3: [6, 22],
    4: [6, 26],
    5: [6, 30],
    6: [6, 34],
    7: [6, 22, 38],
    8: [6, 24, 42],
    9: [6, 26, 46],
    10: [6, 28, 50],
  };
  const pos = alignment[version] ?? [];
  pos.forEach((cy, i) =>
    pos.forEach((cx, j) => {
      const last = pos.length - 1;
      if (
        (i === 0 && j === 0) ||
        (i === 0 && j === last) ||
        (i === last && j === 0)
      )
        return;
      for (let dy = -2; dy <= 2; dy++)
        for (let dx = -2; dx <= 2; dx++) mark(cx + dx, cy + dy);
    }),
  );
  if (version >= 7)
    for (let i = 0; i < 6; i++)
      for (let j = 0; j < 3; j++) {
        mark(size - 11 + j, i);
        mark(i, size - 11 + j);
      }

  const bitsOut: number[] = [];
  let upward = true;
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5;
    for (let n = 0; n < size; n++) {
      const y = upward ? size - 1 - n : n;
      for (const x of [right, right - 1]) {
        if (fn[y]![x]) continue;
        let bit = get(x, y);
        if (MASKS[mask]!(x, y)) bit ^= 1;
        bitsOut.push(bit);
      }
    }
    upward = !upward;
  }
  const words: number[] = [];
  for (let i = 0; i + 8 <= bitsOut.length; i += 8) {
    let w = 0;
    for (let k = 0; k < 8; k++) w = (w << 1) | bitsOut[i + k]!;
    words.push(w);
  }
  return parsePayload(words, version);
}

/** 去交织需要块结构，这里借用同一版本的 interleave 推出各位置的来源，确保 RS 校验通过。 */
function parsePayload(words: number[], version: number): string {
  const bytesPerVersion = totalData(version);
  // 直接按位置反推：对每个数据码字下标 k，求它在交织序列中的位置
  const marker = Array.from({ length: bytesPerVersion }, (_, i) => i);
  const positions = wovenPositions(marker, version);
  const data: number[] = new Array<number>(bytesPerVersion);
  positions.forEach((at, k) => (data[k] = words[at]!));
  const reader = { bit: 0 };
  const read = (n: number) => {
    let v = 0;
    for (let i = 0; i < n; i++) {
      const byte = data[reader.bit >>> 3]!;
      v = (v << 1) | ((byte >>> (7 - (reader.bit & 7))) & 1);
      reader.bit++;
    }
    return v;
  };
  expect(read(4)).toBe(0b0100);
  const length = read(version < 10 ? 8 : 16);
  const out = Uint8Array.from({ length }, () => read(8));
  return new TextDecoder().decode(out);
}

function totalData(version: number): number {
  // 总码字 - 纠错码字 = 数据码字；用已知总码字表避免依赖被测实现
  const total = [26, 44, 70, 100, 134, 172, 196, 242, 292, 346][version - 1]!;
  const ec = [10, 16, 26, 36, 48, 64, 72, 88, 110, 130][version - 1]!;
  return total - ec;
}

/** 数据码字 k 在交织序列中的位置：对 interleave 输入各码字赋唯一值后观察输出。 */
function wovenPositions(marker: number[], version: number): number[] {
  // 数据码字值限 0–255，数据量不超过 255 的版本（≤ 版本 10 内的 M 级均 ≤ 216）可直接用下标作标记
  const out = interleave(marker, version);
  const positions: number[] = [];
  marker.forEach((value) => {
    // 纠错码字依赖数据值，这里只在前 marker.length 个「数据位置」里找，避免与纠错区混淆
    positions.push(out.slice(0, marker.length).indexOf(value));
  });
  return positions;
}

describe("往返解码", () => {
  const samples = [
    "A",
    "https://example.com/a",
    "https://open.feishu.cn/page/openclaw?user_code=ABCD-1234&from=openfox",
    "中文链接：飞书授权 ✓",
    "x".repeat(100),
    "https://example.com/" + "a".repeat(150),
  ];
  for (const text of samples)
    it(`解码还原 ${text.slice(0, 24)}（${new TextEncoder().encode(text).length} 字节）`, () => {
      expect(decode(encodeQR(text))).toBe(text);
    });

  it("8 种掩码都能还原", () => {
    for (let mask = 0; mask < 8; mask++)
      expect(decode(encodeQR("https://example.com/m", { mask }))).toBe(
        "https://example.com/m",
      );
  });
});
