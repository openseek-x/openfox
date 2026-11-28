import { sanitizePageUrl, type PageReference } from "./pageRef";

/** 只在勾选引用后读取当前主框架。导航时标题、地址和选区必须来自同一文档。 */
export async function readCurrentPage(): Promise<PageReference | null> {
  try {
    const [tab] = await chrome.tabs.query({
      active: true,
      currentWindow: true,
    });
    if (tab?.id === undefined) return null;
    const [snapshot] = await chrome.scripting.executeScript({
      target: { tabId: tab.id },
      func: () => ({
        title: document.title,
        url: location.href,
        selection: String(window.getSelection() ?? ""),
      }),
    });
    const page = snapshot?.result;
    return page && sanitizePageUrl(page.url) ? page : null;
  } catch {
    return null;
  }
}
