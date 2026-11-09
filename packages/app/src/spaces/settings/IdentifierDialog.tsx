import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { isApiError, type SpaceDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Dialog, TextInput, useToast } from "@openfox/ui";

import { keys, useApi } from "../../context";
import { normalizeIdentifier } from "../identifier";
import { putSpace, spaceErrorMessage } from "../queries";

/**
 * 更改空间标识符确认：输入与新标识符完全一致后才能提交；
 * 成功后所有任务编号随之变化，旧编号仍可跳转。
 */
export function IdentifierDialog({
  orgId,
  space,
  next,
  onClose,
  onChanged,
}: {
  orgId: string;
  space: SpaceDetail;
  next: string;
  onClose: () => void;
  onChanged: (space: SpaceDetail) => void;
}) {
  const t = useT();
  const api = useApi();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [typed, setTyped] = useState("");
  const matches = typed === next;
  const change = useMutation({
    mutationFn: () =>
      api.spaces.changeIdentifier(orgId, space.id, {
        version: space.version,
        prefix: next,
      }),
    onSuccess: (updated) => {
      putSpace(queryClient, orgId, updated);
      // 任务编号由前缀实时拼接，已缓存的任务都要重新拉取
      void queryClient.invalidateQueries({ queryKey: keys.tasks(orgId) });
      toast(
        t("spaces.toast.identifierChanged", { identifier: updated.prefix }),
      );
      onChanged(updated);
    },
    onError: (error) => {
      toast(
        isApiError(error, "IDENTIFIER_TAKEN")
          ? t("spaces.errors.identifierTaken")
          : spaceErrorMessage(t, error),
        { tone: "error" },
      );
      void queryClient.invalidateQueries({ queryKey: keys.spaces(orgId) });
    },
  });
  const submit = () => {
    if (matches && !change.isPending) change.mutate();
  };
  return (
    <Dialog
      open
      width={384}
      onClose={change.isPending ? undefined : onClose}
      title={t("spaces.identifier.confirmTitle")}
      description={t("spaces.identifier.confirmDescription", {
        old: space.prefix.toUpperCase(),
        next,
      })}
      footer={
        <>
          <Button
            variant="outline"
            onClick={onClose}
            disabled={change.isPending}
          >
            {t("common.cancel")}
          </Button>
          <Button
            onClick={submit}
            disabled={!matches}
            loading={change.isPending}
          >
            {change.isPending
              ? t("spaces.identifier.changing")
              : t("spaces.identifier.submit")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <code className="rounded-md bg-surface-muted px-2 py-1 font-mono text-body-sm text-fg-secondary">
          {`${space.prefix.toUpperCase()}-1234 -> ${next}-1234`}
        </code>
        <p className="text-body-sm text-fg-muted">
          {t("spaces.common.typeToConfirm", { value: next })}
        </p>
        <TextInput
          autoFocus
          aria-label={t("spaces.identifier.confirmInput")}
          placeholder={next}
          value={typed}
          onChange={(event) =>
            setTyped(normalizeIdentifier(event.target.value))
          }
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              submit();
            }
          }}
        />
      </div>
    </Dialog>
  );
}
