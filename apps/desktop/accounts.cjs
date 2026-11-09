// Each additional login has an independent Chromium cookie jar. Only display
// names and slot IDs live in this file. The original login uses defaultSession.
const fs = require("node:fs/promises");
const path = require("node:path");
const { createHash, randomUUID } = require("node:crypto");

async function openAccounts(directory, origin) {
  const key = createHash("sha256").update(origin).digest("hex");
  const file = path.join(directory, "accounts-" + key + ".json");
  let state = { current: "default", slots: [{ id: "default", name: "" }] };
  try {
    const saved = JSON.parse(await fs.readFile(file, "utf8"));
    if (
      Array.isArray(saved.slots) &&
      saved.slots.length <= 32 &&
      saved.slots.every(
        (s) =>
          /^(default|[a-f0-9-]{36})$/.test(s.id) && typeof s.name === "string",
      ) &&
      saved.slots.some((s) => s.id === saved.current)
    )
      state = saved;
  } catch (err) {
    if (err.code !== "ENOENT") throw err;
  }
  let writing = Promise.resolve();
  function save(update) {
    const write = async () => {
      const next = update(state);
      if (next === state) return;
      const tmp = file + "." + randomUUID() + ".tmp";
      await fs.writeFile(tmp, JSON.stringify(next), { mode: 0o600 });
      try {
        await fs.rename(tmp, file);
      } finally {
        await fs.rm(tmp, { force: true }).catch(() => {});
      }
      state = next;
    };
    writing = writing.catch(() => {}).then(write);
    return writing;
  }
  return {
    partition() {
      return state.current === "default"
        ? undefined
        : "persist:account-" + state.current;
    },
    list() {
      return state.slots.map((s) => ({
        ...s,
        current: s.id === state.current,
      }));
    },
    async name(name) {
      if (typeof name !== "string" || name.length > 320)
        throw Error("invalid account name");
      const id = state.current;
      await save((current) => {
        if (current.slots.find((s) => s.id === id)?.name === name)
          return current;
        return {
          ...current,
          slots: current.slots.map((s) => (s.id === id ? { ...s, name } : s)),
        };
      });
    },
    async select(id) {
      await save((current) => {
        let slots = current.slots;
        if (id === null) {
          const empty = slots.find((s) => !s.name && s.id !== current.current);
          if (empty) id = empty.id;
          else {
            if (slots.length >= 32) throw Error("account limit reached");
            id = randomUUID();
            slots = [...slots, { id, name: "" }];
          }
        }
        if (!slots.some((s) => s.id === id)) throw Error("unknown account");
        return { current: id, slots };
      });
    },
  };
}
module.exports = { openAccounts };
