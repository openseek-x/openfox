import { useEffect, useRef, useState } from "react";
import type { UploadedIcon } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, useToast } from "@openfox/ui";
import { useApi } from "../context";
import { viewErrorMessage } from "../views/queries";
import { uploadedIconURL } from "./uploaded";

export function IconUploadInput({
  orgId,
  label,
  onUploaded,
  onBusyChange,
  disabled = false,
}: {
  orgId: string;
  label: string;
  onUploaded: (icon: UploadedIcon) => void;
  onBusyChange: (busy: boolean) => void;
  disabled?: boolean;
}) {
  const api = useApi();
  const t = useT();
  const toast = useToast();
  const [uploading, setUploading] = useState(false);
  const active = useRef(false);
  const pending = useRef(false);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
      onBusyChange(false);
    };
  }, [onBusyChange]);

  const upload = async (file: File) => {
    if (pending.current || disabled) return;
    if (file.size > 1024 * 1024) {
      toast(t("views.errors.iconTooLarge"), { tone: "error" });
      return;
    }
    pending.current = true;
    setUploading(true);
    onBusyChange(true);
    try {
      const icon = await api.icons.upload(orgId, file);
      if (!active.current) return;
      const url = uploadedIconURL(orgId, icon.id);
      if (!url) throw new Error("invalid uploaded icon");
      onUploaded({ ...icon, url });
    } catch (error) {
      if (active.current) toast(viewErrorMessage(t, error), { tone: "error" });
    } finally {
      pending.current = false;
      if (active.current) {
        setUploading(false);
        onBusyChange(false);
      }
    }
  };

  return (
    <label className="flex cursor-pointer flex-col items-center gap-2 rounded-lg border border-dashed border-line-default px-3 py-4 text-body-sm text-fg-muted hover:bg-surface-hover">
      <Icon name="upload" size={18} />
      {uploading ? t("views.icon.uploading") : t("views.icon.uploadHint")}
      <input
        type="file"
        accept="image/png,image/jpeg,image/webp,image/gif"
        aria-label={label}
        className="sr-only"
        disabled={uploading || disabled}
        onChange={(event) => {
          const file = event.target.files?.[0];
          event.target.value = "";
          if (file) void upload(file);
        }}
      />
    </label>
  );
}
