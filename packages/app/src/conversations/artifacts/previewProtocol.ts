import type { ArtifactReviewAnchor, ArtifactReviewFile } from "@openfox/api";
import { artifactPreviewKind, type ArtifactPreviewKind } from "./model";

// File revision identity is the artifact ID, never the mutable metadata version.
// Native documents keep their own node/version model. A live page is not a file
// revision: its frame epoch and captured snapshot have separate lifetimes.
export type PreviewSource =
  | { kind: "artifact"; artifactId: string; sha256: string }
  | {
      kind: "document";
      spaceId: string;
      nodeId: string;
      nodeVersion: number;
      sha256: string;
    }
  | {
      kind: "web-capture";
      captureId: string;
      hostId: string;
      tabId: string;
      frameId: string;
      documentGeneration: number;
    };

export type PreviewCoordinates =
  | { space: "source-text"; encoding: "utf-8"; offsets: "bytes-half-open" }
  | {
      space: "csv-source-field";
      encoding: "utf-8";
      offsets: "bytes-half-open";
      indices: "zero-based";
    }
  | {
      space: "office-source-unit";
      encoding: "utf-8";
      offsets: "bytes-half-open-or-whole-unit";
    }
  | { space: "office-sheet-range"; notation: "A1" }
  | { space: "pdf-user-space"; geometry: "clockwise-quads"; page: "one-based" }
  | { space: "unrotated-page-fraction"; origin: "top-left"; page: "one-based" };

export type PreviewCapabilities = {
  selection: readonly ArtifactReviewAnchor["kind"][];
  sourceToggle: boolean;
  editable: false;
};

// These describe the existing React renderers. Mount/unmount owns resources;
// onSelect delivers a detached source-bound snapshot, located reveals without
// edits, and viewState restores only within a tab and immutable source identity.
const capabilities: Record<ArtifactPreviewKind, PreviewCapabilities> = {
  code: { selection: ["text"], sourceToggle: false, editable: false },
  markdown: { selection: ["text"], sourceToggle: true, editable: false },
  html: { selection: ["text"], sourceToggle: true, editable: false },
  table: { selection: ["cell"], sourceToggle: true, editable: false },
  image: { selection: ["region"], sourceToggle: false, editable: false },
  pdf: {
    selection: ["pdf_text", "region"],
    sourceToggle: false,
    editable: false,
  },
  office: {
    selection: ["office", "office_range", "region"],
    sourceToggle: false,
    editable: false,
  },
  binary: { selection: [], sourceToggle: false, editable: false },
};
export const previewCapabilities = (
  file: Pick<ArtifactReviewFile, "name" | "mime_type">,
) => capabilities[artifactPreviewKind(file)];

export const previewSourceKey = (source: PreviewSource) =>
  JSON.stringify(source);
export function selectionCoordinates(
  anchor: ArtifactReviewAnchor,
): PreviewCoordinates {
  switch (anchor.kind) {
    case "text":
      return {
        space: "source-text",
        encoding: "utf-8",
        offsets: "bytes-half-open",
      };
    case "cell":
      return {
        space: "csv-source-field",
        encoding: "utf-8",
        offsets: "bytes-half-open",
        indices: "zero-based",
      };
    case "office":
      return {
        space: "office-source-unit",
        encoding: "utf-8",
        offsets: "bytes-half-open-or-whole-unit",
      };
    case "office_range":
      return { space: "office-sheet-range", notation: "A1" };
    case "pdf_text":
      return {
        space: "pdf-user-space",
        geometry: "clockwise-quads",
        page: "one-based",
      };
    case "region":
      return {
        space: "unrotated-page-fraction",
        origin: "top-left",
        page: "one-based",
      };
  }
}

export type PreviewSelection = {
  source: PreviewSource;
  coordinates: PreviewCoordinates;
  anchor: ArtifactReviewAnchor;
};
export function capturePreviewSelection(
  source: PreviewSource,
  anchor: ArtifactReviewAnchor,
): PreviewSelection {
  return {
    source: { ...source },
    anchor: structuredClone(anchor),
    coordinates: selectionCoordinates(anchor),
  };
}
