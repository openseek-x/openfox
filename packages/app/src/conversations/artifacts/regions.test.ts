import { describe, expect, it } from "vitest";
import { displayedRegion, regionFromPoints, rotatePoint } from "./regions";

describe("version-bound visual coordinates", () => {
  for (const rotation of [0, 90, 180, 270])
    it(`preserves a region after ${rotation}° rotation and resize`, () => {
      const a = { x: 0.13, y: 0.27 },
        b = { x: 0.62, y: 0.81 };
      const anchor = regionFromPoints(
        rotatePoint(a, rotation),
        rotatePoint(b, rotation),
        7,
        rotation,
      )!;
      expect(anchor.page).toBe(7);
      expect(anchor.x).toBeCloseTo(a.x);
      expect(anchor.y).toBeCloseTo(a.y);
      expect(anchor.width).toBeCloseTo(b.x - a.x);
      expect(anchor.height).toBeCloseTo(b.y - a.y);
      const box = displayedRegion(anchor, rotation);
      for (const viewport of [300, 1200]) {
        const x = (parseFloat(box.left) * viewport) / 100,
          y = (parseFloat(box.top) * viewport) / 100;
        const result = regionFromPoints(
          { x: x / viewport, y: y / viewport },
          {
            x: (x + (parseFloat(box.width) * viewport) / 100) / viewport,
            y: (y + (parseFloat(box.height) * viewport) / 100) / viewport,
          },
          7,
          rotation,
        )!;
        expect(result.x).toBeCloseTo(anchor.x!);
        expect(result.y).toBeCloseTo(anchor.y!);
        expect(result.width).toBeCloseTo(anchor.width!);
        expect(result.height).toBeCloseTo(anchor.height!);
      }
    });
  it("clips reverse drags and rejects a click without a region", () => {
    expect(
      regionFromPoints({ x: 0.7, y: 0.8 }, { x: -0.1, y: -0.2 }, 1, 0),
    ).toMatchObject({ x: 0, y: 0, width: 0.7, height: 0.8 });
    expect(
      regionFromPoints({ x: 0.1, y: 0.1 }, { x: 0.1, y: 0.9 }, 1, 0),
    ).toBeNull();
  });
});
