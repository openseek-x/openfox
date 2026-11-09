import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { isApiError, type Device } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Callout,
  Checkbox,
  Dialog,
  Field,
  Icon,
  Segmented,
  Select,
  Skeleton,
  Stepper,
  TextInput,
  Textarea,
  useToast,
  type IconName,
} from "@openfox/ui";

import { CONFIG_DEFAULTS } from "../agents/model";
import { ConnectComputerPanel } from "../computers/ConnectComputer";
import { keys, useApi } from "../context";
import { desktopBridge, desktopLocalStatusKey, isDesktop } from "../desktop";
import {
  defaultTaskTypes,
  readOnboarding,
  saveOnboarding,
  taskTypeKeys,
  workTypes,
  type OnboardingState,
  type TaskTypeKey,
  type WorkType,
} from "./state";

const workTypeIcons: Record<WorkType, IconName> = {
  software: "code",
  product: "kanban",
  marketing: "megaphone",
  design: "pen-tool",
  project: "folder-kanban",
  operations: "cog",
  it: "wrench",
  hr: "users",
  service: "headphones",
  legal: "scale",
  finance: "wallet",
  sales: "trending-up",
  data: "chart",
};

/** 等待电脑连接时轮询更快；与电脑页共用 keys.devices 查询键 */
const DEVICE_POLL_MS = 5000;

/** 首登向导：768 宽弹窗叠在应用外壳上，不可关闭。 */
export function OnboardingWizard({
  orgId,
  onFinish,
}: {
  orgId: string;
  onFinish: () => void;
}) {
  const t = useT();
  const [state, setState] = useState<OnboardingState>(
    () => readOnboarding(orgId) ?? { step: "work-type" },
  );
  const go = (next: OnboardingState) => {
    saveOnboarding(orgId, next);
    setState(next);
    if (next.step === "done") onFinish();
  };
  const current = state.step === "runtime" ? 2 : state.step === "agent" ? 3 : 1;
  return (
    <Dialog
      open
      width={768}
      closeOnBackdrop={false}
      className="h-[min(832px,calc(100vh-48px))]"
    >
      <div className="flex h-full min-h-[560px] flex-col gap-6">
        <Stepper
          steps={[
            t("wizard.steps.workspace"),
            t("wizard.steps.runtime"),
            t("wizard.steps.agent"),
          ]}
          current={current}
        />
        {state.step === "work-type" && (
          <WorkTypeStep
            onPick={(workType) => go({ ...state, step: "setup", workType })}
          />
        )}
        {state.step === "setup" && (
          <SetupStep
            orgId={orgId}
            workType={state.workType}
            onBack={() => go({ ...state, step: "work-type" })}
            onSaved={(spaceId) => go({ ...state, step: "runtime", spaceId })}
          />
        )}
        {state.step === "runtime" && (
          <RuntimeStep
            orgId={orgId}
            onNext={() => go({ ...state, step: "agent" })}
          />
        )}
        {state.step === "agent" && (
          <AgentStep
            orgId={orgId}
            spaceId={state.spaceId}
            onDone={() => go({ ...state, step: "done" })}
          />
        )}
      </div>
    </Dialog>
  );
}

function StepHeader({ title, desc }: { title: string; desc?: string }) {
  return (
    <div className="flex flex-col gap-1">
      <h2 className="text-subheading font-medium">{title}</h2>
      {desc && <p className="text-body text-fg-muted">{desc}</p>}
    </div>
  );
}

function WorkTypeStep({ onPick }: { onPick: (workType: WorkType) => void }) {
  const t = useT();
  return (
    <>
      <StepHeader
        title={t("wizard.workTypeTitle")}
        desc={t("wizard.workTypeDesc")}
      />
      <div className="grid grid-cols-5 gap-3">
        {workTypes.map((type) => (
          <button
            key={type}
            type="button"
            onClick={() => onPick(type)}
            className="flex h-[76px] flex-col items-center justify-center gap-2 rounded-lg border border-line-default bg-surface-card text-body-sm hover:border-line-strong hover:bg-surface-muted"
          >
            <Icon
              name={workTypeIcons[type]}
              size={20}
              className="text-fg-secondary"
            />
            {t(`wizard.workTypes.${type}`)}
          </button>
        ))}
      </div>
    </>
  );
}

function SetupStep({
  orgId,
  workType,
  onBack,
  onSaved,
}: {
  orgId: string;
  workType: WorkType | undefined;
  onBack: () => void;
  onSaved: (spaceId: string) => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState<Set<TaskTypeKey>>(
    () => new Set(defaultTaskTypes(workType)),
  );
  const [busy, setBusy] = useState(false);

  const toggle = (key: TaskTypeKey) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });

  // 重复保存是安全的：已有空间直接复用，同名任务类型跳过
  const save = async () => {
    setBusy(true);
    try {
      const spaces = await api.spaces.list(orgId);
      const space =
        spaces[0] ??
        (await api.spaces.create(orgId, {
          name: t("wizard.defaultSpaceName"),
          prefix: "open",
          visibility: "shared",
        }));
      const existing = await api.taskTypes.list(orgId, space.id);
      const names = new Set(existing.map((item) => item.name));
      const seeded = existing.find(
        (item) => item.is_default && item.name === "Task",
      );
      for (const key of taskTypeKeys) {
        if (!selected.has(key)) continue;
        const name = t(`wizard.taskTypeNames.${key}`);
        if (names.has(name)) continue;
        // 新空间自带英文名的默认类型「Task」，选中「任务」时改成当前语言的名称
        if (key === "task" && seeded)
          await api.taskTypes.rename(
            orgId,
            space.id,
            seeded.id,
            name,
            seeded.version,
          );
        else await api.taskTypes.create(orgId, space.id, name);
        names.add(name);
      }
      await queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
      onSaved(space.id);
    } catch {
      toast(t("wizard.setupFailed"), { tone: "error" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <StepHeader title={t("wizard.setupTitle")} />
      <section className="flex flex-col gap-3">
        <h3 className="text-body font-medium">{t("wizard.taskTypes")}</h3>
        <div className="grid grid-cols-3 gap-3">
          {taskTypeKeys.map((key) => (
            <label
              key={key}
              className="flex h-9 cursor-pointer items-center gap-2 rounded-lg border border-line-default bg-surface-card px-2.5 text-body hover:bg-surface-muted"
            >
              <Checkbox
                checked={selected.has(key)}
                onChange={() => toggle(key)}
              />
              {t(`wizard.taskTypeNames.${key}`)}
            </label>
          ))}
        </div>
      </section>
      <section className="flex flex-col gap-2">
        <h3 className="text-body font-medium">{t("wizard.modules")}</h3>
        <Callout tone="info">{t("wizard.modulesNote")}</Callout>
      </section>
      <p className="text-body-sm text-fg-muted">{t("wizard.setupHint")}</p>
      <div className="mt-auto flex justify-end gap-2">
        <Button variant="outline" size="md" onClick={onBack} disabled={busy}>
          {t("common.back")}
        </Button>
        <Button
          size="md"
          loading={busy}
          disabled={selected.size === 0}
          onClick={() => void save()}
        >
          {t("common.save")}
        </Button>
      </div>
    </>
  );
}

function useDevices(orgId: string) {
  const api = useApi();
  return useQuery({
    queryKey: keys.devices(orgId),
    queryFn: () => api.devices.list(orgId),
    refetchInterval: DEVICE_POLL_MS,
  });
}

/** 与派发判定一致：电脑与授权都 ACTIVE，且 90 秒内有心跳 */
function deviceOnline(device: Device): boolean {
  return device.online;
}

function RuntimeStep({ orgId, onNext }: { orgId: string; onNext: () => void }) {
  const t = useT();
  const devices = useDevices(orgId);
  const bridge = desktopBridge();
  const local = useQuery({
    queryKey: desktopLocalStatusKey,
    queryFn: () => bridge!.getLocalStatus(),
    enabled: Boolean(bridge),
  });
  const list = devices.data ?? [];

  return (
    <>
      <StepHeader
        title={t("wizard.steps.runtime")}
        desc={t("wizard.runtimeIntro")}
      />
      {bridge && (
        <section className="flex items-center gap-3 rounded-xl border border-line-default p-4">
          <Icon name="laptop" className="text-fg-secondary" />
          <span className="flex-1 text-body font-medium">
            {t("wizard.localRuntime")}
          </span>
          {local.data ? (
            <>
              <span className="text-body-sm text-fg-muted">
                {local.data.runtime.runtime}
                {local.data.runtime.version
                  ? ` ${local.data.runtime.version}`
                  : ""}
              </span>
              <Badge
                variant="status"
                tone={local.data.runtime.runnable ? "success" : "muted"}
              >
                {local.data.runtime.runnable
                  ? t("wizard.runtimeRunnable")
                  : t("wizard.runtimeUnavailable")}
              </Badge>
            </>
          ) : (
            <Skeleton className="h-5 w-24" />
          )}
        </section>
      )}
      <section className="flex flex-col gap-2">
        <div className="flex items-center justify-between">
          <h3 className="text-body font-medium">{t("wizard.devicesTitle")}</h3>
          <Button
            variant="ghost"
            size="28"
            leading={<Icon name="refresh" />}
            onClick={() => void devices.refetch()}
          >
            {t("wizard.refresh")}
          </Button>
        </div>
        {devices.isLoading ? (
          <Skeleton className="h-12 w-full" />
        ) : list.length === 0 ? (
          <p className="rounded-xl border border-dashed border-line-default p-4 text-body-sm text-fg-muted">
            {t("wizard.noDevices")}
          </p>
        ) : (
          <ul className="flex flex-col gap-2">
            {list.map((device) => (
              <li
                key={device.id}
                className="flex items-center gap-3 rounded-xl border border-line-default p-3"
              >
                <Icon name="monitor" className="text-fg-secondary" />
                <span className="flex-1 truncate text-body">{device.name}</span>
                <Badge
                  variant="status"
                  tone={deviceOnline(device) ? "success" : "muted"}
                >
                  {deviceOnline(device)
                    ? t("wizard.deviceOnline")
                    : t("wizard.deviceOffline")}
                </Badge>
              </li>
            ))}
          </ul>
        )}
      </section>
      <section className="flex flex-col gap-2">
        <h3 className="text-body font-medium">{t("connect.title")}</h3>
        <ConnectComputerPanel orgId={orgId} inline />
      </section>
      {!isDesktop() && (
        <p className="text-body-sm text-fg-muted">
          {t("wizard.downloadDesktop")}
        </p>
      )}
      <div className="mt-auto flex items-center justify-end gap-2">
        <span className="mr-auto text-body-sm text-fg-muted">
          {t("wizard.skipHint")}
        </span>
        <Button variant="outline" size="md" onClick={onNext}>
          {t("common.skip")}
        </Button>
        <Button size="md" disabled={list.length === 0} onClick={onNext}>
          {t("common.continue")}
        </Button>
      </div>
    </>
  );
}

type AgentCategory = "code" | "office" | "design";

function AgentStep({
  orgId,
  spaceId,
  onDone,
}: {
  orgId: string;
  spaceId: string | undefined;
  onDone: () => void;
}) {
  const t = useT();
  const api = useApi();
  const devices = useDevices(orgId);
  const spaces = useQuery({
    queryKey: keys.spaces(orgId),
    queryFn: () => api.spaces.list(orgId),
  });
  const bridge = desktopBridge();
  const local = useQuery({
    queryKey: desktopLocalStatusKey,
    queryFn: () => bridge!.getLocalStatus(),
    enabled: Boolean(bridge),
  });

  const [category, setCategory] = useState<AgentCategory>("code");
  const [name, setName] = useState(() => t("wizard.agentNameDefault"));
  const [role, setRole] = useState(() => t("wizard.agentRoleDefault"));
  const [visibility, setVisibility] = useState<"PRIVATE" | "SHARED">("PRIVATE");
  const [deviceId, setDeviceId] = useState<string | null>(null);
  const [instructions, setInstructions] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const list = devices.data ?? [];
  const device =
    list.find((item) => item.id === deviceId) ??
    list.find(deviceOnline) ??
    list[0];
  const space =
    spaces.data?.find((item) => item.id === spaceId) ?? spaces.data?.[0];
  const runtime = local.data?.runtime.runnable
    ? local.data.runtime.runtime
    : "codex";

  const create = async () => {
    if (!device || !space) return;
    const trimmed = name.trim();
    if (!trimmed) {
      setError(t("wizard.agentFailed"));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await api.agents.create(orgId, space.id, {
        name: trimmed,
        description: role.trim(),
        kind: category === "code" ? "CODE" : "OFFICE",
        visibility,
        allow_assign: true,
        allow_direct_chat: true,
        config: {
          ...CONFIG_DEFAULTS,
          computer_id: device.id,
          grant_id: device.grant_id,
          runtime,
          model: "default",
          instructions,
          directory_mode: "NONE",
          directory_roots: [],
        },
      });
      onDone();
    } catch (err) {
      setError(
        isApiError(err) && err.status === 409
          ? t("wizard.agentNameTaken")
          : t("wizard.agentFailed"),
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <Segmented
        label={t("wizard.steps.agent")}
        value={category}
        onChange={setCategory}
        options={[
          { value: "code", label: t("wizard.agentCategories.code") },
          { value: "office", label: t("wizard.agentCategories.office") },
          { value: "design", label: t("wizard.agentCategories.design") },
        ]}
        className="w-[360px]"
      />
      <div className="grid grid-cols-2 gap-4">
        <Field label={t("wizard.agentName")} required>
          <TextInput
            variant="labeled"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field label={t("wizard.agentRole")}>
          <TextInput
            variant="labeled"
            value={role}
            onChange={(e) => setRole(e.target.value)}
          />
        </Field>
        <Field label={t("wizard.agentSpace")}>
          <TextInput
            variant="labeled"
            value={space?.name ?? ""}
            readOnly
            disabled
          />
        </Field>
        <Field label={t("wizard.agentVisibility")}>
          <Select
            variant="labeled"
            value={visibility}
            onChange={setVisibility}
            options={[
              { value: "PRIVATE", label: t("wizard.visibilityPrivate") },
              { value: "SHARED", label: t("wizard.visibilityShared") },
            ]}
          />
        </Field>
        <Field label={t("wizard.agentRuntime")} className="col-span-2">
          <Select
            variant="labeled"
            value={device?.id ?? null}
            onChange={setDeviceId}
            disabled={list.length === 0}
            options={list.map((item) => ({
              value: item.id,
              label: `${runtime} · ${item.name}`,
              icon: <Icon name="monitor" />,
            }))}
          />
        </Field>
        <Field label={t("wizard.agentInstructions")} className="col-span-2">
          <Textarea
            rows={3}
            value={instructions}
            placeholder={t("wizard.agentInstructionsPlaceholder")}
            onChange={(e) => setInstructions(e.target.value)}
          />
        </Field>
      </div>
      {list.length === 0 && (
        <Callout tone="warning">{t("wizard.noComputer")}</Callout>
      )}
      {error && <Callout tone="danger">{error}</Callout>}
      <div className="mt-auto flex justify-end gap-2">
        <Button variant="outline" size="md" onClick={onDone} disabled={busy}>
          {t("wizard.notNow")}
        </Button>
        <Button
          size="md"
          loading={busy}
          disabled={!device || !space}
          onClick={() => void create()}
        >
          {t("common.create")}
        </Button>
      </div>
    </>
  );
}
