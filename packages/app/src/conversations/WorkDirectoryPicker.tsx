import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ConversationWorkspaceSelection, SpaceAgent } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Icon, Popover, TextInput, Tooltip } from "@openfox/ui";
import { useApi } from "../context";
import { useMe, useOrgRole } from "../agents/queries";
import { useLocalStatus } from "../computers/queries";
import { desktopBridge } from "../desktop";
import {
  BaseBranch,
  DirectoryModeDialog,
  validWorkspaceRef,
  type DirectoryMode,
} from "./WorkDirectoryModes";

export const workspaceOptionsKey = (orgId: string, agentId: string) =>
  ["org", orgId, "conversation-workspace-options", agentId] as const;

export function useWorkspaceOptions(
  orgId: string,
  agentId: string,
  enabled: boolean,
  source?: string,
) {
  const api = useApi(),
    me = useMe(),
    role = useOrgRole(orgId);
  return useQuery({
    queryKey: [
      ...workspaceOptionsKey(orgId, agentId),
      me.data?.id,
      role,
      source ?? null,
    ],
    queryFn: () => api.conversations.workspaceOptions(orgId, agentId, source),
    enabled: enabled && !!agentId && !!me.data?.id,
    retry: false,
    staleTime: 0,
  });
}

function basename(value: string) {
  return value.split(/[\\/]/).filter(Boolean).at(-1) ?? value;
}
const contains = (roots: readonly string[], dir: string) =>
  roots.some((root) => dir === root || dir.startsWith(root + "/"));

type Inspected = { directory: string; path: string; worktree: boolean };
export interface WorkDirectoryDraft {
  selection: ConversationWorkspaceSelection;
  path?: string;
  worktree?: boolean;
  deviceId: string;
}

export function WorkDirectoryPicker({
  orgId,
  agentId,
  config,
  value,
  locked,
  busy,
  onChange,
  onEditSettings,
}: {
  orgId: string;
  agentId: string;
  config?: SpaceAgent["config"];
  value?: {
    selection: ConversationWorkspaceSelection;
    path?: string;
    worktree?: boolean;
  };
  locked: boolean;
  busy: boolean;
  onChange: (v: WorkDirectoryDraft | undefined) => void;
  onEditSettings?: () => void;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient();
  const trigger = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false),
    [manual, setManual] = useState(false),
    [path, setPath] = useState("");
  const [inspecting, setInspecting] = useState(false),
    [error, setError] = useState("");
  const [candidate, setCandidate] = useState<
    (Inspected & { deviceId: string }) | null
  >(null);
  const [mode, setMode] = useState<DirectoryMode>("DIRECT"),
    [base, setBase] = useState("");
  const generation = useRef(0);
  useEffect(
    () => () => {
      generation.current++;
    },
    [],
  );
  const options = useWorkspaceOptions(orgId, agentId, open || !!candidate);
  const registration = useLocalStatus().data?.registration;
  const device = options.data?.device_id,
    roots = options.data?.directories ?? [];
  const resolve = desktopBridge()?.resolveWorkDirectory;
  const local =
    !!resolve &&
    registration?.registered &&
    registration.organization_id === orgId &&
    registration.device_id === device;
  const defaultDirs =
    config?.directory_mode === "EXPLICIT" ? (config.directory_roots ?? []) : [];
  const chosen = value?.path || value?.selection.directory;
  const chip = chosen
    ? basename(chosen)
    : defaultDirs.length === 1
      ? basename(defaultDirs[0]!)
      : defaultDirs.length > 1
        ? t("chat.workspace.multiple")
        : "";
  const problem = options.isError
    ? t("chat.workspace.failed")
    : options.isPending
      ? t("chat.workspace.loading")
      : !device
        ? t("chat.workspace.unbound")
        : !options.data?.supported
          ? t("chat.workspace.unsupported")
          : !roots.length
            ? t("chat.workspace.noRoots")
            : "";
  const close = () => {
    generation.current++;
    setOpen(false);
    setInspecting(false);
    setError("");
  };
  const remove = useMutation({
    mutationFn: (directory: string) =>
      api.conversations.removeRecentDirectory(
        orgId,
        agentId,
        device!,
        directory,
      ),
    onSuccess: () =>
      cache.invalidateQueries({
        queryKey: workspaceOptionsKey(orgId, agentId),
      }),
    onError: () => setError(t("chat.workspace.removeFailed")),
  });
  function save(
    next: Inspected,
    selectedMode: DirectoryMode,
    selectedBase: string,
    dismiss: boolean,
  ) {
    if (!device || !contains(roots, next.directory)) return;
    onChange({
      selection: {
        mode: selectedMode,
        directory: next.directory,
        ...(selectedMode === "WORKTREE" && selectedBase.trim()
          ? { base_ref: selectedBase.trim() }
          : {}),
      },
      path: next.path,
      worktree: next.worktree,
      deviceId: device,
    });
    if (dismiss) {
      close();
      setCandidate(null);
      trigger.current?.focus();
    }
  }
  function apply(next: Inspected, prompt: boolean) {
    if (prompt && device && (next.worktree || mode === "WORKTREE")) {
      close();
      setCandidate({ ...next, deviceId: device });
      return;
    }
    if (!next.worktree && mode === "WORKTREE" && !prompt) {
      setError(t("chat.workspace.noWorktree"));
      return;
    }
    save(next, next.worktree ? mode : "DIRECT", base, true);
  }
  async function inspect(
    input: string,
    via: "typed" | "browse" | "remote" | "recent",
  ) {
    if (busy || locked || problem || !device || inspecting) return;
    setError("");
    if (via === "typed" && !/^(?:\/|[A-Za-z]:[\\/]|\\\\)/.test(input.trim())) {
      setError(t("chat.workspace.absolute"));
      return;
    }
    if (mode === "WORKTREE" && !validWorkspaceRef(base)) {
      setError(t("chat.workspace.invalidRef"));
      return;
    }
    if (!local) {
      if (!contains(roots, input)) {
        setError(t("chat.workspace.notAllowed"));
        return;
      }
      apply(
        {
          directory: input,
          path: input,
          worktree: options.data?.git_directories?.includes(input) ?? false,
        },
        via === "remote",
      );
      return;
    }
    const current = ++generation.current;
    setInspecting(true);
    try {
      const checked = await resolve!({
        orgId,
        agentId,
        deviceId: device,
        roots,
        ...(via === "browse" ? { browse: true } : { path: input.trim() }),
      });
      if (current !== generation.current) return;
      if (checked.ok && contains(roots, checked.directory)) {
        apply(checked, via === "browse");
        return;
      }
      if (!checked.ok && checked.reason === "cancelled") return;
      setError(
        t(
          !checked.ok && checked.reason === "unavailable"
            ? "chat.workspace.failed"
            : "chat.workspace.notAllowed",
        ),
      );
    } catch {
      if (current === generation.current) setError(t("chat.workspace.failed"));
    } finally {
      if (current === generation.current) setInspecting(false);
    }
  }
  const savedDir = value?.selection.directory;
  const inline =
    savedDir && path === chosen && contains(roots, savedDir)
      ? {
          directory: savedDir,
          path,
          worktree:
            value.worktree ??
            options.data?.git_directories?.includes(savedDir) ??
            false,
        }
      : undefined;
  function dismiss() {
    if (inspecting || !manual || problem) {
      close();
      return;
    }
    if (!path.trim()) {
      onChange(undefined);
      close();
      return;
    }
    if (inline) {
      if (mode === "WORKTREE" && !validWorkspaceRef(base)) return;
      save(inline, mode, base, true);
      return;
    }
    if (local) {
      void inspect(path, "typed");
      return;
    }
    close();
  }
  const title = locked
    ? t("chat.workspace.locked")
    : chosen
      ? t("chat.workspace.pathTooltip", { path: chosen })
      : t("chat.workspace.defaultTooltip");
  return (
    <>
      <Tooltip content={title}>
        <button
          type="button"
          ref={trigger}
          aria-label={t("chat.workspace.label")}
          title={title}
          aria-expanded={locked ? undefined : open}
          aria-disabled={locked || busy}
          onClick={() => {
            if (locked || busy) return;
            if (open) {
              dismiss();
              return;
            }
            setMode(
              value?.selection.mode === "WORKTREE" ? "WORKTREE" : "DIRECT",
            );
            setBase(value?.selection.base_ref ?? "");
            setManual(!!value);
            setPath(chosen ?? "");
            setOpen(true);
          }}
          className={`inline-flex h-7 max-w-[220px] items-center rounded-full text-[13px] ${chip ? "min-w-8 gap-1.5 px-2" : "w-7 shrink-0 justify-center"} text-fg-secondary hover:bg-surface-hover ${locked ? "cursor-not-allowed" : ""}`}
        >
          <Icon name="folder" size={16} className="shrink-0" />
          {chip && <span className="truncate">{chip}</span>}
        </button>
      </Tooltip>
      <Popover
        open={open && !locked}
        onOpenChange={(v) => {
          if (!v) dismiss();
        }}
        onEscape={close}
        anchorRef={trigger}
        placement="top-start"
        aria-label={t("chat.workspace.title")}
        className="w-[420px] max-w-[calc(100vw-16px)] p-3"
      >
        <div className="mb-3 text-body font-medium">
          {t("chat.workspace.title")}
        </div>
        <div className="flex flex-col gap-3 text-[13px] leading-5">
          <div className="flex items-start gap-2.5">
            <label className="flex flex-1 items-start gap-2.5">
              <input
                type="radio"
                className="mt-1 accent-fg-default"
                checked={!manual}
                onChange={() => {
                  onChange(undefined);
                  close();
                  trigger.current?.focus();
                }}
              />
              <span>
                {t("chat.workspace.follow")}
                <span className="block text-caption text-fg-muted">
                  {defaultDirs.length
                    ? defaultDirs.map(basename).join(", ")
                    : t("chat.workspace.temporary")}
                </span>
              </span>
            </label>
            {options.isSuccess &&
              options.data.can_manage_directory &&
              onEditSettings && (
                <button
                  className="text-caption text-link hover:underline"
                  onClick={() => {
                    close();
                    onEditSettings();
                  }}
                >
                  {t("chat.workspace.editAgent")}
                </button>
              )}
          </div>
          <div>
            <label className="flex items-start gap-2.5">
              <input
                type="radio"
                checked={manual}
                disabled={!!problem}
                className="mt-1 accent-fg-default"
                onChange={() => setManual(true)}
              />
              <span>
                {t("chat.workspace.manual")}
                {problem && (
                  <span className="block text-caption text-fg-muted">
                    {problem}
                  </span>
                )}
              </span>
            </label>
            {options.isError && (
              <button
                className="ml-6 text-caption underline"
                onClick={() => void options.refetch()}
              >
                {t("common.retry")}
              </button>
            )}
            {manual && !problem && (
              <div className="ml-6 mt-2 space-y-2">
                {local ? (
                  <div className="flex items-center gap-2">
                    <TextInput
                      autoFocus
                      aria-label={t("chat.workspace.manual")}
                      placeholder={t("chat.workspace.path")}
                      value={path}
                      onChange={(e) => setPath(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" && !e.nativeEvent.isComposing) {
                          e.preventDefault();
                          void inspect(path, "typed");
                        }
                      }}
                    />
                    <Button
                      variant="outline"
                      size="28"
                      disabled={inspecting}
                      onClick={() => void inspect("", "browse")}
                    >
                      {t("chat.workspace.browse")}
                    </Button>
                  </div>
                ) : (
                  <select
                    autoFocus
                    aria-label={t("chat.workspace.choose")}
                    className="h-8 w-full rounded-md border border-line-default bg-surface-card px-2"
                    value={roots.includes(path) ? path : ""}
                    onChange={(e) => {
                      if (e.target.value)
                        void inspect(e.target.value, "remote");
                    }}
                  >
                    <option value="">{t("chat.workspace.choose")}</option>
                    {roots.map((r) => (
                      <option key={r} value={r}>
                        {r}
                      </option>
                    ))}
                  </select>
                )}
                {inline && (
                  <div className="space-y-1.5">
                    <div className="text-caption text-fg-muted">
                      {t("chat.workspace.chooseMode")}
                    </div>
                    <div
                      role="radiogroup"
                      aria-label={t("chat.workspace.chooseMode")}
                      className="rounded-lg bg-surface-muted px-3 py-2"
                    >
                      {(["DIRECT", "WORKTREE"] as const).map((item) => {
                        const direct = item === "DIRECT",
                          disabled = !direct && !inline.worktree;
                        const hint = t(
                          disabled
                            ? "chat.workspace.noWorktree"
                            : direct
                              ? "chat.workspace.directHelp"
                              : "chat.workspace.worktreeHelp",
                        );
                        return (
                          <div
                            key={item}
                            className="flex min-h-8 items-center gap-2"
                          >
                            <label className="flex items-center gap-2">
                              <input
                                type="radio"
                                checked={mode === item}
                                disabled={disabled}
                                onChange={() => {
                                  setMode(item);
                                  if (direct) setBase("");
                                  save(inline, item, direct ? "" : base, false);
                                }}
                              />
                              <Icon
                                name={direct ? "workflow" : "git-branch"}
                                size={16}
                              />
                              {t(
                                direct
                                  ? "chat.workspace.direct"
                                  : "chat.workspace.worktree",
                              )}
                            </label>
                            <Tooltip content={hint}>
                              <button
                                type="button"
                                aria-label={hint}
                                className="text-fg-muted"
                              >
                                <Icon name="info" size={14} />
                              </button>
                            </Tooltip>
                          </div>
                        );
                      })}
                    </div>
                    {mode === "WORKTREE" && (
                      <BaseBranch value={base} onChange={setBase} />
                    )}
                  </div>
                )}
                {!!options.data?.recent_directories?.length && (
                  <div>
                    <div className="text-caption text-fg-muted">
                      {t("chat.workspace.recent")}
                    </div>
                    {options.data.recent_directories
                      .filter((r) => contains(roots, r))
                      .map((r) => (
                        <div
                          key={r}
                          className="group flex items-center gap-2 rounded-md hover:bg-surface-hover"
                        >
                          <button
                            className="flex min-w-0 flex-1 items-center gap-2 px-1 py-1 text-left"
                            title={r}
                            onClick={() => void inspect(r, "recent")}
                          >
                            <Icon name="folder" size={14} />
                            <span>{basename(r)}</span>
                            <span className="truncate text-fg-muted">
                              {r.split("/").slice(0, -1).join("/")}
                            </span>
                          </button>
                          <button
                            type="button"
                            aria-label={t("chat.workspace.remove")}
                            disabled={remove.isPending}
                            className="p-1 text-fg-muted opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
                            onClick={() => remove.mutate(r)}
                          >
                            <Icon name="x" size={14} />
                          </button>
                        </div>
                      ))}
                  </div>
                )}
              </div>
            )}
          </div>
          {error && (
            <span role="alert" className="text-caption text-danger">
              {error}
            </span>
          )}
        </div>
      </Popover>
      {candidate && (
        <DirectoryModeDialog
          path={candidate.path}
          initialMode={candidate.worktree ? mode : null}
          initialBase={base}
          worktree={candidate.worktree}
          canConfirm={
            options.isSuccess &&
            candidate.deviceId === device &&
            contains(roots, candidate.directory)
          }
          onClose={() => {
            setCandidate(null);
            trigger.current?.focus();
          }}
          onConfirm={(nextMode, nextBase) =>
            save(candidate, nextMode, nextBase, true)
          }
        />
      )}
    </>
  );
}
