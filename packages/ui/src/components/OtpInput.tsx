import { useRef, type ClipboardEvent, type KeyboardEvent } from "react";

import { useT } from "@openfox/i18n";

import { cn } from "../cn";

export interface OtpInputProps {
  value: string;
  onChange: (value: string) => void;
  length?: number;
  invalid?: boolean;
  disabled?: boolean;
  /** 输满时触发 */
  onComplete?: (value: string) => void;
  autoFocus?: boolean;
}

/** 验证码输入：逐格输入、自动跳格、退格回退、支持整段粘贴。 */
export function OtpInput({
  value,
  onChange,
  length = 6,
  invalid = false,
  disabled = false,
  onComplete,
  autoFocus = false,
}: OtpInputProps) {
  const t = useT();
  const refs = useRef<Array<HTMLInputElement | null>>([]);
  const digits = Array.from({ length }, (_, i) => value[i] ?? "");

  const commit = (next: string) => {
    const clean = next.replace(/\D/g, "").slice(0, length);
    onChange(clean);
    if (clean.length === length) onComplete?.(clean);
    return clean;
  };

  const onInput = (index: number, raw: string) => {
    const digit = raw.replace(/\D/g, "").slice(-1);
    if (!digit) return;
    const chars = digits.slice();
    chars[index] = digit;
    const clean = commit(chars.join(""));
    refs.current[Math.min(clean.length, length - 1)]?.focus();
  };

  const onKeyDown = (index: number, event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Backspace") {
      event.preventDefault();
      const chars = digits.slice();
      if (chars[index]) chars[index] = "";
      else if (index > 0) {
        chars[index - 1] = "";
        refs.current[index - 1]?.focus();
      }
      commit(chars.join(""));
    } else if (event.key === "ArrowLeft" && index > 0)
      refs.current[index - 1]?.focus();
    else if (event.key === "ArrowRight" && index < length - 1)
      refs.current[index + 1]?.focus();
  };

  const onPaste = (event: ClipboardEvent<HTMLInputElement>) => {
    event.preventDefault();
    const clean = commit(event.clipboardData.getData("text"));
    refs.current[Math.min(clean.length, length - 1)]?.focus();
  };

  return (
    <div className="flex gap-2" role="group" aria-label={t("ui.otpGroup")}>
      {digits.map((digit, index) => (
        <input
          key={index}
          ref={(node) => {
            refs.current[index] = node;
          }}
          value={digit}
          inputMode="numeric"
          autoComplete={index === 0 ? "one-time-code" : "off"}
          maxLength={1}
          disabled={disabled}
          autoFocus={autoFocus && index === 0}
          aria-label={t("ui.otpDigit", { n: index + 1 })}
          aria-invalid={invalid || undefined}
          onChange={(event) => onInput(index, event.target.value)}
          onKeyDown={(event) => onKeyDown(index, event)}
          onPaste={onPaste}
          onFocus={(event) => event.target.select()}
          className={cn(
            "h-14 w-12 rounded-lg border bg-surface-card text-center font-[Inter,var(--font-sans)] text-2xl font-semibold outline-none",
            invalid
              ? "border-danger text-danger"
              : "border-line-default text-fg-default",
            "focus:border-line-focus focus:shadow-focus disabled:opacity-50",
          )}
        />
      ))}
    </div>
  );
}
