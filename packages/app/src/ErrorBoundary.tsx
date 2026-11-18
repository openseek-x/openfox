import { Component, type ReactNode } from "react";
import { useRouteError } from "react-router-dom";
import { I18nProvider, useT } from "@openfox/i18n";
import { Button } from "@openfox/ui";

export function AppErrorPage({
  error,
  reload = () => window.location.reload(),
}: {
  error: unknown;
  reload?: () => void;
}) {
  const t = useT();
  const chunk =
    error instanceof Error &&
    /ChunkLoadError|Loading chunk|dynamically imported module|Importing a module script/i.test(
      error.message,
    );
  return (
    <div
      role="alert"
      className="flex min-w-0 flex-1 flex-col items-center justify-center gap-3 p-6"
    >
      <p>{t(chunk ? "errors.chunkFailed" : "errors.renderFailed")}</p>
      <Button variant="outline" onClick={reload}>
        {t("errors.reload")}
      </Button>
    </div>
  );
}

/** 限制渲染错误的影响范围；父级以资源 ID 为 key，切换资源时恢复。 */
export class AppErrorBoundary extends Component<
  { children: ReactNode },
  { failed: boolean; error: unknown }
> {
  override state = { failed: false, error: null as unknown };
  static getDerivedStateFromError(error: unknown) {
    return { failed: true, error };
  }
  override render() {
    return this.state.failed ? (
      <AppErrorPage error={this.state.error} />
    ) : (
      this.props.children
    );
  }
}

export function RouteErrorPage() {
  const error = useRouteError();
  return (
    <I18nProvider>
      <AppErrorPage error={error} />
    </I18nProvider>
  );
}
