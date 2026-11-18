import { useRef, useState } from "react";

import { isApiError } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Button, Callout, Dialog, Spinner } from "@openfox/ui";

import { useApi } from "../context";

type Phase = "idle" | "preparing" | "waiting" | "decrypting" | "done";

const bytesToBase64Url = (bytes: Uint8Array) => {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary)
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replace(/=+$/, "");
};

const base64UrlToBytes = (value: string) => {
  const base64 = value.replaceAll("-", "+").replaceAll("_", "/");
  const binary = atob(base64 + "=".repeat((4 - (base64.length % 4)) % 4));
  return Uint8Array.from(binary, (char) => char.charCodeAt(0));
};

const wait = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    if (signal.aborted) {
      reject(signal.reason ?? new DOMException("Aborted", "AbortError"));
      return;
    }
    const onAbort = () => {
      clearTimeout(timer);
      signal.removeEventListener("abort", onAbort);
      reject(signal.reason ?? new DOMException("Aborted", "AbortError"));
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal.addEventListener("abort", onAbort, { once: true });
  });

export function RuntimeSessionExportDialog({
  orgId,
  conversationId,
  onClose,
}: {
  orgId: string;
  conversationId: string;
  onClose: () => void;
}) {
  const api = useApi();
  const t = useT();
  const [phase, setPhase] = useState<Phase>("idle");
  const [error, setError] = useState("");
  const controller = useRef<AbortController | null>(null);

  const close = () => {
    controller.current?.abort();
    controller.current = null;
    onClose();
  };

  class RuntimeExportError extends Error {}

  const exportSession = async () => {
    const abort = new AbortController();
    controller.current?.abort();
    controller.current = abort;
    setError("");
    setPhase("preparing");
    try {
      const keyPair = (await crypto.subtle.generateKey(
        {
          name: "RSA-OAEP",
          modulusLength: 3072,
          publicExponent: new Uint8Array([1, 0, 1]),
          hash: "SHA-256",
        },
        false,
        ["encrypt", "decrypt"],
      )) as CryptoKeyPair;
      const publicKey = bytesToBase64Url(
        new Uint8Array(
          await crypto.subtle.exportKey("spki", keyPair.publicKey),
        ),
      );
      const request = await api.runtimeSessionExports.create(
        orgId,
        conversationId,
        publicKey,
      );
      setPhase("waiting");

      // A command may spend two minutes queued and up to three minutes running.
      const deadline = Date.now() + 330_000;
      let result = request;
      while (result.status === "PENDING" && Date.now() < deadline) {
        await wait(1000, abort.signal);
        result = await api.runtimeSessionExports.get(
          orgId,
          conversationId,
          request.id,
          abort.signal,
        );
      }
      if (result.status === "FAILED") {
        if (result.error_code === "SESSION_UNAVAILABLE")
          throw new RuntimeExportError(
            t("chat.assistant.runtimeExportSessionMissing"),
          );
        throw new RuntimeExportError(t("chat.assistant.runtimeExportFailed"));
      }
      if (result.status !== "READY" || !result.wrapped_key)
        throw new RuntimeExportError(t("chat.assistant.runtimeExportTimedOut"));

      setPhase("decrypting");
      const encryptedBlob = await api.runtimeSessionExports.content(
        orgId,
        conversationId,
        request.id,
        abort.signal,
      );
      const wrappedKey = await crypto.subtle.decrypt(
        {
          name: "RSA-OAEP",
          label: new TextEncoder().encode("openfox-runtime-session-v1"),
        },
        keyPair.privateKey,
        base64UrlToBytes(result.wrapped_key),
      );
      const contentKey = await crypto.subtle.importKey(
        "raw",
        wrappedKey,
        { name: "AES-GCM" },
        false,
        ["decrypt"],
      );
      const encrypted = new Uint8Array(await encryptedBlob.arrayBuffer());
      if (encrypted.byteLength <= 28)
        throw new RuntimeExportError(t("chat.assistant.runtimeExportFailed"));
      const archive = await crypto.subtle.decrypt(
        { name: "AES-GCM", iv: encrypted.slice(0, 12) },
        contentKey,
        encrypted.slice(12),
      );
      const url = URL.createObjectURL(
        new Blob([archive], { type: "application/zip" }),
      );
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = `openfox-runtime-session-${result.runtime}.zip`;
      anchor.click();
      setTimeout(() => URL.revokeObjectURL(url), 30_000);
      setPhase("done");
    } catch (cause) {
      if (abort.signal.aborted) return;
      if (isApiError(cause, "SESSION_NOT_EXPORTABLE"))
        setError(t("chat.assistant.runtimeExportNoSession"));
      else if (isApiError(cause, "EXPORT_DEVICE_UNAVAILABLE"))
        setError(t("chat.assistant.runtimeExportDeviceUnavailable"));
      else if (isApiError(cause, "EXPORT_BUSY"))
        setError(t("chat.assistant.runtimeExportBusy"));
      else if (cause instanceof RuntimeExportError) setError(cause.message);
      else if (isApiError(cause))
        setError(t("chat.assistant.runtimeExportFailed"));
      else if (cause instanceof Error && cause.message)
        setError(t("chat.assistant.runtimeExportFailed"));
      else setError(t("chat.assistant.runtimeExportFailed"));
      setPhase("idle");
    } finally {
      if (controller.current === abort) controller.current = null;
    }
  };

  const busy =
    phase === "preparing" || phase === "waiting" || phase === "decrypting";
  const progressKey =
    phase === "preparing"
      ? "chat.assistant.runtimeExportPreparing"
      : phase === "waiting"
        ? "chat.assistant.runtimeExportWaiting"
        : "chat.assistant.runtimeExportDecrypting";

  return (
    <Dialog
      open
      title={t("chat.assistant.runtimeExportTitle")}
      width={520}
      onClose={close}
    >
      <div className="grid gap-4">
        <p className="text-body-sm text-fg-secondary">
          {t("chat.assistant.runtimeExportDescription")}
        </p>
        <Callout tone="warning">
          {t("chat.assistant.runtimeExportSensitive")}
        </Callout>
        {error && <Callout tone="danger">{error}</Callout>}
        {busy && (
          <div
            role="status"
            className="flex items-center gap-2 text-body-sm text-fg-secondary"
          >
            <Spinner />
            {t(progressKey)}
          </div>
        )}
        {phase === "done" && (
          <Callout tone="success">
            {t("chat.assistant.runtimeExportReady")}
          </Callout>
        )}
        <div className="flex justify-end gap-2">
          {busy ? (
            <Button variant="outline" onClick={close}>
              {t("common.cancel")}
            </Button>
          ) : phase === "done" ? (
            <Button onClick={close}>
              {t("chat.assistant.runtimeExportClose")}
            </Button>
          ) : (
            <>
              <Button variant="outline" onClick={close}>
                {t("common.cancel")}
              </Button>
              <Button onClick={() => void exportSession()}>
                {t("chat.assistant.runtimeExportStart")}
              </Button>
            </>
          )}
        </div>
      </div>
    </Dialog>
  );
}
