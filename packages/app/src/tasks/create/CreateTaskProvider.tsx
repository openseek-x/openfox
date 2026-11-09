import { useQuery } from "@tanstack/react-query";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useMatch, useNavigate } from "react-router-dom";

import { useT } from "@openfox/i18n";
import {
  Icon,
  IconButton,
  MenuItem,
  MenuSeparator,
  Popover,
  Spinner,
  cn,
  handleMenuKeyDown,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { TaskDetailDrawer } from "../detail/TaskDetailDrawer";
import { useTaskParam } from "../parts";
import { TaskTypeIcon } from "../primitives/TaskTypeIcon";
import { CreateTaskDialog, type CreateTaskInit } from "./CreateTaskDialog";

interface CreateTaskValue {
  openCreate: (init?: CreateTaskInit) => void;
}

const CreateTaskContext = createContext<CreateTaskValue | null>(null);

export function useCreateTaskDialog(): CreateTaskValue {
  const value = useContext(CreateTaskContext);
  if (!value)
    throw new Error("useCreateTaskDialog 必须在 CreateTaskProvider 内使用");
  return value;
}

/** 不在 CreateTaskProvider 内时返回 null（调用方据此不提供新建入口）。 */
export function useOptionalCreateTaskDialog(): CreateTaskValue | null {
  return useContext(CreateTaskContext);
}

/** 是否正在输入：输入框、文本域、可编辑区域聚焦时不响应单键快捷键。 */
function typing(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el) return false;
  return (
    el.isContentEditable ||
    ["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName) ||
    el.closest('[role="dialog"]') !== null
  );
}

/**
 * 组织内全局的新建任务弹窗与任务详情抽屉：
 * 任何页面都能用快捷键 C 新建；地址栏带 ?task= 时打开详情。
 */
export function CreateTaskProvider({
  orgId,
  children,
}: {
  orgId: string;
  children: ReactNode;
}) {
  const api = useApi();
  const spaces = useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
  });
  const [init, setInit] = useState<CreateTaskInit | null>(null);
  const [session, setSession] = useState(0);
  const taskParam = useTaskParam();
  const triagePage = useMatch("/o/:orgId/spaces/:spaceId/triage");

  const openCreate = useCallback((next: CreateTaskInit = {}) => {
    setSession((n) => n + 1);
    setInit(next);
  }, []);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (
        event.key.toLowerCase() !== "c" ||
        event.metaKey ||
        event.ctrlKey ||
        event.altKey ||
        event.defaultPrevented ||
        typing(event.target)
      )
        return;
      event.preventDefault();
      // 在空间任务页时默认选中该空间
      const match = /\/spaces\/([^/]+)\/tasks/.exec(window.location.pathname);
      openCreate(match ? { spaceId: decodeURIComponent(match[1]!) } : {});
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [openCreate]);

  const value = useMemo(() => ({ openCreate }), [openCreate]);
  return (
    <CreateTaskContext.Provider value={value}>
      {children}
      {init && spaces.data && (
        <CreateTaskDialog
          key={session}
          orgId={orgId}
          spaces={spaces.data}
          init={init}
          onClose={() => setInit(null)}
          onCreated={(displayId) => taskParam.open(displayId)}
        />
      )}
      {taskParam.displayId && !triagePage && (
        <TaskDetailDrawer
          orgId={orgId}
          displayId={taskParam.displayId}
          onClose={taskParam.close}
          onOpenTask={taskParam.open}
        />
      )}
    </CreateTaskContext.Provider>
  );
}

/** 侧栏「新建任务」：弹出按空间分组的任务类型菜单，选中后打开弹窗。 */
export function NewTaskButton({ orgId }: { orgId: string }) {
  const t = useT();
  const api = useApi();
  const navigate = useNavigate();
  const { openCreate } = useCreateTaskDialog();
  const anchor = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());
  const types = useQuery({
    queryKey: keys.orgTaskTypes(orgId),
    queryFn: () => api.orgTaskTypes.list(orgId),
    enabled: open,
  });
  const groups = (types.data ?? []).filter((s) => s.task_types.length > 0);
  const multi = groups.length > 1;
  return (
    <>
      <IconButton
        ref={anchor}
        label={t("shell.newTask")}
        icon={<Icon name="plus" className="text-fg-inverse" />}
        size="28"
        variant="secondary"
        className="bg-primary hover:bg-primary/90"
        aria-expanded={open}
        aria-haspopup="menu"
        onClick={() => setOpen((v) => !v)}
      />
      <Popover
        open={open}
        onOpenChange={setOpen}
        anchorRef={anchor}
        role="menu"
        aria-label={t("tasks.create.menuLabel")}
        className="max-h-[420px] min-w-[240px] overflow-y-auto"
      >
        <div onKeyDown={handleMenuKeyDown}>
          {types.isLoading && <Spinner className="mx-auto my-3" />}
          {types.data && groups.length === 0 && (
            <div className="px-2.5 py-2 text-body-sm text-fg-muted">
              {t("tasks.create.noTypes")}
            </div>
          )}
          {groups.map((space) => {
            const folded = multi && collapsed.has(space.space_id);
            return (
              <div
                key={space.space_id}
                role="group"
                aria-label={space.space_name}
              >
                {multi ? (
                  <button
                    type="button"
                    aria-expanded={!folded}
                    onClick={() =>
                      setCollapsed((prev) => {
                        const next = new Set(prev);
                        if (next.has(space.space_id))
                          next.delete(space.space_id);
                        else next.add(space.space_id);
                        return next;
                      })
                    }
                    className="flex h-7 w-full items-center gap-1 px-2.5 text-caption text-fg-muted hover:text-fg-default"
                  >
                    <Icon
                      name="chevron-down"
                      size={12}
                      className={cn(
                        "transition-transform",
                        folded && "-rotate-90",
                      )}
                    />
                    {space.space_name}
                  </button>
                ) : (
                  <div className="px-2.5 pt-2 pb-1 text-caption text-fg-muted">
                    {space.space_name}
                  </div>
                )}
                {!folded &&
                  space.task_types.map((type) => (
                    <MenuItem
                      key={type.id}
                      icon={<TaskTypeIcon icon={type.icon} />}
                      onSelect={() => {
                        setOpen(false);
                        openCreate({
                          spaceId: space.space_id,
                          typeId: type.id,
                        });
                      }}
                    >
                      {type.name}
                    </MenuItem>
                  ))}
              </div>
            );
          })}
          <MenuSeparator />
          <MenuItem
            icon={<Icon name="settings" />}
            onSelect={() => {
              setOpen(false);
              navigate(`/o/${orgId}/settings/task-types`);
            }}
          >
            {t("tasks.create.manageTypes")}
          </MenuItem>
        </div>
      </Popover>
    </>
  );
}
