import { useState } from "react";

import { useT } from "@openfox/i18n";

// 视频（测试录像，video/webm、video/mp4）：<video> 直接指向同源鉴权下载地址，
// 由服务端按 HTTP Range 分段返回，边下边播、可拖动，不再整段取回为 Blob。
// 依赖应用 CSP 的 media-src 'self'；下载沿用各自工具栏的下载按钮（原始文件）。
// 产物面板与任务 / 对话附件预览共用。

/** 测试录像播放器：支持播放、暂停、拖动；加载失败时提示改为下载。 */
export function VideoPlayer({ src, name }: { src: string; name: string }) {
  const t = useT();
  const [failed, setFailed] = useState(false);
  return (
    <div className="flex flex-col gap-2 p-4">
      <video
        key={src}
        src={src}
        controls
        preload="metadata"
        playsInline
        aria-label={t("artifacts.preview.videoTitle", { name })}
        onError={() => setFailed(true)}
        onLoadedMetadata={() => setFailed(false)}
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
