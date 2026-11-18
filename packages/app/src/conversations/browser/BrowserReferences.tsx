import type { BrowserContext } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { useOpenArtifactList, BROWSER_PANEL } from "../artifacts/params";

const locations = new Map<
  string,
  BrowserContext["comments"][number]["anchor"]["live"]
>();
export function takeBrowserLocation(conversationId: string) {
  const value = locations.get(conversationId);
  locations.delete(conversationId);
  return value;
}
export function requestBrowserLocation(
  conversationId: string,
  location: BrowserContext["comments"][number]["anchor"]["live"],
  open: () => void,
) {
  locations.set(conversationId, location);
  if (locations.size > 100) locations.delete(locations.keys().next().value!);
  open();
  window.dispatchEvent(new Event("openfox:browser-locate"));
}
type Props = {
  contexts?: BrowserContext[];
  conversationId: string;
  onRemove?: (id: string) => void;
};
export function BrowserReferences(props: Props) {
  return props.contexts?.length ? (
    <References {...props} contexts={props.contexts} />
  ) : null;
}
function References({
  contexts,
  conversationId,
  onRemove,
}: Props & { contexts: BrowserContext[] }) {
  const t = useT(),
    open = useOpenArtifactList(BROWSER_PANEL);
  if (!contexts?.length) return null;
  return (
    <div className="space-y-2" aria-label={t("artifacts.browser.annotations")}>
      {contexts.map((context) => (
        <div
          key={context.id}
          className="rounded-lg border border-line-default px-3 py-2 text-body-sm"
        >
          <div className="truncate font-medium" title={context.source_url}>
            {context.title || context.source_url}
          </div>
          {context.comments.map((comment) => (
            <div key={comment.id} className="mt-2 space-y-1">
              <blockquote className="line-clamp-2 whitespace-pre-wrap break-words border-l-2 border-line-default pl-2 text-fg-secondary">
                {comment.anchor.snapshot.quote || t("artifacts.browser.region")}
              </blockquote>
              <p className="whitespace-pre-wrap break-words">{comment.body}</p>
              <div className="flex gap-3 text-caption text-fg-secondary">
                <button
                  type="button"
                  onClick={() => {
                    requestBrowserLocation(
                      conversationId,
                      comment.anchor.live,
                      open,
                    );
                  }}
                >
                  {t("artifacts.browser.locate")}
                </button>
                {onRemove && (
                  <button type="button" onClick={() => onRemove(comment.id)}>
                    {t("artifacts.review.removeComment")}
                  </button>
                )}
              </div>
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}
