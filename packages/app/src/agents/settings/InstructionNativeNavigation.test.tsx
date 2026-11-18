import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { useState } from "react";
import {
  createMemoryRouter,
  RouterProvider,
  Routes,
  Route,
  useLocation,
  useNavigate,
} from "react-router-dom";
import { afterEach, describe, expect, it } from "vitest";
import {
  InstructionNavigationProvider,
  InstructionNavigationPrompt,
} from "./InstructionNavigationProvider";
import {
  useInstructionDirty,
  useInstructionNavigation,
} from "./instructionNavigation";

const initialURL = window.location.href;
afterEach(() => {
  Reflect.deleteProperty(window, "navigation");
  window.history.replaceState(null, "", initialURL);
});

function setup(
  paths = ["/previous", "/agents?agentSettings=a1", "/next", "/last"],
) {
  const entries = paths.map((path, index) => ({
    path,
    key: `router-${index}`,
    nativeKey: `native-${index}`,
  }));
  let index = 1;
  let failNext = false;
  let delayNext = false;
  let finishDelayed: (() => void) | undefined;
  let security!: () => void;
  let save!: () => void;
  let native: EventTarget & {
    currentEntry: { key: string; url: string };
    traverseTo: (
      key: string,
      options: { info: object },
    ) => { committed: Promise<void>; finished: Promise<void> };
  };
  function Editor() {
    const [draft, setDraft] = useState("saved");
    const [other, setOther] = useState("other");
    const [saved, setSaved] = useState("saved");
    useInstructionDirty(draft !== saved || other !== "other");
    save = () => setSaved(draft);
    return (
      <>
        <input
          aria-label="draft"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
        />
        <input
          aria-label="other"
          value={other}
          onChange={(e) => setOther(e.target.value)}
        />
      </>
    );
  }
  function Page() {
    const location = useLocation();
    const navigation = useInstructionNavigation()!;
    const navigate = useNavigate();
    security = () => {
      navigation.releaseForSecurity();
      void navigate("/login", { replace: true });
    };
    return (
      <>
        <output>{location.pathname + location.search}</output>
        {location.pathname === "/agents" && <Editor />}
      </>
    );
  }
  function HeldRoutes() {
    const location = useLocation();
    const navigation = useInstructionNavigation()!;
    return (
      <Routes
        key={navigation.routeEpoch}
        location={navigation.heldLocation ?? location}
      >
        <Route path="*" element={<Page />} />
      </Routes>
    );
  }
  const router = createMemoryRouter(
    [
      {
        path: "*",
        element: (
          <InstructionNavigationProvider>
            <HeldRoutes />
            <InstructionNavigationPrompt />
          </InstructionNavigationProvider>
        ),
      },
    ],
    {
      initialEntries: entries.map((entry) => ({
        pathname: entry.path.split("?")[0],
        search: entry.path.includes("?") ? "?" + entry.path.split("?")[1] : "",
        key: entry.key,
      })),
      initialIndex: index,
    },
  );
  const url = (i: number) => new URL(entries[i]!.path, initialURL).href;
  const move = (target: number, info?: object) => {
    const entry = entries[target]!;
    native.dispatchEvent(
      Object.assign(new Event("navigate"), {
        navigationType: "traverse",
        destination: {
          key: entry.nativeKey,
          url: url(target),
          sameDocument: true,
        },
        info,
      }),
    );
    native.currentEntry = { key: entry.nativeKey, url: url(target) };
    window.history.replaceState({ key: entry.key }, "", entry.path);
    const delta = target - index;
    index = target;
    return router.navigate(delta);
  };
  native = Object.assign(new EventTarget(), {
    currentEntry: { key: entries[index]!.nativeKey, url: url(index) },
    traverseTo: (key: string, options: { info: object }) => {
      if (failNext) {
        failNext = false;
        return {
          committed: Promise.reject(new Error("entry unavailable")),
          finished: Promise.reject(new Error("entry unavailable")),
        };
      }
      const target = entries.findIndex((entry) => entry.nativeKey === key);
      const committed = Promise.resolve(move(target, options.info));
      const finished = delayNext
        ? new Promise<void>((resolve) => {
            finishDelayed = resolve;
          })
        : committed;
      delayNext = false;
      return { committed, finished };
    },
  });
  window.history.replaceState(
    { key: entries[index]!.key },
    "",
    entries[index]!.path,
  );
  Object.defineProperty(window, "navigation", {
    configurable: true,
    value: native,
  });
  render(<RouterProvider router={router} />);
  const edit = () =>
    fireEvent.change(screen.getByRole("textbox", { name: "draft" }), {
      target: { value: "中文 draft" },
    });
  return {
    router,
    native,
    edit,
    move,
    save: () => save(),
    security: () => security(),
    fail: () => {
      failNext = true;
    },
    delay: () => {
      delayNext = true;
    },
    finish: () => finishDelayed?.(),
  };
}
const cancel = () =>
  fireEvent.click(screen.getByRole("button", { name: "继续编辑" }));
const proceed = () =>
  fireEvent.click(screen.getByRole("button", { name: "放弃修改" }));
const closed = () =>
  waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

describe("native history holds the actual editor subtree", () => {
  it("keeps mixed drafts mounted across repeated mixed-direction POP, cancels by entry key, then confirms the first target", async () => {
    const s = setup();
    s.edit();
    const input = screen.getByRole("textbox", { name: "draft" });
    fireEvent.change(screen.getByRole("textbox", { name: "other" }), {
      target: { value: "mixed draft" },
    });
    await act(() => s.move(0));
    await act(() => s.move(2));
    await act(() => s.move(3));
    expect(screen.getByRole("textbox", { name: "draft" })).toBe(input);
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    cancel();
    await closed();
    expect(s.native.currentEntry.key).toBe("native-1");
    expect(s.router.state.location.key).toBe("router-1");
    expect(input).toHaveValue("中文 draft");
    expect(screen.getByRole("textbox", { name: "other" })).toHaveValue(
      "mixed draft",
    );
    await act(() => s.move(2));
    await act(() => s.move(0));
    proceed();
    await closed();
    expect(s.native.currentEntry.key).toBe("native-2");
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    await act(() => s.move(1));
    s.edit();
    await act(() => s.move(0));
    cancel();
    await closed();
    expect(screen.getByRole("textbox", { name: "draft" })).toHaveValue(
      "中文 draft",
    );
  });
  it("retains drafts after rejected recovery, supports retry and explicit discard at the current address", async () => {
    const s = setup();
    s.edit();
    await act(() => s.move(2));
    s.fail();
    cancel();
    await screen.findByText(/未能确认历史位置/);
    expect(screen.getByRole("textbox", { name: "draft" })).toHaveValue(
      "中文 draft",
    );
    cancel();
    await closed();
    expect(s.native.currentEntry.key).toBe("native-1");
    await act(() => s.move(2));
    await act(() => s.move(1));
    s.fail();
    proceed();
    await screen.findByText(/未能确认历史位置/);
    fireEvent.click(
      screen.getByRole("button", { name: "放弃草稿并继续到当前地址" }),
    );
    await closed();
    expect(s.native.currentEntry.key).toBe("native-1");
    expect(screen.getByRole("textbox", { name: "draft" })).toHaveValue("saved");
  });
  it("does not accept a stale traversal completion after a newer native navigation", async () => {
    const s = setup();
    s.edit();
    await act(() => s.move(0));
    s.delay();
    cancel();
    await act(() => s.move(3));
    await screen.findByText(/未能确认历史位置/);
    await act(() => s.finish());
    expect(screen.getByRole("dialog")).toBeVisible();
    expect(screen.getByRole("textbox", { name: "draft" })).toHaveValue(
      "中文 draft",
    );
    proceed();
    await closed();
    expect(s.native.currentEntry.key).toBe("native-0");
  });
  it("restores the source when an in-flight save completes and retains beforeunload while dirty", async () => {
    const s = setup();
    s.edit();
    await act(() => s.move(2));
    const unload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);
    act(() => s.save());
    await closed();
    expect(s.native.currentEntry.key).toBe("native-1");
    expect(screen.getByRole("textbox", { name: "draft" })).toHaveValue(
      "中文 draft",
    );
    const cleanUnload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(cleanUnload);
    expect(cleanUnload.defaultPrevented).toBe(false);
  });
  it("updates the held location and source key for benign panel navigation", async () => {
    const s = setup([
      "/previous",
      "/agents?agentSettings=a1",
      "/agents?agentSettings=a1&agentPanel=full",
      "/next",
    ]);
    s.edit();
    await act(() => s.move(2));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(
      screen.getByText("/agents?agentSettings=a1&agentPanel=full"),
    ).toBeVisible();
    await act(() => s.move(3));
    cancel();
    await closed();
    expect(s.native.currentEntry.key).toBe("native-2");
  });
  it("releases pending protection for an actual security redirect", async () => {
    const s = setup();
    s.edit();
    await act(() => s.move(0));
    await act(() => s.security());
    await closed();
    expect(s.router.state.location.pathname).toBe("/login");
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });
});
