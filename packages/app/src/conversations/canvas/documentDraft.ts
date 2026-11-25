import { useEffectEvent, useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { DocNodeDetail } from "@openfox/api";
import { useApi, keys } from "../../context";
import {
  DocSaveConflict,
  saveDocPatch,
  useDebouncedSave,
} from "../../docs/queries";
import { workspaceStore } from "../workspaceStorage";

export const canvasDraftKey = (space: string, node: string) =>
  `canvas:${space}:${node}`;
export type CanvasDraft = { base: DocNodeDetail; body: string };
const writers = new Map<string, () => Promise<boolean>>();
export async function flushCanvasSource(space: string, node: string) {
  const key = canvasDraftKey(space, node);
  const live = writers.get(`${workspaceStore().scope}:${key}`);
  if (live) return live();
  return !workspaceStore().read<CanvasDraft>(key);
}

export function useCanvasDocument(
  org: string,
  space: string,
  node: DocNodeDetail,
) {
  const api = useApi(),
    client = useQueryClient(),
    store = workspaceStore();
  const key = canvasDraftKey(space, node.id);
  const [initial] = useState(() => store.read<CanvasDraft>(key));
  const base = useRef(initial?.base ?? node);
  const body = useRef(initial?.body ?? node.body ?? "");
  const [epoch, setEpoch] = useState(0);
  const [, update] = useState(0);
  const [busy, setBusy] = useState(false);
  const [localError, setLocalError] = useState(false);
  const mounted = useRef(true);
  const save = useDebouncedSave<{ body: string }>(async (patch) => {
    if (mounted.current) setBusy(true);
    try {
      const before = base.current;
      const sent = patch.body!;
      // A local recovery copy must be durable before starting a remote mutation.
      await store.flush();
      const saved = await saveDocPatch(
        () =>
          api.docNodes.update(org, space, node.id, {
            version: before.version,
            body: sent,
          }),
        () => api.docNodes.get(org, space, node.id),
        { title: before.title, body: sent },
      );
      base.current = saved;
      const current = store.read<CanvasDraft>(key);
      // A late response may advance its own pending draft, never replace newer input.
      if (current?.base.version === before.version)
        store.write(
          key,
          current.body === sent
            ? undefined
            : { base: saved, body: current.body },
        );
      client.setQueryData<DocNodeDetail>(
        keys.docNode(org, space, node.id),
        (old) => (!old || old.version <= saved.version ? saved : old),
      );
      if (mounted.current) update((v) => v + 1);
    } finally {
      if (mounted.current) setBusy(false);
    }
  });
  const change = (text: string) => {
    body.current = text;
    store.write(key, { base: base.current, body: text });
    save.schedule({ body: text });
    update((v) => v + 1);
  };
  const flush = async () => {
    setBusy(true);
    setLocalError(false);
    try {
      await store.flush();
      return await save.flush();
    } catch {
      setLocalError(true);
      return false;
    } finally {
      if (mounted.current) setBusy(false);
    }
  };
  const restoreDraft = useEffectEvent(() => {
    if (initial && node.can_edit) save.schedule({ body: initial.body });
  });
  const flushRef = useRef(flush);
  flushRef.current = flush;
  useEffect(() => {
    mounted.current = true;
    const id = `${store.scope}:${key}`;
    const fn = () => flushRef.current();
    writers.set(id, fn);
    restoreDraft();
    return () => {
      mounted.current = false;
      if (writers.get(id) === fn) writers.delete(id);
    };
  }, [store.scope, key]);
  useEffect(() => {
    if (store.read(key) || node.version <= base.current.version) return;
    // Clean editors follow accepted proposals/history; dirty editors keep their baseline.
    if (body.current !== (base.current.body ?? "")) return;
    base.current = node;
    body.current = node.body ?? "";
    setEpoch((v) => v + 1);
  }, [node, store, key]);
  const merge = async () => {
    if (!(save.error instanceof DocSaveConflict)) return;
    base.current = save.error.remote;
    change(body.current);
    await flush();
  };
  return {
    body,
    base,
    epoch,
    change,
    flush,
    busy,
    merge,
    error: save.error,
    localError,
    dirty: Boolean(store.read(key)),
  };
}
