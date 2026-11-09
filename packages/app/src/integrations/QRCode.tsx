import { useMemo } from "react";

import { encodeQR } from "./qr";

/** 安静区宽度（模块数），规范要求至少 4 */
const QUIET = 4;

/** 把文本渲染成二维码 SVG（单个 path，crispEdges，保证缩放后边缘清晰）。 */
export function QRCode({
  value,
  label,
  size = 200,
}: {
  value: string;
  label: string;
  size?: number;
}) {
  const modules = useMemo(() => encodeQR(value), [value]);
  const total = modules.length + QUIET * 2;
  const path = useMemo(() => {
    const parts: string[] = [];
    modules.forEach((row, y) => {
      // 同一行连续的深色模块合并成一段，减小 path 体积
      let x = 0;
      while (x < row.length) {
        if (!row[x]) {
          x++;
          continue;
        }
        let end = x;
        while (end < row.length && row[end]) end++;
        parts.push(`M${x + QUIET} ${y + QUIET}h${end - x}v1h-${end - x}z`);
        x = end;
      }
    });
    return parts.join("");
  }, [modules]);
  return (
    <svg
      role="img"
      aria-label={label}
      width={size}
      height={size}
      viewBox={`0 0 ${total} ${total}`}
      shapeRendering="crispEdges"
      className="shrink-0 rounded-lg bg-white"
    >
      <rect width={total} height={total} fill="#fff" />
      <path d={path} fill="#000" />
    </svg>
  );
}
