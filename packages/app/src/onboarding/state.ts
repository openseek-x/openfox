import { localKeys, readLocal, writeLocal } from "../storage";

/** 首登向导的步骤。进度只保存在本机：创建组织时写入，完成或跳过后标记 done。 */
export const onboardingSteps = [
  "work-type",
  "setup",
  "runtime",
  "agent",
  "done",
] as const;
export type OnboardingStep = (typeof onboardingSteps)[number];

export const workTypes = [
  "software",
  "product",
  "marketing",
  "design",
  "project",
  "operations",
  "it",
  "hr",
  "service",
  "legal",
  "finance",
  "sales",
  "data",
] as const;
export type WorkType = (typeof workTypes)[number];

export const taskTypeKeys = ["requirement", "bug", "task", "story"] as const;
export type TaskTypeKey = (typeof taskTypeKeys)[number];

export interface OnboardingState {
  step: OnboardingStep;
  workType?: WorkType;
  spaceId?: string;
}

/** 按工作类型预选任务类型：软件开发全选，其余默认「需求」和「任务」。 */
export function defaultTaskTypes(
  workType: WorkType | undefined,
): TaskTypeKey[] {
  return workType === "software" ? [...taskTypeKeys] : ["requirement", "task"];
}

export function readOnboarding(orgId: string): OnboardingState | null {
  const raw = readLocal(localKeys.onboarding(orgId));
  if (!raw) return null;
  try {
    const value = JSON.parse(raw) as Partial<OnboardingState>;
    if (value && onboardingSteps.includes(value.step as OnboardingStep))
      return value as OnboardingState;
  } catch {
    // 旧格式或被手动改坏：视为没有进行中的向导
  }
  return null;
}

export function saveOnboarding(orgId: string, state: OnboardingState): void {
  writeLocal(localKeys.onboarding(orgId), JSON.stringify(state));
}

export function startOnboarding(orgId: string): void {
  saveOnboarding(orgId, { step: "work-type" });
}

export function onboardingPending(orgId: string): boolean {
  const state = readOnboarding(orgId);
  return state !== null && state.step !== "done";
}
