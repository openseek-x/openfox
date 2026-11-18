import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { SpaceTaskType } from "@openfox/api";
import {
  apiError,
  findCall,
  json,
  renderSettings,
  ts,
} from "../settings/testing";
import { moveTaskType } from "./settings/type-order";
const base = "/v1/organizations/o1/spaces/s1";
function type(id: string, archived = false): SpaceTaskType {
  return {
    id,
    organization_id: "o1",
    space_id: "s1",
    name: id,
    description: "",
    enabled: true,
    is_default: id === "First",
    version: 3,
    created_at: ts,
    statuses: [],
    archived_at: archived ? ts : undefined,
    commit_type: null,
  };
}
function fixture(
  options: { readOnly?: boolean; conflict?: boolean; detail?: boolean } = {},
) {
  let types = [type("First"), type("Archived", true), type("Last")];
  const calls = renderSettings(
    `/o/o1/settings/spaces/s1/task-types${options.detail ? "/First" : ""}`,
    (call) => {
      if (call.path === base)
        return json(200, {
          id: "s1",
          prefix: "OPEN",
          name: "Space",
          visibility: "shared",
          version: 1,
          created_at: ts,
          updated_at: ts,
          icon: null,
          purpose: "software_development",
          features: {
            views: true,
            projects: true,
            iterations: false,
            triage: true,
          },
          multi_assignee: false,
          estimation: {
            scale: "notUsed",
            extended: false,
            allow_zero: false,
            count_unestimated: false,
          },
          status: "active",
          retired_at: null,
          position: 0,
          can_manage: !options.readOnly,
          my_role: options.readOnly ? "MEMBER" : "OWNER",
          member_count: 1,
          task_type_count: 3,
          label_count: 0,
          task_count: 6,
        });

      if (call.path === `${base}/task-types`)
        return json(200, { task_types: types });
      if (call.path.endsWith("/task-type-templates"))
        return json(200, { templates: [] });
      if (call.path.endsWith("/task-statuses"))
        return json(200, { statuses: [] });
      if (call.path.endsWith("/fields"))
        return json(200, { custom_fields: [] });
      if (call.path === `${base}/task-types/reorder`) {
        if (options.conflict) return apiError(409, "VERSION_CONFLICT");
        const order = (
          call.body as { types: { id: string; version: number }[] }
        ).types;
        types = order.map((item) => ({
          ...types.find((type) => type.id === item.id)!,
          version: item.version + 1,
        }));
        return json(200, { task_types: types });
      }
      if (call.path === `${base}/task-types/First` && call.method === "PATCH") {
        if (options.conflict) return apiError(409, "VERSION_CONFLICT");
        types[0] = {
          ...types[0]!,
          ...(call.body as object),
          version: types[0]!.version + 1,
        };
        return json(200, types[0]);
      }
      return undefined;
    },
    options.readOnly ? "MEMBER" : "OWNER",
  );
  return { calls };
}
describe("任务类型顺序和提交前缀", () => {
  it("上移使用完整版本列表且保留归档槽位，读回顺序", async () => {
    const user = userEvent.setup();
    const { calls } = fixture();
    await user.click(
      await screen.findByRole("button", { name: "Last 更多操作" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "上移" }));
    await waitFor(() =>
      expect(
        findCall(calls, "POST", `${base}/task-types/reorder`)?.body,
      ).toEqual({
        types: [
          { id: "Last", version: 3 },
          { id: "Archived", version: 3 },
          { id: "First", version: 3 },
        ],
      }),
    );
    await waitFor(() =>
      expect(
        within(
          screen.getByRole("list", { name: "任务类型 & 状态" }),
        ).getAllByRole("listitem")[0],
      ).toHaveTextContent("Last"),
    );
  });
  it("冲突回读且不显示假顺序", async () => {
    const user = userEvent.setup();
    fixture({ conflict: true });
    await user.click(
      await screen.findByRole("button", { name: "Last 更多操作" }),
    );
    await user.click(screen.getByRole("menuitem", { name: "上移" }));
    await screen.findByText("内容已被他人修改，已刷新");
    expect(
      within(
        screen.getByRole("list", { name: "任务类型 & 状态" }),
      ).getAllByRole("listitem")[0],
    ).toHaveTextContent("First");
  });
  it("只读用户不显示排序柄，提交前缀不可编辑", async () => {
    fixture({ readOnly: true, detail: true });
    expect(
      await screen.findByRole("textbox", { name: "提交类型前缀" }),
    ).toBeDisabled();
    expect(
      screen.queryByRole("button", { name: "调整「First」顺序" }),
    ).not.toBeInTheDocument();
  });
  it("前缀验证、版本保存和清空，保留大小写", async () => {
    const user = userEvent.setup();
    const { calls } = fixture({ detail: true });
    const input = await screen.findByRole("textbox", { name: "提交类型前缀" });
    await user.type(input, "bad:value");
    await user.tab();
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(
      findCall(calls, "PATCH", `${base}/task-types/First`),
    ).toBeUndefined();
    await user.clear(input);
    await user.type(input, "Fix-42");
    await user.tab();
    await waitFor(() =>
      expect(
        findCall(calls, "PATCH", `${base}/task-types/First`)?.body,
      ).toEqual({ commit_type: "Fix-42", version: 3 }),
    );
    await waitFor(() => expect(input).not.toBeDisabled());
    await user.clear(input);
    await user.tab();
    await waitFor(() =>
      expect(
        calls.filter((call) => call.method === "PATCH").at(-1)?.body,
      ).toEqual({ commit_type: null, version: 4 }),
    );
  });
  it("归档槽位和空列表移动边界", () => {
    const all = [type("First"), type("Archived", true), type("Last")];
    expect(moveTaskType(all, "Last", "First").map((t) => t.id)).toEqual([
      "Last",
      "Archived",
      "First",
    ]);
    expect(moveTaskType(all, "unknown", "First")).toBe(all);
    expect(moveTaskType([], "Last", "First")).toEqual([]);
  });
});
