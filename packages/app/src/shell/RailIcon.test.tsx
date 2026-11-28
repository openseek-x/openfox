import { render } from "@testing-library/react";
import { expect, it } from "vitest";

import { RailIcon } from "./RailIcon";
import { railItemsFor } from "./layoutMode";

it("两种布局的每个入口都能从线框切换到实心且由链接提供名称", () => {
  const names = new Set(
    [...railItemsFor("task"), ...railItemsFor("chat")].map((item) => item.icon),
  );
  for (const name of names) {
    const { container, rerender, unmount } = render(
      <RailIcon name={name} active={false} />,
    );
    const svg = container.querySelector("svg")!;
    expect(svg.childElementCount).toBeGreaterThan(0);
    expect(svg).toHaveAttribute("aria-hidden", "true");
    expect(svg).toHaveAttribute("fill", "none");
    rerender(<RailIcon name={name} active />);
    expect(svg).toHaveAttribute("fill", "currentColor");
    unmount();
  }
});
