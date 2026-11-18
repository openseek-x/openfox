import { expect, it } from "vitest";
import {
  addFileComment,
  readDraft,
  resetDrafts,
  removeFileComment,
} from "../drafts";
it("keeps native feedback separate from artifacts and isolated by chat", () => {
  resetDrafts();
  const file = {
    name: "draft.md",
    space_id: "s1",
    node_id: "n1",
    node_version: 2,
    sha256: "a".repeat(64),
  };
  const comment = {
    thread_id: "t1",
    anchor: { kind: "text" as const, start: 0, end: 6, quote: "原文" },
    body: "精简",
  };
  expect(addFileComment("c1", file, comment)).toBe(true);
  expect(readDraft("c2")).toBeUndefined();
  const saved = readDraft("c1")!.fileContexts![0]!;
  expect(saved.artifact_id).toBeUndefined();
  expect(saved.node_id).toBe("n1");
  expect(
    addFileComment("c1", { ...file, sha256: "b".repeat(64) }, comment),
  ).toBe(false);
  removeFileComment("c1", saved.id, "t1");
  expect(readDraft("c1")?.fileContexts ?? []).toHaveLength(0);
});
