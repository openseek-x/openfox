import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import {
  createMemoryRouter,
  Link,
  RouterProvider,
  useLocation,
  useNavigate,
} from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { InstructionsEditor } from "./InstructionsEditor";
import {
  InstructionNavigationProvider,
  InstructionNavigationPrompt,
} from "./InstructionNavigationProvider";
import {
  useInstructionDirty,
  useInstructionNavigation,
} from "./instructionNavigation";

function Draft({
  save,
}: {
  save: (value: string) => Promise<string | undefined>;
}) {
  const [dirty, setDirty] = useState(false);
  useInstructionDirty(dirty);
  return (
    <InstructionsEditor
      saved="saved"
      label="指令"
      editable
      onSave={save}
      onDirty={setDirty}
    />
  );
}

function setup(
  save = async (value: string): Promise<string | undefined> => value,
) {
  function Page() {
    const location = useLocation();
    const navigate = useNavigate();
    const navigation = useInstructionNavigation()!;
    return (
      <>
        <output>{location.pathname + location.search}</output>
        <Link to="/next">下一页</Link>
        <button
          onClick={() => navigation.runConfirmed(() => navigate("/next"))}
        >
          抽屉已确认离开
        </button>
        <button onClick={() => navigation.runConfirmed(() => {})}>
          仅确认卡片折叠
        </button>
        {location.pathname === "/agents" && (
          <Draft
            key={new URLSearchParams(location.search).get("agentSettings")}
            save={save}
          />
        )}
      </>
    );
  }
  const router = createMemoryRouter(
    [
      {
        path: "*",
        element: (
          <InstructionNavigationProvider>
            <Page />
            <InstructionNavigationPrompt />
          </InstructionNavigationProvider>
        ),
      },
    ],
    {
      initialEntries: ["/previous", "/agents?agentSettings=a1", "/next"],
      initialIndex: 1,
    },
  );
  const view = render(<RouterProvider router={router} />);
  const edit = () =>
    fireEvent.change(screen.getByRole("textbox"), {
      target: { value: "unsaved 中文" },
    });
  return { router, edit, view };
}

describe("Data Router instruction navigation", () => {
  it("cancels back/forward with the same URL and draft, then proceeds in the original history direction", async () => {
    const { router, edit } = setup();
    const user = userEvent.setup();
    edit();
    expect(screen.getByText(/此浏览器不支持精确恢复连续返回/)).toBeVisible();
    for (const delta of [-1, 1, -1]) {
      await act(() => router.navigate(delta));
      await user.click(await screen.findByRole("button", { name: "继续编辑" }));
      expect(
        router.state.location.pathname + router.state.location.search,
      ).toBe("/agents?agentSettings=a1");
      expect(screen.getByRole("textbox")).toHaveValue("unsaved 中文");
    }
    await act(() => router.navigate(-1));
    await user.click(await screen.findByRole("button", { name: "放弃修改" }));
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/previous"),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await act(() => router.navigate(1));
    expect(screen.getByRole("textbox")).toHaveValue("saved");
    edit();
    await act(() => router.navigate(1));
    await user.click(await screen.findByRole("button", { name: "放弃修改" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/next"));
  });

  it("has one confirmation for links/programmatic switches and scopes prior drawer approval to its action", async () => {
    const confirm = vi.spyOn(window, "confirm");
    const { router, edit } = setup();
    const user = userEvent.setup();
    edit();
    await user.click(screen.getByRole("button", { name: "仅确认卡片折叠" }));
    await user.click(screen.getByRole("link", { name: "下一页" }));
    expect(await screen.findAllByRole("dialog")).toHaveLength(1);
    expect(confirm).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "继续编辑" }));
    await act(() => router.navigate("/agents?agentSettings=a2"));
    await user.click(await screen.findByRole("button", { name: "继续编辑" }));
    expect(screen.getByRole("textbox")).toHaveValue("unsaved 中文");
    await user.click(screen.getByRole("button", { name: "抽屉已确认离开" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/next"));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("preserves dirty edits during panel width/anchor changes and still protects hard unload", async () => {
    const { router, edit } = setup();
    edit();
    await act(() =>
      router.navigate(
        "/agents?agentSettings=a1&agentPanel=full&agentSection=run#section",
      ),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("textbox")).toHaveValue("unsaved 中文");
    const unload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);
    await act(() => router.navigate("/agents?agentSettings=a1&agentTab=runs"));
    expect(await screen.findByRole("dialog")).toBeVisible();
  });

  it("keeps a failed save guarded and releases the prompt if an in-flight save later succeeds", async () => {
    let finish!: (value: string | undefined) => void;
    const save = vi.fn(
      () =>
        new Promise<string | undefined>((resolve) => {
          finish = resolve;
        }),
    );
    const { router, edit } = setup(save);
    const user = userEvent.setup();
    edit();
    await user.click(screen.getByRole("button", { name: "保存" }));
    await act(async () => finish(undefined));
    await act(() => router.navigate(-1));
    await user.click(await screen.findByRole("button", { name: "继续编辑" }));
    expect(screen.getByRole("textbox")).toHaveValue("unsaved 中文");
    await user.click(screen.getByRole("button", { name: "保存" }));
    await act(() => router.navigate(-1));
    expect(await screen.findByRole("dialog")).toBeVisible();
    await act(async () => finish("unsaved 中文"));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(router.state.location.pathname).toBe("/agents");
    await act(() => router.navigate(-1));
    expect(router.state.location.pathname).toBe("/previous");
  });
});
