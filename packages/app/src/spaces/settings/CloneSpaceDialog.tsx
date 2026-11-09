import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import {
  isApiError,
  type CloneIssue,
  type CloneJob,
  type ClonePreview,
  type CloneRequest,
  type SpaceDetail,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Dialog,
  Icon,
  Select,
  Spinner,
  TextInput,
  cn,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import {
  isValidIdentifier,
  normalizeIdentifier,
  validateSpaceName,
} from "../identifier";
import {
  canCancelClone,
  cloneFailureReason,
  cloneStatusTone,
  isCloneTerminal,
  newCloneKey,
  phaseKey,
  visibleCounts,
} from "./clone-model";

// 克隆空间到另一个组织：预览视图与任务详情视图。

const REPREVIEW_DELAY_MS = 400;
const POLL_MS = 2000;

/** 请求签名：用来判断预览是否对应当前的目标与命名。 */
function signature(request: CloneRequest | null): string {
  return request ? JSON.stringify(request) : "";
}

export function CloneSpaceDialog({
  orgId,
  space,
  onClose,
}: {
  orgId: string;
  space: SpaceDetail;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const queryClient = useQueryClient();

  const targets = useQuery({
    queryKey: keys.cloneTargets(orgId, space.id),
    queryFn: () => api.spaceClone.targets(orgId, space.id),
    staleTime: 0,
  });
  const [chosenId, setChosenId] = useState<string | null>(null);
  const targetList = targets.data?.targets ?? [];
  const target =
    targetList.find((item) => item.id === chosenId) ?? targetList[0] ?? null;

  const [preview, setPreview] = useState<ClonePreview | null>(null);
  const [previewedSig, setPreviewedSig] = useState("");
  // 目标组织有同名 / 同标识符空间后进入「确认副本名称」，之后的命名随请求一起提交
  const [named, setNamed] = useState(false);
  const [name, setName] = useState("");
  const [prefix, setPrefix] = useState("");
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [startError, setStartError] = useState<string | null>(null);
  const [key, setKey] = useState(newCloneKey);
  const [view, setView] = useState<"form" | "job">("form");
  const [job, setJob] = useState<{ id: string; orgId: string } | null>(null);

  const nameError = named ? validateSpaceName(name) : null;
  const prefixOk = !named || isValidIdentifier(prefix);
  const namingValid = !nameError && prefixOk;

  const request: CloneRequest | null = target
    ? {
        target_organization_id: target.id,
        ...(named ? { name: name.trim(), prefix } : {}),
      }
    : null;
  const requestSig = signature(request);

  // 异步回调里读取最新输入，丢弃已过期的预览响应
  const latest = useRef({ requestSig, named });
  latest.current = { requestSig, named };

  const previewMutation = useMutation({
    mutationFn: (input: CloneRequest) =>
      api.spaceClone.preview(orgId, space.id, input),
    onSuccess: (result, input) => {
      const sent = signature(input);
      if (!latest.current.named) {
        if (sent !== latest.current.requestSig) return;
        setPreviewError(null);
        setPreview(result);
        if (result.name_conflict) {
          // 第一次发现冲突：用服务端建议的名称与标识符预填，并把它们视为已预览过的命名
          const next: CloneRequest = {
            target_organization_id: input.target_organization_id,
            name: result.target_name,
            prefix: result.target_prefix,
          };
          setNamed(true);
          setName(result.target_name);
          setPrefix(result.target_prefix);
          setPreviewedSig(signature(next));
          latest.current = { requestSig: signature(next), named: true };
        } else {
          setPreviewedSig(sent);
        }
        return;
      }
      if (sent !== latest.current.requestSig) return;
      setPreviewError(null);
      setPreview(result);
      setPreviewedSig(sent);
    },
    onError: (failure) => setPreviewError(describeError(t, failure)),
  });

  const startMutation = useMutation({
    mutationFn: (input: CloneRequest) =>
      api.spaceClone.start(orgId, space.id, input, key),
    onSuccess: (created, input) => {
      queryClient.setQueryData(
        keys.cloneJob(input.target_organization_id, created.id),
        created,
      );
      setJob({ id: created.id, orgId: input.target_organization_id });
      setStartError(null);
      setView("job");
    },
    onError: (failure, input) => {
      setStartError(describeError(t, failure));
      // 命名冲突或出现阻塞项：回到预览重新校验
      if (
        isApiError(failure, "IDENTIFIER_TAKEN") ||
        isApiError(failure, "NAME_TAKEN") ||
        isApiError(failure, "CLONE_BLOCKED")
      ) {
        previewMutation.mutate(input);
      }
    },
  });

  const resetPreview = () => {
    setPreview(null);
    setPreviewedSig("");
    setNamed(false);
    setName("");
    setPrefix("");
    setPreviewError(null);
    setStartError(null);
    setKey(newCloneKey());
  };

  // 命名改动 400ms 后自动重新预览
  useEffect(() => {
    if (!named || !request || !namingValid || !preview) return;
    if (requestSig === previewedSig) return;
    const timer = setTimeout(
      () => previewMutation.mutate(request),
      REPREVIEW_DELAY_MS,
    );
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- request 由 requestSig 决定
  }, [named, namingValid, requestSig, previewedSig, Boolean(preview)]);

  const previewCurrent = Boolean(preview) && previewedSig === requestSig;
  const canStart =
    previewCurrent &&
    namingValid &&
    !previewMutation.isPending &&
    !startMutation.isPending &&
    preview!.can_start &&
    preview!.blockers.length === 0 &&
    !preview!.name_conflict;

  const jobQuery = useQuery({
    queryKey: job ? keys.cloneJob(job.orgId, job.id) : ["clone-job", "none"],
    queryFn: () => api.spaceClone.get(job!.orgId, job!.id),
    enabled: Boolean(job),
    refetchInterval: (query) => {
      const data = query.state.data;
      return data && isCloneTerminal(data.status) ? false : POLL_MS;
    },
  });
  const jobData = jobQuery.data ?? null;

  // 到达终态：刷新源、目标两个组织的空间列表（没有完成 toast）
  const refreshed = useRef<string | null>(null);
  useEffect(() => {
    if (!job || !jobData || !isCloneTerminal(jobData.status)) return;
    if (refreshed.current === job.id) return;
    refreshed.current = job.id;
    void queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
    if (job.orgId !== orgId)
      void queryClient.invalidateQueries({ queryKey: keys.spaces(job.orgId) });
  }, [job, jobData, orgId, queryClient]);

  const cancelMutation = useMutation({
    mutationFn: () => api.spaceClone.cancel(job!.orgId, job!.id),
    onSuccess: (updated) => {
      queryClient.setQueryData(keys.cloneJob(job!.orgId, job!.id), updated);
      setStartError(null);
    },
    onError: () => setStartError(t("spaces.clone.job.cancelFailed")),
  });

  const goBack = () => {
    if (jobData && isCloneTerminal(jobData.status)) {
      setJob(null);
      setKey(newCloneKey());
    }
    setView("form");
  };

  const showJob = view === "job" && job !== null;
  const busy = startMutation.isPending;

  return (
    <Dialog
      open
      width={896}
      className="max-h-[85vh]"
      onClose={busy ? undefined : onClose}
      title={
        showJob ? (
          <button
            type="button"
            onClick={goBack}
            className="-ml-1 inline-flex items-center gap-1 rounded-md px-1 hover:bg-surface-hover"
          >
            <Icon name="chevron-left" size={16} aria-hidden />
            {t("spaces.clone.title")}
          </button>
        ) : (
          t("spaces.clone.title")
        )
      }
      description={showJob ? undefined : t("spaces.clone.description")}
    >
      {showJob ? (
        <JobView
          sourceName={space.name}
          job={jobData}
          loadFailed={jobQuery.isError && !jobData}
          loading={jobQuery.isLoading}
          cancelling={cancelMutation.isPending}
          error={startError}
          onCancel={() => cancelMutation.mutate()}
        />
      ) : (
        <div className="flex flex-col gap-4">
          <div className="flex flex-col gap-3 rounded-xl border border-line-default p-4">
            <div className="flex items-center justify-between gap-4">
              <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="text-body font-medium text-fg-default">
                  {t("spaces.clone.targetOrg")}
                </span>
                <span className="text-body-sm text-fg-muted">
                  {t("spaces.clone.targetHint")}
                </span>
              </div>
              <div className="w-[320px] shrink-0">
                {targets.isLoading ? (
                  <div
                    role="status"
                    aria-label={t("spaces.clone.targetsLoading")}
                    className="flex justify-center py-1"
                  >
                    <Spinner />
                  </div>
                ) : targets.isError ? (
                  <p role="alert" className="text-body-sm text-danger">
                    {t("spaces.clone.targetsFailed")}
                  </p>
                ) : target ? (
                  <Select
                    aria-label={t("spaces.clone.targetOrg")}
                    value={target.id}
                    disabled={previewMutation.isPending || busy}
                    options={targetList.map((item) => ({
                      value: item.id,
                      label: item.name,
                    }))}
                    onChange={(id) => {
                      if (id === target.id) return;
                      setChosenId(id);
                      resetPreview();
                    }}
                  />
                ) : (
                  <p className="text-body-sm text-fg-muted">
                    {t("spaces.clone.noTargets")}
                  </p>
                )}
              </div>
            </div>
            {!targets.isLoading && !targets.isError && !target && (
              <p className="text-body-sm text-fg-muted">
                {t("spaces.clone.noTargetsHint")}
              </p>
            )}
            <div className="flex justify-end gap-2">
              <Button
                variant="secondary"
                size="28"
                disabled={!request || busy}
                loading={previewMutation.isPending}
                onClick={() => request && previewMutation.mutate(request)}
              >
                {previewMutation.isPending
                  ? t("spaces.clone.previewing")
                  : t("spaces.clone.preview")}
              </Button>
              <Button
                size="28"
                disabled={!canStart}
                loading={startMutation.isPending}
                onClick={() => request && startMutation.mutate(request)}
              >
                {startMutation.isPending
                  ? t("spaces.clone.starting")
                  : t("spaces.clone.start")}
              </Button>
            </div>
          </div>
          {(previewError ?? startError) && (
            <p role="alert" className="text-body-sm text-danger">
              {previewError ?? startError}
            </p>
          )}
          {preview && (
            <PreviewPanel
              preview={preview}
              stale={!previewCurrent}
              named={named}
              name={name}
              prefix={prefix}
              nameError={nameError}
              prefixOk={prefixOk}
              onName={(value) => {
                setName(value);
                setKey(newCloneKey());
              }}
              onPrefix={(value) => {
                setPrefix(normalizeIdentifier(value));
                setKey(newCloneKey());
              }}
            />
          )}
        </div>
      )}
    </Dialog>
  );
}

function describeError(t: ReturnType<typeof useT>, failure: unknown): string {
  if (isApiError(failure, "FORBIDDEN")) return t("spaces.clone.forbidden");
  if (
    isApiError(failure, "IDENTIFIER_TAKEN") ||
    isApiError(failure, "NAME_TAKEN")
  )
    return t("spaces.clone.nameTaken");
  if (isApiError(failure, "CLONE_BLOCKED")) return t("spaces.clone.blocked");
  return isApiError(failure) && failure.status === 0
    ? t("spaces.clone.previewFailed")
    : t("spaces.clone.startFailed");
}

function PreviewPanel({
  preview,
  stale,
  named,
  name,
  prefix,
  nameError,
  prefixOk,
  onName,
  onPrefix,
}: {
  preview: ClonePreview;
  stale: boolean;
  named: boolean;
  name: string;
  prefix: string;
  nameError: "empty" | "tooLong" | null;
  prefixOk: boolean;
  onName: (value: string) => void;
  onPrefix: (value: string) => void;
}) {
  const t = useT();
  const counts = visibleCounts(preview.counts);
  return (
    <section
      aria-label={t("spaces.clone.previewTitle")}
      aria-busy={stale}
      className={cn(
        "flex flex-col gap-4 rounded-xl border border-line-default p-4",
        stale && "opacity-70",
      )}
    >
      <h3 className="text-title font-medium text-fg-default">
        {t("spaces.clone.previewTitle")}
      </h3>
      <div className="flex flex-wrap items-center gap-2 text-body">
        <span className="font-medium">{preview.source_name}</span>
        <Icon
          name="arrow-right"
          size={14}
          className="text-fg-muted"
          aria-hidden
        />
        <span className="font-medium">{preview.target_organization_name}</span>
        <Badge tone="neutral">{preview.target_prefix}</Badge>
      </div>
      {named && (
        <div className="flex flex-col gap-2 rounded-lg bg-surface-muted p-3">
          <span className="text-body font-medium">
            {t("spaces.clone.confirmName")}
          </span>
          <p className="text-body-sm text-fg-muted">
            {t("spaces.clone.confirmNameHint")}
          </p>
          <div className="grid grid-cols-2 gap-3">
            <label className="flex flex-col gap-1 text-body-sm text-fg-secondary">
              {t("spaces.clone.nameLabel")}
              <TextInput
                value={name}
                invalid={Boolean(nameError)}
                onChange={(event) => onName(event.target.value)}
              />
              {nameError && (
                <span role="alert" className="text-danger">
                  {t("spaces.clone.nameInvalid")}
                </span>
              )}
            </label>
            <label className="flex flex-col gap-1 text-body-sm text-fg-secondary">
              {t("spaces.clone.prefixLabel")}
              <TextInput
                value={prefix}
                invalid={!prefixOk}
                onChange={(event) => onPrefix(event.target.value)}
              />
              {!prefixOk && (
                <span role="alert" className="text-danger">
                  {t("spaces.clone.prefixInvalid")}
                </span>
              )}
            </label>
          </div>
        </div>
      )}
      {counts.length > 0 && (
        <dl className="grid grid-cols-4 gap-2">
          {counts.map(({ key, value }) => (
            <div
              key={key}
              className="flex flex-col gap-0.5 rounded-lg bg-surface-muted px-3 py-2"
            >
              <dd className="text-title font-medium text-fg-default">
                {value.toLocaleString()}
              </dd>
              <dt className="text-caption text-fg-muted">
                {t(`spaces.clone.counts.${key}`)}
              </dt>
            </div>
          ))}
        </dl>
      )}
      <IssueList
        title={t("spaces.clone.blockers")}
        items={preview.blockers}
        icon="x-circle"
        iconClass="text-danger"
      />
      <IssueList
        title={t("spaces.clone.warnings")}
        items={preview.warnings}
        icon="alert-triangle"
        iconClass="text-warning"
      />
      <CollapsibleIssues
        title={t("spaces.clone.acceptedRisks")}
        items={preview.accepted_risks}
      />
      <CollapsibleIssues
        title={t("spaces.clone.skipped")}
        items={preview.skipped}
      />
    </section>
  );
}

function IssueList({
  title,
  items,
  icon,
  iconClass,
}: {
  title: string;
  items: readonly CloneIssue[];
  icon: "x-circle" | "alert-triangle" | "info";
  iconClass: string;
}) {
  if (items.length === 0) return null;
  return (
    <div className="flex flex-col gap-1.5">
      <h4 className="text-body font-medium text-fg-default">{title}</h4>
      <ul className="flex flex-col gap-1">
        {items.map((item) => (
          <li
            key={`${item.code}:${item.message}`}
            className="flex items-start gap-2 text-body-sm text-fg-secondary"
          >
            <Icon
              name={icon}
              size={14}
              className={cn("mt-0.5 shrink-0", iconClass)}
              aria-hidden
            />
            <span>{item.message}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** 可折叠分组：标题右侧显示数量与 chevron，默认折叠；没有条目时不显示。 */
function CollapsibleIssues({
  title,
  items,
}: {
  title: string;
  items: readonly CloneIssue[];
}) {
  const [open, setOpen] = useState(false);
  if (items.length === 0) return null;
  return (
    <div className="flex flex-col gap-1.5">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className="flex items-center justify-between gap-2 rounded-md py-1 text-left hover:bg-surface-hover"
      >
        <span className="text-body font-medium text-fg-default">{title}</span>
        <span className="flex items-center gap-1 text-body-sm text-fg-muted">
          {items.length}
          <Icon
            name={open ? "chevron-up" : "chevron-down"}
            size={14}
            aria-hidden
          />
        </span>
      </button>
      {open && (
        <ul className="flex flex-col gap-1">
          {items.map((item) => (
            <li
              key={`${item.code}:${item.message}`}
              className="flex items-start gap-2 text-body-sm text-fg-secondary"
            >
              <Icon
                name="info"
                size={14}
                className="mt-0.5 shrink-0 text-info"
                aria-hidden
              />
              <span>{item.message}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function JobView({
  sourceName,
  job,
  loading,
  loadFailed,
  cancelling,
  error,
  onCancel,
}: {
  sourceName: string;
  job: CloneJob | null;
  loading: boolean;
  loadFailed: boolean;
  cancelling: boolean;
  error: string | null;
  onCancel: () => void;
}) {
  const t = useT();
  if (!job) {
    return loadFailed ? (
      <p role="alert" className="text-body-sm text-danger">
        {t("spaces.clone.job.loadFailed")}
      </p>
    ) : loading ? (
      <div className="flex justify-center py-4">
        <Spinner />
      </div>
    ) : null;
  }
  const key = phaseKey(job.phase);
  const cancelPending = cancelling || job.status === "CANCEL_REQUESTED";
  const reason = cloneFailureReason(job);
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center gap-3 rounded-xl border border-line-default p-4">
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <div className="flex flex-wrap items-center gap-2" aria-live="polite">
            <Badge tone={cloneStatusTone(job.status)}>
              {t(`spaces.clone.job.status.${job.status}`)}
            </Badge>
            {key && (
              <span className="text-body-sm text-fg-muted">
                {t("spaces.clone.job.phaseLabel", { phase: t(key) })}
              </span>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2 text-body">
            <span className="font-medium">{sourceName}</span>
            <Icon
              name="arrow-right"
              size={14}
              className="text-fg-muted"
              aria-hidden
            />
            <span className="font-medium">{job.target_space_name}</span>
          </div>
          <div className="flex flex-wrap gap-3 text-body-sm text-fg-muted">
            <span>
              {t("spaces.clone.job.progress", {
                created: job.created_count,
                total: job.total_count,
              })}
            </span>
            {job.skipped_count > 0 && (
              <span>
                {t("spaces.clone.job.skipped", { count: job.skipped_count })}
              </span>
            )}
            {job.failed_count > 0 && (
              <span className="text-danger">
                {t("spaces.clone.job.failed", { count: job.failed_count })}
              </span>
            )}
          </div>
        </div>
        {canCancelClone(job) && (
          <Button
            variant="outline"
            size="28"
            disabled={cancelPending}
            loading={cancelling}
            onClick={onCancel}
          >
            {cancelPending
              ? t("spaces.clone.job.canceling")
              : t("spaces.clone.job.cancel")}
          </Button>
        )}
      </div>
      {error && (
        <p role="alert" className="text-body-sm text-danger">
          {error}
        </p>
      )}
      {reason && (
        <div className="flex flex-col gap-1">
          <h4 className="text-body font-medium text-fg-default">
            {t("spaces.clone.job.errorTitle")}
          </h4>
          <p className="text-body-sm text-danger">
            {"code" in reason
              ? t(`spaces.clone.job.errors.${reason.code}`)
              : reason.message}
          </p>
        </div>
      )}
      {job.failures.length > 0 && (
        <div className="flex flex-col gap-1.5">
          <h4 className="text-body font-medium text-fg-default">
            {t("spaces.clone.job.failures")}
          </h4>
          <ul className="flex flex-col gap-1">
            {job.failures.map((item) => (
              <li
                key={`${item.ref}:${item.error}`}
                className="text-body-sm text-danger"
              >
                {item.ref}
                {item.ref && item.error ? " — " : ""}
                {item.error}
              </li>
            ))}
          </ul>
        </div>
      )}
      {job.operation_id &&
        isCloneTerminal(job.status) &&
        job.status !== "SUCCEEDED" && (
          <p className="text-caption text-fg-muted">
            {t("spaces.clone.job.operation", { id: job.operation_id })}
          </p>
        )}
    </div>
  );
}
