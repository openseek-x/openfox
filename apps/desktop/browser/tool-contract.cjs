const { failure } = require("./policy.cjs");

const id = (value) =>
  typeof value === "string" && /^[a-zA-Z0-9_-]{1,128}$/.test(value);
const string =
  (max, empty = false) =>
  (value) =>
    typeof value === "string" &&
    (empty || value.length > 0) &&
    value.length <= max &&
    !value.includes("\0");
const number = (max) => (value) =>
  Number.isFinite(value) && Math.abs(value) <= max;
const validators = {
  action: string(32),
  tab_id: id,
  frame_id: id,
  ref: id,
  file_id: id,
  document_generation: (value) => Number.isSafeInteger(value) && value > 0,
  url: string(16384),
  text: string(32768, true),
  query: string(256),
  key: (value) =>
    [
      "Enter",
      "Tab",
      "Escape",
      "Backspace",
      "Delete",
      "ArrowLeft",
      "ArrowUp",
      "ArrowRight",
      "ArrowDown",
      "Home",
      "End",
      "PageUp",
      "PageDown",
      "Space",
    ].includes(value),
  checked: (value) => typeof value === "boolean",
  accept: (value) => typeof value === "boolean",
  delta_x: number(10000),
  delta_y: number(10000),
  values: (value) =>
    Array.isArray(value) &&
    value.length > 0 &&
    value.length <= 100 &&
    value.every(string(2048, true)),
  paths: (value) =>
    Array.isArray(value) &&
    value.length > 0 &&
    value.length <= 10 &&
    value.every(string(4096)),
  timeout_ms: (value) =>
    Number.isInteger(value) && value >= 0 && value <= 30000,
};
const element = "tab_id frame_id document_generation ref";
// A trailing ? denotes an optional field. There are no pass-through fields.
const actions = {
  openfox_browser_tabs: {
    list: "",
    open: "url",
    focus: "tab_id",
    close: "tab_id",
  },
  openfox_browser_observe: {
    snapshot: "tab_id frame_id?",
    frames: "tab_id",
    find: "tab_id frame_id? query",
    screenshot: "tab_id",
    wait: "tab_id frame_id? query timeout_ms?",
    console: "tab_id",
  },
  openfox_browser_act: {
    navigate: "tab_id url",
    back: "tab_id",
    forward: "tab_id",
    reload: "tab_id",
    stop: "tab_id",
    click: element,
    hover: element,
    fill: `${element} text`,
    select: `${element} values`,
    check: `${element} checked`,
    press: `${element} key`,
    scroll: `${element} delta_y delta_x?`,
    dialog: "tab_id accept text?",
  },
  openfox_browser_files: {
    list: "tab_id",
    upload: `${element} paths`,
    get: "tab_id file_id",
  },
};

function validateTool(tool, args) {
  if (
    !args ||
    typeof args !== "object" ||
    Array.isArray(args) ||
    !Object.hasOwn(actions, tool) ||
    !Object.hasOwn(actions[tool], args.action)
  )
    throw failure("INVALID_REQUEST");
  const required = ["action"],
    allowed = new Set(["action"]);
  for (const field of actions[tool][args.action].split(" ").filter(Boolean)) {
    const name = field.replace(/\?$/, "");
    allowed.add(name);
    if (!field.endsWith("?")) required.push(name);
  }
  if (required.some((name) => !Object.hasOwn(args, name)))
    throw failure("INVALID_REQUEST");
  for (const [name, value] of Object.entries(args))
    if (!allowed.has(name) || !validators[name](value))
      throw failure("INVALID_REQUEST");
  return args;
}

module.exports = { validateTool };
