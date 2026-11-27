import { useEffect, useState } from "react";

import { useT } from "@openfox/i18n";
import { Skeleton } from "@openfox/ui";

// 视频产物（测试录像，video/webm、video/mp4）：鉴权取回整段 Blob 后用 <video> 播放。
// 依赖应用 CSP 的 media-src blob:；下载沿用产物工具栏的下载按钮（原始文件）。

/** 测试录像：整段 Blob 播放，支持暂停、拖动；下载走工具栏的下载按钮。 */
export function VideoBlob({ blob, name }: { blob: Blob; name: string }) {
  const t = useT();
  const [url, setUrl] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const objectUrl = URL.createObjectURL(blob);
    setUrl(objectUrl);
    setFailed(false);
    return () => {
      URL.revokeObjectURL(objectUrl);
      setUrl(null);
    };
  }, [blob]);
  if (!url)
    return (
      <div aria-busy="true" className="p-4">
        <Skeleton className="h-40 w-full" />
      </div>
    );
  return (
    <div className="flex flex-col gap-2 p-4">
      <video
        src={url}
        controls
        preload="metadata"
        playsInline
        aria-label={t("artifacts.preview.videoTitle", { name })}
        onError={() => setFailed(true)}
        className="max-h-[70vh] w-full rounded-md border border-line-default bg-black"
      />
      {failed && (
        <p role="status" className="text-body-sm text-fg-muted">
          {t("artifacts.preview.videoUnavailable")}
        </p>
      )}
    </div>
  );
}
