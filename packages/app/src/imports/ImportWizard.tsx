import { TapdSourceForm } from "./TapdSourceForm";
import { ZenSourceForm } from "./ZenSourceForm";
import type { TAPDImportCredentials, ZenTaoCredentials } from "@openfox/api";
import { SpaceTarget } from "./parts";
import { ProjectPicker } from "./ProjectPicker";
import { readImportWorkbook } from "./excel";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";

import { isApiError } from "@openfox/api";
import type {
  ImportColumnMapping,
  ImportJob,
  ImportOptions,
  ImportStatusMapping,
  ImportUserMapping,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import { Button, Callout, Icon, IconButton, cn } from "@openfox/ui";

import { keys, useApi } from "../context";
import { useSpaceList } from "../spaces/queries";
import {
  API_STEPS,
  CSV_STEPS,
  EMPTY_API_FORM,
  EMPTY_NEW_SPACE,
  formFromPrefill,
  NEW_SPACE,
  apiFormValid,
  buildApiRequest,
  defaultColumns,
  isConnectionReason,
  isFetching,
  isTerminal,
  newSpacePayload,
  targetValid,
  type ApiForm,
  type ApiPrefill,
  type ImportMode,
  type NewSpaceDraft,
  type WizardStep,
  reconcileStatuses,
  reconcileUsers,
  statusMappingsValid,
  userMappingsValid,
  validateColumns,
} from "./model";
import {
  ApiConfigStep,
  CheckStep,
  SelectTasksStep,
  type ConnectionError,
} from "./ApiSteps";
import { importErrorText, useImportJob } from "./queries";
import {
  ColumnsStep,
  ConfirmStep,
  DoneView,
  OptionsStep,
  PreviewStep,
  RunningView,
  StatusesStep,
  UploadStep,
  UsersStep,
  WelcomeStep,
} from "./WizardSteps";

// Jira CSV 导入向导：在导入与导出页内原地展开，没有子路由。

type Step = WizardStep;

const DEFAULT_OPTIONS: ImportOptions = {
  import_open: true,
  import_done: true,
  migrated_label: false,
  overwrite: false,
  import_sprints: false,
  keep_numbers: false,
};

export function ImportWizard({
  orgId,
  initialSpaceId,
  initialFile = false,
  initialContinuousSync = false,
  mode = "csv",
  brand = "Jira",
  prefill,
  canManage,
  onClose,
}: {
  orgId: string;
  initialSpaceId?: string;
  initialFile?: boolean;
  initialContinuousSync?: boolean;
  mode?: ImportMode;
  brand?: "Jira" | "TAPD" | "Teambition" | "PingCode" | "Shortcut" | "禅道";
  prefill?: ApiPrefill;
  canManage: boolean;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const spaces = useSpaceList(orgId);
  const writable = (spaces.data ?? []).filter(
    (space) =>
      space.status !== "retired" &&
      (space.can_manage || (space.my_role && space.my_role !== "VIEWER")),
  );
  const zen = brand === "禅道";
  const displayBrand = zen ? t("imports.wizard.zenBrand") : brand;
  const [credentials, setCredentials] = useState<ZenTaoCredentials>({
    site: "",
    account: "",
    password: "",
  });
  const projectProvider =
    brand === "Teambition" ||
    zen ||
    brand === "TAPD" ||
    brand === "PingCode" ||
    brand === "Shortcut";
  const [cloud, setCloud] = useState(
    projectProvider && (!initialFile || brand === "Shortcut"),
  );
  const [connection, setConnection] = useState("");
  const [project, setProject] = useState("");
  const [tapdCredentials, setTapdCredentials] = useState<TAPDImportCredentials>(
    { method: "basic", secret: "" },
  );
  const inlineTapd = cloud && brand === "TAPD";
  const [step, setStep] = useState<Step>("welcome");
  const [newSpace, setNewSpace] = useState<NewSpaceDraft>(EMPTY_NEW_SPACE);
  const [apiForm, setApiForm] = useState<ApiForm>(() =>
    prefill ? formFromPrefill(prefill) : EMPTY_API_FORM,
  );
  const [connError, setConnError] = useState<ConnectionError | null>(null);
  const [creating, setCreating] = useState(false);
  const [spaceId, setSpaceId] = useState(initialSpaceId ?? "");
  const defaultTarget = writable[0]?.id;
  useEffect(() => {
    if (cloud && !spaceId && defaultTarget) setSpaceId(defaultTarget);
  }, [cloud, spaceId, defaultTarget]);
  const [sheet, setSheet] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [syncTicket, setSyncTicket] = useState("");
  const [job, setJob] = useState<ImportJob | null>(null);
  const [columns, setColumns] = useState<ImportColumnMapping[]>([]);
  const [statuses, setStatuses] = useState<ImportStatusMapping[]>([]);
  const [users, setUsers] = useState<ImportUserMapping[]>([]);
  const [options, setOptions] = useState<ImportOptions>(DEFAULT_OPTIONS);
  const [confirmed, setConfirmed] = useState(false);
  const [error, setError] = useState("");

  const polled = useImportJob(
    orgId,
    job?.id,
    step === "running" || step === "check",
  );
  const fresh =
    job && polled.data && polled.data.id === job.id
      ? polled.data.version >= job.version
        ? polled.data
        : null
      : null;
  // 检查步骤：展示最新的拉取进度；拉完或失败后写回草稿，驱动后续步骤
  const checking = step === "check" ? (fresh ?? job) : job;
  useEffect(() => {
    if (step !== "check" || !job || !fresh) return;
    if (!isFetching(job) || isFetching(fresh)) return;
    setJob(fresh);
    if (fresh.analysis) {
      const analysis = fresh.analysis;
      setStatuses((current) => reconcileStatuses(analysis, current));
      setUsers((current) => reconcileUsers(analysis, current));
      if (initialContinuousSync && analysis.capabilities.project_sync)
        setOptions((current) => ({ ...current, continuous_sync: true }));
    }
  }, [step, job, fresh, initialContinuousSync]);
  const live =
    step === "running" &&
    job &&
    polled.data &&
    polled.data.version >= job.version
      ? polled.data
      : job;

  const refreshList = () =>
    queryClient.invalidateQueries({ queryKey: keys.imports(orgId) });

  const apply = (next: ImportJob) => {
    setJob(next);
    if (next.analysis) {
      setStatuses((current) => reconcileStatuses(next.analysis!, current));
      setUsers((current) => reconcileUsers(next.analysis!, current));
    }
  };

  const upload = useMutation({
    mutationFn: async () => {
      if (zen)
        return api.imports.fromZenTao(
          orgId,
          credentials,
          project,
          spaceId === NEW_SPACE ? newSpacePayload(newSpace) : spaceId,
        );
      if (cloud)
        return api.imports.fromProject(
          orgId,
          brand.toLowerCase(),
          inlineTapd ? tapdCredentials : connection,
          project,
          spaceId === NEW_SPACE ? newSpacePayload(newSpace) : spaceId,
        );
      let input = file!;
      if (brand === "TAPD") {
        const r = await readImportWorkbook(input, sheet);
        if (!r.csv)
          throw new Error(
            r.error === "precision"
              ? t("imports.upload.precision")
              : t("imports.upload.badExcel"),
          );
        input = new File([r.csv], input.name, { type: "text/csv" });
      }
      return api.imports.uploadJiraCsv(
        orgId,
        spaceId === NEW_SPACE ? newSpacePayload(newSpace) : spaceId,
        input,
        brand === "TAPD"
          ? "tapd_excel"
          : brand === "Teambition"
            ? "teambition_csv"
            : brand === "PingCode"
              ? "pingcode_csv"
              : "jira_csv",
      );
    },
    onSuccess: (created) => {
      setError("");
      if (zen) setCredentials({ site: "", account: "", password: "" });
      setSyncTicket(created.sync_ticket ?? "");
      setJob(created);
      setColumns(
        created.config?.columns ??
          defaultColumns(created.preview?.columns ?? []),
      );
      setStatuses(created.config?.statuses ?? []);
      setUsers(created.config?.users ?? []);
      setOptions({
        ...(created.config?.options ?? DEFAULT_OPTIONS),
        ...(initialContinuousSync && created.analysis?.capabilities.project_sync
          ? { continuous_sync: true }
          : {}),
      });
      if (cloud && created.analysis) {
        setStatuses(
          reconcileStatuses(created.analysis, created.config?.statuses ?? []),
        );
        setUsers(reconcileUsers(created.analysis, created.config?.users ?? []));
      }
      setStep(cloud ? "check" : "preview");
      void refreshList();
    },
    onError: (e) =>
      setError(
        brand === "TAPD" &&
          e instanceof Error &&
          [
            t("imports.upload.precision"),
            t("imports.upload.badExcel"),
          ].includes(e.message)
          ? e.message
          : importErrorText(t, e),
      ),
  });

  // API 导入：同步校验连接后创建草稿。凭证只经过这个函数，提交后立即清空表单里的密文。
  const createApiDraft = async () => {
    const body = buildApiRequest(apiForm, { spaceId, newSpace });
    setCreating(true);
    setConnError(null);
    try {
      const created = await api.imports.createJiraApi(orgId, body);
      setSyncTicket(created.sync_ticket ?? "");
      setJob(created);
      setColumns(created.config?.columns ?? []);
      setStatuses(created.config?.statuses ?? []);
      setUsers(created.config?.users ?? []);
      setOptions({
        ...(created.config?.options ?? DEFAULT_OPTIONS),
        ...(initialContinuousSync && created.analysis?.capabilities.project_sync
          ? { continuous_sync: true }
          : {}),
      });
      setStep("check");
      void refreshList();
    } catch (e) {
      const reason = isApiError(e, "JIRA_CONNECTION_FAILED")
        ? e.details?.reason
        : undefined;
      setConnError({
        reason: isConnectionReason(reason) ? reason : null,
        requestId: isApiError(e) ? (e.requestId ?? "") : "",
        message: importErrorText(t, e),
      });
    } finally {
      setCreating(false);
      setApiForm((form) => ({ ...form, secret: "" }));
    }
  };

  // 回到配置：丢弃已创建的草稿，重新提交会得到新的草稿
  const backToConfig = () => {
    const current = checking ?? job;
    if (current && current.status === "DRAFT")
      void api.imports
        .cancel(orgId, current.id, current.version)
        .catch(() => undefined)
        .finally(() => void refreshList());
    setSyncTicket("");
    setJob(null);
    setConnError(null);
    setError("");
    if (cloud) setProject("");
    setStep(cloud ? "upload" : "config");
  };

  // 保存配置并进入下一步；返回的 analysis 驱动后续步骤
  const save = useMutation({
    mutationFn: (_next: Step) =>
      api.imports.putConfig(orgId, job!.id, {
        config_version: job!.config_version,
        config: { columns, statuses, users, options },
      }),
    onSuccess: (next, target) => {
      setError("");
      apply(next);
      setStep(
        target === "select" &&
          next.source === "jira_csv" &&
          !next.analysis?.counts.projects
          ? "statuses"
          : target,
      );
    },
    onError: (e) => setError(importErrorText(t, e)),
  });

  const start = useMutation({
    mutationFn: () =>
      api.imports.start(orgId, job!.id, {
        config_version: job!.config_version,
        ...(options.continuous_sync && syncTicket
          ? { sync_ticket: syncTicket }
          : {}),
        ...(job!.analysis?.overwrite.modified && options.overwrite
          ? { confirm_overwrite: true }
          : {}),
      }),
    onSuccess: (next) => {
      setError("");
      setJob(next);
      setSyncTicket("");
      setStep("running");
      void refreshList();
    },
    onError: (e) => setError(importErrorText(t, e)),
  });

  const cancel = useMutation({
    mutationFn: (current: ImportJob) =>
      api.imports.cancel(orgId, current.id, current.version),
    onSuccess: (next) => {
      setJob(next);
      void refreshList();
    },
    onError: (e) => setError(importErrorText(t, e)),
  });

  const close = () => {
    // 草稿状态下关闭：丢弃草稿
    const current = step === "check" ? (checking ?? job) : job;
    if (current && current.status === "DRAFT")
      cancel.mutate(current, { onSuccess: onClose });
    else onClose();
  };

  const fileSelection =
    job?.source === "jira_csv" && !!job.analysis?.counts.projects;
  const STEPS: readonly Exclude<Step, "running">[] = cloud
    ? [
        "welcome",
        "upload",
        "check",
        "select",
        "statuses",
        ...(inlineTapd ? [] : (["users"] as const)),
        "options",
        "confirm",
      ]
    : mode === "api"
      ? API_STEPS
      : fileSelection
        ? (CSV_STEPS.flatMap((s): Step[] =>
            s === "columns" ? [s, "select"] : [s],
          ) as Exclude<Step, "running">[])
        : CSV_STEPS;
  const index = STEPS.indexOf(step as (typeof STEPS)[number]);
  const analysis = job?.analysis;
  const columnsOk = validateColumns(columns).length === 0;
  const busy =
    upload.isPending ||
    save.isPending ||
    start.isPending ||
    cancel.isPending ||
    creating;
  const targetOk = targetValid(spaceId, newSpace);
  const hasTasks =
    !!analysis &&
    ((options.import_open && analysis.counts.open > 0) ||
      (options.import_done && analysis.counts.done > 0));

  // 当前步骤能否继续
  const nextDisabled =
    busy ||
    (step === "upload" &&
      (!targetOk ||
        (cloud
          ? (!zen && !inlineTapd && !connection) || !project
          : !file || (brand === "TAPD" && !sheet)))) ||
    (step === "config" && (!targetOk || !apiFormValid(apiForm))) ||
    (step === "check" &&
      !(job && job.status === "DRAFT" && !isFetching(job) && job.analysis)) ||
    (step === "select" &&
      !hasTasks &&
      !(analysis?.counts.projects && options.file_projects)) ||
    (step === "columns" && !columnsOk) ||
    (step === "statuses" && !statusMappingsValid(statuses)) ||
    (step === "users" && !userMappingsValid(users)) ||
    (step === "confirm" &&
      (!canManage ||
        !analysis ||
        analysis.issues.length > 0 ||
        (options.overwrite && analysis.overwrite.modified > 0 && !confirmed)));

  const next = () => {
    setError("");
    switch (step) {
      case "welcome":
        return setStep(mode === "api" ? "config" : "upload");
      case "upload":
        return upload.mutate();
      case "config":
        return void createApiDraft();
      case "check":
        return setStep("select");
      case "select":
        return save.mutate("statuses");
      case "preview":
        return setStep("columns");
      case "columns":
        return save.mutate(job?.source === "jira_csv" ? "select" : "statuses");
      case "statuses":
        return save.mutate(inlineTapd ? "options" : "users");
      case "users":
        return save.mutate("options");
      case "options":
        return save.mutate("confirm");
      case "confirm":
        return start.mutate();
    }
  };
  const back = () => {
    setError("");
    // 上传成功后草稿已存在，预览之前不再回到上传
    if (step === "columns") return setStep("preview");
    if (step === "check") return backToConfig();
    if (step !== "running" && index > 0) setStep(STEPS[index - 1]!);
  };

  let body;
  if (step === "running" && live) {
    body = isTerminal(live.status) ? (
      <DoneView
        job={live}
        onViewSpace={() => {
          if (live.space_id)
            navigate(`/o/${orgId}/spaces/${live.space_id}/tasks`);
        }}
        onFinish={onClose}
      />
    ) : (
      <RunningView
        job={live}
        cancelling={cancel.isPending}
        onCancel={() => cancel.mutate(live)}
      />
    );
  } else {
    switch (step) {
      case "welcome":
        body =
          brand === "Jira" ? (
            <WelcomeStep mode={mode} />
          ) : (
            <div className="flex flex-col gap-3">
              <h3 className="text-title font-semibold">
                {t("imports.wizard.welcomeTitle")}
              </h3>
              <p className="text-body-sm text-fg-muted">
                {t("imports.wizard.projectIntro", { brand: displayBrand })}
              </p>
              {projectProvider && !zen && brand !== "Shortcut" && (
                <fieldset className="grid gap-2">
                  <legend className="mb-2">
                    {t("imports.wizard.howImport")}
                  </legend>
                  <label className="flex items-center gap-2">
                    <input
                      type="radio"
                      checked={cloud}
                      onChange={() => setCloud(true)}
                    />
                    {t("imports.wizard.apiCredentials", {
                      brand: displayBrand,
                    })}
                  </label>
                  <label className="flex items-center gap-2">
                    <input
                      type="radio"
                      checked={!cloud}
                      onChange={() => setCloud(false)}
                    />
                    {brand === "TAPD"
                      ? t("imports.wizard.tapdWorkbook")
                      : t("imports.wizard.csvFile")}
                  </label>
                </fieldset>
              )}
              {!cloud && (
                <p className="text-body-sm text-fg-muted">
                  {brand === "TAPD"
                    ? t("imports.upload.excelDesc")
                    : t("imports.wizard.providerFile", { brand: displayBrand })}
                </p>
              )}
            </div>
          );
        break;
      case "config":
        body = (
          <ApiConfigStep
            form={apiForm}
            onForm={setApiForm}
            spaces={writable}
            spaceId={spaceId}
            onSpace={setSpaceId}
            newSpace={newSpace}
            onNewSpace={setNewSpace}
            error={connError}
            prefilled={Boolean(prefill)}
          />
        );
        break;
      case "check":
        body = checking && (
          <CheckStep
            brand={displayBrand}
            job={checking}
            onBack={backToConfig}
          />
        );
        break;
      case "select":
        body = analysis && (
          <SelectTasksStep
            file={fileSelection}
            analysis={analysis}
            options={options}
            onChange={setOptions}
          />
        );
        break;
      case "upload":
        body = zen ? (
          <ZenSourceForm
            orgId={orgId}
            credentials={credentials}
            onCredentials={setCredentials}
            project={project}
            onProject={setProject}
          >
            <SpaceTarget
              spaces={writable}
              spaceId={spaceId}
              onSpace={setSpaceId}
              newSpace={newSpace}
              onNewSpace={setNewSpace}
              prefixHint={t("imports.newSpace.prefixCsvHint")}
            />
          </ZenSourceForm>
        ) : inlineTapd ? (
          <TapdSourceForm
            orgId={orgId}
            credentials={tapdCredentials}
            onCredentials={setTapdCredentials}
            project={project}
            onProject={setProject}
          >
            <SpaceTarget
              spaces={writable}
              spaceId={spaceId}
              onSpace={setSpaceId}
              newSpace={newSpace}
              onNewSpace={setNewSpace}
              prefixHint={t("imports.newSpace.prefixCsvHint")}
            />
          </TapdSourceForm>
        ) : cloud ? (
          <ProjectPicker
            orgId={orgId}
            brand={brand}
            connection={connection}
            project={project}
            onConnection={(id) => {
              setConnection(id);
              setProject("");
            }}
            onProject={setProject}
          >
            <SpaceTarget
              spaces={writable}
              spaceId={spaceId}
              onSpace={setSpaceId}
              newSpace={newSpace}
              onNewSpace={setNewSpace}
              prefixHint={t("imports.newSpace.prefixCsvHint")}
            />
          </ProjectPicker>
        ) : (
          <UploadStep
            excel={brand === "TAPD"}
            sheet={sheet}
            onSheet={setSheet}
            spaces={writable}
            spaceId={spaceId}
            onSpace={setSpaceId}
            newSpace={newSpace}
            onNewSpace={setNewSpace}
            file={file}
            onFile={setFile}
            error={error}
          />
        );
        break;
      case "preview":
        body = job && <PreviewStep job={job} />;
        break;
      case "columns":
        body = job && (
          <ColumnsStep job={job} columns={columns} onChange={setColumns} />
        );
        break;
      case "statuses":
        body = analysis && (
          <StatusesStep
            brand={displayBrand}
            orgId={orgId}
            analysis={analysis}
            mappings={statuses}
            onChange={setStatuses}
          />
        );
        break;
      case "users":
        body = analysis && (
          <UsersStep
            brand={displayBrand}
            orgId={orgId}
            analysis={analysis}
            mappings={users}
            onChange={setUsers}
          />
        );
        break;
      case "options":
        body = analysis && (
          <OptionsStep
            brand={displayBrand}
            options={options}
            onChange={setOptions}
            mode={cloud ? "api" : mode}
            capabilities={analysis.capabilities}
          />
        );
        break;
      case "confirm":
        body = analysis && (
          <ConfirmStep
            analysis={analysis}
            options={options}
            statuses={statuses}
            confirmed={confirmed}
            onConfirmed={setConfirmed}
            canManage={canManage}
            newSpace={job?.new_space ?? null}
          />
        );
        break;
    }
  }

  const content = (
    <div className="flex min-w-0 flex-1 flex-col gap-4 p-6">
      {!projectProvider && (
        <div className="flex justify-end">
          <IconButton
            label={t("imports.wizard.close")}
            icon={<Icon name="x" />}
            size="28"
            disabled={busy}
            onClick={close}
          />
        </div>
      )}
      {body}
      {error && (cloud || step !== "upload") && step !== "config" && (
        <Callout tone="danger">{error}</Callout>
      )}
      {step !== "running" && (
        <footer className="flex justify-between pt-2">
          <Button
            variant={projectProvider ? "ghost" : "secondary"}
            onClick={back}
            disabled={
              busy ||
              step === "welcome" ||
              (step === "upload" && !projectProvider) ||
              step === "config" ||
              step === "preview"
            }
            className={step === "welcome" ? "invisible" : undefined}
          >
            {t(projectProvider ? "common.back" : "imports.wizard.back")}
          </Button>
          <Button loading={busy} disabled={nextDisabled} onClick={next}>
            {step === "confirm"
              ? t("imports.wizard.start")
              : step === "upload" && !cloud
                ? t("imports.wizard.upload")
                : step === "config"
                  ? t("imports.api.connect")
                  : t(
                      step === "welcome" && projectProvider
                        ? "common.continue"
                        : "imports.wizard.next",
                    )}
          </Button>
        </footer>
      )}
    </div>
  );
  if (projectProvider) {
    const steps = STEPS.filter((name) => name !== "welcome");
    const active =
      step === "running"
        ? steps.length
        : step === "welcome"
          ? 0
          : steps.findIndex((name) => name === step);
    return (
      <section
        aria-label={t("imports.wizard.providerTitle", { brand: displayBrand })}
        className="flex flex-col gap-4"
      >
        <Button
          variant="ghost-muted"
          size="28"
          className="self-start"
          leading={<Icon name="arrow-left" />}
          onClick={close}
          disabled={busy}
        >
          {brand === "Shortcut"
            ? t("imports.wizard.backToIntegration", { brand })
            : t("imports.wizard.exit")}
        </Button>
        <div className="mx-auto w-full max-w-[768px]">
          <h2 className="mb-3 px-1 text-title font-semibold">
            {t("imports.wizard.providerTitle", { brand: displayBrand })}
          </h2>
          <div className="rounded-xl border border-line-default bg-surface-card">
            <nav
              aria-label={t("imports.wizard.steps")}
              className="overflow-x-auto border-b border-line-soft"
            >
              <ol className="flex min-w-max px-6 py-4">
                {steps.map((name, i) => (
                  <li
                    key={name}
                    aria-current={active === i ? "step" : undefined}
                    className={cn(
                      "relative flex min-w-16 flex-1 flex-col items-center gap-2 text-caption",
                      i === active
                        ? "font-medium text-fg-default"
                        : "text-fg-muted",
                    )}
                  >
                    {i > 0 && (
                      <span
                        aria-hidden="true"
                        className="absolute top-3 right-1/2 h-px w-full bg-line-default"
                      />
                    )}
                    <span
                      className={cn(
                        "relative z-[1] grid size-6 place-items-center rounded-full border bg-surface-card",
                        i <= active
                          ? "border-fg-default text-fg-default"
                          : "border-line-default",
                      )}
                    >
                      {i < active ? <Icon name="check" /> : i + 1}
                    </span>
                    {cloud && name === "upload"
                      ? t("imports.wizard.connectStep")
                      : cloud && name === "select"
                        ? t("imports.wizard.selectionStep")
                        : name === "users"
                          ? t("imports.wizard.peopleStep")
                          : t(`imports.wizard.step.${name}` as MessageKey)}
                  </li>
                ))}
              </ol>
            </nav>
            {content}
          </div>
        </div>
      </section>
    );
  }
  return (
    <section
      aria-label={t("imports.wizard.title")}
      className="flex rounded-xl border border-line-default bg-surface-card"
    >
      <nav
        aria-label={t("imports.wizard.steps")}
        className="flex w-[200px] shrink-0 flex-col gap-1 border-r border-line-soft p-4"
      >
        <h2 className="px-2 pb-2 text-body font-bold">
          {t("imports.wizard.title")}
        </h2>
        {STEPS.map((name, i) => (
          <div
            key={name}
            aria-current={name === step ? "step" : undefined}
            className={cn(
              "flex items-center gap-2 rounded-lg px-2 py-1.5 text-body-sm",
              name === step ? "bg-surface-hover font-medium" : "text-fg-muted",
            )}
          >
            {step === "running" || STEPS.indexOf(name) < index ? (
              <Icon name="check" className="text-accent" />
            ) : (
              <span className="inline-flex size-4 items-center justify-center text-caption">
                {i + 1}
              </span>
            )}
            {t(`imports.wizard.step.${name}` as MessageKey)}
          </div>
        ))}
      </nav>
      {content}
    </section>
  );
}
