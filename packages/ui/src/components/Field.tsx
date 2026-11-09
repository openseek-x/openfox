import { createContext, useContext, useId, type ReactNode } from "react";

import { cn } from "../cn";

interface FieldContextValue {
  inputId: string;
  describedBy?: string;
  invalid: boolean;
}

const FieldContext = createContext<FieldContextValue | null>(null);

/** 控件读取所在 Field 的 id、描述和错误状态，用于关联 label 与 aria 属性。 */
export function useFieldContext(): FieldContextValue | null {
  return useContext(FieldContext);
}

export interface FieldProps {
  label?: ReactNode;
  /** 帮助文字；error 存在时显示 error */
  helper?: ReactNode;
  error?: ReactNode;
  required?: boolean;
  /** 标签颜色：登录页为 muted，表单弹窗为 default。 */
  labelTone?: "default" | "muted";
  className?: string;
  children: ReactNode;
}

/** 表单项：标签、控件和帮助或错误文字。 */
export function Field({
  label,
  helper,
  error,
  required,
  labelTone = "default",
  className,
  children,
}: FieldProps) {
  const inputId = useId();
  const messageId = useId();
  const message = error ?? helper;
  return (
    <FieldContext.Provider
      value={{
        inputId,
        describedBy: message ? messageId : undefined,
        invalid: Boolean(error),
      }}
    >
      <div className={cn("flex flex-col gap-2", className)}>
        {label && (
          <label
            htmlFor={inputId}
            className={cn(
              "text-body font-medium",
              labelTone === "muted" ? "text-fg-muted" : "text-fg-default",
            )}
          >
            {label}
            {required && (
              <span className="ml-0.5 text-danger" aria-hidden>
                *
              </span>
            )}
          </label>
        )}
        {children}
        {message && (
          <p
            id={messageId}
            className={cn(
              "text-caption",
              error ? "text-danger" : "text-fg-muted",
            )}
            role={error ? "alert" : undefined}
          >
            {message}
          </p>
        )}
      </div>
    </FieldContext.Provider>
  );
}
