import { describe, expect, it } from "vitest";
import type { ImportAnalysis } from "@openfox/api";
import { reconcileUsers } from "./model";

const binding = { id: "binding", user_id: "member", version: 2 };
const analysis = {
  users: [
    {
      source_id: "remote",
      name: "Remote",
      count: 1,
      suggested_user_id: null,
      verified_binding: binding,
    },
  ],
} as ImportAnalysis;

describe("verified import identity compatibility", () => {
  it("keeps an automatic binding reference, while the old suggested-user contract stays empty", () => {
    expect(reconcileUsers(analysis, [])).toEqual([
      {
        source_id: "remote",
        mode: "member",
        user_id: "member",
        verified_binding_id: "binding",
      },
    ]);
    const oldReader = analysis.users.map((u) =>
      u.suggested_user_id ? "member" : "external",
    );
    expect(oldReader).toEqual(["external"]);
  });
  it("preserves an explicit manual member or external choice over later identity suggestions", () => {
    for (const manual of [
      { source_id: "remote", mode: "member" as const, user_id: "other" },
      { source_id: "remote", mode: "external" as const },
    ]) {
      expect(reconcileUsers(analysis, [manual])).toEqual([manual]);
    }
  });
  it("retains the reference after refresh so a lost binding is rechecked by the worker", () => {
    const selected = reconcileUsers(analysis, []);
    const changed = {
      ...analysis,
      users: analysis.users.map((u) => ({ ...u, verified_binding: undefined })),
    };
    expect(reconcileUsers(changed, selected)[0]?.verified_binding_id).toBe(
      "binding",
    );
  });
});
