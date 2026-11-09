import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App, createQueryClient } from "../App";
import { backend, json, ORG, SPACE, type Override } from "./test-backend";

// 任务详情的类型菜单底部「管理任务类型」，只对能管理空间设置的人显示。

const spaceDetail = (canManage: boolean, myRole: string) => ({
  id: SPACE,
  prefix: "OPEN",
  name: "默认空间",
  visibility: "shared",
  version: 1,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
  icon: null,
  purpose: "software_development",
  features: { views: false, projects: false, iterations: false, triage: false },
  multi_assignee: false,
  estimation: {
    scale: "notUsed",
    extended: false,
    allow_zero: false,
    count_unestimated: false,
  },
  status: "active",
  retired_at: null,
  position: null,
  my_role: myRole,
  can_manage: canManage,
  member_count: 1,
  task_type_count: 1,
  label_count: 0,
  task_count: 3,
});

const withSpace =
  (canManage: boolean, myRole = "OWNER"): Override =>
  (call) =>
    call.path === `/v1/organizations/${ORG}/spaces/${SPACE}`
      ? json(200, spaceDetail(canManage, myRole))
      : undefined;

/** 显示当前地址，并提供「后退」检查 push 的历史记录 */
function Probe() {
  const location = useLocation();
  const navigate = useNavigate();
  return (
    <>
      <output aria-label="当前地址">
        {location.pathname + location.search}
      </output>
      <button type="button" onClick={() => navigate(-1)}>
        测试后退
      </button>
    </>
  );
}

function renderAt(override: Override) {
  const server = backend(override);
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <MemoryRouter
      initialEntries={[`/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`]}
    >
      <App api={server.api} queryClient={queryClient} locale="zh-CN" />
      <Probe />
    </MemoryRouter>,
  );
  return server;
}

const where = () => screen.getByLabelText("当前地址").textContent;

beforeEach(() => {
  localStorage.setItem(`openfox:onboarding:${ORG}`, JSON.stringify({}));
});
afterEach(() => localStorage.clear());

const openTypeMenu = async (user: ReturnType<typeof userEvent.setup>) => {
  const trigger = await screen.findByRole("button", { name: /^修改任务类型/ });
  await waitFor(() => expect(trigger).toBeEnabled());
  await user.click(trigger);
  await screen.findByRole("menuitemradio", { name: /任务/ });
};

describe("任务类型菜单 · 管理任务类型", () => {
  it("能管理空间时显示在底部，点击 push 到该空间的任务类型设置，后退回到任务", async () => {
    const user = userEvent.setup();
    renderAt(withSpace(true));
    await openTypeMenu(user);
    const manage = await screen.findByRole("menuitem", {
      name: "管理任务类型",
    });
    await user.click(manage);
    await waitFor(() =>
      expect(where()).toBe(`/o/${ORG}/settings/spaces/${SPACE}/task-types`),
    );
    await user.click(screen.getByRole("button", { name: "测试后退" }));
    await waitFor(() =>
      expect(where()).toBe(`/o/${ORG}/spaces/${SPACE}/tasks?task=OPEN-1`),
    );
  });

  it("不能管理空间时不显示（不是禁用）", async () => {
    const user = userEvent.setup();
    const server = renderAt((call) => {
      if (call.path === "/v1/me/organizations")
        return json(200, {
          organizations: [
            {
              id: ORG,
              slug: "lin",
              name: "林的团队",
              role: "MEMBER",
              created_at: "2026-10-01T00:00:00Z",
            },
          ],
        });
      return withSpace(false, "MEMBER")(call);
    });
    await openTypeMenu(user);
    await waitFor(() =>
      expect(server.find("GET", `/spaces/${SPACE}`).length).toBeGreaterThan(0),
    );
    expect(screen.queryByRole("menuitem", { name: "管理任务类型" })).toBeNull();
    expect(
      within(screen.getByRole("menu")).queryByRole("separator"),
    ).toBeNull();
  });
});
