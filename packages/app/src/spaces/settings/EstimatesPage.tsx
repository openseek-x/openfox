import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import type { SpaceDetail, SpaceEstimation } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Dialog,
  Icon,
  Menu,
  MenuItem,
  MenuSeparator,
  Select,
  Switch,
  TextInput,
  useToast,
} from "@openfox/ui";

import { keys, useApi } from "../../context";
import {
  SCALE_MENU,
  formatEstimate,
  isFixedScale,
  parseDuration,
  planConversion,
  resolveConversion,
  type ConversionPlan,
  type RoundingMode,
} from "../estimation";
import { scaleName, scaleOptionLabel } from "../estimation-labels";
import { putSpace, spaceErrorMessage } from "../queries";
import type { SpacePageProps } from "./GeneralPage";
import { PageHeading, RetiredBanner, SettingRow, SettingsCard } from "./parts";

const PREVIEW_ROWS = 6;

/** 任务估时。 */
export function EstimatesPage({
  orgId,
  space,
  canManage,
  retired,
}: SpacePageProps) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const editable = canManage && !retired;
  const current = space.estimation;
  const [pending, setPending] = useState<{
    target: SpaceEstimation;
    plan: ConversionPlan;
  } | null>(null);
  const save = useMutation({
    mutationFn: (input: {
      target: SpaceEstimation;
      convert?: Array<{ from: number; to: number | null }>;
    }) =>
      api.spaces.updateEstimation(orgId, space.id, {
        version: space.version,
        ...input.target,
        convert:
          input.convert && input.convert.length > 0 ? input.convert : undefined,
      }),
    onSuccess: (result) => {
      putSpace(queryClient, orgId, result.space);
      if (result.converted_tasks > 0)
        void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
      setPending(null);
    },
    onError: (error, input) =>
      toast(
        spaceErrorMessage(
          t,
          error,
          input.convert
            ? "spaces.estimates.convertFailed"
            : "spaces.estimates.saveFailed",
        ),
        { tone: "error" },
      ),
  });
  const [checking, setChecking] = useState(false);

  /** 刻度、扩展、允许零变化时：先看已有值能否自动映射，不能时弹转换弹窗。 */
  const change = async (patch: Partial<SpaceEstimation>) => {
    const target: SpaceEstimation = { ...current, ...patch };
    if (!isFixedScale(target.scale)) {
      target.extended = false;
      target.allow_zero = false;
      target.count_unestimated = false;
    }
    if (target.scale === "notUsed") return save.mutate({ target });
    setChecking(true);
    try {
      const preview = await api.spaces.estimationPreview(
        orgId,
        space.id,
        target,
      );
      const plan = planConversion({
        from: current,
        to: target,
        values: preview.values.map((item) => item.value),
      });
      if (plan.manual.length > 0) setPending({ target, plan });
      else
        save.mutate({
          target,
          convert: plan.auto.filter((item) => item.to !== item.from),
        });
    } catch (error) {
      toast(spaceErrorMessage(t, error, "spaces.estimates.saveFailed"), {
        tone: "error",
      });
    } finally {
      setChecking(false);
    }
  };
  const busy = save.isPending || checking;
  const fixed = isFixedScale(current.scale);
  const options = { extended: current.extended, allowZero: current.allow_zero };

  return (
    <div className="mx-auto flex w-full max-w-[768px] flex-col gap-5 px-6 pt-6 pb-12">
      {retired && <RetiredBanner />}
      <PageHeading
        title={t("spaces.estimates.sectionTitle")}
        description={t("spaces.estimates.sectionDescription")}
      />
      <SettingsCard>
        <SettingRow
          title={t("spaces.estimates.issueTitle")}
          description={t("spaces.estimates.issueDescription")}
          control={
            <Menu
              label={t("spaces.estimates.issueTitle")}
              placement="bottom-end"
              className="w-[260px]"
              trigger={({ ref, toggle, open }) => (
                <button
                  ref={ref}
                  type="button"
                  disabled={!editable || busy}
                  aria-haspopup="menu"
                  aria-expanded={open}
                  aria-label={t("spaces.estimates.issueTitle")}
                  onClick={toggle}
                  className="flex h-8 w-[260px] items-center gap-2 rounded-lg border border-line-default px-2.5 text-body-sm hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-50"
                >
                  <span className="min-w-0 flex-1 truncate text-left">
                    {current.scale === "notUsed"
                      ? t(scaleName("notUsed"))
                      : scaleOptionLabel(t, current.scale, options)}
                  </span>
                  <Icon
                    name="chevron-down"
                    size={14}
                    className="text-fg-muted"
                  />
                </button>
              )}
            >
              {SCALE_MENU.map((scale, index) =>
                scale === null ? (
                  <MenuSeparator key={`sep-${index}`} />
                ) : (
                  <MenuItem
                    key={scale}
                    selected={scale === current.scale}
                    onSelect={() => {
                      if (scale !== current.scale) void change({ scale });
                    }}
                  >
                    {scale === "notUsed"
                      ? t(scaleName(scale))
                      : scaleOptionLabel(t, scale, options)}
                  </MenuItem>
                ),
              )}
            </Menu>
          }
        />
        {fixed && (
          <SettingRow
            title={t("spaces.estimates.extendedTitle")}
            description={t("spaces.estimates.extendedDescription")}
            control={
              <Switch
                checked={current.extended}
                disabled={!editable || busy}
                label={t("spaces.estimates.extendedTitle")}
                onCheckedChange={(extended) => void change({ extended })}
              />
            }
          />
        )}
        {fixed && (
          <SettingRow
            title={t("spaces.estimates.allowZeroTitle")}
            description={t("spaces.estimates.allowZeroDescription")}
            control={
              <Switch
                checked={current.allow_zero}
                disabled={!editable || busy}
                label={t("spaces.estimates.allowZeroTitle")}
                onCheckedChange={(allow_zero) => void change({ allow_zero })}
              />
            }
          />
        )}
        {fixed && (
          <SettingRow
            title={t("spaces.estimates.countUnestimatedTitle")}
            description={t("spaces.estimates.countUnestimatedDescription")}
            control={
              <Switch
                checked={current.count_unestimated}
                disabled={!editable || busy}
                label={t("spaces.estimates.countUnestimatedTitle")}
                onCheckedChange={(count_unestimated) =>
                  save.mutate({ target: { ...current, count_unestimated } })
                }
              />
            }
          />
        )}
      </SettingsCard>
      {pending && (
        <ConvertDialog
          space={space}
          target={pending.target}
          plan={pending.plan}
          busy={save.isPending}
          onCancel={() => setPending(null)}
          onApply={(convert) =>
            save.mutate({ target: pending.target, convert })
          }
        />
      )}
    </div>
  );
}

/** 转换已有估时：选择换算方式 → 预览 → 转换并应用。 */
export function ConvertDialog({
  space,
  target,
  plan,
  busy,
  onCancel,
  onApply,
}: {
  space: Pick<SpaceDetail, "estimation">;
  target: SpaceEstimation;
  plan: ConversionPlan;
  busy: boolean;
  onCancel: () => void;
  onApply: (convert: Array<{ from: number; to: number | null }>) => void;
}) {
  const t = useT();
  const [step, setStep] = useState<1 | 2>(1);
  const [rounding, setRounding] = useState<RoundingMode>("nearest");
  const [rate, setRate] = useState("");
  const needsRate =
    plan.method === "pointsToTime" || plan.method === "timeToPoints";
  const secondsPerPoint = needsRate ? parseDuration(rate) : null;
  const rateInvalid =
    needsRate && rate.trim() !== "" && secondsPerPoint === null;
  const ready = !needsRate || (secondsPerPoint !== null && secondsPerPoint > 0);
  const convert = ready
    ? resolveConversion(plan, target, {
        rounding,
        secondsPerPoint: secondsPerPoint ?? undefined,
      })
    : [];
  const cleared = convert.filter((item) => item.to === null).length;
  const show = (
    value: number,
    settings: SpaceEstimation | SpaceDetail["estimation"],
  ) => formatEstimate(value, settings) || String(value);
  const unitChange =
    plan.method === "pointsToTime" || plan.method === "timeToPoints";
  return (
    <Dialog
      open
      width={384}
      onClose={busy ? undefined : onCancel}
      title={t("spaces.estimates.convert.title")}
      description={
        unitChange
          ? t("spaces.estimates.convert.descriptionUnit")
          : t("spaces.estimates.convert.description")
      }
      footer={
        step === 1 ? (
          <>
            <Button variant="outline" onClick={onCancel} disabled={busy}>
              {t("common.cancel")}
            </Button>
            <Button disabled={!ready} onClick={() => setStep(2)}>
              {t("spaces.estimates.convert.next")}
            </Button>
          </>
        ) : (
          <>
            <Button
              variant="outline"
              onClick={() => setStep(1)}
              disabled={busy}
            >
              {t("spaces.estimates.convert.back")}
            </Button>
            <Button loading={busy} onClick={() => onApply(convert)}>
              {t("spaces.estimates.convert.apply")}
            </Button>
          </>
        )
      }
    >
      {step === 1 ? (
        <div className="flex flex-col gap-3">
          {plan.auto.length > 0 && (
            <p className="text-body-sm text-fg-muted">
              {t("spaces.estimates.convert.autoCount", {
                count: plan.auto.length,
              })}
            </p>
          )}
          <p className="text-body-sm text-fg-muted">
            {t("spaces.estimates.convert.manualCount", {
              count: plan.manual.length,
            })}
          </p>
          {plan.method === "round" ||
          (plan.method === "timeToPoints" && isFixedScale(target.scale)) ? (
            <div className="flex items-center justify-between gap-3">
              <span className="text-body">
                {t("spaces.estimates.convert.rounding")}
              </span>
              <Select<RoundingMode>
                aria-label={t("spaces.estimates.convert.rounding")}
                value={rounding}
                onChange={setRounding}
                className="w-[160px]"
                options={[
                  {
                    value: "nearest",
                    label: t("spaces.estimates.convert.roundNearest"),
                  },
                  {
                    value: "ceil",
                    label: t("spaces.estimates.convert.roundCeil"),
                  },
                  {
                    value: "floor",
                    label: t("spaces.estimates.convert.roundFloor"),
                  },
                ]}
              />
            </div>
          ) : null}
          {needsRate && (
            <div className="flex flex-col gap-1">
              <div className="flex items-center justify-between gap-3">
                <span className="text-body">
                  {t("spaces.estimates.convert.rate")}
                </span>
                <TextInput
                  autoFocus
                  aria-label={t("spaces.estimates.convert.rate")}
                  className="w-[160px]"
                  placeholder={t(
                    "spaces.estimates.convert.durationPlaceholder",
                  )}
                  value={rate}
                  invalid={rateInvalid}
                  onChange={(event) => setRate(event.target.value)}
                />
              </div>
              <span className="text-caption text-fg-muted">
                {rateInvalid
                  ? t("spaces.estimates.convert.durationInvalid")
                  : t("spaces.estimates.convert.rateHint")}
              </span>
            </div>
          )}
        </div>
      ) : (
        <div className="flex flex-col gap-2">
          <span className="text-body font-medium">
            {t("spaces.estimates.convert.previewTitle")}
          </span>
          <ul
            aria-label={t("spaces.estimates.convert.previewTitle")}
            className="flex flex-col gap-1"
          >
            {convert.slice(0, PREVIEW_ROWS).map((item) => (
              <li
                key={item.from}
                className="flex items-center justify-between text-body-sm"
              >
                <span>{show(item.from, space.estimation)}</span>
                <span className="text-fg-muted">→</span>
                <span
                  className={item.to === null ? "text-fg-muted" : undefined}
                >
                  {item.to === null
                    ? t("spaces.estimates.convert.cleared")
                    : show(item.to, target)}
                </span>
              </li>
            ))}
          </ul>
          {convert.length > PREVIEW_ROWS && (
            <p className="text-caption text-fg-muted">
              {t("spaces.estimates.convert.previewMore", {
                count: convert.length - PREVIEW_ROWS,
              })}
            </p>
          )}
          {cleared > 0 && (
            <p className="text-caption text-fg-muted">
              {t("spaces.estimates.convert.clearedNote", { count: cleared })}
            </p>
          )}
        </div>
      )}
    </Dialog>
  );
}
