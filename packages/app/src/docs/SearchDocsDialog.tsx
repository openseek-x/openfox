import { useEffect, useRef, useState } from "react";

import type { DocNode, DocSet } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Icon,
  Spinner,
  TextInput,
  useModalBehavior,
} from "@openfox/ui";

import { useDocSets, useDocSearch } from "./queries";
import { containerName, NodeIcon } from "./parts";

// 搜索文档库：宽 384，顶边在视口 1/3 处。
// 输入后防抖调用 searchDocs，结果显示图标、标题与所在容器，点击打开。

/** 输入停止这么久后才发起搜索 */
const SEARCH_DEBOUNCE_MS = 200;

export function SearchDocsDialog({
  orgId,
  spaceId,
  open,
  onClose,
  onOpen,
}: {
  orgId: string;
  spaceId: string;
  open: boolean;
  onClose: () => void;
  onOpen: (nodeId: string) => void;
}) {
  if (!open) return null;
  return (
    <SearchPanel
      orgId={orgId}
      spaceId={spaceId}
      onClose={onClose}
      onOpen={onOpen}
    />
  );
}

function SearchPanel({
  orgId,
  spaceId,
  onClose,
  onOpen,
}: {
  orgId: string;
  spaceId: string;
  onClose: () => void;
  onOpen: (nodeId: string) => void;
}) {
  const t = useT();
  const panel = useRef<HTMLDivElement | null>(null);
  useModalBehavior(true, panel, onClose);
  const [text, setText] = useState("");
  const [query, setQuery] = useState("");
  // 防抖：输入停止后才把关键词交给查询
  useEffect(() => {
    const timer = setTimeout(() => setQuery(text.trim()), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [text]);
  const results = useDocSearch(orgId, spaceId, query);
  const sets = useDocSets(orgId, spaceId);
  const nodes = results.data ?? [];

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-scrim/25 p-6 pt-[33vh]"
      onPointerDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-label={t("docs.search.label")}
        className="flex w-[384px] max-w-full flex-col gap-2 rounded-xl bg-surface-card p-3 shadow-lg"
      >
        <div className="flex items-center gap-2">
          <TextInput
            data-autofocus
            className="!h-8 !w-full min-w-0 flex-1"
            leading={<Icon name="search" size={14} className="text-fg-muted" />}
            placeholder={t("docs.search.placeholder")}
            aria-label={t("docs.search.label")}
            value={text}
            onChange={(event) => setText(event.target.value)}
          />
          <Button variant="ghost" size="28" onClick={onClose}>
            {t("docs.search.close")}
          </Button>
        </div>
        <SearchBody
          hasQuery={query !== ""}
          loading={results.isFetching}
          failed={results.isError}
          nodes={nodes}
          sets={sets.data}
          onOpen={onOpen}
        />
      </div>
    </div>
  );
}

function SearchBody({
  hasQuery,
  loading,
  failed,
  nodes,
  sets,
  onOpen,
}: {
  hasQuery: boolean;
  loading: boolean;
  failed: boolean;
  nodes: readonly DocNode[];
  sets: readonly DocSet[] | undefined;
  onOpen: (nodeId: string) => void;
}) {
  const t = useT();
  if (!hasQuery)
    return (
      <p className="px-2 py-6 text-center text-body-sm text-fg-muted">
        {t("docs.search.hint")}
      </p>
    );
  if (loading && nodes.length === 0)
    return (
      <div
        role="status"
        className="flex items-center justify-center gap-2 py-6 text-body-sm text-fg-muted"
      >
        <Spinner size={14} />
        {t("docs.search.searching")}
      </div>
    );
  if (failed)
    return (
      <p
        role="alert"
        className="px-2 py-6 text-center text-body-sm text-danger"
      >
        {t("docs.search.failed")}
      </p>
    );
  if (nodes.length === 0)
    return (
      <p className="px-2 py-6 text-center text-body-sm text-fg-muted">
        {t("docs.search.empty")}
      </p>
    );
  return (
    <ul className="flex max-h-[320px] flex-col gap-0.5 overflow-y-auto">
      {nodes.map((node) => (
        <li key={node.id}>
          <button
            type="button"
            onClick={() => onOpen(node.id)}
            className="flex h-9 w-full min-w-0 items-center gap-2 rounded-lg px-2 text-left hover:bg-surface-hover"
          >
            <NodeIcon node={node} />
            <span className="min-w-0 flex-1 truncate text-body">
              {node.title || t("docs.untitled")}
            </span>
            <span className="max-w-[140px] shrink-0 truncate text-caption text-fg-muted">
              {containerName(node.container, sets, t("docs.search.personal"))}
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
}
