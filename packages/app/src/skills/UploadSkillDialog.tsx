import { useRef, useState } from "react";

import type { SkillUploadFile } from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import { Button, Callout, Dialog, Icon, useToast } from "@openfox/ui";

import { checkUploadLimits } from "./model";
import { skillErrorText, useUploadSkill } from "./queries";

// 上传技能文件：一个 .md、一个 .zip，或整个文件夹。客户端先查 5 MB 与 100 个文件，超限不发请求。

/** 文件夹里选到的文件，取浏览器给的相对路径，并去掉最外层目录名。 */
function folderPath(file: File): string {
  const raw =
    (file as File & { webkitRelativePath?: string }).webkitRelativePath ||
    file.name;
  const parts = raw.split("/");
  return parts.length > 1 ? parts.slice(1).join("/") : raw;
}

export function UploadSkillDialog({
  orgId,
  spaceId,
  onClose,
  onCreated,
}: {
  orgId: string;
  spaceId: string;
  onClose: () => void;
  onCreated: (skillId: string) => void;
}) {
  const t = useT();
  const toast = useToast();
  const upload = useUploadSkill(orgId, spaceId);
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const [localError, setLocalError] = useState<MessageKey | null>(null);

  const submit = (files: SkillUploadFile[]) => {
    const limit = checkUploadLimits(files.map((item) => item.file));
    if (limit !== "ok") {
      setLocalError(
        limit === "tooMany"
          ? "skills.upload.tooMany"
          : "skills.upload.tooLarge",
      );
      return;
    }
    setLocalError(null);
    upload.mutate(files, {
      onSuccess: (skill) => {
        toast(t("skills.upload.uploaded"), { tone: "success" });
        onCreated(skill.id);
      },
    });
  };

  return (
    <Dialog
      open
      onClose={upload.isPending ? undefined : onClose}
      title={t("skills.upload.title")}
      description={t("skills.upload.desc")}
      width={480}
    >
      <div className="flex flex-col gap-3">
        {localError && <Callout tone="danger">{t(localError)}</Callout>}
        {upload.isError && (
          <Callout tone="danger">
            {skillErrorText(
              t,
              upload.error,
              "skills.upload.failed",
              "skills.nameTaken",
            )}
          </Callout>
        )}
        <p className="text-caption text-fg-muted">
          {t("skills.upload.limits")}
        </p>
        <div className="flex gap-2">
          <Button
            variant="outline"
            disabled={upload.isPending}
            leading={<Icon name="file-text" />}
            onClick={() => fileInput.current?.click()}
          >
            {t("skills.upload.pickFile")}
          </Button>
          <Button
            variant="outline"
            disabled={upload.isPending}
            leading={<Icon name="folder" />}
            onClick={() => folderInput.current?.click()}
          >
            {t("skills.upload.pickFolder")}
          </Button>
        </div>
        {upload.isPending && (
          <p role="status" className="text-body-sm text-fg-muted">
            {t("skills.upload.uploading")}
          </p>
        )}
        <input
          ref={fileInput}
          type="file"
          hidden
          data-testid="skill-file-input"
          accept=".md,.markdown,.zip,text/markdown,application/zip"
          onChange={(event) => {
            const file = event.target.files?.[0];
            event.target.value = "";
            if (file) submit([{ file }]);
          }}
        />
        <input
          ref={folderInput}
          type="file"
          hidden
          multiple
          data-testid="skill-folder-input"
          {...{ webkitdirectory: "" }}
          onChange={(event) => {
            const files = Array.from(event.target.files ?? []);
            event.target.value = "";
            if (files.length > 0)
              submit(files.map((file) => ({ file, path: folderPath(file) })));
          }}
        />
      </div>
    </Dialog>
  );
}
