import type { Device, DeviceRuntimeTestResult } from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import { Button, Icon, cn, useToast } from "@openfox/ui";

import { useDeviceCommand } from "../../computers/commands";
import {
  OwnerOnlyButton,
  commandRequestError,
  commandSettledError,
} from "../../computers/ComputerStorage";
import { isSupportedRuntime } from "../model";

type DeviceRuntimeTestProvider = "codex" | "claude-code" | "opencode" | "pi";

/** 测试连接不可用的原因；null = 可以测试 */
export function runtimeTestBlock(
  device: Pick<Device, "owner_user_id" | "online"> | undefined,
  provider: string,
  meId: string | undefined,
): "ownerOnly" | "offline" | "unsupported" | null {
  if (!device || meId === undefined || device.owner_user_id !== meId)
    return "ownerOnly";
  if (!isSupportedRuntime(provider)) return "unsupported";
  if (!device.online) return "offline";
  return null;
}

function testResult(result: unknown): DeviceRuntimeTestResult | null {
  const value = result as Partial<DeviceRuntimeTestResult> | null;
  if (!value || typeof value.ok !== "boolean") return null;
  return value as DeviceRuntimeTestResult;
}

/** 结果文案与色调：已连接 · N ms / 模型不在列表 / 未登录 / 失败 */
function describeResult(
  result: DeviceRuntimeTestResult,
  t: Translate,
): { tone: "success" | "warning" | "danger"; text: string } {
  if (result.ok && result.model_available)
    return {
      tone: "success",
      text: t("connect.test.ok", { ms: result.latency_ms }),
    };
  if (result.ok)
    return {
      tone: "warning",
      text: t("connect.test.modelMissing", { model: result.model }),
    };
  if (result.login_status === "LOGGED_OUT")
    return { tone: "danger", text: t("connect.test.loginRequired") };
  return { tone: "danger", text: result.message || t("connect.test.failed") };
}

const toneClass = {
  success: "text-accent",
  warning: "text-warning",
  danger: "text-danger",
} as const;

/**
 * Agent 设置 · 电脑 & Runtime 的「测试连接」：runtime_test 远程命令，
 * 只做 Runtime 自检（登录状态 + 模型列表），不调用模型。只有电脑所有者可点。
 */
export function RuntimeTestButton({
  orgId,
  device,
  provider,
  model,
  meId,
}: {
  orgId: string;
  device: Device | undefined;
  provider: string;
  model: string;
  meId: string | undefined;
}) {
  const t = useT();
  const toast = useToast();
  const block = runtimeTestBlock(device, provider, meId);
  const test = useDeviceCommand(orgId, device?.id ?? "", "runtime_test", {
    onSettled: (command) => {
      const error = commandSettledError(command, t);
      if (error) toast(error, { tone: "error" });
    },
    onError: (error) => toast(commandRequestError(error, t), { tone: "error" }),
  });
  const result =
    test.command?.status === "SUCCEEDED"
      ? testResult(test.command.result)
      : null;
  const shown = result && !test.running ? describeResult(result, t) : null;

  return (
    <span className="flex items-center gap-2">
      {shown && (
        <span
          role="status"
          className={cn(
            "max-w-56 truncate text-caption",
            toneClass[shown.tone],
          )}
          title={shown.text}
        >
          {shown.text}
        </span>
      )}
      <OwnerOnlyButton
        isOwner={block === null}
        reason={block ? t(`connect.test.${block}`) : ""}
      >
        <Button
          variant="outline"
          size="32"
          leading={<Icon name="zap" />}
          loading={test.running}
          disabled={block !== null || test.running}
          onClick={() =>
            test.start({
              provider: provider as DeviceRuntimeTestProvider,
              ...(model && model !== "default" ? { model } : {}),
            })
          }
        >
          {t("connect.test.button")}
        </Button>
      </OwnerOnlyButton>
    </span>
  );
}
