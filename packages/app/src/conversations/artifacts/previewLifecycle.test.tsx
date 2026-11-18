import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { I18nProvider } from "@openfox/i18n";
import { createApi, HttpClient, type OfficeView } from "@openfox/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ApiProvider } from "../../context";
import { OfficeReview } from "./OfficeReview";
import { ArtifactReview } from "./ArtifactReview";
import { PreviewStateKey } from "./viewState";
import { VisualReview } from "./VisualReview";

vi.mock("./OfficePages", () => ({
  OfficePages: ({ onFailure }: { onFailure: () => void }) => (
    <button onClick={onFailure}>parser failed</button>
  ),
}));
it("unmount cancels an in-flight Office fallback conversion", async () => {
  let signal: AbortSignal | undefined;
  const fallback = vi.fn((s: AbortSignal) => {
    signal = s;
    return new Promise<Blob>((_, reject) =>
      s.addEventListener("abort", () =>
        reject(new DOMException("Aborted", "AbortError")),
      ),
    );
  });
  const view = {
    kind: "docx",
    sha256: "a".repeat(64),
    units: [],
  } as OfficeView;
  const { unmount } = render(
    <I18nProvider initialLocale="zh-CN">
      <OfficeReview
        view={view}
        name="report.docx"
        blob={new Blob()}
        loadFallback={fallback}
      />
    </I18nProvider>,
  );
  fireEvent.click(await screen.findByText("parser failed"));
  await waitFor(() => expect(fallback).toHaveBeenCalledTimes(1));
  unmount();
  expect(signal?.aborted).toBe(true);
});

// The parent CSV pager is zero-based; a PDF child is one-based. They must not
// overwrite each other's persisted state on first mount or on an Office fallback.
vi.mock("./PDFPage", () => ({
  default: ({ page }: { page: number }) => <p>PDF page {page}</p>,
}));

it("restores the PDF page independently from the parent CSV pager", async () => {
  const api = createApi(
    new HttpClient("/api", async () => new Response("PDF")),
  );
  const mount = () =>
    render(
      <I18nProvider initialLocale="zh-CN">
        <ApiProvider api={api}>
          <QueryClientProvider client={new QueryClient()}>
            <PreviewStateKey.Provider value="pdf-remount">
              <ArtifactReview
                orgId="o1"
                conversationId="c1"
                embedded
                artifact={{
                  organization_id: "o1",
                  id: "pdf",
                  run_id: "r1",
                  name: "sample.pdf",
                  mime_type: "application/pdf",
                  size_bytes: 3,
                  sha256: "a".repeat(64),
                  state: "AVAILABLE",
                  created_at: "2026-11-17T00:00:00Z",
                  pinned: false,
                  version: 1,
                }}
              />
            </PreviewStateKey.Provider>
          </QueryClientProvider>
        </ApiProvider>
      </I18nProvider>,
    );
  const first = mount();
  await screen.findByText("PDF page 1");
  fireEvent.change(screen.getByRole("combobox", { name: "缩放" }), {
    target: { value: "1.5" },
  });
  first.unmount();
  mount();
  await screen.findByText("PDF page 1");
  expect(screen.getByRole("combobox", { name: "缩放" })).toHaveValue("1.5");
});

// Locating an immutable region does not turn ordinary browsing into selection mode.
it("shows a located image region with annotation disabled and preserves it through zoom", async () => {
  const anchor = {
    kind: "region" as const,
    start: 0,
    end: 0,
    quote: "",
    page: 1,
    x: 0.1,
    y: 0.2,
    width: 0.3,
    height: 0.4,
  };
  const select = vi.fn();
  const { container } = render(
    <I18nProvider initialLocale="zh-CN">
      <VisualReview
        blob={new Blob(["image"])}
        name="located.png"
        located={anchor}
        onSelect={select}
      />
    </I18nProvider>,
  );
  const image = await screen.findByRole("img", { name: "located.png" });
  fireEvent.load(image);
  const region = container.querySelector('[data-located-region="true"]');
  for (const [key, value] of Object.entries({
    left: 10,
    top: 20,
    width: 30,
    height: 40,
  }))
    expect(
      parseFloat((region as HTMLElement).style.getPropertyValue(key)),
    ).toBeCloseTo(value, 6);
  fireEvent.change(screen.getByRole("combobox", { name: "缩放" }), {
    target: { value: "1.5" },
  });
  for (const [key, value] of Object.entries({
    left: 10,
    top: 20,
    width: 30,
    height: 40,
  }))
    expect(
      parseFloat((region as HTMLElement).style.getPropertyValue(key)),
    ).toBeCloseTo(value, 6);
  fireEvent.pointerDown(image, { button: 0, clientX: 20, clientY: 20 });
  fireEvent.pointerUp(image, { button: 0, clientX: 40, clientY: 40 });
  expect(select).not.toHaveBeenCalled();
});
