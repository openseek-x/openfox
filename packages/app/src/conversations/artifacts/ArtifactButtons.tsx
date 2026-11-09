import type { RunArtifact } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, cn } from "@openfox/ui";

import { useRunArtifacts } from "../steps/queries";
import { useArtifactOpener } from "./context";
import { artifactExpired, artifactPreviewKind } from "./model";

// 消息里的「产物：<文件名>」按钮：点击打开右侧产物面板并选中该产物。

export function ArtifactButtons({
  artifacts,
  onOpen,
}: {
  artifacts: readonly RunArtifact[];
  onOpen: (id: string) => void;
}) {
  const t = useT();
  if (artifacts.length === 0) return null;
  return (
    <div
      role="group"
      aria-label={t("artifacts.buttonsLabel")}
      className="flex flex-wrap gap-1.5"
    >
      {artifacts.map((artifact) => {
        const expired = artifactExpired(artifact);
        return (
          <button
            key={artifact.id}
            type="button"
            onClick={() => onOpen(artifact.id)}
            title={artifact.name}
            className={cn(
              "inline-flex h-7 max-w-[280px] min-w-0 items-center gap-1.5 rounded-md border border-line-default bg-surface-card px-2 text-body-sm text-fg-default hover:bg-surface-hover",
              expired && "text-fg-muted",
            )}
          >
            <Icon
              name={
                artifactPreviewKind(artifact) === "image"
                  ? "image"
                  : "file-text"
              }
              size={14}
              className="shrink-0 text-fg-muted"
            />
            <span className={cn("truncate", expired && "line-through")}>
              {t("artifacts.button", { name: artifact.name })}
            </span>
          </button>
        );
      })}
    </div>
  );
}

/** 某次 Run 的产物按钮（回复里使用）；没有产物面板（无提供者）或没有产物时不显示。 */
export function RunArtifactButtons({
  orgId,
  runId,
}: {
  orgId: string;
  runId: string;
}) {
  const open = useArtifactOpener();
  const artifacts = useRunArtifacts(orgId, runId, Boolean(open));
  if (!open || !artifacts.data?.length) return null;
  return <ArtifactButtons artifacts={artifacts.data} onOpen={open} />;
}
