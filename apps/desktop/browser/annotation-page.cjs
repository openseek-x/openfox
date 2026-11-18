const { randomUUID } = require("node:crypto");
const { failure } = require("./policy.cjs");

// Fixed, read-only page scripts. Nothing supplied by the website or renderer is
// evaluated as code. Form values and protected controls are never extracted.
function readSelection(kind, start, end) {
  if (!this.isConnected) return null;
  const doc = this.ownerDocument || this;
  const win = doc.defaultView;
  const sensitive =
    'input,textarea,select,[contenteditable="true"],[autocomplete="one-time-code"],[id*="captcha" i],[class*="captcha" i]';
  const protectedNode = (node) => {
    for (let depth = 0; node && depth < 64; depth++) {
      const element = node.nodeType === 1 ? node : node.parentElement;
      if (element?.closest(sensitive)) return true;
      node = node.getRootNode()?.host;
    }
    return false;
  };
  let element = this.nodeType === 1 ? this : this.parentElement;
  if (kind === "region") element = doc.documentElement;
  if (
    !element ||
    (kind !== "region" &&
      (protectedNode(element) || element.querySelector(sensitive)))
  )
    return null;
  let rect,
    quote = "",
    prefix = "",
    suffix = "";
  if (kind === "text") {
    const caret = (p) => {
      if (doc.caretRangeFromPoint) return doc.caretRangeFromPoint(p.x, p.y);
      const c = doc.caretPositionFromPoint(p.x, p.y);
      if (!c) return null;
      const r = doc.createRange();
      r.setStart(c.offsetNode, c.offset);
      r.collapse(true);
      return r;
    };
    const a = caret(start),
      b = caret(end);
    if (!a || !b) return null;
    const r = doc.createRange();
    const first = a.compareBoundaryPoints(0, b) <= 0 ? a : b;
    const last = first === a ? b : a;
    r.setStart(first.startContainer, first.startOffset);
    r.setEnd(last.startContainer, last.startOffset);
    const common =
      r.commonAncestorContainer.nodeType === 1
        ? r.commonAncestorContainer
        : r.commonAncestorContainer.parentElement;
    if (!common || protectedNode(common) || common.querySelector(sensitive))
      return null;
    quote = r.toString();
    if (!quote.trim()) return null;
    prefix = first.startContainer.textContent.slice(
      Math.max(0, first.startOffset - 24),
      first.startOffset,
    );
    suffix = last.startContainer.textContent.slice(
      last.startOffset,
      last.startOffset + 24,
    );
    rect = r.getBoundingClientRect();
  } else if (kind === "element") {
    rect = element.getBoundingClientRect();
    quote = element.innerText || element.textContent || "";
  } else {
    rect = {
      x: Math.min(start.x, end.x),
      y: Math.min(start.y, end.y),
      width: Math.abs(end.x - start.x),
      height: Math.abs(end.y - start.y),
    };
    // The explicit region can include ordinary form content, but not secrets.
    for (const node of doc.querySelectorAll(
      'input[type="password"],[autocomplete="one-time-code"],[id*="captcha" i],[class*="captcha" i]',
    )) {
      const r = node.getBoundingClientRect();
      if (
        r.width &&
        r.height &&
        r.x < rect.x + rect.width &&
        r.right > rect.x &&
        r.y < rect.y + rect.height &&
        r.bottom > rect.y
      )
        return null;
    }
  }
  const x = Math.max(0, rect.x),
    y = Math.max(0, rect.y);
  const width = Math.min(win.innerWidth, rect.x + rect.width) - x,
    height = Math.min(win.innerHeight, rect.y + rect.height) - y;
  if (width < 1 || height < 1) return null;
  // UTF-8 bounds, matching the API contract, without cutting surrogate pairs.
  const cut = (s, n) => {
    let out = "",
      bytes = 0;
    for (const c of String(s)) {
      bytes += new TextEncoder().encode(c).length;
      if (bytes > n) break;
      out += c;
    }
    return out;
  };
  return {
    snapshot: {
      role: cut(
        element.getAttribute("role") || element.tagName.toLowerCase(),
        80,
      ),
      name: cut(element.getAttribute("aria-label") || "", 1024),
      quote: cut(quote, 8192),
      prefix: cut(prefix, 128),
      suffix: cut(suffix, 128),
      selector_hint: cut(element.tagName.toLowerCase(), 1024),
    },
    visual: {
      width: win.innerWidth,
      height: win.innerHeight,
      scroll_x: win.scrollX,
      scroll_y: win.scrollY,
      rect: { x, y, width, height },
    },
  };
}

function createAnnotationPage({
  frameList,
  getFrame,
  frameSession,
  send,
  call,
}) {
  const refs = new Map();
  const mapping = async (frame, guard) => {
    let x = 0,
      y = 0,
      sx = 1,
      sy = 1,
      current = frame;
    while (current.parent) {
      const parent = getFrame(current.parent),
        session = frameSession(parent);
      const owner = await send(
        guard,
        "DOM.getFrameOwner",
        { frameId: current.id },
        session,
        parent,
      );
      const box = await call(
        guard,
        { backendID: owner.backendNodeId, session, frame: parent },
        "function(){const r=this.getBoundingClientRect();const sx=this.offsetWidth?r.width/this.offsetWidth:1,sy=this.offsetHeight?r.height/this.offsetHeight:1;return {x:r.x+this.clientLeft*sx,y:r.y+this.clientTop*sy,sx,sy};}",
      );
      x = box.x + x * box.sx;
      y = box.y + y * box.sy;
      sx *= box.sx;
      sy *= box.sy;
      current = parent;
    }
    return { x, y, sx, sy };
  };
  const read = async (ref, guard) => {
    getFrame(ref.frame.id, ref.generation);
    const box = await call(
      guard,
      ref,
      "function(){const e=this.nodeType===1?this:this.parentElement;const r=e?.getBoundingClientRect();return r?{x:r.x,y:r.y}:null}",
    );
    const shift = (p) =>
      ref.kind === "text" && box && ref.box
        ? { x: p.x + box.x - ref.box.x, y: p.y + box.y - ref.box.y }
        : p;
    const value = await call(guard, ref, readSelection.toString(), [
      ref.kind,
      shift(ref.start),
      shift(ref.end),
    ]);
    if (!ref.box) ref.box = box;
    if (!value) throw failure("INVALID_TARGET");
    getFrame(ref.frame.id, ref.generation);
    const transform = await mapping(ref.frame, guard);
    const metrics = await send(guard, "Page.getLayoutMetrics");
    const viewport = metrics.cssLayoutViewport;
    const r = value.visual.rect;
    const x = Math.max(0, transform.x + r.x * transform.sx),
      y = Math.max(0, transform.y + r.y * transform.sy);
    const width =
      Math.min(
        viewport.clientWidth,
        transform.x + (r.x + r.width) * transform.sx,
      ) - x;
    const height =
      Math.min(
        viewport.clientHeight,
        transform.y + (r.y + r.height) * transform.sy,
      ) - y;
    if (width < 1 || height < 1) throw failure("TARGET_NOT_VISIBLE");
    return {
      ...value,
      visual: {
        width: viewport.clientWidth,
        height: viewport.clientHeight,
        scroll_x: viewport.pageX,
        scroll_y: viewport.pageY,
        rect: { x, y, width, height },
      },
    };
  };
  return {
    async select(kind, start, end, guard, remember = true) {
      const frames = await frameList(guard),
        root = frames.find((f) => !f.parent);
      let hit;
      if (kind === "region") {
        const { root: node } = await send(guard, "DOM.getDocument");
        hit = { backendNodeId: node.backendNodeId, frameId: root.id };
      } else {
        // CDP hit testing uses document coordinates; pointer events and the
        // returned highlight rectangle use viewport coordinates.
        const { cssLayoutViewport: viewport } = await send(
          guard,
          "Page.getLayoutMetrics",
        );
        hit = await send(guard, "DOM.getNodeForLocation", {
          x: Math.floor(start.x + viewport.pageX),
          y: Math.floor(start.y + viewport.pageY),
          includeUserAgentShadowDOM: true,
        });
      }
      // Chromium hit testing stops at an OOPIF owner. Descend using a fixed
      // function in that frame's isolated world; never reuse root coordinates.
      if (kind !== "region")
        for (let depth = 0; depth < 16; depth++) {
          const parent = getFrame(hit.frameId),
            session = frameSession(parent);
          const { node } = await send(
            guard,
            "DOM.describeNode",
            { backendNodeId: hit.backendNodeId },
            session,
            parent,
          );
          if (!node.frameId || node.frameId === parent.id) break;
          const child = getFrame(node.frameId),
            childSession = frameSession(child),
            transform = await mapping(child, guard);
          const { executionContextId } = await send(
            guard,
            "Page.createIsolatedWorld",
            {
              frameId: child.id,
              worldName: "openfox-annotation-read",
              grantUniveralAccess: false,
            },
            childSession,
            child,
          );
          const { result } = await send(
            guard,
            "Runtime.callFunctionOn",
            {
              executionContextId,
              functionDeclaration:
                "function(x,y){return document.elementFromPoint(x,y)}",
              arguments: [
                { value: (start.x - transform.x) / transform.sx },
                { value: (start.y - transform.y) / transform.sy },
              ],
            },
            childSession,
            child,
          );
          if (!result.objectId) throw failure("INVALID_TARGET");
          try {
            const { node: target } = await send(
              guard,
              "DOM.describeNode",
              { objectId: result.objectId },
              childSession,
              child,
            );
            hit = { backendNodeId: target.backendNodeId, frameId: child.id };
          } finally {
            await send(
              guard,
              "Runtime.releaseObject",
              { objectId: result.objectId },
              childSession,
              child,
            ).catch(() => {});
          }
        }
      const frame = getFrame(hit.frameId),
        transform = await mapping(frame, guard);
      const local = (p) => ({
        x: (p.x - transform.x) / transform.sx,
        y: (p.y - transform.y) / transform.sy,
      });
      const ref = {
        backendID: hit.backendNodeId,
        frame,
        session: frameSession(frame),
        generation: frame.generation,
        kind,
        start: local(start),
        end: local(end),
      };
      const value = await read(ref, guard),
        id = randomUUID();
      if (remember) {
        if (refs.size >= 1000) refs.delete(refs.keys().next().value);
        refs.set(id, { ...ref, value });
      }
      return {
        kind,
        live: {
          frame_id: frame.id,
          document_generation: frame.generation,
          ref: id,
        },
        ...value,
      };
    },
    async locate(live, guard, scroll = true) {
      await frameList(guard);
      const ref = refs.get(live.ref);
      if (
        !ref ||
        ref.frame.id !== live.frame_id ||
        ref.generation !== live.document_generation
      )
        throw failure("STALE_TARGET");
      if (scroll && ref.kind !== "region")
        await send(
          guard,
          "DOM.scrollIntoViewIfNeeded",
          { backendNodeId: ref.backendID },
          ref.session,
          ref.frame,
        );
      const value = await read(ref, guard);
      if (
        JSON.stringify(value.snapshot) !== JSON.stringify(ref.value.snapshot) ||
        (ref.kind === "region" &&
          JSON.stringify(value.visual) !== JSON.stringify(ref.value.visual))
      )
        throw failure("STALE_TARGET");
      return { ...value, kind: ref.kind };
    },
  };
}
module.exports = { createAnnotationPage };
