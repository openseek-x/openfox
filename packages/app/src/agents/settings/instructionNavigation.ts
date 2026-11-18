import { createContext, useContext, useLayoutEffect, useState } from "react";
import type { Location } from "react-router-dom";

export const InstructionNavigationContext = createContext<{
  setDirty: (id: symbol, dirty: boolean) => void;
  runConfirmed: (action: () => void) => void;
  heldLocation?: Location;
  routeEpoch: number;
  releaseForSecurity: () => void;
  unsupported: boolean;
  prompt: boolean;
  recovering: boolean;
  recoveryFailed: boolean;
  cancel: () => void;
  proceed: () => void;
  discardAtCurrentLocation: () => void;
} | null>(null);

export function useInstructionNavigation() {
  return useContext(InstructionNavigationContext);
}

/** One registration per drawer, including its existing non-instruction drafts. */
export function useInstructionDirty(dirty: boolean) {
  const setDirty = useInstructionNavigation()?.setDirty;
  const [id] = useState(() => Symbol("instruction-drawer"));
  useLayoutEffect(() => {
    setDirty?.(id, dirty);
    return () => setDirty?.(id, false);
  }, [setDirty, id, dirty]);
}

/** Legacy non-data-router hosts still use the local cancellable leave request. */
export const INSTRUCTION_NAVIGATION = "openfox:instruction-navigation";

export function allowInstructionNavigation(): boolean {
  return window.dispatchEvent(
    new Event(INSTRUCTION_NAVIGATION, { cancelable: true }),
  );
}
