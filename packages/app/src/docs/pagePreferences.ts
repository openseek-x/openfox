import { useEffect, useState } from "react";

import { readLocal, writeLocal } from "../storage";

export const docPageWidths = {
  narrow: 600,
  standard: 768,
  wide: 1120,
} as const;
export type DocPageWidth = keyof typeof docPageWidths;
export const docPageWidthKey = (userId: string) =>
  `openfox:doc-page-width:${userId}`;

export function readDocPageWidth(userId: string): DocPageWidth {
  const value = userId ? readLocal(docPageWidthKey(userId)) : null;
  return value === "narrow" || value === "wide" ? value : "standard";
}

export function useDocPageWidth(userId: string) {
  const [stored, setStored] = useState(() => ({
    userId,
    width: readDocPageWidth(userId),
  }));
  useEffect(() => {
    setStored({ userId, width: readDocPageWidth(userId) });
  }, [userId]);
  const width =
    stored.userId === userId ? stored.width : readDocPageWidth(userId);
  const setWidth = (next: DocPageWidth) => {
    setStored({ userId, width: next });
    if (userId) writeLocal(docPageWidthKey(userId), next);
  };
  return [width, setWidth] as const;
}
