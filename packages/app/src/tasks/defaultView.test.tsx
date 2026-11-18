import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import { keys } from "../context";
import { defaultDisplay, displayStorageKey, saveDisplay } from "./model";
import { defaultViewModeKey } from "./spaceDefaultView";
import { backend, json, ORG, SPACE, type Override } from "./test-backend";

afterEach(() => localStorage.clear());

function fixture(extra?: Override) {
  let space = {
    id: SPACE,
    prefix: "open",
    name: "Space",
    visibility: "shared",
    version: 1,
    can_manage: true,
    default_view_id: "v1" as string | null,
    features: {
      views: true,
      projects: false,
      iterations: false,
      triage: false,
    },
  };
  let view = {
    id: "v1",
    space_id: SPACE,
    name: "Default A",
    visibility: "space",
    version: 1,
    filters: { priority: ["HIGH"] },
    display: { ...defaultDisplay("space"), layout: "table" as const },
    member_view_mode: "list",
  };
  const server = backend((call) => {
    const override = extra?.(call);
    if (override) return override;
    const base = `/v1/organizations/${ORG}`;
    if (call.path === `${base}/spaces`) return json(200, { spaces: [space] });
    if (call.path === `${base}/spaces/${SPACE}` && call.method === "GET")
      return json(200, space);
    if (call.path === `${base}/views/v1`) return json(200, view);
    if (call.path === `${base}/spaces/${SPACE}/views`)
      return json(200, {
        views: [
          view,
          { ...view, id: "private", visibility: "personal", name: "Private" },
        ],
      });
    return undefined;
  });
  const cache = createQueryClient();
  cache.setDefaultOptions({ queries: { retry: false } });
  const mount = () => {
    localStorage.setItem(`openfox:onboarding:${ORG}`, "{}");
    return render(
      <MemoryRouter initialEntries={[`/o/${ORG}/spaces/${SPACE}/tasks`]}>
        <App api={server.api} queryClient={cache} locale="zh-CN" />
      </MemoryRouter>,
    );
  };
  return {
    server,
    cache,
    mount,
    get space() {
      return space;
    },
    set space(next) {
      space = next;
    },
    get view() {
      return view;
    },
    set view(next) {
      view = next;
    },
  };
}

it("inherits the shared display and server view filter, keeps temporary filters across live source edits", async () => {
  const f = fixture();
  f.mount();
  const user = userEvent.setup();
  await screen.findByText("空间默认：Default A");
  await screen.findByRole("checkbox", { name: "全选" });
  expect(
    f.server
      .find("GET", "/tasks/counts")
      .every((c) => c.query.get("view_id") === "v1"),
  ).toBe(true);
  expect(
    localStorage.getItem(displayStorageKey(ORG, `space:${SPACE}`)),
  ).toBeNull();
  await user.click(screen.getByRole("button", { name: "筛选" }));
  await user.click(await screen.findByRole("menuitem", { name: "优先级" }));
  await user.click(await screen.findByRole("option", { name: "高" }));
  await user.keyboard("{Escape}");
  await waitFor(() =>
    expect(
      f.server
        .find("GET", "/tasks/counts")
        .some(
          (c) =>
            c.query.get("view_id") === "v1" &&
            c.query.getAll("priority").includes("HIGH"),
        ),
    ).toBe(true),
  );
  await act(async () => {
    f.view = {
      ...f.view,
      name: "Changed source",
      display: { ...f.view.display, layout: "gantt" as never },
    };
    await f.cache.invalidateQueries({ queryKey: keys.view(ORG, "v1") });
  });
  await screen.findByText("空间默认：Changed source");
  await screen.findByRole("switch", { name: "显示已完成" });
  expect(screen.getByRole("group", { name: "优先级" })).toBeVisible();
});

it("leaves legacy local displays and their task scope unchanged", async () => {
  saveDisplay(ORG, `space:${SPACE}`, {
    ...defaultDisplay("space"),
    layout: "list",
  });
  const f = fixture();
  f.mount();
  await screen.findByText("个人视图");
  await waitFor(() =>
    expect(f.server.find("GET", "/tasks/counts").length).toBeGreaterThan(0),
  );
  expect(
    f.server.find("GET", "/tasks/counts").every((c) => !c.query.has("view_id")),
  ).toBe(true);
  expect(f.server.find("GET", "/views/v1")).toHaveLength(0);
  expect(localStorage.getItem(defaultViewModeKey(ORG, SPACE))).toBeNull();
});

it("preserves inherited filters after personal display changes and reload; explicit exit and reset change the scope", async () => {
  const f = fixture();
  let mounted = f.mount();
  const user = userEvent.setup();
  await screen.findByText("空间默认：Default A");
  await user.click(screen.getByRole("button", { name: "显示设置" }));
  await user.click(screen.getByRole("radio", { name: "列表" }));
  await user.keyboard("{Escape}");
  expect(localStorage.getItem(defaultViewModeKey(ORG, SPACE))).toBe("true");
  mounted.unmount();
  mounted = f.mount();
  await screen.findByText("空间默认：Default A");
  await user.click(screen.getByRole("button", { name: "显示设置" }));
  expect(screen.getByRole("radio", { name: "列表" })).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await user.keyboard("{Escape}");
  await user.click(screen.getByRole("button", { name: "改用个人视图" }));
  await screen.findByText("个人视图");
  await waitFor(() =>
    expect(
      f.server.find("GET", "/tasks/counts").at(-1)?.query.has("view_id"),
    ).toBe(false),
  );
  expect(localStorage.getItem(defaultViewModeKey(ORG, SPACE))).toBeNull();
  await user.click(screen.getByRole("button", { name: "使用空间默认" }));
  await screen.findByText("空间默认：Default A");
  await screen.findByRole("checkbox", { name: "全选" });
  expect(
    localStorage.getItem(displayStorageKey(ORG, `space:${SPACE}`)),
  ).toBeNull();
});

it("blocks on source 5xx and falls back only when the source is unavailable", async () => {
  let status = 503;
  const f = fixture((c) =>
    c.path.endsWith("/views/v1")
      ? json(status, {
          error: {
            code: status === 404 ? "NOT_FOUND" : "UNAVAILABLE",
            message: "controlled",
          },
        })
      : undefined,
  );
  f.mount();
  await screen.findByText("无法加载空间默认视图");
  expect(f.server.find("GET", "/tasks/counts")).toHaveLength(0);
  status = 404;
  f.space = { ...f.space, default_view_id: null };
  await userEvent.setup().click(screen.getByRole("button", { name: "重试" }));
  await screen.findByText("系统默认");
  await waitFor(() =>
    expect(f.server.find("GET", "/tasks/counts").length).toBeGreaterThan(0),
  );
  expect(
    f.server.find("GET", "/tasks/counts").every((c) => !c.query.has("view_id")),
  ).toBe(true);
});

it("uses shared same-space choices and retains the selection through an explicit version-conflict refresh", async () => {
  let attempts = 0;
  const f = fixture((c) => {
    if (c.method === "PATCH" && c.path.endsWith(`/spaces/${SPACE}`)) {
      attempts++;
      if (attempts === 1)
        return json(409, {
          error: { code: "VERSION_CONFLICT", message: "controlled" },
        });
      return json(200, { ...f.space, version: 3, default_view_id: null });
    }
    return undefined;
  });
  f.mount();
  const user = userEvent.setup();
  await screen.findByText("空间默认：Default A");
  await user.click(screen.getByRole("button", { name: "空间默认视图" }));
  const dialog = await screen.findByRole("dialog", { name: "空间默认视图" });
  expect(
    within(dialog).queryByRole("option", { name: "Private" }),
  ).not.toBeInTheDocument();
  await user.selectOptions(
    within(dialog).getByRole("combobox", { name: "默认共享视图" }),
    "",
  );
  await user.click(
    within(dialog).getByRole("button", { name: "保存默认视图" }),
  );
  await within(dialog).findByRole("alert");
  f.space = { ...f.space, version: 2 };
  await user.click(
    within(dialog).getByRole("button", { name: "刷新版本后重试" }),
  );
  await waitFor(() =>
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument(),
  );
  expect(
    within(dialog).getByRole("combobox", { name: "默认共享视图" }),
  ).toHaveValue("");
  await user.click(
    within(dialog).getByRole("button", { name: "保存默认视图" }),
  );
  await waitFor(() =>
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
  );
  expect(f.server.find("PATCH", `/spaces/${SPACE}`).map((c) => c.body)).toEqual(
    [
      { version: 1, default_view_id: null },
      { version: 2, default_view_id: null },
    ],
  );
});
