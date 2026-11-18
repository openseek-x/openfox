import { useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { newIdempotencyKey, type SpaceAgent } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Dialog,
  Field,
  Select,
  TextInput,
  useToast,
} from "@openfox/ui";
import { useApi } from "../context";
import { deviceScopeBlock, runtimeChoices } from "./model";
import {
  agentErrorMessage,
  storeAgent,
  useDevices,
  useMe,
  useSpaces,
} from "./queries";

/** 新建分身时只覆盖目标空间不兼容的电脑；其余组在设置页按需覆盖或恢复继承。 */
export function CreateVariantDialog({
  orgId,
  parent,
  onClose,
  onCreated,
}: {
  orgId: string;
  parent: SpaceAgent;
  onClose: () => void;
  onCreated: (agent: SpaceAgent) => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const qc = useQueryClient();
  const spaces = useSpaces(orgId);
  const devices = useDevices(orgId);
  const me = useMe();
  const [spaceId, setSpaceId] = useState<string | null>(parent.space_id);
  const [name, setName] = useState("");
  const [computer, setComputer] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // 提交失败后保留同一个键，避免网络结果不确定时误建第二份。
  const [key, setKey] = useState(newIdempotencyKey);
  const target = spaces.data?.find((space) => space.id === spaceId);
  const parentDevice = devices.data?.find(
    (device) => device.id === parent.config?.computer_id,
  );
  const computerRequired =
    !target ||
    !parentDevice ||
    !me.data ||
    deviceScopeBlock(parentDevice, target.id, me.data.id) !== null;
  const choices = useMemo(
    () =>
      target && me.data
        ? runtimeChoices(devices.data ?? [], target.id, me.data.id).filter(
            (choice) =>
              choice.block === null &&
              choice.provider === parent.config?.runtime,
          )
        : [],
    [target, me.data, devices.data, parent.config?.runtime],
  );
  const chosen = choices.find((choice) => choice.value === computer);
  async function submit() {
    if (!target || busy || (computerRequired && !chosen)) return;
    setBusy(true);
    try {
      const created = await api.agents.createVariant(
        orgId,
        parent.space_id,
        parent.id,
        {
          space_id: target.id,
          ...(name.trim() ? { name: name.trim() } : {}),
          ...(chosen
            ? {
                overrides: {
                  computer: {
                    computer_id: chosen.deviceId,
                    grant_id: chosen.grantId,
                  },
                },
              }
            : {}),
        },
        key,
      );
      storeAgent(qc, orgId, created);
      onCreated(created);
    } catch (error) {
      toast(agentErrorMessage(t, error), { tone: "error" });
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog
      open
      onClose={busy ? undefined : onClose}
      title={t("agents.drawer.clone")}
      width={480}
      footer={
        <>
          <Button variant="outline" onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={busy}
            disabled={!target || (computerRequired && !chosen)}
            onClick={() => void submit()}
          >
            {t("agents.drawer.clone")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <p className="text-body text-fg-muted">
          {t("agents.drawer.cloneDesc")}
        </p>
        <Field label={t("agents.create.space")}>
          <Select
            value={spaceId}
            onChange={(value) => {
              setSpaceId(value);
              setComputer(null);
              setKey(newIdempotencyKey());
            }}
            options={(spaces.data ?? [])
              .filter((item) => item.status === "active")
              .map((item) => ({ value: item.id, label: item.name }))}
          />
        </Field>
        <Field label={t("agents.create.name")}>
          <TextInput
            value={name}
            maxLength={255}
            onChange={(event) => {
              setName(event.target.value);
              setKey(newIdempotencyKey());
            }}
          />
        </Field>
        {computerRequired && (
          <>
            <Callout tone="info">{t("agents.access.deviceMismatch")}</Callout>
            <Field label={t("agents.create.runtime")}>
              <Select
                value={computer}
                onChange={(value) => {
                  setComputer(value);
                  setKey(newIdempotencyKey());
                }}
                options={choices.map((choice) => ({
                  value: choice.value,
                  label: `${choice.deviceName} · ${choice.provider}`,
                }))}
              />
            </Field>
          </>
        )}
      </div>
    </Dialog>
  );
}
