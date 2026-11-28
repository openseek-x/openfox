import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Icon } from "./Icon";

describe("功能图标", () => {
  it("小图加重并保留调用方设置的填充、线宽和无障碍名称", () => {
    const { container, rerender } = render(<Icon name="star" size={12} />);
    const svg = container.querySelector("svg")!;
    expect(svg).toHaveAttribute("aria-hidden", "true");
    expect(svg).toHaveAttribute("stroke-width", "2.5");

    rerender(
      <Icon name="star" label="已收藏" fill="currentColor" strokeWidth={3} />,
    );
    expect(screen.getByRole("img", { name: "已收藏" })).toBe(svg);
    expect(svg).toHaveAttribute("width", "16");
    expect(svg).toHaveAttribute("stroke-width", "3");
    expect(svg).toHaveAttribute("fill", "currentColor");
    expect(svg.querySelector('[fill="none"]')).toBeNull();
  });
});
