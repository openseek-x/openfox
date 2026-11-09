import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";

import {
  isApiError,
  type CreateSpaceInput,
  type SpaceFeatures,
  type SpacePurpose,
} from "@openfox/api";
import { useT, type MessageKey } from "@openfox/i18n";
import {
  Button,
  Checkbox,
  Dialog,
  Field,
  Icon,
  Radio,
  Select,
  TextInput,
  Tooltip,
  cn,
  useToast,
  type IconName,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import { useIdempotencyKey } from "../../settings/common";
import { TaskTypeIcon } from "../../tasks/primitives/TaskTypeIcon";
import {
  isValidIdentifier,
  normalizeIdentifier,
  suggestIdentifier,
  validateSpaceName,
} from "../identifier";
import {
  isOrgAdmin,
  spaceErrorMessage,
  useOrgRole,
  useSpaceList,
} from "../queries";
import { SPACE_PURPOSES, purposeLabel } from "../settings/GeneralPage";
import { templateText } from "../settings/TaskTypesPage";

/** 工作类型卡片的图标。 */
const PURPOSE_ICONS: Record<SpacePurpose, IconName> = {
  software_development: "code-xml",
  product_management: "box",
  marketing: "megaphone",
  design: "pen-tool",
  project_management: "clipboard",
  operations: "cog",
  it_support: "wrench",
  human_resources: "users",
  customer_service: "headphones",
  legal: "scale",
  finance: "dollar",
  sales: "trending-up",
  data_science: "bar-chart",
  other: "shapes",
};

/** 默认勾选的推荐类型数量（其余可手动勾选）。 */
const DEFAULT_TEMPLATE_COUNT = 3;

type Step = "purpose" | "setup" | "details";
type Method = "template" | "copy";
type IterationMode = "manual" | "automatic";

/** 向导的步骤顺序：只有软件开发进入「设置你的空间」，其他类型直接填写信息。 */
export function nextStep(
  step: Step,
  purpose: SpacePurpose | null,
): Step | null {
  if (step === "purpose")
    return purpose === "software_development" ? "setup" : "details";
  if (step === "setup") return "details";
  return null;
}
export function previousStep(
  step: Step,
  purpose: SpacePurpose | null,
): Step | null {
  if (step === "details")
    return purpose === "software_development" ? "setup" : "purpose";
  if (step === "setup") return "purpose";
  return null;
}

const FEATURE_ITEMS: Array<{
  key: keyof SpaceFeatures | "tasks";
  icon: IconName;
  label: MessageKey;
  description: MessageKey;
}> = [
  {
    key: "tasks",
    icon: "square-check",
    label: "spaces.create.modules.tasks",
    description: "spaces.create.modules.tasksDescription",
  },
  {
    key: "views",
    icon: "layers",
    label: "spaces.create.modules.views",
    description: "spaces.create.modules.viewsDescription",
  },
  {
    key: "iterations",
    icon: "repeat",
    label: "spaces.create.modules.iterations",
    description: "spaces.create.modules.iterationsDescription",
  },
  {
    key: "projects",
    icon: "target",
    label: "spaces.create.modules.projects",
    description: "spaces.create.modules.projectsDescription",
  },
  {
    key: "triage",
    icon: "inbox",
    label: "spaces.create.modules.triage",
    description: "spaces.create.modules.triageDescription",
  },
];

/** 由 OrgLayout 渲染：地址带 ?createSpace=1 时打开新建空间向导（仅组织 OWNER / ADMIN）。 */
export function CreateSpaceHost({ orgId }: { orgId: string }) {
  const [params, setParams] = useSearchParams();
  const role = useOrgRole(orgId);
  if (params.get("createSpace") !== "1" || !isOrgAdmin(role)) return null;
  const close = () => {
    const next = new URLSearchParams(params);
    next.delete("createSpace");
    setParams(next, { replace: true });
  };
  return <CreateSpaceDialog orgId={orgId} onClose={close} />;
}

/** 新建空间向导。 */
export function CreateSpaceDialog({
  orgId,
  onClose,
}: {
  orgId: string;
  onClose: () => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const keyFor = useIdempotencyKey();
  const spaces = useSpaceList(orgId);
  const [step, setStep] = useState<Step>("purpose");
  const [purpose, setPurpose] = useState<SpacePurpose | null>(null);
  const [method, setMethod] = useState<Method>("template");
  const [copyFrom, setCopyFrom] = useState<string | null>(null);
  const [picked, setPicked] = useState<Set<string> | null>(null);
  const [features, setFeatures] = useState<SpaceFeatures>({
    views: true,
    projects: true,
    iterations: false,
    triage: false,
  });
  const [iterationMode, setIterationMode] =
    useState<IterationMode>("automatic");
  const [iterationPrompt, setIterationPrompt] = useState(false);
  const [name, setName] = useState("");
  const [prefix, setPrefix] = useState("");
  const [prefixTouched, setPrefixTouched] = useState(false);
  const [visibility, setVisibility] = useState<"shared" | "private">("shared");
  const [takenError, setTakenError] = useState<string | null>(null);

  const templates = useQuery({
    queryKey: [...keys.taskTypeTemplates(orgId), purpose],
    queryFn: () => api.taskTypeTemplates.list(orgId, purpose!),
    enabled: Boolean(purpose),
    staleTime: Infinity,
  });
  // 换了工作类型后重新按推荐勾选
  useEffect(() => setPicked(null), [purpose]);
  const templateList = templates.data ?? [];
  const selected =
    picked ??
    new Set(
      templateList.slice(0, DEFAULT_TEMPLATE_COUNT).map((item) => item.key),
    );

  // 标识符默认由名称推荐，用户改过后不再自动覆盖
  useEffect(() => {
    if (!prefixTouched) setPrefix(suggestIdentifier(name));
  }, [name, prefixTouched]);
  const [debounced, setDebounced] = useState(prefix);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(prefix), 400);
    return () => window.clearTimeout(timer);
  }, [prefix]);
  const check = useQuery({
    queryKey: ["org", orgId, "space-identifier", debounced],
    queryFn: () => api.spaces.checkIdentifier(orgId, debounced),
    enabled: step === "details" && isValidIdentifier(debounced),
    staleTime: 10_000,
  });

  const nameError = name ? validateSpaceName(name) : null;
  const prefixInvalid = prefix !== "" && !isValidIdentifier(prefix);
  const taken =
    takenError ??
    (check.data && check.data.prefix === prefix && !check.data.available
      ? t("spaces.create.identifierTaken", { identifier: prefix })
      : null);

  const create = useMutation({
    mutationFn: (input: CreateSpaceInput) =>
      api.spaces.create(orgId, input, keyFor(input)),
    onSuccess: async (space) => {
      if (features.iterations) {
        await api.iterationSettings
          .update(orgId, space.id, {
            version: space.version,
            enabled: true,
            mode: iterationMode,
          })
          .catch(() => undefined);
      }
      await queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
      toast(t("spaces.create.created", { name: space.name }));
      onClose();
      navigate(`/o/${orgId}/spaces/${space.id}/tasks`);
    },
    onError: (error) => {
      if (isApiError(error, "IDENTIFIER_TAKEN"))
        setTakenError(
          t("spaces.create.identifierTaken", { identifier: prefix }),
        );
      else
        toast(spaceErrorMessage(t, error, "spaces.create.failed"), {
          tone: "error",
        });
    },
  });

  const submit = () => {
    if (
      !purpose ||
      validateSpaceName(name) ||
      !isValidIdentifier(prefix) ||
      taken
    )
      return;
    const copying =
      purpose === "software_development" && method === "copy" && copyFrom;
    const keysToCreate =
      purpose === "software_development"
        ? [...selected]
        : templateList.slice(0, DEFAULT_TEMPLATE_COUNT).map((item) => item.key);
    create.mutate({
      name: name.trim(),
      prefix,
      visibility,
      purpose,
      features,
      ...(copying
        ? { copy_from_space_id: copyFrom }
        : keysToCreate.length > 0
          ? { template_keys: keysToCreate }
          : {}),
    });
  };

  const goNext = () => {
    const next = nextStep(step, purpose);
    if (next) setStep(next);
  };
  const goBack = () => {
    const prev = previousStep(step, purpose);
    if (prev) setStep(prev);
  };

  const title =
    step === "purpose"
      ? t("spaces.create.purposeTitle")
      : step === "setup"
        ? t("spaces.create.setupTitle")
        : t("spaces.create.detailsTitle");
  const description =
    step === "purpose"
      ? t("spaces.create.purposeDescription")
      : step === "setup"
        ? t("spaces.create.setupDescription")
        : t("spaces.create.detailsDescription");
  const canCopy = (spaces.data ?? []).length > 0;

  const footer =
    step === "purpose" ? undefined : (
      <div className="flex w-full items-center justify-between">
        <Button
          variant="outline"
          leading={<Icon name="arrow-left" size={14} />}
          onClick={goBack}
          disabled={create.isPending}
        >
          {t("spaces.create.back")}
        </Button>
        {step === "setup" ? (
          <Button
            trailing={<Icon name="arrow-right" size={14} />}
            disabled={method === "copy" && !copyFrom}
            onClick={goNext}
          >
            {t("spaces.create.continue")}
          </Button>
        ) : (
          <Button
            loading={create.isPending}
            disabled={
              !name.trim() ||
              Boolean(nameError) ||
              !isValidIdentifier(prefix) ||
              Boolean(taken)
            }
            onClick={submit}
          >
            {create.isPending
              ? t("spaces.create.creating")
              : t("spaces.create.submit")}
          </Button>
        )}
      </div>
    );

  return (
    <>
      <Dialog
        open
        width={step === "purpose" ? 640 : 560}
        onClose={create.isPending ? undefined : onClose}
        title={
          <span className="flex flex-col gap-1">
            <span className="text-caption font-normal text-fg-muted">
              {t("spaces.create.dialogTitle")}
            </span>
            <span>{title}</span>
          </span>
        }
        description={description}
        footer={footer}
      >
        {step === "purpose" && (
          <div
            role="radiogroup"
            aria-label={t("spaces.create.purposeTitle")}
            className="grid grid-cols-5 gap-2"
          >
            {SPACE_PURPOSES.map((value) => (
              <button
                key={value}
                type="button"
                role="radio"
                aria-checked={purpose === value}
                onClick={() => {
                  setPurpose(value);
                  setStep(nextStep("purpose", value)!);
                }}
                className={cn(
                  "flex h-[72px] flex-col items-center justify-center gap-1.5 rounded-xl border border-line-default px-2 text-body-sm hover:bg-surface-hover",
                  purpose === value && "border-line-focus bg-surface-active",
                )}
              >
                <Icon
                  name={PURPOSE_ICONS[value]}
                  size={18}
                  className="text-fg-secondary"
                />
                <span className="truncate">{t(purposeLabel(value))}</span>
              </button>
            ))}
          </div>
        )}

        {step === "setup" && (
          <div className="flex flex-col gap-5">
            <section
              className="flex flex-col gap-2"
              aria-label={t("spaces.create.typesTitle")}
            >
              <h3 className="text-body font-semibold">
                {t("spaces.create.typesTitle")}
              </h3>
              <div
                role="radiogroup"
                className="flex items-center gap-4 text-body-sm"
              >
                <label className="flex items-center gap-1.5">
                  <Radio
                    name="method"
                    checked={method === "template"}
                    onChange={() => setMethod("template")}
                  />
                  {t("spaces.create.methodTemplate")}
                </label>
                {canCopy && (
                  <label className="flex items-center gap-1.5">
                    <Radio
                      name="method"
                      checked={method === "copy"}
                      onChange={() => setMethod("copy")}
                    />
                    {t("spaces.create.methodCopy")}
                  </label>
                )}
              </div>
              {method === "copy" ? (
                <Field label={t("spaces.create.copyFrom")}>
                  <Select
                    aria-label={t("spaces.create.copyFrom")}
                    value={copyFrom}
                    placeholder={t("spaces.create.copyFromPlaceholder")}
                    onChange={setCopyFrom}
                    options={(spaces.data ?? []).map((space) => ({
                      value: space.id,
                      label: space.name,
                    }))}
                  />
                  <p className="text-caption text-fg-muted">
                    {t("spaces.create.copyHint")}
                  </p>
                </Field>
              ) : (
                <div className="grid grid-cols-3 gap-2">
                  {templateList.map((template) => {
                    const text = templateText(t, template);
                    const checked = selected.has(template.key);
                    return (
                      <label
                        key={template.key}
                        className="flex h-10 cursor-pointer items-center gap-2 rounded-lg border border-line-default px-2.5 text-body-sm hover:bg-surface-hover"
                      >
                        <TaskTypeIcon
                          icon={template.icon}
                          color={template.color}
                          size={14}
                        />
                        <span className="min-w-0 flex-1 truncate">
                          {text.name}
                        </span>
                        <Checkbox
                          aria-label={text.name}
                          checked={checked}
                          onChange={() => {
                            const next = new Set(selected);
                            if (checked) next.delete(template.key);
                            else next.add(template.key);
                            setPicked(next);
                          }}
                        />
                      </label>
                    );
                  })}
                </div>
              )}
              <p className="text-caption text-fg-muted">
                {t("spaces.create.typesHint")}
              </p>
            </section>
            <section
              className="flex flex-col gap-2"
              aria-label={t("spaces.create.modulesTitle")}
            >
              <h3 className="text-body font-semibold">
                {t("spaces.create.modulesTitle")}
              </h3>
              <div className="grid grid-cols-3 gap-2">
                {FEATURE_ITEMS.map((item) => {
                  const locked = item.key === "tasks";
                  const checked = locked
                    ? true
                    : features[item.key as keyof SpaceFeatures];
                  return (
                    <Tooltip
                      key={item.key}
                      content={
                        <span className="block max-w-[260px]">
                          {t(item.description)}
                        </span>
                      }
                    >
                      <label
                        className={cn(
                          "flex h-10 items-center gap-2 rounded-lg border border-line-default px-2.5 text-body-sm",
                          locked
                            ? "cursor-default"
                            : "cursor-pointer hover:bg-surface-hover",
                        )}
                      >
                        <Icon
                          name={item.icon}
                          size={14}
                          className="text-fg-secondary"
                        />
                        <span className="min-w-0 flex-1 truncate">
                          {t(item.label)}
                        </span>
                        <Checkbox
                          aria-label={t(item.label)}
                          checked={checked}
                          disabled={locked}
                          onChange={() => {
                            if (locked) return;
                            const key = item.key as keyof SpaceFeatures;
                            if (key === "iterations" && !features.iterations) {
                              setIterationMode("automatic");
                              setIterationPrompt(true);
                            } else
                              setFeatures((prev) => ({
                                ...prev,
                                [key]: !prev[key],
                              }));
                          }}
                        />
                      </label>
                    </Tooltip>
                  );
                })}
              </div>
              <p className="text-caption text-fg-muted">
                {t("spaces.create.modulesHint")}
              </p>
            </section>
          </div>
        )}

        {step === "details" && (
          <div className="flex flex-col gap-5">
            <Field
              label={t("spaces.create.nameLabel")}
              error={
                nameError === "tooLong"
                  ? t("spaces.validation.nameTooLong")
                  : undefined
              }
            >
              <TextInput
                autoFocus
                value={name}
                placeholder={t("spaces.general.namePlaceholder")}
                trailing={
                  <span className="text-caption">
                    {[...name.trim()].length}/32
                  </span>
                }
                onChange={(event) => setName(event.target.value)}
              />
            </Field>
            <Field
              label={t("spaces.create.identifierLabel")}
              helper={t("spaces.create.identifierHelp")}
              error={
                prefixInvalid
                  ? t("spaces.validation.identifierFormat")
                  : (taken ?? undefined)
              }
            >
              <TextInput
                value={prefix}
                placeholder={t("spaces.general.identifierPlaceholder")}
                trailing={
                  <span className="text-caption">{prefix.length}/7</span>
                }
                onChange={(event) => {
                  setPrefixTouched(true);
                  setTakenError(null);
                  setPrefix(normalizeIdentifier(event.target.value));
                }}
              />
            </Field>
            <div className="flex flex-col gap-2">
              <span className="text-body font-medium">
                {t("spaces.access.title")}
              </span>
              <div
                role="radiogroup"
                aria-label={t("spaces.access.title")}
                className="grid grid-cols-2 gap-2"
              >
                {(["shared", "private"] as const).map((value) => (
                  <button
                    key={value}
                    type="button"
                    role="radio"
                    aria-checked={visibility === value}
                    onClick={() => setVisibility(value)}
                    className={cn(
                      "flex items-start gap-2 rounded-xl border border-line-default p-3 text-left hover:bg-surface-hover",
                      visibility === value && "border-line-focus",
                    )}
                  >
                    <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                      <span className="text-body font-medium">
                        {t(
                          value === "shared"
                            ? "spaces.access.public"
                            : "spaces.create.accessPrivate",
                        )}
                      </span>
                      <span className="text-caption text-fg-muted">
                        {t(
                          value === "shared"
                            ? "spaces.create.accessPublicHint"
                            : "spaces.create.accessPrivateHint",
                        )}
                      </span>
                    </span>
                    <Radio
                      checked={visibility === value}
                      readOnly
                      tabIndex={-1}
                      aria-hidden
                    />
                  </button>
                ))}
              </div>
            </div>
          </div>
        )}
      </Dialog>
      {iterationPrompt && (
        <IterationModeDialog
          mode={iterationMode}
          onChange={setIterationMode}
          onCancel={() => setIterationPrompt(false)}
          onConfirm={() => {
            setIterationPrompt(false);
            setFeatures((prev) => ({ ...prev, iterations: true }));
          }}
        />
      )}
    </>
  );
}

/**
 * 启用迭代 · 选择迭代模式。
 * 启用时保存迭代开关，所选模式不持久化（契约里暂无字段）。
 */
function IterationModeDialog({
  mode,
  onChange,
  onCancel,
  onConfirm,
}: {
  mode: IterationMode;
  onChange: (mode: IterationMode) => void;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const t = useT();
  return (
    <Dialog
      open
      width={448}
      onClose={onCancel}
      title={t("spaces.create.iterationMode.title")}
      description={t("spaces.create.iterationMode.description")}
      footer={
        <>
          <Button variant="ghost" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <Button onClick={onConfirm}>
            {t("spaces.create.iterationMode.confirm")}
          </Button>
        </>
      }
    >
      <div role="radiogroup" className="flex flex-col gap-2">
        {(["manual", "automatic"] as const).map((value) => (
          <label
            key={value}
            className={cn(
              "flex cursor-pointer items-start gap-2 rounded-xl border border-line-default p-3",
              mode === value && "border-line-focus",
            )}
          >
            <Radio
              name="iteration-mode"
              checked={mode === value}
              onChange={() => onChange(value)}
            />
            <span className="flex flex-col gap-0.5">
              <span className="text-body font-medium">
                {t(`spaces.create.iterationMode.${value}`)}
              </span>
              <span className="text-caption text-fg-muted">
                {t(`spaces.create.iterationMode.${value}Description`)}
              </span>
            </span>
          </label>
        ))}
        <p className="text-caption text-fg-muted">
          {t("spaces.create.iterationMode.note")}
        </p>
      </div>
    </Dialog>
  );
}
