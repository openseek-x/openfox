import { useQueryClient } from "@tanstack/react-query";
import { lazy, Suspense, useEffect, useRef, useState } from "react";

import { isApiError, type DocNodeDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Skeleton } from "@openfox/ui";

import { useApi } from "../context";
import { DOC_TITLE_MAX } from "./model";
import { putDocNode, useDebouncedSave } from "./queries";

// 文档编辑页：标题可以直接编辑；正文是 TipTap，以 Markdown 存取。
// TipTap 编辑器体积较大，只在打开文档时加载（懒加载，不进入主包）。

const BlockBody = lazy(() => import("./BlockBody"));

interface DocPatch {
  title?: string;
  body?: string;
}

export function DocEditor({
  orgId,
  spaceId,
  node,
  onFlushReady,
}: {
  orgId: string;
  spaceId: string;
  node: DocNodeDetail;
  onFlushReady: (flush: () => Promise<boolean>) => void;
}) {
  const t = useT();
  const api = useApi();
  const queryClient = useQueryClient();
  const [title, setTitle] = useState(node.title);
  const titleFocused = useRef(false);
  const versionRef = useRef(node.version);
  // 正文只在打开时取一次：TipTap 自己持有编辑状态，之后的刷新不再覆盖它
  const initialBody = useRef(node.body ?? "");
  useEffect(() => {
    if (node.version > versionRef.current) versionRef.current = node.version;
  }, [node.version]);
  // 其他地方改了标题（重命名）时同步到输入框；输入框聚焦时不打断用户输入
  useEffect(() => {
    if (!titleFocused.current) setTitle(node.title);
  }, [node.title]);

  const save = useDebouncedSave<DocPatch>(async (patch) => {
    const saved = await api.docNodes.update(orgId, spaceId, node.id, {
      version: versionRef.current,
      ...patch,
    });
    versionRef.current = saved.version;
    putDocNode(queryClient, orgId, spaceId, saved);
  });
  const editable = node.can_edit;
  useEffect(() => {
    onFlushReady(save.flush);
  }, [onFlushReady, save.flush]);

  return (
    // 左侧留出 64px 槽位给块手柄（块手柄定位在块左侧 52px，宽约 24px），标题与正文共用同一左缘
    <div className="mx-auto flex w-full max-w-[768px] flex-col gap-4 py-6 pr-6 pl-16">
      <input
        aria-label={t("docs.editor.titlePlaceholder")}
        value={title}
        readOnly={!editable}
        maxLength={DOC_TITLE_MAX}
        placeholder={t("docs.editor.titlePlaceholder")}
        onFocus={() => {
          titleFocused.current = true;
        }}
        onBlur={() => {
          titleFocused.current = false;
        }}
        onChange={(event) => {
          setTitle(event.target.value);
          if (editable) save.schedule({ title: event.target.value.trim() });
        }}
        className="w-full bg-transparent text-[24px] leading-8 font-bold outline-none placeholder:text-fg-muted read-only:cursor-default"
      />
      <Suspense fallback={<BodySkeleton />}>
        <BlockBody
          orgId={orgId}
          spaceId={spaceId}
          nodeId={node.id}
          initialMarkdown={initialBody.current}
          editable={editable}
          onChange={(markdown) => save.schedule({ body: markdown })}
        />
      </Suspense>
      <SaveStatus error={save.error} />
    </div>
  );
}

function BodySkeleton() {
  return (
    <div aria-busy="true" className="flex flex-col gap-2">
      <Skeleton className="h-4 w-full" />
      <Skeleton className="h-4 w-5/6" />
    </div>
  );
}

/** 保存状态：版本冲突提示刷新；其余失败显示红字。 */
export function SaveStatus({ error }: { error: unknown }) {
  const t = useT();
  if (!error) return null;
  const conflict = isApiError(error, "VERSION_CONFLICT");
  return (
    <div
      role="alert"
      className="flex items-center gap-2 text-body-sm text-danger"
    >
      <span>
        {conflict ? t("docs.editor.conflict") : t("docs.editor.saveFailed")}
      </span>
      {conflict && (
        <Button
          variant="ghost"
          size="28"
          onClick={() => window.location.reload()}
        >
          {t("docs.editor.refresh")}
        </Button>
      )}
    </div>
  );
}
