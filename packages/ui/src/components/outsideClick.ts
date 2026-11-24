import {
  useEffect,
  useEffectEvent,
  type PointerEventHandler,
  type RefObject,
} from "react";

// Dialogs and popovers share z-50, detail drawers z-40, and settings drawers z-30.
// Within each level portals are painted in document order.
const layers = [
  '[data-openfox-popover], [aria-modal="true"]:not([data-openfox-drawer])',
  "[data-openfox-drawer]",
  "[data-openfox-settings-drawer]",
];
const pressLayers = new WeakMap<Event, Element | null>();

function topLayerAtPress(event: Event) {
  if (!pressLayers.has(event)) {
    let top: Element | null = null;
    for (const selector of layers) {
      const matches = document.querySelectorAll(selector);
      if (matches.length) {
        top = matches[matches.length - 1]!;
        break;
      }
    }
    pressLayers.set(event, top);
  }
  return pressLayers.get(event);
}

export function isOutsidePress(event: PointerEvent, panel: Element | null) {
  return (
    event.button === 0 &&
    panel !== null &&
    topLayerAtPress(event) === panel &&
    !event.composedPath().includes(panel)
  );
}

/** Modal shells render their panel as the first child of the backdrop. */
export function dismissOnBackdrop(
  onClose?: () => void,
): PointerEventHandler<HTMLElement> {
  return (event) => {
    if (
      event.button === 0 &&
      event.target === event.currentTarget &&
      topLayerAtPress(event.nativeEvent) ===
        event.currentTarget.firstElementChild
    ) {
      // Keep the browser from focusing the backdrop after the panel restores focus.
      event.preventDefault();
      onClose?.();
    }
  };
}

/** Let the outside control handle its own click, including switching tasks or navigating. */
export function useDrawerOutsideClick(
  panel: RefObject<HTMLElement | null>,
  open: boolean,
  onClose: () => void,
  itemKey?: string,
) {
  const close = useEffectEvent(onClose);
  useEffect(() => {
    if (!open) return;
    let outsidePress: { href: string; state: unknown } | undefined;
    let timer: number | undefined;
    const onPress = (event: PointerEvent) => {
      outsidePress = isOutsidePress(event, panel.current)
        ? { href: window.location.href, state: window.history.state }
        : undefined;
    };
    const onClick = (event: MouseEvent) => {
      const entry = outsidePress;
      outsidePress = undefined;
      if (
        !entry ||
        !panel.current ||
        event.composedPath().includes(panel.current)
      )
        return;
      window.clearTimeout(timer);
      timer = window.setTimeout(() => {
        // A background link or task row can navigate before React commits the new panel.
        if (
          !event.defaultPrevented &&
          window.location.href === entry.href &&
          window.history.state === entry.state
        )
          close();
      }, 0);
    };
    window.addEventListener("pointerdown", onPress, true);
    window.addEventListener("click", onClick, true);
    return () => {
      window.clearTimeout(timer);
      window.removeEventListener("pointerdown", onPress, true);
      window.removeEventListener("click", onClick, true);
    };
  }, [panel, open, itemKey]);
}
