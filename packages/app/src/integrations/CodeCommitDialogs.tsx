import { useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  isApiError,
  type CodeCommitBridgeResult,
  type CodeCommitConfiguration,
  type CodeCommitJob,
  type CodeCommitRepository,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Checkbox,
  Dialog,
  Field,
  Select,
  TextInput,
} from "@openfox/ui";
import { keys, useApi } from "../context";
import { codecommitError } from "./codecommit-model";

// Secret-bearing writes deliberately bypass React Query's mutation cache.
// A closed dialog aborts its request and cannot display a late one-time token.
function useDialogWrite(refresh: () => void) {
  const active = useRef<AbortController | null>(null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(),
    [conflict, setConflict] = useState(false);
  useEffect(
    () => () => {
      active.current?.abort();
      active.current = null;
    },
    [],
  );
  async function run<T>(
    request: (signal: AbortSignal) => Promise<T>,
    done: (value: T) => void,
  ) {
    if (active.current || conflict) return;
    const controller = new AbortController();
    active.current = controller;
    setBusy(true);
    setError(undefined);
    try {
      const value = await request(controller.signal);
      if (active.current === controller && !controller.signal.aborted) {
        done(value);
        refresh();
      }
    } catch (e) {
      if (active.current === controller && !controller.signal.aborted) {
        setError(e);
        if (isApiError(e) && e.status === 409) {
          setConflict(true);
          refresh();
        }
      }
    } finally {
      if (active.current === controller) {
        active.current = null;
        setBusy(false);
      }
    }
  }
  return { run, busy, error, conflict };
}

export function CodeCommitCredentialsDialog({
  orgId,
  config,
  refresh,
  onClose,
}: {
  orgId: string;
  config?: CodeCommitConfiguration;
  refresh: () => void;
  onClose: () => void;
}) {
  const api = useApi(),
    t = useT(),
    write = useDialogWrite(refresh);
  const [region, setRegion] = useState(config?.region ?? ""),
    [access, setAccess] = useState(""),
    [secret, setSecret] = useState(""),
    [session, setSession] = useState("");
  const regions = useQuery({
    queryKey: [...keys.integrations(orgId), "codecommit-regions"],
    queryFn: ({ signal }) => api.codecommit.regions(orgId, signal),
  });
  const valid =
    regions.data?.regions.includes(region) &&
    /^[A-Z0-9]{16,128}$/.test(access) &&
    /^[\x21-\x7e]{1,4096}$/.test(secret) &&
    (!session || /^[\x21-\x7e]{1,12288}$/.test(session)) &&
    access.length + secret.length + session.length <= 16384;
  function clearAndClose() {
    setAccess("");
    setSecret("");
    setSession("");
    onClose();
  }
  function save() {
    if (!valid) return;
    const input = {
      region,
      access_key_id: access,
      secret_access_key: secret,
      session_token: session,
      version: config?.connection.version ?? 0,
    };
    void write.run(
      (signal) =>
        config
          ? api.codecommit.credentials(
              orgId,
              config.connection.id,
              input,
              signal,
            )
          : api.codecommit.create(orgId, input, signal),
      clearAndClose,
    );
  }
  return (
    <Dialog
      open
      title={t(
        config
          ? config.connection.status === "DISCONNECTED"
            ? "integrations.codecommit.reconnect"
            : "integrations.codecommit.credentials"
          : "integrations.codecommit.bind",
      )}
      onClose={clearAndClose}
      footer={
        <>
          <Button variant="outline" onClick={clearAndClose}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={write.busy}
            disabled={!valid || write.conflict}
            onClick={save}
          >
            {t("integrations.codecommit.verify")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Callout tone="info">
          {t("integrations.codecommit.credentialsHelp")}
        </Callout>
        {config && (
          <Callout tone="warning">
            {t("integrations.codecommit.replaceHelp")}
            <p>
              {t("integrations.codecommit.account")}: {config.account} ·{" "}
              {config.region}
            </p>
          </Callout>
        )}
        {regions.isError && (
          <Callout tone="danger">
            {t("integrations.codecommit.loadFailed")}
          </Callout>
        )}
        {write.error !== undefined && (
          <Callout tone="danger">{codecommitError(t, write.error)}</Callout>
        )}
        <Field label={t("integrations.codecommit.region")}>
          <Select
            value={region || null}
            options={(regions.data?.regions ?? []).map((value) => ({
              value,
              label: value,
            }))}
            onChange={setRegion}
            disabled={Boolean(config) || write.busy}
          />
        </Field>
        <Field label={t("integrations.codecommit.accessKey")}>
          <TextInput
            type="password"
            autoComplete="off"
            spellCheck={false}
            value={access}
            disabled={write.busy}
            onChange={(e) => setAccess(e.target.value)}
          />
        </Field>
        <Field label={t("integrations.codecommit.secretKey")}>
          <TextInput
            type="password"
            autoComplete="off"
            spellCheck={false}
            value={secret}
            disabled={write.busy}
            onChange={(e) => setSecret(e.target.value)}
          />
        </Field>
        <Field label={t("integrations.codecommit.sessionToken")}>
          <TextInput
            type="password"
            autoComplete="off"
            spellCheck={false}
            value={session}
            disabled={write.busy}
            onChange={(e) => setSession(e.target.value)}
          />
        </Field>
      </div>
    </Dialog>
  );
}

function BridgeResult({ result }: { result: CodeCommitBridgeResult }) {
  const t = useT(),
    [copied, setCopied] = useState(false),
    [failed, setFailed] = useState(false);
  return (
    <div className="flex flex-col gap-4">
      <Callout tone="warning">
        {t("integrations.codecommit.bridgeOnce")}
      </Callout>
      <Field label={t("integrations.codecommit.bridgeURL")}>
        <TextInput
          value={result.url}
          readOnly
          autoComplete="off"
          spellCheck={false}
        />
      </Field>
      <Field label={t("integrations.codecommit.bridgeToken")}>
        <TextInput
          type="password"
          value={result.token}
          readOnly
          autoComplete="off"
          spellCheck={false}
        />
      </Field>
      <p className="text-body-sm text-fg-muted">
        {t("integrations.codecommit.bridgeHelp")}
      </p>
      {failed && (
        <Callout tone="danger">
          {t("integrations.codecommit.copyFailed")}
        </Callout>
      )}
      <Button
        variant="outline"
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(
              `${result.url}\nAuthorization: Bearer ${result.token}`,
            );
            setCopied(true);
            setFailed(false);
          } catch {
            setFailed(true);
          }
        }}
      >
        {t(
          copied
            ? "integrations.codecommit.copied"
            : "integrations.codecommit.copy",
        )}
      </Button>
    </div>
  );
}

export function CodeCommitRepositoryDialog({
  orgId,
  config,
  repository,
  action = "enable",
  refresh,
  onClose,
}: {
  orgId: string;
  config: CodeCommitConfiguration;
  repository?: CodeCommitRepository;
  action?: "enable" | "rotate" | "disable" | "reconcile";
  refresh: () => void;
  onClose: () => void;
}) {
  const api = useApi(),
    t = useT(),
    write = useDialogWrite(refresh);
  const [cursor, setCursor] = useState(""),
    [name, setName] = useState(repository?.name ?? ""),
    [confirmed, setConfirmed] = useState(false);
  const [result, setResult] = useState<CodeCommitBridgeResult | null>(null);
  const remote = useQuery({
    queryKey: [
      ...keys.integrations(orgId),
      "codecommit-remote",
      config.connection.id,
      config.connection.version,
      cursor,
    ],
    queryFn: ({ signal }) =>
      api.codecommit.repositories(orgId, config.connection.id, cursor, signal),
    enabled: !repository && action === "enable",
    retry: false,
  });
  const refreshedMismatch = useRef(false);
  useEffect(() => {
    const changed =
      remote.data &&
      remote.data.connection_version !== config.connection.version;
    if (
      !refreshedMismatch.current &&
      (changed || isApiError(remote.error, "VERSION_CONFLICT"))
    ) {
      refreshedMismatch.current = true;
      refresh();
    }
  }, [
    remote.data?.connection_version,
    remote.error,
    config.connection.version,
    refresh,
  ]);
  const valid =
    action !== "enable" ||
    (confirmed &&
      (repository
        ? true
        : remote.isSuccess &&
          remote.data.connection_version === config.connection.version &&
          remote.data.repositories.some((item) => item.name === name)));
  function close() {
    setResult(null);
    onClose();
  }
  function submit() {
    if (!valid) return;
    const input = {
      connection_version: config.connection.version,
      version: repository?.version ?? 0,
    };
    void write.run(
      (signal) =>
        action === "enable"
          ? api.codecommit.enable(
              orgId,
              config.connection.id,
              { ...input, name, confirm_initialization: true },
              signal,
            )
          : action === "rotate"
            ? api.codecommit.rotate(
                orgId,
                config.connection.id,
                repository!.id,
                input,
                signal,
              )
            : action === "reconcile"
              ? api.codecommit.reconcileRepository(
                  orgId,
                  config.connection.id,
                  repository!.id,
                  input,
                  signal,
                )
              : api.codecommit.disable(
                  orgId,
                  config.connection.id,
                  repository!.id,
                  input,
                  signal,
                ),
      (value) => {
        if (action === "disable" || action === "reconcile") close();
        else setResult(value);
      },
    );
  }
  const title = result
    ? "bridgeTitle"
    : action === "rotate"
      ? "rotate"
      : action === "reconcile"
        ? "reconcile"
        : action === "disable"
          ? "disable"
          : repository
            ? "reenable"
            : "addRepository";
  return (
    <Dialog
      open
      title={t(`integrations.codecommit.${title}`)}
      onClose={close}
      footer={
        result ? (
          <Button onClick={close}>{t("common.close")}</Button>
        ) : (
          <>
            <Button variant="outline" onClick={close}>
              {t("common.cancel")}
            </Button>
            <Button
              loading={write.busy}
              disabled={
                !valid || write.conflict || (!repository && remote.isError)
              }
              variant={action === "disable" ? "danger" : "primary"}
              onClick={submit}
            >
              {t(
                `integrations.codecommit.${action === "enable" ? "initialize" : action}`,
              )}
            </Button>
          </>
        )
      }
    >
      {result ? (
        <BridgeResult result={result} />
      ) : (
        <div className="flex flex-col gap-4">
          {write.error !== undefined && (
            <Callout tone="danger">
              {action === "reconcile" &&
              isApiError(write.error, "VERSION_CONFLICT")
                ? t("integrations.codecommit.reconcileConflict")
                : codecommitError(t, write.error)}
            </Callout>
          )}
          {repository && (
            <p className="break-all text-body">
              {repository.name}
              <br />
              {repository.arn}
            </p>
          )}
          <Callout tone={action === "enable" ? "info" : "warning"}>
            {t(
              `integrations.codecommit.${action === "enable" ? "initializationHelp" : action === "rotate" ? "rotateHelp" : action === "reconcile" ? "reconcileHelp" : "disableHelp"}`,
            )}
          </Callout>
          {action === "enable" && (
            <>
              <p className="text-body-sm text-fg-muted">
                {t("integrations.codecommit.repositoryHelp")}
              </p>
              {!repository && (
                <>
                  {remote.isError && (
                    <Callout tone="danger">
                      {codecommitError(t, remote.error)}
                    </Callout>
                  )}
                  {remote.data &&
                    remote.data.connection_version !==
                      config.connection.version && (
                      <Callout tone="warning">
                        {t("integrations.codecommit.conflict")}
                      </Callout>
                    )}
                  <Field label={t("integrations.codecommit.repository")}>
                    <Select
                      value={name || null}
                      disabled={write.busy || remote.isFetching}
                      placeholder={t(
                        "integrations.codecommit.selectRepository",
                      )}
                      options={(remote.data?.repositories ?? []).map(
                        (item) => ({ value: item.name, label: item.name }),
                      )}
                      onChange={setName}
                    />
                  </Field>
                  {remote.data?.repositories.length === 0 && (
                    <p>{t("integrations.codecommit.noRemote")}</p>
                  )}
                  <div className="flex gap-2">
                    {cursor && (
                      <Button
                        variant="outline"
                        disabled={write.busy || remote.isFetching}
                        onClick={() => {
                          setCursor("");
                          setName("");
                        }}
                      >
                        {t("integrations.codecommit.first")}
                      </Button>
                    )}
                    {remote.data?.next_cursor && (
                      <Button
                        variant="outline"
                        disabled={write.busy || remote.isFetching}
                        onClick={() => {
                          setCursor(remote.data!.next_cursor);
                          setName("");
                        }}
                      >
                        {t("integrations.codecommit.next")}
                      </Button>
                    )}
                  </div>
                </>
              )}
              <label className="flex items-start gap-2 text-body-sm">
                <Checkbox
                  checked={confirmed}
                  disabled={write.busy}
                  onChange={(e) => setConfirmed(e.target.checked)}
                />
                {t("integrations.codecommit.initializationConfirm")}
              </label>
            </>
          )}
        </div>
      )}
    </Dialog>
  );
}

export function CodeCommitReconcileDialog({
  orgId,
  config,
  job,
  refresh,
  onClose,
}: {
  orgId: string;
  config: CodeCommitConfiguration;
  job: CodeCommitJob;
  refresh: () => void;
  onClose: () => void;
}) {
  const api = useApi(),
    t = useT(),
    write = useDialogWrite(refresh),
    [confirmed, setConfirmed] = useState(false);
  const unknown = job.backlink_state === "UNKNOWN";
  return (
    <Dialog
      open
      title={t(
        unknown
          ? "integrations.codecommit.unknown"
          : "integrations.codecommit.retry",
      )}
      description={t(
        unknown
          ? "integrations.codecommit.unknownHelp"
          : "integrations.codecommit.retryHelp",
      )}
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={write.busy}
            disabled={
              !job.can_reconcile || write.conflict || (unknown && !confirmed)
            }
            onClick={() =>
              void write.run(
                (signal) =>
                  api.codecommit.reconcile(
                    orgId,
                    config.connection.id,
                    job.id,
                    {
                      connection_version: config.connection.version,
                      version: job.version,
                      confirm_resume_unknown: unknown && confirmed,
                    },
                    signal,
                  ),
                onClose,
              )
            }
          >
            {t(
              unknown
                ? "integrations.codecommit.unknown"
                : "integrations.codecommit.retry",
            )}
          </Button>
        </>
      }
    >
      {write.error !== undefined && (
        <Callout tone="danger">{codecommitError(t, write.error)}</Callout>
      )}
      {unknown && (
        <label className="flex items-start gap-2 text-body-sm">
          <Checkbox
            checked={confirmed}
            disabled={write.busy}
            onChange={(e) => setConfirmed(e.target.checked)}
          />
          {t("integrations.codecommit.unknownConfirm")}
        </label>
      )}
    </Dialog>
  );
}
