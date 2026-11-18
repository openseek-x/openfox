// 二维码编码器（ISO/IEC 18004）：字节模式（UTF-8）、纠错等级 M、版本 1–20 自动选择。
// 项目没有二维码依赖，扫码授权只需要编码一段 https 链接，所以在这里自行实现。

/** 纠错等级 M 在格式信息里的 2 位编码 */
const ECC_FORMAT_BITS_M = 0;

/** 每个版本（下标 = 版本号 - 1）的纠错块结构：每块纠错码字数、[块数, 每块数据码字数] 的分组（先短块后长块）。 */
const BLOCKS_M: ReadonlyArray<{
  ecPerBlock: number;
  groups: ReadonlyArray<readonly [number, number]>;
}> = [
  { ecPerBlock: 10, groups: [[1, 16]] },
  { ecPerBlock: 16, groups: [[1, 28]] },
  { ecPerBlock: 26, groups: [[1, 44]] },
  { ecPerBlock: 18, groups: [[2, 32]] },
  { ecPerBlock: 24, groups: [[2, 43]] },
  { ecPerBlock: 16, groups: [[4, 27]] },
  { ecPerBlock: 18, groups: [[4, 31]] },
  {
    ecPerBlock: 22,
    groups: [
      [2, 38],
      [2, 39],
    ],
  },
  {
    ecPerBlock: 22,
    groups: [
      [3, 36],
      [2, 37],
    ],
  },
  {
    ecPerBlock: 26,
    groups: [
      [4, 43],
      [1, 44],
    ],
  },
  {
    ecPerBlock: 30,
    groups: [
      [1, 50],
      [4, 51],
    ],
  },
  {
    ecPerBlock: 22,
    groups: [
      [6, 36],
      [2, 37],
    ],
  },
  {
    ecPerBlock: 22,
    groups: [
      [8, 37],
      [1, 38],
    ],
  },
  {
    ecPerBlock: 24,
    groups: [
      [4, 40],
      [5, 41],
    ],
  },
  {
    ecPerBlock: 24,
    groups: [
      [5, 41],
      [5, 42],
    ],
  },
  {
    ecPerBlock: 28,
    groups: [
      [7, 45],
      [3, 46],
    ],
  },
  {
    ecPerBlock: 28,
    groups: [
      [10, 46],
      [1, 47],
    ],
  },
  {
    ecPerBlock: 26,
    groups: [
      [9, 43],
      [4, 44],
    ],
  },
  {
    ecPerBlock: 26,
    groups: [
      [3, 44],
      [11, 45],
    ],
  },
  {
    ecPerBlock: 26,
    groups: [
      [3, 41],
      [13, 42],
    ],
  },
];

/** 校正图形中心坐标（版本 2–20，下标 = 版本号 - 2） */
const ALIGNMENT: ReadonlyArray<readonly number[]> = [
  [6, 18],
  [6, 22],
  [6, 26],
  [6, 30],
  [6, 34],
  [6, 22, 38],
  [6, 24, 42],
  [6, 26, 46],
  [6, 28, 50],
  [6, 30, 54],
  [6, 32, 58],
  [6, 34, 62],
  [6, 26, 46, 66],
  [6, 26, 48, 70],
  [6, 26, 50, 74],
  [6, 30, 54, 78],
  [6, 30, 56, 82],
  [6, 30, 58, 86],
  [6, 34, 62, 90],
];

export const MIN_VERSION = 1;
export const MAX_VERSION = 20;

export const sizeOf = (version: number) => version * 4 + 17;

function dataCodewords(version: number): number {
  return BLOCKS_M[version - 1]!.groups.reduce(
    (sum, [count, size]) => sum + count * size,
    0,
  );
}

/** 字节模式的字符数指示符位数 */
const countBits = (version: number) => (version < 10 ? 8 : 16);

/** UTF-8 字节 */
function utf8(text: string): number[] {
  return Array.from(new TextEncoder().encode(text));
}

/** 能容纳 length 个字节的最小版本；超出版本 20 抛错。 */
export function selectVersion(length: number): number {
  for (let version = MIN_VERSION; version <= MAX_VERSION; version++) {
    if (4 + countBits(version) + 8 * length <= dataCodewords(version) * 8)
      return version;
  }
  throw new RangeError("二维码内容过长（超过版本 20 的容量）");
}

// ---------- GF(256) 与 Reed–Solomon ----------

const EXP: number[] = new Array<number>(512).fill(0);
const LOG: number[] = new Array<number>(256).fill(0);
(() => {
  let x = 1;
  for (let i = 0; i < 255; i++) {
    EXP[i] = x;
    LOG[x] = i;
    x <<= 1;
    if (x & 0x100) x ^= 0x11d;
  }
  for (let i = 255; i < 512; i++) EXP[i] = EXP[i - 255]!;
})();

/** GF(256) 乘法（本原多项式 0x11d） */
export function gfMul(a: number, b: number): number {
  if (a === 0 || b === 0) return 0;
  return EXP[LOG[a]! + LOG[b]!]!;
}

/** 次数为 degree 的 RS 生成多项式系数（最高次项的 1 省略，从次高项开始）。 */
export function rsGenerator(degree: number): number[] {
  const result: number[] = new Array<number>(degree).fill(0);
  result[degree - 1] = 1;
  let root = 1;
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < degree; j++) {
      result[j] = gfMul(result[j]!, root);
      if (j + 1 < degree) result[j] = result[j]! ^ result[j + 1]!;
    }
    root = gfMul(root, 2);
  }
  return result;
}

/** 数据多项式除以生成多项式的余式，即 degree 个纠错码字。 */
export function rsRemainder(data: readonly number[], degree: number): number[] {
  const generator = rsGenerator(degree);
  const result: number[] = new Array<number>(degree).fill(0);
  for (const byte of data) {
    const factor = byte ^ result.shift()!;
    result.push(0);
    for (let i = 0; i < degree; i++)
      result[i] = result[i]! ^ gfMul(generator[i]!, factor);
  }
  return result;
}

// ---------- 格式 / 版本信息 ----------

/** 15 位格式信息（纠错等级 M + 掩码编号，BCH(15,5)，已与 0x5412 异或）。 */
export function formatBits(mask: number): number {
  const data = (ECC_FORMAT_BITS_M << 3) | mask;
  let rem = data;
  for (let i = 0; i < 10; i++) rem = (rem << 1) ^ ((rem >>> 9) * 0x537);
  return ((data << 10) | rem) ^ 0x5412;
}

/** 18 位版本信息（版本 ≥ 7，BCH(18,6)）。 */
export function versionBits(version: number): number {
  let rem = version;
  for (let i = 0; i < 12; i++) rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25);
  return (version << 12) | rem;
}

// ---------- 数据码字 ----------

function buildDataCodewords(bytes: number[], version: number): number[] {
  const bits: number[] = [];
  const push = (value: number, length: number) => {
    for (let i = length - 1; i >= 0; i--) bits.push((value >>> i) & 1);
  };
  push(0b0100, 4);
  push(bytes.length, countBits(version));
  for (const byte of bytes) push(byte, 8);
  const capacity = dataCodewords(version) * 8;
  push(0, Math.min(4, capacity - bits.length));
  push(0, (8 - (bits.length % 8)) % 8);
  const words: number[] = [];
  for (let i = 0; i < bits.length; i += 8) {
    let word = 0;
    for (let j = 0; j < 8; j++) word = (word << 1) | bits[i + j]!;
    words.push(word);
  }
  for (let pad = 0xec; words.length < capacity / 8; pad ^= 0xec ^ 0x11)
    words.push(pad);
  return words;
}

/** 分块、逐块计算纠错码字，再按列交织成最终码字序列。 */
export function interleave(data: number[], version: number): number[] {
  const { ecPerBlock, groups } = BLOCKS_M[version - 1]!;
  const dataBlocks: number[][] = [];
  let offset = 0;
  for (const [count, size] of groups) {
    for (let i = 0; i < count; i++) {
      dataBlocks.push(data.slice(offset, offset + size));
      offset += size;
    }
  }
  const ecBlocks = dataBlocks.map((block) => rsRemainder(block, ecPerBlock));
  const result: number[] = [];
  const longest = Math.max(...dataBlocks.map((block) => block.length));
  for (let i = 0; i < longest; i++)
    for (const block of dataBlocks)
      if (i < block.length) result.push(block[i]!);
  for (let i = 0; i < ecPerBlock; i++)
    for (const block of ecBlocks) result.push(block[i]!);
  return result;
}

// ---------- 矩阵 ----------

interface Grid {
  size: number;
  modules: boolean[][];
  /** 功能图形（定位、校正、时序、格式、版本）占用的位置，数据不能写入也不参与掩码 */
  isFunction: boolean[][];
}

function newGrid(version: number): Grid {
  const size = sizeOf(version);
  const matrix = () =>
    Array.from({ length: size }, () => new Array<boolean>(size).fill(false));
  return { size, modules: matrix(), isFunction: matrix() };
}

function setFunction(grid: Grid, x: number, y: number, dark: boolean) {
  grid.modules[y]![x] = dark;
  grid.isFunction[y]![x] = true;
}

function drawFormat(grid: Grid, mask: number) {
  const { size } = grid;
  const bits = formatBits(mask);
  const bit = (i: number) => ((bits >>> i) & 1) !== 0;
  for (let i = 0; i <= 5; i++) setFunction(grid, 8, i, bit(i));
  setFunction(grid, 8, 7, bit(6));
  setFunction(grid, 8, 8, bit(7));
  setFunction(grid, 7, 8, bit(8));
  for (let i = 9; i < 15; i++) setFunction(grid, 14 - i, 8, bit(i));
  for (let i = 0; i < 8; i++) setFunction(grid, size - 1 - i, 8, bit(i));
  for (let i = 8; i < 15; i++) setFunction(grid, 8, size - 15 + i, bit(i));
  setFunction(grid, 8, size - 8, true); // 固定的暗模块
}

function drawFunctionPatterns(grid: Grid, version: number) {
  const { size } = grid;
  for (let i = 0; i < size; i++) {
    setFunction(grid, 6, i, i % 2 === 0);
    setFunction(grid, i, 6, i % 2 === 0);
  }
  const finder = (cx: number, cy: number) => {
    for (let dy = -4; dy <= 4; dy++)
      for (let dx = -4; dx <= 4; dx++) {
        const dist = Math.max(Math.abs(dx), Math.abs(dy));
        const x = cx + dx;
        const y = cy + dy;
        if (x >= 0 && x < size && y >= 0 && y < size)
          setFunction(grid, x, y, dist !== 2 && dist !== 4);
      }
  };
  finder(3, 3);
  finder(size - 4, 3);
  finder(3, size - 4);
  const positions = version >= 2 ? ALIGNMENT[version - 2]! : [];
  const last = positions.length - 1;
  positions.forEach((cy, i) =>
    positions.forEach((cx, j) => {
      if (
        (i === 0 && j === 0) ||
        (i === 0 && j === last) ||
        (i === last && j === 0)
      )
        return;
      for (let dy = -2; dy <= 2; dy++)
        for (let dx = -2; dx <= 2; dx++)
          setFunction(
            grid,
            cx + dx,
            cy + dy,
            Math.max(Math.abs(dx), Math.abs(dy)) !== 1,
          );
    }),
  );
  drawFormat(grid, 0); // 先占位，选定掩码后重画
  if (version >= 7) {
    const bits = versionBits(version);
    for (let i = 0; i < 18; i++) {
      const dark = ((bits >>> i) & 1) !== 0;
      const a = size - 11 + (i % 3);
      const b = Math.floor(i / 3);
      setFunction(grid, a, b, dark);
      setFunction(grid, b, a, dark);
    }
  }
}

/** 按之字形从右下角开始写入码字比特；剩余位（余数位）保持为浅色。 */
function drawCodewords(grid: Grid, words: number[]) {
  const { size } = grid;
  let index = 0;
  const total = words.length * 8;
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5; // 跳过竖直时序线
    for (let vert = 0; vert < size; vert++)
      for (let j = 0; j < 2; j++) {
        const x = right - j;
        const upward = ((right + 1) & 2) === 0;
        const y = upward ? size - 1 - vert : vert;
        if (!grid.isFunction[y]![x] && index < total) {
          grid.modules[y]![x] =
            ((words[index >>> 3]! >>> (7 - (index & 7))) & 1) !== 0;
          index++;
        }
      }
  }
}

export const MASKS: ReadonlyArray<(x: number, y: number) => boolean> = [
  (x, y) => (x + y) % 2 === 0,
  (_x, y) => y % 2 === 0,
  (x) => x % 3 === 0,
  (x, y) => (x + y) % 3 === 0,
  (x, y) => (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0,
  (x, y) => ((x * y) % 2) + ((x * y) % 3) === 0,
  (x, y) => (((x * y) % 2) + ((x * y) % 3)) % 2 === 0,
  (x, y) => (((x + y) % 2) + ((x * y) % 3)) % 2 === 0,
];

function applyMask(grid: Grid, mask: number) {
  const fn = MASKS[mask]!;
  for (let y = 0; y < grid.size; y++)
    for (let x = 0; x < grid.size; x++)
      if (!grid.isFunction[y]![x] && fn(x, y))
        grid.modules[y]![x] = !grid.modules[y]![x];
}

/** 规范的四条罚分规则：同色连续 ≥5、2×2 同色块、类定位图形 1:1:3:1:1、深浅比例偏离。 */
export function penalty(modules: boolean[][]): number {
  const size = modules.length;
  let score = 0;
  const line = (get: (i: number) => boolean) => {
    let run = 1;
    for (let i = 1; i < size; i++) {
      if (get(i) === get(i - 1)) {
        run++;
        if (run === 5) score += 3;
        else if (run > 5) score += 1;
      } else run = 1;
    }
    // 10111010000 或 00001011101（模块值 1 为深色）
    for (let i = 0; i + 11 <= size; i++) {
      let a = true;
      let b = true;
      const pa = [1, 0, 1, 1, 1, 0, 1, 0, 0, 0, 0];
      for (let k = 0; k < 11; k++) {
        const v = get(i + k) ? 1 : 0;
        if (v !== pa[k]) a = false;
        if (v !== pa[10 - k]) b = false;
      }
      if (a) score += 40;
      if (b) score += 40;
    }
  };
  for (let y = 0; y < size; y++) line((i) => modules[y]![i]!);
  for (let x = 0; x < size; x++) line((i) => modules[i]![x]!);
  let dark = 0;
  for (let y = 0; y < size; y++)
    for (let x = 0; x < size; x++) {
      if (modules[y]![x]) dark++;
      if (
        x + 1 < size &&
        y + 1 < size &&
        modules[y]![x] === modules[y]![x + 1] &&
        modules[y]![x] === modules[y + 1]![x] &&
        modules[y]![x] === modules[y + 1]![x + 1]
      )
        score += 3;
    }
  const total = size * size;
  score += (Math.ceil(Math.abs(dark * 20 - total * 10) / total) - 1) * 10;
  return score;
}

/** 编码为模块矩阵（true = 深色，不含安静区）。options.mask 仅供测试固定掩码。 */
export function encodeQR(
  text: string,
  options: { mask?: number } = {},
): boolean[][] {
  const bytes = utf8(text);
  const version = selectVersion(bytes.length);
  const words = interleave(buildDataCodewords(bytes, version), version);
  const grid = newGrid(version);
  drawFunctionPatterns(grid, version);
  drawCodewords(grid, words);

  const render = (mask: number) => {
    const copy: Grid = {
      size: grid.size,
      modules: grid.modules.map((row) => row.slice()),
      isFunction: grid.isFunction,
    };
    applyMask(copy, mask);
    drawFormat(copy, mask);
    return copy.modules;
  };
  if (options.mask !== undefined) return render(options.mask);
  let best: boolean[][] = [];
  let bestScore = Infinity;
  for (let mask = 0; mask < 8; mask++) {
    const modules = render(mask);
    const score = penalty(modules);
    if (score < bestScore) {
      bestScore = score;
      best = modules;
    }
  }
  return best;
}
