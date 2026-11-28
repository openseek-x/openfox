import type { ReactElement } from "react";

import type { RailItem } from "./layoutMode";

function glyphFor(name: RailItem["icon"], detail: string): ReactElement {
  switch (name) {
    case "home":
      return (
        <>
          <path d="M3 8.7a2 2 0 0 1 .8-1.6l5-3.7a2 2 0 0 1 2.4 0l5 3.7a2 2 0 0 1 .8 1.6V15a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z" />
          <path d="M8 12.8h4" stroke={detail} />
        </>
      );
    case "messages":
      return (
        <>
          <path d="M8.5 14.6H6l-3 2v-4a5 5 0 0 1-1-3.1 6.2 6.2 0 0 1 12.4 0c0 3.1-2.5 5.1-5.9 5.1Z" />
          <path d="M16.2 7.7a5 5 0 0 1 .8 7.1v2.6l-2.6-1.2h-2" fill="none" />
        </>
      );
    case "users":
      return (
        <>
          <circle cx="8" cy="6.1" r="2.9" />
          <path d="M2.4 15.4c0-2.5 2.6-4.2 5.6-4.2s5.6 1.7 5.6 4.2c0 .9-.8 1.5-1.8 1.5H4.2c-1 0-1.8-.6-1.8-1.5Z" />
          <path
            d="M13.3 3.9a2.6 2.6 0 0 1 0 4.7m2.1 3a3.8 3.8 0 0 1 2.2 3.3"
            fill="none"
          />
        </>
      );
    case "hexagon-cog":
      return (
        <>
          <path d="m11.2 2.7 4.6 2.7A2.4 2.4 0 0 1 17 7.5v5a2.4 2.4 0 0 1-1.2 2.1l-4.6 2.7a2.4 2.4 0 0 1-2.4 0l-4.6-2.7A2.4 2.4 0 0 1 3 12.5v-5a2.4 2.4 0 0 1 1.2-2.1l4.6-2.7a2.4 2.4 0 0 1 2.4 0Z" />
          <circle cx="10" cy="10" r="2.6" stroke={detail} fill="none" />
        </>
      );
    case "square-check":
      return (
        <>
          <rect x="3" y="3" width="14" height="14" rx="3.2" />
          <path d="m6.8 10 2.2 2.3 4.3-4.7" stroke={detail} fill="none" />
        </>
      );
  }
}

/** 两种布局共享五个入口。选中时填实主体，并用选中底色留出内部细节。 */
export function RailIcon({
  name,
  active,
}: {
  name: RailItem["icon"];
  active: boolean;
}) {
  const detail = active ? "var(--color-surface-active)" : "currentColor";

  return (
    <svg
      width="20"
      height="20"
      viewBox="0 0 20 20"
      fill={active ? "currentColor" : "none"}
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
      className="shrink-0"
    >
      {glyphFor(name, detail)}
    </svg>
  );
}
