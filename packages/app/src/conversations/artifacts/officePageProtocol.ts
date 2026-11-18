import type { OfficeView } from "@openfox/api";

export type OfficeTextSpan = {
  target: string;
  offset: number;
  text: string;
  x: number;
  y: number;
  width: number;
  height: number;
  size: number;
};
export type OfficeObject = {
  name: string;
  x: number;
  y: number;
  width: number;
  height: number;
};
export type OfficePageRequest =
  | { kind: "open"; bytes: ArrayBuffer; view: OfficeView }
  | { kind: "page"; page: number };
export type OfficePageReply =
  | { kind: "ready"; pages: number; targets: Record<string, number> }
  | {
      kind: "page";
      page: number;
      width: number;
      height: number;
      bitmap: ImageBitmap;
      spans: OfficeTextSpan[];
      objects?: OfficeObject[];
    }
  | { kind: "error" };
