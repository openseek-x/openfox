// Local content only. Browser leases, grants, cookies and credentials never enter this store.
export const WORKSPACE_DB = "openfox-workspace";
export const MAX_WORKSPACE_BYTES = 512 * 1024 * 1024;
const MAX_RECORDS = 600;
type Row = {
  scope: string;
  key: string;
  revision: string;
  bytes: number;
  value?: unknown;
};
type Meta = { scope: string; bytes: number; count: number };
export type StorageStatus = "saved" | "saving" | "failed" | "conflict";

function request<T>(r: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    r.onsuccess = () => resolve(r.result);
    r.onerror = () => reject(r.error);
  });
}
function completed(tx: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onabort = tx.onerror = () =>
      reject(tx.error ?? new Error("STORAGE_ABORTED"));
  });
}
export async function openWorkspaceDB(): Promise<IDBDatabase> {
  const r = indexedDB.open(WORKSPACE_DB, 1);
  r.onupgradeneeded = () => {
    const rows = r.result.createObjectStore("records", {
      keyPath: ["scope", "key"],
    });
    rows.createIndex("scope", "scope");
    r.result.createObjectStore("meta", { keyPath: "scope" });
  };
  const db = await request(r);
  db.onversionchange = () => db.close();
  return db;
}
function size(value: unknown): number {
  let blobs = 0;
  const text = JSON.stringify(value, (_key, item: unknown) => {
    if (item instanceof Blob) {
      blobs += item.size;
      return { blob: item.size, type: item.type };
    }
    return item;
  });
  return blobs + new TextEncoder().encode(text ?? "").length;
}

/** A hydrated scope. Writes capture this instance, so a late response cannot write another account. */
export class WorkspaceStore {
  readonly values = new Map<string, unknown>();
  readonly restored = new Set<string>();
  private revisions = new Map<string, string>();
  private dirty = new Map<string, number>();
  private sequence = 0;
  private timer?: ReturnType<typeof setTimeout>;
  private writing?: Promise<void>;
  private stopped = false;
  status: StorageStatus = "saved";
  readonly listeners = new Set<() => void>();
  constructor(
    readonly scope: string,
    private db?: IDBDatabase,
  ) {}
  get version() {
    return this.sequence;
  }
  get durable() {
    return Boolean(this.db);
  }
  /** 当前是否有尚未落盘的用户输入内容（排除 workspace/view/browser 视图态） */
  get hasDirtyContent() {
    for (const key of this.dirty.keys()) {
      if (!/^(workspace|view|browser):/.test(key)) return true;
    }
    return false;
  }
  async hydrate() {
    if (!this.db) return;
    const tx = this.db.transaction("records", "readonly");
    const rows = await request<Row[]>(
      tx
        .objectStore("records")
        .index("scope")
        .getAll(this.scope, MAX_RECORDS + 1),
    );
    if (rows.length > MAX_RECORDS) throw new Error("STORAGE_LIMIT");
    for (const row of rows) {
      this.revisions.set(row.key, row.revision);
      if (row.value !== undefined) {
        this.values.set(row.key, row.value);
        this.restored.add(row.key);
      }
    }
  }
  read<T>(key: string): T | undefined {
    return this.values.get(key) as T | undefined;
  }
  write(key: string, value: unknown) {
    if (this.stopped) return;
    if (Object.is(this.values.get(key), value)) return;
    const view = /^(workspace|view|browser):/.test(key);
    if (view && JSON.stringify(this.values.get(key)) === JSON.stringify(value))
      return;
    if (value === undefined) this.values.delete(key);
    else this.values.set(key, value);
    this.dirty.set(key, ++this.sequence);
    for (const listener of this.listeners) listener();
    if (!this.db) return;
    this.setStatus(this.status === "conflict" ? "conflict" : "saving");
    clearTimeout(this.timer);
    this.timer = setTimeout(() => void this.flush().catch(() => {}), 80);
  }
  private setStatus(status: StorageStatus) {
    if (this.status === status) return;
    this.status = status;
    for (const listener of this.listeners) listener();
  }
  async flush(): Promise<void> {
    clearTimeout(this.timer);
    if (this.stopped) throw new Error("STORAGE_CLOSED");
    if (!this.db) return;
    while (this.writing) await this.writing;
    if (this.status === "conflict") throw new Error("STORAGE_CONFLICT");
    if (!this.dirty.size) return;
    const batch = [...this.dirty].map(([key, seq]) => ({
      key,
      seq,
      value: this.values.get(key),
    }));
    this.writing = this.commit(batch);
    try {
      await this.writing;
    } catch (error) {
      this.setStatus(
        error instanceof Error && error.message === "STORAGE_CONFLICT"
          ? "conflict"
          : "failed",
      );
      throw error;
    } finally {
      this.writing = undefined;
    }
    if (this.dirty.size) return this.flush();
  }
  private async commit(batch: { key: string; seq: number; value: unknown }[]) {
    const tx = this.db!.transaction(["records", "meta"], "readwrite", {
      durability: "strict",
    });
    const done = completed(tx);
    // Handle aborts even when a validation check throws before awaiting completion.
    void done.catch(() => {});
    try {
      const records = tx.objectStore("records"),
        meta = tx.objectStore("meta");
      const totals: Meta = (await request(meta.get(this.scope))) ?? {
        scope: this.scope,
        bytes: 0,
        count: 0,
      };
      const next: Row[] = [];
      for (const item of batch) {
        const old: Row | undefined = await request(
          records.get([this.scope, item.key]),
        );
        if (
          !/^(workspace|view|browser):/.test(item.key) &&
          old?.revision !== this.revisions.get(item.key)
        )
          throw new Error("STORAGE_CONFLICT");
        const bytes = size(item.value);
        totals.bytes += bytes - (old?.bytes ?? 0);
        totals.count += Number(item.value !== undefined) - Number(Boolean(old));
        next.push({
          scope: this.scope,
          key: item.key,
          revision: crypto.randomUUID(),
          bytes,
          value: item.value,
        });
      }
      // Never evict unsent user input to make room. The current draft stays in memory with a visible error.
      if (totals.bytes > MAX_WORKSPACE_BYTES || totals.count > MAX_RECORDS)
        throw new Error("STORAGE_LIMIT");
      for (const row of next) {
        if (row.value === undefined) records.delete([this.scope, row.key]);
        else records.put(row);
      }
      meta.put(totals);
      await done;
      for (let i = 0; i < batch.length; i++) {
        const item = batch[i]!;
        if (item.value === undefined) this.revisions.delete(item.key);
        else this.revisions.set(item.key, next[i]!.revision);
        if (this.dirty.get(item.key) === item.seq) this.dirty.delete(item.key);
      }
      this.setStatus(this.dirty.size ? "saving" : "saved");
    } catch (error) {
      try {
        tx.abort();
      } catch {
        /* already completed/aborted */
      }
      this.setStatus(
        error instanceof Error && error.message === "STORAGE_CONFLICT"
          ? "conflict"
          : "failed",
      );
      throw error;
    }
  }
  async close() {
    try {
      await this.flush();
    } finally {
      this.stopped = true;
      this.db?.close();
    }
  }
  async erase() {
    clearTimeout(this.timer);
    this.stopped = true;
    await this.writing?.catch(() => {});
    this.values.clear();
    this.dirty.clear();
    if (!this.db) return;
    // The scope may already have been closed by a route unmount.
    const db = await openWorkspaceDB();
    const tx = db.transaction(["records", "meta"], "readwrite", {
        durability: "strict",
      }),
      done = completed(tx);
    void done.catch(() => {});
    try {
      const rows = tx.objectStore("records");
      const scopes: IDBValidKey[] = await request(
        tx.objectStore("meta").getAllKeys(),
      );
      const user = JSON.parse(this.scope)[0];
      for (const scope of scopes) {
        if (typeof scope !== "string" || JSON.parse(scope)[0] !== user)
          continue;
        const keys = await request(rows.index("scope").getAllKeys(scope));
        for (const key of keys) rows.delete(key);
        tx.objectStore("meta").delete(scope);
      }
      await done;
    } finally {
      db.close();
      this.db.close();
    }
  }
}

let active = new WorkspaceStore("");
export const workspaceStore = () => active;
export function bindWorkspaceStore(store: WorkspaceStore) {
  active = store;
}
export function resetWorkspaceMemory() {
  active = new WorkspaceStore("");
}

export function scopedMap<T>(prefix: string) {
  return {
    get: (key: string) => active.read<T>(prefix + key),
    set: (key: string, value: T) => active.write(prefix + key, value),
    delete: (key: string) => {
      if (active.values.has(prefix + key))
        active.write(prefix + key, undefined);
    },
    keys: () =>
      [...active.values.keys()]
        .filter((key) => key.startsWith(prefix))
        .map((key) => key.slice(prefix.length))
        .values(),
    get size() {
      return [...active.values.keys()].filter((key) => key.startsWith(prefix))
        .length;
    },
  };
}
