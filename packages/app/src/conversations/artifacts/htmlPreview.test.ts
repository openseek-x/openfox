import { describe, expect, it } from "vitest";

import { htmlPreviewDocument } from "./htmlDocument";

describe("static artifact HTML", () => {
  it("keeps styles, tables and embedded raster images, removing active content and navigation", () => {
    const html = htmlPreviewDocument(`<!doctype html><html><head>
      <meta http-equiv="refresh" content="0;url=https://evil.test">
      <base href="https://evil.test"><style>h1 { color: red }</style>
      <link rel="stylesheet" href="https://evil.test/style.css"></head><body onload="alert(1)">
      <h1 class="title">Report</h1><script>window.parent.stolen = true</script>
      <iframe src="https://evil.test"></iframe><object data="https://evil.test"></object>
      <svg><script>alert(1)</script></svg><math href="javascript:alert(1)"></math>
      <template><img src="https://evil.test/hidden"></template>
      <a href="https://evil.test" target="_top">Link</a>
      <form action="https://evil.test"><input name="secret" autofocus><button formaction="https://evil.test">Send</button></form>
      <img src="https://evil.test/image" srcset="https://evil.test/2x 2x" onerror="alert(1)">
      <img alt="pixel" src="data:image/png;base64,aGVsbG8=">
      <img src="data:image/svg+xml;base64,aGVsbG8=">
      <table><tr><td colspan="2">Cell</td></tr></table></body></html>`).document;
    const doc = new DOMParser().parseFromString(html, "text/html");
    expect(
      doc
        .querySelector('meta[http-equiv="Content-Security-Policy"]')
        ?.getAttribute("content"),
    ).toContain("script-src 'none'");
    expect(doc.querySelector("h1")?.className).toBe("title");
    expect(doc.querySelector("td")?.getAttribute("colspan")).toBe("2");
    expect(html).toContain("h1 { color: red }");
    expect(
      doc.querySelector(
        "script,iframe,object,svg,math,template,base,link,[href],[action],[formaction],[onerror],[onload],[srcset],[autofocus]",
      ),
    ).toBeNull();
    expect(doc.querySelector("input")?.disabled).toBe(true);
    expect(doc.querySelector("button")?.disabled).toBe(true);
    expect(doc.querySelectorAll("img[src]")).toHaveLength(1);
    expect(doc.querySelector("img[src]")?.getAttribute("alt")).toBe("pixel");
  });
});
