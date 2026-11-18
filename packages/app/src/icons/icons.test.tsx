import { StrictMode, useState, type ReactNode } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { createApi } from "@openfox/api";
import { I18nProvider } from "@openfox/i18n";
import { ToastProvider } from "@openfox/ui";
import { ApiProvider } from "../context";
import { AgentAvatar, AvatarPicker } from "../agents/AgentAvatar";
import { readAvatar, type AvatarSpec } from "../agents/model";
import {
  SpaceIcon,
  SpaceIconPicker,
  parseSpaceIcon,
} from "../spaces/SpaceIcon";
import { uploadedIconURL } from "./uploaded";

const orgId = "00000000-0000-0000-0000-000000000001";
const iconId = "00000000-0000-0000-0000-000000000002";
const url = uploadedIconURL(orgId, iconId)!;
const uploaded = { id: iconId, url, content_type: "image/png", size_bytes: 4 };
const file = () => new File(["png"], "avatar.png", { type: "image/png" });

function mount(
  children: ReactNode,
  upload = vi.fn().mockResolvedValue(uploaded),
) {
  const api = createApi();
  api.icons.upload = upload;
  const result = render(
    <StrictMode>
      <MemoryRouter initialEntries={[`/o/${orgId}`]}>
        <Routes>
          <Route
            path="/o/:orgId"
            element={
              <I18nProvider initialLocale="zh-CN">
                <ApiProvider api={api}>
                  <ToastProvider>{children}</ToastProvider>
                </ApiProvider>
              </I18nProvider>
            }
          />
        </Routes>
      </MemoryRouter>
    </StrictMode>,
  );
  return { ...result, upload };
}

function AgentHarness() {
  const [avatar, setAvatar] = useState<AvatarSpec>({
    kind: "icon",
    value: "bot",
    color: "#22C55E",
  });
  const [busy, setBusy] = useState(false);
  return (
    <>
      <AvatarPicker
        orgId={orgId}
        name="Helper"
        value={avatar}
        onChange={setAvatar}
        onBusyChange={setBusy}
      />
      <button disabled={busy}>Create</button>
    </>
  );
}

function SpaceHarness() {
  const [icon, setIcon] = useState<string>("orbit:#22C55E");
  return (
    <>
      <SpaceIconPicker orgId={orgId} icon={icon} onChange={setIcon} />
      <SpaceIcon icon={icon} size={16} />
    </>
  );
}

describe("uploaded icon references", () => {
  it("accepts only same-origin API icon paths and keeps legacy formats", () => {
    expect(readAvatar({ kind: "upload", value: url })?.value).toBe(url);
    for (const value of [
      "https://example.test/icon.png",
      "javascript:alert(1)",
      `${url}?x=1`,
      `${url}/../other`,
    ]) {
      expect(readAvatar({ kind: "upload", value })).toBeNull();
    }
    expect(readAvatar({ kind: "mood", value: "ᵔᴗᵔ" })?.kind).toBe("mood");
    expect(parseSpaceIcon("orbit:#22C55E")).toEqual({
      name: "orbit",
      color: "#22C55E",
    });
    expect(parseSpaceIcon("emoji:🚀").emoji).toBe("🚀");
    expect(uploadedIconURL(orgId, "../../other")).toBeNull();
  });

  it("renders uploaded Agent avatars without losing private and offline states", () => {
    const { container } = mount(
      <AgentAvatar
        name="Helper"
        avatar={{ kind: "upload", value: url }}
        locked
        offline
      />,
    );
    expect(container.querySelector("img")).toHaveAttribute("src", url);
    expect(screen.getByRole("img", { name: "Helper" })).toHaveClass(
      "grayscale",
    );
    expect(container.querySelector("[aria-label='私有']")).toBeTruthy();
  });
});

describe("icon pickers", () => {
  it("uploads an Agent image, blocks creation while pending, and displays the result", async () => {
    let finish!: (value: typeof uploaded) => void;
    const upload = vi.fn(
      () =>
        new Promise<typeof uploaded>((resolve) => {
          finish = resolve;
        }),
    );
    const user = userEvent.setup();
    const { container } = mount(<AgentHarness />, upload);
    await user.click(screen.getByRole("button", { name: "选择头像" }));
    await user.upload(screen.getByLabelText("上传图片"), file());
    expect(screen.getByRole("button", { name: "Create" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "#22C55E" })).toBeDisabled();
    expect(upload).toHaveBeenCalledExactlyOnceWith(orgId, expect.any(File));
    await act(async () => finish(uploaded));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Create" })).toBeEnabled(),
    );
    expect(container.querySelector("img")).toHaveAttribute("src", url);
  });

  it("preserves the avatar on failure and allows retrying the same file", async () => {
    const upload = vi
      .fn()
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce(uploaded);
    const user = userEvent.setup();
    const { container } = mount(<AgentHarness />, upload);
    await user.click(screen.getByRole("button", { name: "选择头像" }));
    const input = screen.getByLabelText("上传图片");
    const selected = file();
    await user.upload(input, selected);
    expect(container.querySelector("img")).toBeNull();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Create" })).toBeEnabled(),
    );
    await user.upload(input, selected);
    await waitFor(() =>
      expect(container.querySelector("img")).toHaveAttribute("src", url),
    );
    expect(upload).toHaveBeenCalledTimes(2);
  });

  it("rejects oversized images without issuing an upload", async () => {
    const user = userEvent.setup();
    const { upload } = mount(<AgentHarness />);
    await user.click(screen.getByRole("button", { name: "选择头像" }));
    await user.upload(
      screen.getByLabelText("上传图片"),
      new File([new Uint8Array(1024 * 1024 + 1)], "large.png", {
        type: "image/png",
      }),
    );
    expect(upload).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Create" })).toBeEnabled();
  });

  it("ignores a completed upload after closing the picker", async () => {
    let finish!: (value: typeof uploaded) => void;
    const upload = vi.fn(
      () =>
        new Promise<typeof uploaded>((resolve) => {
          finish = resolve;
        }),
    );
    const user = userEvent.setup();
    const { container } = mount(<AgentHarness />, upload);
    await user.click(screen.getByRole("button", { name: "选择头像" }));
    await user.upload(screen.getByLabelText("上传图片"), file());
    await user.keyboard("{Escape}");
    await act(async () => finish(uploaded));
    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByRole("button", { name: "Create" })).toBeEnabled();
  });

  it("selects and clears an emoji and renders an uploaded space icon in other entry points", async () => {
    const user = userEvent.setup();
    const { container } = mount(<SpaceHarness />);
    await user.click(screen.getByRole("button", { name: "选择图标" }));
    await user.click(screen.getByRole("tab", { name: "表情" }));
    await user.click(screen.getByRole("button", { name: "🚀" }));
    expect(screen.getAllByText("🚀")).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: "选择图标" }));
    await user.click(screen.getByRole("button", { name: "🚀" }));
    expect(screen.queryByText("🚀")).toBeNull();
    await user.click(screen.getByRole("button", { name: "选择图标" }));
    await user.click(screen.getByRole("tab", { name: "上传" }));
    await user.upload(
      screen.getByLabelText("上传", { selector: "input" }),
      file(),
    );
    await waitFor(() =>
      expect(container.querySelectorAll("img")).toHaveLength(2),
    );
    for (const image of container.querySelectorAll("img"))
      expect(image).toHaveAttribute("src", url);
  });
});
