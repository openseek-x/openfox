import type {
  ImportAnalysis,
  ImportConnectionErrorReason,
  ImportJob,
  ImportOptions,
  SpaceDetail,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  Callout,
  Icon,
  Segmented,
  Select,
  Switch,
  TextInput,
  Tooltip,
  useToast,
} from "@openfox/ui";

import {
  defaultPrefix,
  isFetching,
  isFetchFailed,
  siteUrlValid,
  type ApiForm,
  type NewSpaceDraft,
} from "./model";
import { FormRow, OptionRow, SpaceTarget, StepTitle } from "./parts";

// Jira API 导入向导的配置、检查、选择任务。

export interface ConnectionError {
  /** 400 JIRA_CONNECTION_FAILED 的 details.reason；其余错误为 null */
  reason: ImportConnectionErrorReason | null;
  requestId: string;
  /** 非连接类错误的提示文案 */
  message: string;
}

/** 5.13 错误面板：标题、排查步骤和可复制的操作编号。 */
export function ConnectionErrorPanel({
  reason,
  requestId,
  message,
}: ConnectionError) {
  const t = useT();
  const toast = useToast();
  const copy = () => {
    void navigator.clipboard
      ?.writeText(requestId)
      .then(() => toast(t("imports.conn.copied")));
  };
  const steps: MessageKey[] = reason
    ? [
        `imports.conn.reasons.${reason}.step1`,
        `imports.conn.reasons.${reason}.step2`,
        `imports.conn.reasons.${reason}.step3`,
      ]
    : [];
  return (
    <Callout
      tone="danger"
      title={reason ? t(`imports.conn.reasons.${reason}.title`) : message}
    >
      <div role="alert" className="flex flex-col gap-2">
        {steps.length > 0 && (
          <ol className="flex list-decimal flex-col gap-0.5 pl-5">
            {steps.map((key) => (
              <li key={key}>{t(key)}</li>
            ))}
          </ol>
        )}
        {requestId && (
          <span className="flex items-center gap-1 text-caption">
            {t("imports.conn.operation")}
            <button
              type="button"
              onClick={copy}
              aria-label={t("imports.conn.copy", { id: requestId })}
              className="rounded px-1 font-mono hover:bg-danger-soft"
            >
              {requestId}
            </button>
          </span>
        )}
      </div>
    </Callout>
  );
}

// ---------------- 5.5 配置 ----------------

export function ApiConfigStep({
  form,
  onForm,
  spaces,
  spaceId,
  onSpace,
  newSpace,
  onNewSpace,
  error,
  prefilled,
}: {
  form: ApiForm;
  onForm: (form: ApiForm) => void;
  spaces: readonly SpaceDetail[];
  spaceId: string;
  onSpace: (id: string) => void;
  newSpace: NewSpaceDraft;
  onNewSpace: (value: NewSpaceDraft) => void;
  error: ConnectionError | null;
  prefilled: boolean;
}) {
  const t = useT();
  const set = (patch: Partial<ApiForm>) => onForm({ ...form, ...patch });
  const cloud = form.deployment === "cloud";
  const siteBad = form.siteUrl.trim() !== "" && !siteUrlValid(form.siteUrl);
  const secretLabel = cloud
    ? t("imports.api.apiToken")
    : form.serverAuth === "pat"
      ? t("imports.api.patToken")
      : t("imports.api.passwordField");
  return (
    <div className="flex flex-col gap-4">
      <StepTitle
        title={t("imports.api.title")}
        description={t("imports.api.description")}
      />
      {prefilled && (
        <Callout tone="info">{t("imports.api.reimportHint")}</Callout>
      )}
      {error && <ConnectionErrorPanel {...error} />}
      <FormRow label={t("imports.api.deployment")}>
        <Segmented
          label={t("imports.api.deployment")}
          value={form.deployment}
          onChange={(deployment) => set({ deployment })}
          options={[
            { value: "cloud", label: t("imports.api.cloud") },
            { value: "server", label: t("imports.api.server") },
          ]}
        />
      </FormRow>
      <FormRow
        label={t("imports.api.site")}
        error={siteBad ? t("imports.api.siteInvalid") : undefined}
      >
        <TextInput
          aria-label={t("imports.api.site")}
          inputMode="url"
          autoComplete="off"
          placeholder={
            cloud
              ? t("imports.api.siteCloudPlaceholder")
              : t("imports.api.siteServerPlaceholder")
          }
          value={form.siteUrl}
          invalid={siteBad}
          onChange={(event) => set({ siteUrl: event.target.value })}
        />
      </FormRow>
      {cloud ? (
        <FormRow label={t("imports.api.email")}>
          <TextInput
            type="email"
            aria-label={t("imports.api.email")}
            autoComplete="off"
            value={form.email}
            onChange={(event) => set({ email: event.target.value })}
          />
        </FormRow>
      ) : (
        <>
          <FormRow label={t("imports.api.authKind")}>
            <Select
              aria-label={t("imports.api.authKind")}
              value={form.serverAuth}
              onChange={(serverAuth) => set({ serverAuth })}
              options={[
                { value: "pat", label: t("imports.api.pat") },
                { value: "password", label: t("imports.api.password") },
              ]}
            />
          </FormRow>
          {form.serverAuth === "password" && (
            <FormRow label={t("imports.api.username")}>
              <TextInput
                aria-label={t("imports.api.username")}
                autoComplete="off"
                value={form.username}
                onChange={(event) => set({ username: event.target.value })}
              />
            </FormRow>
          )}
        </>
      )}
      <FormRow label={secretLabel} hint={t("imports.api.secretHint")}>
        <TextInput
          type="password"
          aria-label={secretLabel}
          autoComplete="new-password"
          value={form.secret}
          onChange={(event) => set({ secret: event.target.value })}
        />
      </FormRow>
      <Callout tone="info">{t("imports.api.adminHint")}</Callout>
      <FormRow label={t("imports.api.projectKey")}>
        <TextInput
          aria-label={t("imports.api.projectKey")}
          autoComplete="off"
          placeholder={t("imports.api.projectPlaceholder")}
          value={form.projectKey}
          onChange={(event) => set({ projectKey: event.target.value })}
        />
      </FormRow>
      <FormRow label={t("imports.api.jql")}>
        <TextInput
          aria-label={t("imports.api.jql")}
          autoComplete="off"
          placeholder={t("imports.api.jqlPlaceholder")}
          value={form.jql}
          onChange={(event) => set({ jql: event.target.value })}
        />
      </FormRow>
      <SpaceTarget
        spaces={spaces}
        spaceId={spaceId}
        onSpace={onSpace}
        newSpace={newSpace}
        onNewSpace={onNewSpace}
        prefixDefault={defaultPrefix(form.projectKey)}
        prefixHint={t("imports.newSpace.prefixHint")}
      />
      <div className="rounded-xl border border-line-default bg-surface-card">
        <OptionRow
          title={t("imports.api.proxy")}
          description=""
          control={
            <Tooltip content={t("imports.api.proxyTip")}>
              <span tabIndex={0} className="inline-flex">
                <Switch
                  label={t("imports.api.proxy")}
                  checked={false}
                  disabled
                />
              </span>
            </Tooltip>
          }
        />
      </div>
    </div>
  );
}

// ---------------- 5.6 检查 ----------------

const PHASES = ["connect", "fetch", "analyze"] as const;

export function CheckStep({
  brand = "Jira",
  job,
  onBack,
}: {
  brand?: string;
  job: ImportJob;
  onBack: () => void;
}) {
  const t = useT();
  if (isFetchFailed(job)) {
    const message =
      job.error_code === "SECRETS_UNAVAILABLE"
        ? t("imports.conn.secretsUnavailable")
        : job.error_message || job.error_code;
    return (
      <div className="flex flex-col gap-4">
        <StepTitle title={t("imports.check.failedTitle")} />
        <ConnectionErrorPanel
          reason={null}
          requestId={job.operation_id}
          message={message}
        />
        <div>
          <Button variant="secondary" onClick={onBack}>
            {t("imports.check.backToConfig")}
          </Button>
        </div>
      </div>
    );
  }
  const fetching = isFetching(job);
  const fetched = job.fetch?.fetched ?? 0;
  const total = job.fetch?.total ?? null;
  const analysis = job.analysis;
  const current = fetching ? 1 : analysis ? 3 : 2;
  const stats: [MessageKey, number][] = analysis
    ? [
        ["imports.check.stats.tasks", analysis.counts.total],
        ["imports.check.stats.open", analysis.counts.open],
        ["imports.check.stats.done", analysis.counts.done],
        ["imports.check.stats.statuses", analysis.statuses.length],
        ["imports.check.stats.types", analysis.types.length],
        ["imports.check.stats.users", analysis.users.length],
        ["imports.check.stats.sprints", analysis.capabilities.sprint_count],
      ]
    : [];
  return (
    <div className="flex flex-col gap-4">
      <StepTitle
        title={t("imports.check.title")}
        description={t("imports.check.description", { brand })}
      />
      <ol className="flex flex-col gap-2">
        {PHASES.map((phase, index) => (
          <li
            key={phase}
            aria-current={index === current ? "step" : undefined}
            className={
              index > current
                ? "flex items-center gap-2 text-body text-fg-muted"
                : "flex items-center gap-2 text-body"
            }
          >
            <Icon
              name={index < current ? "check" : "circle"}
              className={index < current ? "text-accent" : undefined}
            />
            {t(`imports.check.phases.${phase}`, { brand })}
          </li>
        ))}
      </ol>
      {fetching && (
        <p role="status" className="text-body">
          {total === null
            ? t("imports.check.fetchedNoTotal", { fetched })
            : t("imports.check.fetched", { fetched, total })}
        </p>
      )}
      {stats.length > 0 && (
        <div className="grid grid-cols-4 gap-3">
          {stats.map(([key, value]) => (
            <div
              key={key}
              className="flex flex-col gap-0.5 rounded-xl border border-line-default bg-surface-card px-4 py-3"
            >
              <span className="text-caption text-fg-muted">{t(key)}</span>
              <span className="text-title font-bold">{value}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// ---------------- 5.7 选择任务 ----------------

export function SelectTasksStep({
  file = false,
  analysis,
  options,
  onChange,
}: {
  file?: boolean;
  analysis: ImportAnalysis;
  options: ImportOptions;
  onChange: (options: ImportOptions) => void;
}) {
  const t = useT();
  if (file) {
    const tasks = analysis.counts.open + analysis.counts.done;
    const taskTitle = t("imports.select.fileTasks", { count: tasks });
    const projectTitle = t("imports.select.fileProjects", {
      count: analysis.counts.projects ?? 0,
    });
    return (
      <div className="flex flex-col gap-4">
        <StepTitle title={t("imports.select.fileTitle")} />
        <OptionRow
          title={taskTitle}
          description={t("imports.select.fileTaskDesc")}
          control={
            <Switch
              label={taskTitle}
              disabled={!tasks}
              checked={!!tasks && (options.import_open || options.import_done)}
              onCheckedChange={(enabled) =>
                onChange({
                  ...options,
                  import_open: enabled,
                  import_done: enabled,
                })
              }
            />
          }
        />
        <OptionRow
          title={projectTitle}
          description={t("imports.select.fileProjectDesc")}
          control={
            <Switch
              label={projectTitle}
              checked={!!options.file_projects}
              onCheckedChange={(enabled) =>
                onChange({ ...options, file_projects: enabled })
              }
            />
          }
        />
      </div>
    );
  }
  const rows: ["import_open" | "import_done", MessageKey, number][] = [
    ["import_open", "imports.select.open", analysis.counts.open],
    ["import_done", "imports.select.done", analysis.counts.done],
  ];
  return (
    <div className="flex flex-col gap-4">
      <StepTitle
        title={t("imports.select.title")}
        description={t("imports.select.description")}
      />
      <div className="flex flex-col rounded-xl border border-line-default bg-surface-card">
        {rows.map(([key, title, count]) => (
          <OptionRow
            key={key}
            title={t(title, { count })}
            description=""
            control={
              <Switch
                label={t(title, { count })}
                checked={options[key] && count > 0}
                disabled={count === 0}
                onCheckedChange={(value) =>
                  onChange({ ...options, [key]: value })
                }
              />
            }
          />
        ))}
      </div>
      {options.file_projects !== undefined && (
        <OptionRow
          title={t("imports.select.fileProjects", {
            count: analysis.counts.projects ?? 0,
          })}
          description={t("imports.select.fileProjectDesc")}
          control={
            <Switch
              label={t("imports.select.fileProjects", {
                count: analysis.counts.projects ?? 0,
              })}
              checked={!!options.file_projects}
              onCheckedChange={(value) =>
                onChange({ ...options, file_projects: value })
              }
            />
          }
        />
      )}
      {!options.import_open &&
        !options.import_done &&
        !options.file_projects && (
          <p role="alert" className="text-body-sm text-danger">
            {t("imports.select.none")}
          </p>
        )}
    </div>
  );
}
