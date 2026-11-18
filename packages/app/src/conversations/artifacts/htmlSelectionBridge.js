// Trusted selection bridge, emitted as a local hashed asset. Artifact scripts are removed.
(() => {
  const session = document.currentScript.nonce;
  let enabled = false;
  let selected = null,
    oldStyle = null;
  const highlight = (element) => {
    if (selected) {
      if (oldStyle === null) selected.removeAttribute("style");
      else selected.setAttribute("style", oldStyle);
    }
    selected = element;
    oldStyle = element.getAttribute("style");
    element.style.setProperty("outline", "3px solid #2563eb", "important");
    element.style.setProperty("outline-offset", "2px", "important");
  };
  const choose = (event) => {
    if (!enabled) return;
    if (event.type === "keydown" && event.key !== "Enter" && event.key !== " ")
      return;
    if (!getSelection().isCollapsed) return;
    const element =
      event.target instanceof Element
        ? event.target.closest("[data-openfox-review]")
        : null;
    if (!element) return;
    event.preventDefault();
    event.stopPropagation();
    highlight(element);
    const box = element.getBoundingClientRect();
    parent.postMessage(
      {
        type: "openfox-html-select",
        session,
        id: element.getAttribute("data-openfox-review"),
        rect: {
          left: box.left,
          top: box.top,
          width: box.width,
          height: box.height,
        },
      },
      "*",
    );
  };
  const path = (node) => {
    const parts = [];
    while (node && node !== document.documentElement) {
      const p = node.parentNode;
      if (!p) return null;
      parts.unshift(Array.prototype.indexOf.call(p.childNodes, node));
      node = p;
    }
    return node ? parts.join(".") : null;
  };
  const selectText = () => {
    if (!enabled) return;
    const selection = getSelection();
    if (!selection || selection.isCollapsed || selection.rangeCount !== 1)
      return;
    const r = selection.getRangeAt(0),
      box = r.getBoundingClientRect();
    parent.postMessage(
      {
        type: "openfox-html-text",
        session,
        start: { path: path(r.startContainer), offset: r.startOffset },
        end: { path: path(r.endContainer), offset: r.endOffset },
        rect: {
          left: box.left,
          top: box.top,
          width: box.width,
          height: box.height,
        },
      },
      "*",
    );
  };
  document.addEventListener("mouseup", selectText, true);
  document.addEventListener(
    "keyup",
    (event) => {
      if (event.key === "Shift" || event.shiftKey) selectText();
    },
    true,
  );
  document.addEventListener(
    "click",
    (event) => {
      if (event.target instanceof HTMLImageElement) choose(event);
    },
    true,
  );
  document.addEventListener("keydown", choose, true);
  addEventListener(
    "scroll",
    () =>
      parent.postMessage(
        { type: "openfox-html-scroll", session, x: scrollX, y: scrollY },
        "*",
      ),
    { passive: true },
  );
  addEventListener("message", (event) => {
    const m = event.data;
    if (
      event.source === parent &&
      m?.session === session &&
      m.type === "openfox-html-restore-scroll" &&
      [m.x, m.y].every((n) => Number.isFinite(n) && n >= 0 && n <= 10_000_000)
    ) {
      scrollTo(m.x, m.y);
      return;
    }
    if (
      event.source === parent &&
      m?.session === session &&
      m.type === "openfox-html-mode"
    ) {
      enabled = m.enabled === true;
      if (!enabled && selected) {
        if (oldStyle === null) selected.removeAttribute("style");
        else selected.setAttribute("style", oldStyle);
        selected = null;
      }
      return;
    }
    if (
      !enabled ||
      event.source !== parent ||
      !m ||
      m.type !== "openfox-html-locate" ||
      m.session !== session ||
      typeof m.id !== "string" ||
      !/^e[0-9]{1,5}$/.test(m.id)
    )
      return;
    const element = document.querySelector(
      '[data-openfox-review="' + m.id + '"]',
    );
    if (element) {
      highlight(element);
      element.scrollIntoView({ block: "center" });
      element.focus({ preventScroll: true });
    }
  });
})();
