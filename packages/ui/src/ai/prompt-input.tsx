import {
  createContext,
  forwardRef,
  useContext,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  type ComponentProps,
} from "react";

import { cn } from "../cn";

const PromptInputContext = createContext({
  value: "",
  onValueChange: (_value: string) => {},
  maxHeight: 240,
  disabled: false,
});

export const PromptInput = forwardRef<
  HTMLDivElement,
  Omit<ComponentProps<"div">, "onChange"> & {
    value: string;
    onValueChange: (value: string) => void;
    maxHeight?: number;
    disabled?: boolean;
  }
>(function PromptInput(
  {
    value,
    onValueChange,
    maxHeight = 240,
    disabled = false,
    className,
    children,
    ...props
  },
  ref,
) {
  return (
    <PromptInputContext.Provider
      value={{ value, onValueChange, maxHeight, disabled }}
    >
      <div
        {...props}
        ref={ref}
        data-slot="prompt-input"
        className={cn(
          "@container/prompt flex min-w-0 flex-col gap-1 rounded-[18px] border border-line-default bg-surface-card p-3 shadow-sm transition-colors focus-within:border-line-focus",
          disabled && "bg-surface-muted",
          className,
        )}
      >
        {children}
      </div>
    </PromptInputContext.Provider>
  );
});

export const PromptInputTextarea = forwardRef<
  HTMLTextAreaElement,
  Omit<ComponentProps<"textarea">, "value" | "defaultValue">
>(function PromptInputTextarea(
  { className, onChange, style, disabled, ...props },
  ref,
) {
  const context = useContext(PromptInputContext);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  useImperativeHandle(ref, () => textareaRef.current!, []);

  useLayoutEffect(() => {
    const element = textareaRef.current;
    if (!element) return;
    const resize = () => {
      element.style.height = "auto";
      element.style.height = `${Math.min(element.scrollHeight, context.maxHeight)}px`;
    };
    resize();
    if (typeof ResizeObserver === "undefined") return;
    let width = element.clientWidth;
    const observer = new ResizeObserver(() => {
      if (element.clientWidth === width) return;
      width = element.clientWidth;
      resize();
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, [context.value, context.maxHeight]);

  return (
    <textarea
      {...props}
      ref={textareaRef}
      data-slot="prompt-input-textarea"
      value={context.value}
      disabled={disabled ?? context.disabled}
      onChange={(event) => {
        context.onValueChange(event.target.value);
        onChange?.(event);
      }}
      style={{ ...style, maxHeight: context.maxHeight }}
      className={cn(
        "w-full resize-none overflow-y-auto border-0 bg-transparent text-body leading-6 text-fg-default outline-none placeholder:text-fg-subtle focus-visible:shadow-none disabled:cursor-not-allowed",
        className,
      )}
    />
  );
});

export function PromptInputActions({
  className,
  ...props
}: ComponentProps<"div">) {
  return (
    <div
      {...props}
      data-slot="prompt-input-actions"
      className={cn("flex min-w-0 items-center gap-1", className)}
    />
  );
}
