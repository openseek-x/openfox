"""OpenFox's fixed-version Hermes tool boundary; embedded in the Go executable.

No user plugin discovery, source rewriting or shell fallback is added here.
This module is executed from the verified source directory by the verified Python.
"""
import builtins
import atexit
import copy
import json
import os
import pathlib
import runpy
import select
import shutil
import signal
import stat
import subprocess
import sys
import threading
import tempfile
import time
import weakref

options = json.loads(sys.argv[1])
if sys.argv[2] != "--":
    raise RuntimeError("invalid OpenFox Hermes bootstrap invocation")
command = sys.argv[3:]
workspace = pathlib.Path(options["cwd"])
rg_path = options["rg"]
ALLOWED = frozenset(("read_file", "search_files"))
TOOLSET = "openfox_read_search"

parent_gone = threading.Event()
parent_cleanup_done = threading.Event()
children_lock = threading.Lock()
children = weakref.WeakSet()
root_cleanup_lock = threading.Lock()

def cleanup_owned_root():
    with root_cleanup_lock:
        # The fixed startup may mkdir skills before its denied index read.
        # Remove these known empty directories without granting any read of
        # extension content. Nonempty directories are not opened specially.
        for name in ("skills", "hooks", "plugins"):
            try:
                os.rmdir(os.path.join(run_root, "home", "profile", name))
            except OSError:
                pass
        shutil.rmtree(run_root, ignore_errors=True)
        parent_cleanup_done.set()

def stop_owned_children():
    with children_lock:
        active = list(children)
    for process in active:
        if process.poll() is None:
            try:
                process.kill()
            except ProcessLookupError:
                pass
    deadline = time.monotonic() + 1
    for process in active:
        try:
            process.wait(timeout=max(.001, deadline - time.monotonic()))
        except subprocess.TimeoutExpired:
            pass

# Only Go owns the writer. No PID scanning, launchd, guardian executable or
# signal permission beyond direct children is needed when Go is SIGKILLed.
parent_fd = options.get("parent_fd")
if parent_fd is not None:
    if type(parent_fd) is not int or parent_fd < 4:
        raise RuntimeError("invalid parent lifetime descriptor")
    os.set_inheritable(parent_fd, False)
    run_root = options["run_root"]

    def parent_lost():
        if os.read(parent_fd, 1):
            return
        parent_gone.set()
        stop_owned_children()
        os.kill(os.getpid(), signal.SIGINT)
        # Let the official gateway/ACP shut down, then bound blocked native
        # background threads. Every participant cleans only its own Run root.
        time.sleep(2)
        stop_owned_children()
        cleanup_owned_root()
        os._exit(143)

    def parent_exit_cleanup():
        # Cover EOF racing with normal interpreter exit before the watcher has
        # been scheduled. With a live Go writer this check never blocks.
        if parent_gone.is_set() or select.select([parent_fd], [], [], 0)[0]:
            if not parent_cleanup_done.wait(4):
                stop_owned_children()
                cleanup_owned_root()

    atexit.register(parent_exit_cleanup)
    threading.Thread(target=parent_lost, name="openfox-parent-lifetime", daemon=True).start()

# The trusted Go host pins the canonical workspace with a no-symlink dirfd.
# Reading its ancestors is not added to the native sandbox permissions.
workspace_fd = options["workspace_fd"]
if type(workspace_fd) is not int or workspace_fd < 3 or not stat.S_ISDIR(os.fstat(workspace_fd).st_mode):
    raise RuntimeError("invalid OpenFox workspace descriptor")
os.set_inheritable(workspace_fd, False)
from tools import file_tools
from tools import file_operations
from tools import file_operations_search
from tools.registry import registry
from toolsets import create_custom_toolset
from run_agent import AIAgent
import gateway.session_managed_worker as managed_worker

if parent_fd is not None:
    import gateway.run as native_gateway
    original_gateway_exit = native_gateway._exit_after_graceful_shutdown

    def owned_gateway_exit(exit_code):
        if parent_gone.is_set():
            # This function is called after the official gateway teardown.
            # Its normal final os._exit would bypass both atexit and our
            # daemon watcher. Private profile metrics/locks need no rewrite
            # once their owner is gone and the whole Run root is removed.
            stop_owned_children()
            cleanup_owned_root()
            os._exit(exit_code)
        return original_gateway_exit(exit_code)

    native_gateway._exit_after_graceful_shutdown = owned_gateway_exit

class SearchSubprocesses:
    def __getattr__(self, name):
        return getattr(subprocess, name)

    def Popen(self, args, *a, **kw):
        if not args or args[0] != rg_path:
            raise PermissionError("OpenFox search permits its fixed rg only")
        if getattr(local, "search_deadline", None) is None:
            raise PermissionError("OpenFox rg requires an authorized snapshot")
        if args[-1] != getattr(local, "snapshot_target", None):
            raise PermissionError("OpenFox rg target differs from its authorized snapshot")
        # Fixed upstream puts pattern/root last. These exclusions must follow
        # the caller's glob, which can otherwise re-include Git control data.
        args = [*args[:-2], "--glob", "!.git", "--glob", "!**/.git/**", *args[-2:]]
        with children_lock:
            if parent_gone.is_set():
                raise PermissionError("OpenFox parent is gone")
            process = subprocess.Popen(args, *a, **kw)
            children.add(process)
            return process

# The fixed search module alone receives the tracked subprocess lookup.
file_operations_search.subprocess = SearchSubprocesses()

local = threading.local()
search_lock = threading.Lock()
original_open = builtins.open
original_os_open = os.open
original_rg_native = file_operations_search.SearchMixin._run_rg_native

def bounded_rg_native(self, argv, fetch_limit, timeout, merge_stderr=False):
    deadline = getattr(local, "search_deadline", None)
    if deadline is None or deadline <= time.monotonic():
        raise SnapshotError("snapshot/search deadline exceeded")
    result = original_rg_native(self, argv, fetch_limit, min(timeout, deadline-time.monotonic()), merge_stderr)
    if result.exit_code in (124, 130) or time.monotonic() >= deadline:
        raise SnapshotError("search cancelled or timed out; no partial success")
    return result

file_operations_search.SearchMixin._run_rg_native = bounded_rg_native

# read_file's native backend uses its module-global open after stat. Guard the actual
# open rather than trusting its earlier path/size probe. Other native operations
# (loading immutable code, runtime storage) keep their original open behavior.
def guarded_open(file, mode="r", *args, **kwargs):
    if not getattr(local, "read_guard", False):
        return original_open(file, mode, *args, **kwargs)
    if not isinstance(file, (str, bytes, os.PathLike)) or mode not in ("r", "rb") or kwargs.get("opener") is not None:
        raise PermissionError("OpenFox read policy rejected this open")
    path = pathlib.Path(os.fsdecode(file))
    if not path.is_absolute():
        path = workspace / path
    try:
        relative = path.relative_to(workspace)
    except ValueError:
        raise PermissionError("OpenFox read is limited to its workspace") from None
    parts = relative.parts
    if not parts or any(part in (".", "..") for part in parts):
        raise PermissionError("invalid OpenFox read path")
    parent = os.dup(workspace_fd)
    descriptor = None
    try:
        for part in parts[:-1]:
            next_fd = original_os_open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
            os.close(parent)
            parent = next_fd
        descriptor = original_os_open(parts[-1], os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW, dir_fd=parent)
        if not stat.S_ISREG(os.fstat(descriptor).st_mode):
            raise PermissionError("OpenFox reads regular files only")
        result = original_open(descriptor, mode, *args, **kwargs)
        descriptor = None
        return result
    finally:
        os.close(parent)
        if descriptor is not None:
            os.close(descriptor)

# Only this fixed upstream module's open lookup is wrapped, once. Runtime
# imports, logging, SQLite and other gateway threads keep their normal I/O.
file_operations.open = guarded_open
original_read = file_tools._handle_read_file

def refusal(code):
    return json.dumps({"error": code, "host_rejected": True})

def private_run_path(path):
    for part in path.parts:
        head, separator, suffix = part.rpartition("-")
        if separator and head.casefold() == "openfox-hermes":
            if suffix and suffix.isascii() and suffix.isdecimal():
                return True
    return False

def checked_path(value, verb):
    if not isinstance(value, str) or not value or "\x00" in value:
        raise ValueError("invalid_path")
    if file_tools.get_nt_namespace_error(value, verb=verb):
        raise ValueError("invalid_path_namespace")
    candidate = pathlib.Path(value)
    if not candidate.is_absolute():
        candidate = workspace / candidate
    try:
        candidate = candidate.resolve(strict=True)
        if private_run_path(candidate):
            raise ValueError("private_run_state_is_not_workspace_data")
        if not candidate.is_relative_to(workspace):
            raise ValueError("outside_workspace")
        if file_tools.get_read_block_error(str(candidate)):
            raise ValueError("protected_file")
        if not candidate.is_file() and not candidate.is_dir():
            raise ValueError("unsupported_root")
        if not os.access(candidate, os.R_OK):
            raise ValueError("unreadable_root")
    except (OSError, RuntimeError):
        raise ValueError("missing_or_unreadable_root") from None
    return candidate

def read_file(args, **kw):
    if not isinstance(args, dict):
        return refusal("invalid_args")
    try:
        candidate = checked_path(args.get("path"), "Read")
    except ValueError as error:
        return refusal(str(error))
    call = dict(args)
    call["path"] = str(candidate)
    local.read_guard = True
    try:
        return original_read(call, **kw)
    finally:
        local.read_guard = False

def search_files(args, **kw):
    if not isinstance(args, dict):
        return refusal("invalid_args")
    if set(args) - {"pattern", "target", "path", "file_glob", "limit", "offset", "output_mode", "context", "order"}:
        return refusal("unknown_parameter")
    if args.get("target", "content") != "content":
        return refusal("unsupported_target")
    if args.get("order", "discovery") != "discovery":
        return refusal("unsupported_order")
    output = args.get("output_mode", "content")
    if output not in ("content", "files_only", "count"):
        return refusal("unsupported_output_mode")
    pattern = args.get("pattern")
    if not isinstance(pattern, str) or not pattern or "\x00" in pattern:
        return refusal("invalid_pattern")
    if pattern.startswith("-"):
        return refusal("leading_dash_pattern_use_bracket_literal")
    glob = args.get("file_glob")
    if glob is not None and (not isinstance(glob, str) or "\x00" in glob):
        return refusal("invalid_file_glob")
    limit, offset, context = args.get("limit", 50), args.get("offset", 0), args.get("context", 0)
    if any(type(value) is not int for value in (limit, offset, context)):
        return refusal("invalid_number")
    if not 1 <= limit <= 200 or offset < 0 or offset + limit > 2000 or not 0 <= context <= 20:
        return refusal("search_budget_exceeded_narrow_the_search")
    try:
        candidate = checked_path(args.get("path", "."), "Search")
        if any(part.casefold() == ".git" for part in candidate.relative_to(workspace).parts):
            return refusal("Git control files are not searchable content")
    except ValueError as error:
        return refusal(str(error))
    if not search_lock.acquire(blocking=False):
        return refusal("search_busy_retry_after_current_search")
    try:
        return snapshot_search(candidate, pattern, glob, limit, offset, output, context, kw.get("task_id") or "default")
    except SnapshotError as error:
        return refusal(str(error))
    except OSError:
        return refusal("snapshot_read_failed_or_source_changed")
    finally:
        local.search_deadline = None
        local.snapshot_target = None
        search_lock.release()

def snapshot_search(candidate, pattern, glob, limit, offset, output, context, task_id):
    path = str(candidate)
    key = ("search", pattern, "content", path, glob or "", limit, offset, "discovery")
    with file_tools._read_tracker_lock:
        task_data = file_tools._read_tracker.setdefault(task_id, {"last_key": None, "consecutive": 0, "read_history": set()})
        count = file_tools._bump_consecutive(task_data, key)
    if count >= 4:
        return json.dumps({"error": "repeated_search", "already_searched": count, file_tools.GUARDRAIL_REFUSAL_KEY: True})
    ops = file_tools._get_file_ops(task_id)
    if not ops._native_read_enabled():
        return refusal("native_backend_required")
    from tools import interrupt
    def blocked(relative):
        source = workspace / relative
        return private_run_path(source) or bool(file_tools.get_read_block_error(str(source)))
    stage = tempfile.mkdtemp(prefix="search-", dir=os.environ["TMPDIR"])
    snapshot = Snapshot(workspace_fd, stage, cancel=lambda:parent_gone.is_set() or interrupt.is_interrupted(), blocked=blocked)
    try:
        relative = candidate.relative_to(workspace).as_posix()
        if relative == ".":relative = ""
        snapshot.run(glob, relative, root_is_file=candidate.is_file())
        snapshot.check()
        local.search_deadline = snapshot.deadline
        local.snapshot_target = str(pathlib.Path(stage)/relative)
        result = ops._search_with_rg(pattern, local.snapshot_target, glob, limit, offset, output, context, rg_executable=rg_path)
        snapshot.check()
        # Normalize structured paths before the original sensitive-path filter.
        def source_path(value):
            return str(workspace) + value[len(stage):] if value.startswith(stage+"/") else value
        for match in result.matches or ():
            match.path = source_path(match.path)
        if result.files is not None:result.files = [source_path(value) for value in result.files]
        if result.counts is not None:result.counts = {source_path(key):value for key,value in result.counts.items()}
    finally:
        shutil.rmtree(stage, ignore_errors=True)

    omitted = file_tools._filter_read_blocked_search_results(result, task_id)
    for match in result.matches or ():
        if match.content:
            match.content = file_tools.redact_sensitive_text(match.content, file_read=True, secret_file=file_tools._is_secret_file_arg(file_tools._resolved_match_path(match.path, task_id)))
    payload = result.to_dict(densify=True)
    if omitted:
        payload["_omitted"] = f"{omitted} protected result(s) omitted."
    if count >= 3:
        payload["_warning"] = "You have run this exact search 3 times consecutively."
    if snapshot.git_metadata_warning:
        payload["_git_metadata"] = "Shared Git metadata outside the workspace was not read; workspace ignore files were used."
    if payload.get("truncated"):
        payload["_hint"] = (f"Results truncated. Use offset={offset + limit} within the 2000-result budget, or narrow the search." if offset+limit<2000 else "Result budget reached. Narrow the search root or pattern.")
    return json.dumps(payload, ensure_ascii=False).replace(stage, str(workspace))

schema = copy.deepcopy(registry.get_schema("search_files"))
properties = schema["parameters"]["properties"]
properties["target"]["enum"] = ["content"]
properties["order"]["enum"] = ["discovery"]
properties["limit"].update(minimum=1, maximum=200, default=50)
properties["offset"].update(minimum=0, maximum=1999, default=0)
properties["context"].update(minimum=0, maximum=20, default=0)
schema["description"] = "Read-only content search via a fresh private workspace snapshot: 8 MiB/file, 64 MiB total, 10000 files, 50000 entries, depth 64 and total 60 seconds. One search at a time. Discovery order and later pages may change. No filename/modified/multiple-root search; limit 1..200, offset+limit <=2000, context 0..20. A leading '-' pattern is rejected; use [-] to match it."
registry.register(name="read_file", toolset="file", schema=registry.get_schema("read_file"), handler=read_file, override=True, max_result_size_chars=100000)
registry.register(name="search_files", toolset="file", schema=schema, handler=search_files, override=True, max_result_size_chars=100000)
create_custom_toolset(TOOLSET, "OpenFox workspace read and bounded content search", tools=sorted(ALLOWED))

# Upstream name repair can turn write_file into read_file. Preserve the model's
# original name so its actual request is rejected instead of silently changed.
AIAgent._repair_tool_call = lambda self, name: None
original_execute = AIAgent._execute_tool_calls

def execute_tool_calls(self, assistant_message, messages, effective_task_id, api_call_count=0):
    if any(call.function.name not in ALLOWED for call in assistant_message.tool_calls):
        raise RuntimeError("OpenFox refused an unsupported tool before execution")
    return original_execute(self, assistant_message, messages, effective_task_id, api_call_count)

AIAgent._execute_tool_calls = execute_tool_calls
original_dispatch = registry.dispatch

def dispatch(name, args, **kw):
    if name not in ALLOWED:
        return refusal("unsupported_tool")
    return original_dispatch(name, args, **kw)

registry.dispatch = dispatch

def reject_managed_policy(authority, ref):
    from gateway.session_policy import policy_for_source
    policy = policy_for_source(authority.runner, authority.sessions[ref.session_id].source)
    if policy is not None and (policy.safe_mode or policy.ignore_user_config or policy.kanban_json is not None or policy.config().get("gateway", {}).get("managed_workers") is True):
        raise RuntimeError("OpenFox does not support managed workers or policy bypass sessions")
    return None

managed_worker.managed_policy = reject_managed_policy
sys.argv = ["hermes", *command]
runpy.run_module("hermes_cli.main", run_name="__main__", alter_sys=True)
