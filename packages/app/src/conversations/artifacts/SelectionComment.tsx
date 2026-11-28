import { useRef, useState } from "react";
import type { ArtifactReviewAnchor } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Popover } from "@openfox/ui";
import { readAnnotationBody, saveAnnotationBody } from "../drafts";
import { utf8Length } from "./anchors";
import { AnnotationInput } from "./AnnotationInput";
import type { SelectionRect } from "./selection";

export type CommentSelection = {
  anchor: ArtifactReviewAnchor;
  rect: SelectionRect;
};

export function SelectionComment({
  selection,
  draftKey,
  onClose,
  onAdd,
}: {
  selection: CommentSelection;
  draftKey: string;
  onClose: () => void;
  onAdd: (body: string) => boolean;
}) {
  const t = useT();
  const anchor = useRef<HTMLSpanElement>(null);
  const [body, setBody] = useState(() => readAnnotationBody(draftKey));
  const [error, setError] = useState("");
  const add = () => {
    if (!body.trim()) return;
    if (utf8Length(body) > 8192 || !onAdd(body.trim())) {
      setError(t("artifacts.review.draftLimit"));
      return;
    }
    saveAnnotationBody(draftKey, "");
    onClose();
  };
  return (
    <>
      <span
        ref={anchor}
        aria-hidden
        style={{
          position: "fixed",
          pointerEvents: "none",
          ...selection.rect,
          left: selection.rect.left + selection.rect.width / 2 - 150,
          width: 300,
        }}
      />
      <Popover
        open
        onOpenChange={(open) => {
          if (!open) onClose();
        }}
        anchorRef={anchor}
        placement="top-start"
        offset={8}
        aria-label={t("artifacts.review.comment")}
        className="of-input-frame w-75 max-w-[calc(100vw-16px)] rounded-[22px] border-line-floating p-[7px] text-body-sm shadow-popover focus-within:border-line-strong"
      >
        <AnnotationInput
          value={body}
          onChange={(value) => {
            setBody(value);
            saveAnnotationBody(draftKey, value);
            setError("");
          }}
          onSubmit={add}
          onClose={onClose}
          label={t("artifacts.review.comment")}
          placeholder={t("artifacts.browser.annotationPlaceholder")}
          closeLabel={t("artifacts.review.closeDraft")}
          submitLabel={t("artifacts.review.addToChat")}
          error={error}
        />
      </Popover>
    </>
  );
}
