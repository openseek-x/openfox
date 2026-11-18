// Linear work, bounded rendering. Show the changed block with surrounding lines;
// full original/candidate previews remain available for large or distant edits.
export function changedLines(before: string, after: string) {
  if (before === after) return { lines: [], truncated: false };
  const a = before.split("\n"),
    b = after.split("\n");
  let start = 0,
    end = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;
  while (
    end < a.length - start &&
    end < b.length - start &&
    a[a.length - 1 - end] === b[b.length - 1 - end]
  )
    end++;
  const removed = a.slice(start, a.length - end),
    added = b.slice(start, b.length - end);
  const lines = [
    ...a
      .slice(Math.max(0, start - 3), start)
      .map((text) => ({ kind: "context", text })),
    ...removed.slice(0, 1000).map((text) => ({ kind: "remove", text })),
    ...added.slice(0, 1000).map((text) => ({ kind: "add", text })),
    ...a
      .slice(a.length - end, a.length - end + 3)
      .map((text) => ({ kind: "context", text })),
  ];
  return { lines, truncated: removed.length > 1000 || added.length > 1000 };
}
