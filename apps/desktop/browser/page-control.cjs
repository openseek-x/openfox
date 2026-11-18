const { randomUUID } = require("node:crypto");
const { nativeImage } = require("electron");
const { failure } = require("./policy.cjs");
const { createAnnotationPage } = require("./annotation-page.cjs");

const MAX_NODES = 1000;
const MAX_SNAPSHOT_BYTES = 100 * 1024;
const MAX_IMAGE_BYTES = 4 * 1024 * 1024;
const text = (value, limit = 2048) => String(value ?? "").slice(0, limit);
const fingerprint = (node) =>
  JSON.stringify([node.role?.value, node.name?.value]);

// Main-process-only CDP client. No method names or scripts come from callers.
// Each public operation receives a guard which rechecks the current grant,
// AbortSignal, binding lease and allowed origins before every CDP command.
function createPageControl(contents) {
  const cdp = contents.debugger;
  const frames = new Map();
  const sessions = new Map();
  const initializing = new Set();
  const refs = new Map();
  const logs = [];
  const contexts = new Map();
  let revision = 0;
  let closed = false;
  let dialog = null;
  let failed = false;
  const raw = (method, params = {}, session) =>
    cdp.sendCommand(method, params, session || undefined);
  const invalidate = (id) => {
    for (const [key, ref] of refs) if (ref.frameID === id) refs.delete(key);
  };
  const remember = (frame, parentID) => {
    const previous = frames.get(frame.id);
    const changed = !previous || previous.loader !== frame.loaderId;
    if (changed) invalidate(frame.id);
    const item = {
      id: frame.id,
      parent: frame.parentId || parentID || previous?.parent,
      url: frame.url,
      origin: frame.securityOrigin,
      loader: frame.loaderId,
      generation: changed ? ++revision : previous.generation,
    };
    frames.set(frame.id, item);
    return item;
  };
  const removeFrame = (id) => {
    for (const child of frames.values())
      if (child.parent === id) removeFrame(child.id);
    invalidate(id);
    frames.delete(id);
    sessions.delete(id);
  };
  const initialize = async (session) => {
    await raw("Page.enable", {}, session);
    await raw("DOM.enable", {}, session);
    await raw("Runtime.enable", {}, session);
    await raw("Accessibility.enable", {}, session);
    await raw(
      "Target.setAutoAttach",
      {
        autoAttach: true,
        waitForDebuggerOnStart: false,
        flatten: true,
        filter: [{ type: "iframe", exclude: false }, { exclude: true }],
      },
      session,
    );
  };
  const message = (_event, method, params, session) => {
    if (
      method === "Target.attachedToTarget" &&
      params.targetInfo.type === "iframe"
    ) {
      sessions.set(params.targetInfo.targetId, params.sessionId);
      const pending = initialize(params.sessionId).catch(() => {
        // A cross-origin iframe can detach while its domains are enabling.
        // Its removed session must not disable control of the remaining page.
        if (
          !closed &&
          sessions.get(params.targetInfo.targetId) === params.sessionId
        )
          failed = true;
      });
      initializing.add(pending);
      pending.finally(() => initializing.delete(pending));
    }
    if (method === "Target.detachedFromTarget") {
      for (const [id, value] of sessions)
        if (value === params.sessionId) removeFrame(id);
    }
    if (method === "Page.frameNavigated") {
      // Main-document navigation invalidates all descendants, including refs
      // whose iframe detach notification has not arrived yet.
      for (const child of [...frames.values()])
        if (child.parent === params.frame.id) removeFrame(child.id);
      remember(params.frame);
    }
    if (method === "Page.frameDetached" && params.reason !== "swap")
      removeFrame(params.frameId);
    if (method === "Page.navigatedWithinDocument") {
      const frame = frames.get(params.frameId);
      if (frame) {
        frame.url = params.url;
        frame.generation = ++revision;
        invalidate(frame.id);
      }
    }
    if (method === "Page.javascriptDialogOpening")
      dialog = {
        type: params.type,
        message: text(params.message),
        url: params.url,
        session,
      };
    if (method === "Page.javascriptDialogClosed") dialog = null;
    if (method === "Runtime.executionContextCreated") {
      if (contexts.size >= 512) contexts.delete(contexts.keys().next().value);
      contexts.set(
        `${session || "main"}:${params.context.id}`,
        params.context.origin,
      );
    }
    if (method === "Runtime.executionContextDestroyed")
      contexts.delete(`${session || "main"}:${params.executionContextId}`);
    if (method === "Runtime.executionContextsCleared")
      for (const key of contexts.keys())
        if (key.startsWith(`${session || "main"}:`)) contexts.delete(key);
    if (method === "Runtime.consoleAPICalled") {
      const origin = contexts.get(
        `${session || "main"}:${params.executionContextId}`,
      );
      if (!origin || !/^https?:/.test(origin)) return;
      // Never fetch objects or invoke toString/getters from console arguments.
      logs.push({
        origin,
        type: text(params.type, 32),
        text: text(
          (params.args || [])
            .map((arg) =>
              typeof arg.value === "string" ? arg.value : arg.type,
            )
            .join(" "),
        ),
      });
      if (logs.length > 50) logs.shift();
    }
  };
  const detach = () => {
    if (!closed) failed = true;
  };
  cdp.on("message", message);
  cdp.on("detach", detach);
  try {
    cdp.attach("1.3");
  } catch (error) {
    cdp.removeListener("message", message);
    cdp.removeListener("detach", detach);
    throw error;
  }
  const ready = initialize();
  ready.catch(() => {
    failed = true;
  });

  const check = (guard, frame) => {
    if (closed || contents.isDestroyed()) throw failure("TARGET_CLOSED");
    if (failed || !cdp.isAttached()) throw failure("UNAVAILABLE");
    guard(
      frame
        ? /^https?:/.test(frame.origin || "")
          ? frame.origin
          : frame.url
        : contents.getURL(),
    );
  };
  const send = async (guard, method, params = {}, session, frame) => {
    check(guard, frame);
    const value = await raw(method, params, session);
    check(guard, frame);
    return value;
  };
  const frameSession = (frame) => {
    let item = frame;
    for (let depth = 0; item && depth < 64; depth++) {
      if (sessions.has(item.id)) return sessions.get(item.id);
      item = frames.get(item.parent);
    }
  };
  const frameList = async (guard) => {
    await ready;
    await Promise.all([...initializing]);
    check(guard);
    const found = new Set();
    const visit = (tree, parent, depth = 0) => {
      if (depth > 64 || found.size >= 128) throw failure("LIMIT_EXCEEDED");
      const frame = remember(tree.frame, parent);
      found.add(frame.id);
      for (const child of tree.childFrames || [])
        visit(child, frame.id, depth + 1);
    };
    visit((await send(guard, "Page.getFrameTree")).frameTree);
    for (const [id, session] of sessions) {
      const tree = await send(guard, "Page.getFrameTree", {}, session);
      visit(tree.frameTree, frames.get(id)?.parent);
    }
    for (const id of frames.keys()) if (!found.has(id)) removeFrame(id);
    return [...frames.values()];
  };
  const getFrame = (id, generation) => {
    const frame = frames.get(id);
    if (!frame || (generation !== undefined && frame.generation !== generation))
      throw failure("STALE_TARGET");
    return frame;
  };
  const target = (args) => {
    const ref = refs.get(args.ref);
    const frame = getFrame(args.frame_id, args.document_generation);
    if (
      !ref ||
      ref.frameID !== frame.id ||
      ref.generation !== frame.generation ||
      ref.session !== frameSession(frame)
    )
      throw failure("STALE_TARGET");
    return { ...ref, frame };
  };
  const verify = async (guard, ref) => {
    getFrame(ref.frame.id, ref.generation);
    const { nodes } = await send(
      guard,
      "Accessibility.getPartialAXTree",
      { backendNodeId: ref.backendID, fetchRelatives: false },
      ref.session,
      ref.frame,
    );
    const node = nodes.find((node) => node.backendDOMNodeId === ref.backendID);
    if (!node || node.ignored || fingerprint(node) !== ref.fingerprint)
      throw failure("STALE_TARGET");
  };
  const call = async (guard, ref, script, args = []) => {
    const { object } = await send(
      guard,
      "DOM.resolveNode",
      { backendNodeId: ref.backendID },
      ref.session,
      ref.frame,
    );
    if (!object.objectId) throw failure("STALE_TARGET");
    try {
      const response = await send(
        guard,
        "Runtime.callFunctionOn",
        {
          objectId: object.objectId,
          functionDeclaration: script,
          arguments: args.map((value) => ({ value })),
          returnByValue: true,
          userGesture: true,
        },
        ref.session,
        ref.frame,
      );
      if (response.exceptionDetails) throw failure("STALE_TARGET");
      return response.result.value;
    } finally {
      // Cleanup must still run after control has been revoked.
      await raw(
        "Runtime.releaseObject",
        { objectId: object.objectId },
        ref.session,
      ).catch(() => {});
    }
  };
  const snapshot = async (args, guard) => {
    const list = await frameList(guard);
    const frame = args.frame_id
      ? getFrame(args.frame_id)
      : list.find((f) => !f.parent);
    if (!frame) throw failure("STALE_TARGET");
    check(guard, frame);
    const generation = frame.generation;
    const session = frameSession(frame);
    const { nodes } = await send(
      guard,
      "Accessibility.getFullAXTree",
      { frameId: frame.id, depth: 32 },
      session,
      frame,
    );
    getFrame(frame.id, generation);
    refs.clear();
    const output = [];
    let bytes = 0,
      truncated = false;
    for (const node of nodes) {
      if (node.ignored || !node.backendDOMNodeId) continue;
      const role = text(node.role?.value, 80),
        name = text(node.name?.value);
      if (
        args.query &&
        !`${role} ${name}`.toLowerCase().includes(args.query.toLowerCase())
      )
        continue;
      const id = randomUUID();
      const row = { ref: id, role, name };
      // Password input values must never become a snapshot field.
      if (
        node.value &&
        !node.properties?.some((p) => p.name === "protected" && p.value.value)
      )
        row.value = text(node.value.value);
      for (const property of node.properties || [])
        if (
          [
            "checked",
            "disabled",
            "expanded",
            "selected",
            "readonly",
            "required",
          ].includes(property.name)
        )
          row[property.name] = property.value.value;
      const size = Buffer.byteLength(JSON.stringify(row));
      if (output.length >= MAX_NODES || bytes + size > MAX_SNAPSHOT_BYTES) {
        truncated = true;
        break;
      }
      bytes += size;
      refs.set(id, {
        backendID: node.backendDOMNodeId,
        frameID: frame.id,
        generation,
        session,
        fingerprint: fingerprint(node),
      });
      output.push(row);
    }
    return {
      frame_id: frame.id,
      document_generation: generation,
      nodes: output,
      truncated,
    };
  };
  const point = async (guard, ref) => {
    await verify(guard, ref);
    await send(
      guard,
      "DOM.scrollIntoViewIfNeeded",
      { backendNodeId: ref.backendID },
      ref.session,
      ref.frame,
    );
    await contents.capturePage();
    check(guard, ref.frame);
    const bounds = await call(
      guard,
      ref,
      "function(){if(!this.isConnected)return null;const r=this.getBoundingClientRect();const hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return {x:r.x,y:r.y,width:r.width,height:r.height,visible:!!hit&&(hit===this||this.contains(hit))};}",
    );
    if (!bounds || !bounds.visible || bounds.width <= 0 || bounds.height <= 0)
      throw failure("TARGET_NOT_VISIBLE");
    let frame = ref.frame;
    let x = bounds.x + bounds.width / 2,
      y = bounds.y + bounds.height / 2;
    while (frame.parent) {
      const parent = getFrame(frame.parent),
        session = frameSession(parent);
      const owner = await send(
        guard,
        "DOM.getFrameOwner",
        { frameId: frame.id },
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
      const visible = await call(
        guard,
        { backendID: owner.backendNodeId, session, frame: parent },
        "function(x,y){return document.elementFromPoint(x,y)===this;}",
        [x, y],
      );
      if (!visible) throw failure("TARGET_NOT_VISIBLE");
      frame = parent;
    }
    const { cssVisualViewport: viewport } = await send(
      guard,
      "Page.getLayoutMetrics",
    );
    if (
      ![x, y].every(Number.isFinite) ||
      x < 0 ||
      y < 0 ||
      x >= viewport.clientWidth ||
      y >= viewport.clientHeight
    )
      throw failure("TARGET_NOT_VISIBLE");
    // Scrolling can trigger lazy rendering and replace the original element.
    await verify(guard, ref);
    return { x, y };
  };
  const act = async (args, guard) => {
    if (args.action === "dialog") {
      await ready;
      check(guard);
      if (!dialog) throw failure("STALE_TARGET");
      guard(dialog.url);
      // Frame-tree commands can block while JavaScript is paused in a modal.
      await send(
        guard,
        "Page.handleJavaScriptDialog",
        { accept: args.accept, promptText: args.text },
        dialog.session,
      );
      return { completed: true };
    }
    await frameList(guard);
    const ref = target(args);
    await verify(guard, ref);
    if (["click", "hover", "check"].includes(args.action)) {
      if (args.action === "check") {
        const checked = await call(
          guard,
          ref,
          "function(){return this instanceof HTMLInputElement&&['checkbox','radio'].includes(this.type)?this.checked:null;}",
        );
        if (checked === null) throw failure("INVALID_TARGET");
        if (checked === args.checked) return { completed: true };
      }
      const coordinates = await point(guard, ref);
      await send(guard, "Input.dispatchMouseEvent", {
        type: "mouseMoved",
        ...coordinates,
      });
      if (args.action !== "hover") {
        await send(guard, "Input.dispatchMouseEvent", {
          type: "mousePressed",
          button: "left",
          clickCount: 1,
          ...coordinates,
        });
        try {
          await send(guard, "Input.dispatchMouseEvent", {
            type: "mouseReleased",
            button: "left",
            clickCount: 1,
            ...coordinates,
          });
        } catch (error) {
          // Release the already pressed button even on cancellation; never send
          // a fresh press or retry a potentially submitted action.
          await raw("Input.dispatchMouseEvent", {
            type: "mouseReleased",
            button: "left",
            clickCount: 1,
            ...coordinates,
          }).catch(() => {});
          throw error;
        }
      }
    } else if (args.action === "fill") {
      const accepted = await call(
        guard,
        ref,
        "function(){if(!this.isConnected||this.disabled||this.readOnly)return false;if(this instanceof HTMLInputElement){if(!['text','search','email','url','tel','password','number'].includes(this.type))return false;this.focus();this.select();return true}if(this instanceof HTMLTextAreaElement){this.focus();this.select();return true}if(this.isContentEditable){this.focus();const r=document.createRange();r.selectNodeContents(this);const s=getSelection();s.removeAllRanges();s.addRange(r);return true}return false;}",
      );
      if (!accepted) throw failure("INVALID_TARGET");
      await verify(guard, ref);
      if (
        !(await call(
          guard,
          ref,
          "function(){return this.isConnected&&(document.activeElement===this||this.contains(document.activeElement));}",
        ))
      )
        throw failure("STALE_TARGET");
      await send(
        guard,
        "Input.insertText",
        { text: args.text },
        ref.session,
        ref.frame,
      );
    } else if (args.action === "select") {
      const selected = await call(
        guard,
        ref,
        "function(values){if(!(this instanceof HTMLSelectElement)||this.disabled||(!this.multiple&&values.length!==1))return false;const options=[...this.options];if(values.some(v=>!options.some(o=>o.value===v&&!o.disabled)))return false;for(const o of options)o.selected=values.includes(o.value);this.dispatchEvent(new Event('input',{bubbles:true}));this.dispatchEvent(new Event('change',{bubbles:true}));return true;}",
        [args.values],
      );
      if (!selected) throw failure("INVALID_TARGET");
    } else if (args.action === "press") {
      const keys = {
        Enter: [13, "Enter"],
        Tab: [9, "Tab"],
        Escape: [27, "Escape"],
        Backspace: [8, "Backspace"],
        Delete: [46, "Delete"],
        ArrowLeft: [37, "ArrowLeft"],
        ArrowUp: [38, "ArrowUp"],
        ArrowRight: [39, "ArrowRight"],
        ArrowDown: [40, "ArrowDown"],
        Home: [36, "Home"],
        End: [35, "End"],
        PageUp: [33, "PageUp"],
        PageDown: [34, "PageDown"],
        Space: [32, "Space"],
      };
      const key = keys[args.key];
      if (!key) throw failure("INVALID_REQUEST");
      await send(
        guard,
        "DOM.focus",
        { backendNodeId: ref.backendID },
        ref.session,
        ref.frame,
      );
      const event = {
        key: args.key === "Space" ? " " : args.key,
        code: key[1],
        windowsVirtualKeyCode: key[0],
      };
      await send(
        guard,
        "Input.dispatchKeyEvent",
        { type: "keyDown", ...event },
        ref.session,
        ref.frame,
      );
      try {
        await send(
          guard,
          "Input.dispatchKeyEvent",
          { type: "keyUp", ...event },
          ref.session,
          ref.frame,
        );
      } catch (error) {
        await raw(
          "Input.dispatchKeyEvent",
          { type: "keyUp", ...event },
          ref.session,
        ).catch(() => {});
        throw error;
      }
    } else if (args.action === "scroll") {
      const coordinates = await point(guard, ref);
      await send(guard, "Input.dispatchMouseEvent", {
        type: "mouseWheel",
        deltaX: args.delta_x || 0,
        deltaY: args.delta_y || 0,
        ...coordinates,
      });
    } else throw failure("INVALID_REQUEST");
    return { completed: true };
  };
  const annotations = createAnnotationPage({
    frameList,
    getFrame,
    frameSession,
    send,
    call,
  });
  return {
    annotations,
    ready,
    snapshot,
    act,
    async upload(args, paths, guard) {
      await frameList(guard);
      const ref = target(args);
      await verify(guard, ref);
      const valid = await call(
        guard,
        ref,
        "function(count){return this.isConnected&&this instanceof HTMLInputElement&&this.type==='file'&&!this.disabled&&(this.multiple||count===1);}",
        [paths.length],
      );
      if (!valid) throw failure("INVALID_TARGET");
      await send(
        guard,
        "DOM.setFileInputFiles",
        { files: paths, backendNodeId: ref.backendID },
        ref.session,
        ref.frame,
      );
      return { completed: true };
    },
    async frames(guard) {
      return (await frameList(guard)).map((frame) => {
        check(guard, frame);
        return {
          frame_id: frame.id,
          parent_id: frame.parent,
          document_generation: frame.generation,
          url: text(frame.url),
        };
      });
    },
    async screenshot(guard) {
      const list = await frameList(guard);
      for (const frame of list) check(guard, frame);
      const before = revision;
      const captured = await send(guard, "Page.captureScreenshot", {
        format: "png",
        fromSurface: true,
        captureBeyondViewport: false,
      });
      let image = nativeImage.createFromBuffer(
        Buffer.from(captured.data, "base64"),
      );
      check(guard);
      if (revision !== before) throw failure("STALE_TARGET");
      if (image.isEmpty()) throw failure("CAPTURE_FAILED");
      const size = image.getSize();
      if (Math.max(size.width, size.height) > 2048) {
        const scale = 2048 / Math.max(size.width, size.height);
        image = image.resize({
          width: Math.round(size.width * scale),
          height: Math.round(size.height * scale),
        });
      }
      let data = image.toPNG(),
        mime_type = "image/png";
      if (data.length > MAX_IMAGE_BYTES) {
        data = image.toJPEG(85);
        mime_type = "image/jpeg";
      }
      if (data.length > MAX_IMAGE_BYTES) throw failure("LIMIT_EXCEEDED");
      return { mime_type, data: data.toString("base64") };
    },
    console(guard) {
      check(guard);
      for (const log of logs) guard(log.origin);
      if (dialog) guard(dialog.url);
      return {
        entries: logs.map(({ origin: _origin, ...entry }) => entry),
        truncated: logs.length === 50,
        dialog: dialog ? { type: dialog.type, message: dialog.message } : null,
      };
    },
    close() {
      if (closed) return;
      closed = true;
      refs.clear();
      logs.length = 0;
      contexts.clear();
      frames.clear();
      sessions.clear();
      cdp.removeListener("message", message);
      cdp.removeListener("detach", detach);
      if (!contents.isDestroyed() && cdp.isAttached()) cdp.detach();
    },
  };
}

module.exports = { createPageControl };
