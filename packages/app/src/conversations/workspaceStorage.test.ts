import { describe, expect, it } from "vitest";
import {
  openWorkspaceDB,
  WorkspaceStore,
  bindWorkspaceStore,
  MAX_WORKSPACE_BYTES,
} from "./workspaceStorage";
import {
  readDraft,
  saveDraft,
  saveAnnotationBody,
  readAnnotationBody,
} from "./drafts";
import { consumeSentDraft } from "./pendingSend";

const scope = JSON.stringify(["user-1", "org-1"]);
async function open(key = scope) {
  const store = new WorkspaceStore(key, await openWorkspaceDB());
  await store.hydrate();
  return store;
}
describe("durable workspace", () => {
  it("restores drafts, annotations and view state after closing the database", async () => {
    const first = await open();
    bindWorkspaceStore(first);
    saveDraft("c1", { body: "未发送内容", picked: [], files: [] });
    saveAnnotationBody("anchor", "未提交批注");
    first.write("workspace:c1", { tabs: ["word", "pdf"], selected: "pdf" });
    first.write("view:pdf", { page: 3, zoom: 1.25 });
    await first.close();
    const second = await open();
    bindWorkspaceStore(second);
    expect(readDraft("c1")?.body).toBe("未发送内容");
    expect(readAnnotationBody("anchor")).toBe("未提交批注");
    expect(second.read("workspace:c1")).toEqual({
      tabs: ["word", "pdf"],
      selected: "pdf",
    });
    expect(second.read("view:pdf")).toEqual({ page: 3, zoom: 1.25 });
  });
  it("isolates accounts and organizations, and logout erases only that account", async () => {
    const a = await open(),
      b = await open(JSON.stringify(["user-1", "org-2"])),
      c = await open(JSON.stringify(["user-2", "org-1"]));
    for (const store of [a, b, c]) {
      store.write("draft:c1", store.scope);
      await store.flush();
    }
    expect(a.read("draft:c1")).not.toEqual(b.read("draft:c1"));
    await b.close();
    await c.close();
    await a.erase();
    const restoredA = await open(),
      restoredB = await open(b.scope),
      restoredC = await open(c.scope);
    expect(restoredA.read("draft:c1")).toBeUndefined();
    expect(restoredB.read("draft:c1")).toBeUndefined();
    expect(restoredC.read("draft:c1")).toBe(c.scope);
    await Promise.all([
      restoredA.close(),
      restoredB.close(),
      restoredC.close(),
    ]);
  });
  it("rejects a stale window instead of overwriting a newer draft", async () => {
    const first = await open(),
      second = await open();
    first.write("draft:c1", "first");
    await first.flush();
    second.write("draft:c1", "second");
    await expect(second.flush()).rejects.toThrow("STORAGE_CONFLICT");
    expect(second.status).toBe("conflict");
    expect(second.read("draft:c1")).toBe("second");
    const restored = await open();
    expect(restored.read("draft:c1")).toBe("first");
    await first.close();
    await second.close().catch(() => {});
    await restored.close();
  });
  it("quota failure retains memory and never evicts previously saved content", async () => {
    const store = await open();
    store.write("draft:c1", "saved");
    await store.flush();
    const blob = new Blob([new Uint8Array(1024 * 1024)]);
    store.write(
      "draft:large",
      Array(MAX_WORKSPACE_BYTES / blob.size + 1).fill(blob),
    );
    await expect(store.flush()).rejects.toThrow("STORAGE_LIMIT");
    expect(store.status).toBe("failed");
    expect(store.read("draft:large")).toBeDefined();
    const restored = await open();
    expect(restored.read("draft:c1")).toBe("saved");
    expect(restored.read("draft:large")).toBeUndefined();
    store.write("draft:large", undefined);
    await store.close();
    await restored.close();
  });
  it("concurrent flushes serialize and the latest edit survives a database reopen", async () => {
    const store = await open();
    store.write("draft:c1", "a");
    const first = store.flush();
    store.write("draft:c1", "b");
    const second = store.flush();
    store.write("draft:c1", "c");
    await Promise.all([first, second, store.flush()]);
    await store.close();
    const restored = await open();
    expect(restored.read("draft:c1")).toBe("c");
    await restored.close();
  });
  it("an old receipt preserves newer input even when the user retypes the same text", async () => {
    const store = await open();
    bindWorkspaceStore(store);
    saveDraft("c1", { body: "same", picked: [], files: [] });
    const source = readDraft("c1")!;
    saveDraft("c1", { body: "changed", picked: [], files: [] });
    saveDraft("c1", { body: "same", picked: [], files: [] });
    consumeSentDraft(store, "c1", source);
    expect(readDraft("c1")?.body).toBe("same");
    await store.flush();
  });
});
