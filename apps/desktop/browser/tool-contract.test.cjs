const test = require("node:test");
const assert = require("node:assert/strict");
const { validateTool } = require("./tool-contract.cjs");

test("browser tools reject forged identity, scripts, unsupported actions and incomplete refs", () => {
  const click = {
    action: "click",
    tab_id: "tab",
    frame_id: "frame",
    document_generation: 1,
    ref: "ref",
  };
  assert.equal(validateTool("openfox_browser_act", click), click);
  for (const args of [
    { ...click, run_id: "another" },
    { ...click, script: "fetch('/private')" },
    { ...click, document_generation: 0 },
    { ...click, ref: "" },
    { ...click, frame_id: undefined },
    { ...click, action: "evaluate" },
    { ...click, text: "unexpected" },
  ])
    assert.throws(() => validateTool("openfox_browser_act", args), {
      code: "INVALID_REQUEST",
    });
  assert.throws(() => validateTool("__proto__", { action: "list" }), {
    code: "INVALID_REQUEST",
  });
  assert.throws(
    () => validateTool("openfox_browser_tabs", { action: "constructor" }),
    { code: "INVALID_REQUEST" },
  );
  assert.throws(
    () =>
      validateTool("openfox_browser_tabs", {
        action: "navigate",
        url: "https://example.test",
      }),
    { code: "INVALID_REQUEST" },
  );
  assert.throws(
    () =>
      validateTool("openfox_browser_act", {
        ...click,
        action: "fill",
        text: "x".repeat(32769),
      }),
    { code: "INVALID_REQUEST" },
  );
  assert.throws(
    () =>
      validateTool("openfox_browser_files", {
        ...click,
        action: "upload",
        paths: ["a\0b"],
      }),
    { code: "INVALID_REQUEST" },
  );
});
