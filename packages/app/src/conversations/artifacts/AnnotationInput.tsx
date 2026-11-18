import { useEffect, useLayoutEffect, useRef } from "react";
import { Icon, IconButton } from "@openfox/ui";

/** 网页、文件选区共用的紧凑输入；提交仅加入对话草稿。 */
export function AnnotationInput({
  value,
  onChange,
  onSubmit,
  onClose,
  label,
  placeholder,
  closeLabel,
  submitLabel,
  error,
}: {
  value: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  onClose: () => void;
  label: string;
  placeholder: string;
  closeLabel: string;
  submitLabel: string;
  error?: string;
}) {
  const input = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const frame = requestAnimationFrame(() =>
      input.current?.focus({ preventScroll: true }),
    );
    return () => cancelAnimationFrame(frame);
  }, []);
  useLayoutEffect(() => {
    const field = input.current;
    if (!field) return;
    const resize = () => {
      field.style.height = "0px";
      field.style.height = `${Math.max(24, Math.min(field.scrollHeight, 120))}px`;
    };
    resize();
    let width = field.clientWidth;
    const observer =
      typeof ResizeObserver === "undefined"
        ? null
        : new ResizeObserver(() => {
            if (field.clientWidth === width) return;
            width = field.clientWidth;
            resize();
          });
    observer?.observe(field);
    return () => observer?.disconnect();
  }, [value]);
  return (
    <>
      <div className="flex items-end gap-1">
        <IconButton
          size="28"
          label={closeLabel}
          icon={<Icon name="x" size={16} />}
          className="rounded-full!"
          onClick={onClose}
        />
        <textarea
          ref={input}
          aria-label={label}
          placeholder={placeholder}
          value={value}
          rows={1}
          maxLength={8192}
          className="my-0.5 min-h-6 min-w-0 flex-1 resize-none border-0 bg-transparent px-1 py-0 text-fg-default leading-6 outline-none focus-visible:shadow-none placeholder:text-fg-muted"
          onChange={(event) => onChange(event.target.value)}
          onKeyDown={(event) => {
            if (
              event.key === "Enter" &&
              !event.shiftKey &&
              !event.nativeEvent.isComposing &&
              event.keyCode !== 229
            ) {
              event.preventDefault();
              onSubmit();
            }
          }}
        />
        <IconButton
          size="28"
          label={submitLabel}
          icon={<Icon name="arrow-up" size={16} />}
          disabled={!value.trim()}
          onClick={onSubmit}
          className="rounded-full! bg-fg-default! text-surface-card! hover:opacity-80"
        />
      </div>
      {error && (
        <p role="alert" className="px-2 pb-1 pt-2 text-caption text-danger">
          {error}
        </p>
      )}
    </>
  );
}
