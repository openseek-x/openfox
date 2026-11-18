// This preload belongs only to the inert, isolated annotation overlay.
const { ipcRenderer } = require("electron");
window.addEventListener(
  "wheel",
  (event) => {
    if (!event.isTrusted) return;
    event.preventDefault();
    const unit =
      event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? innerHeight : 1;
    ipcRenderer.send("openfox:annotation-wheel", {
      x: event.clientX,
      y: event.clientY,
      deltaX: event.deltaX * unit,
      deltaY: event.deltaY * unit,
    });
  },
  { passive: false },
);
