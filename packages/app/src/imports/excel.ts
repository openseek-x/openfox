type Result = {
  csv?: Blob;
  sheets?: string[];
  error?: "precision" | "invalid";
};
export function readImportWorkbook(
  file: File,
  sheet?: string,
): Promise<Result> {
  return new Promise((resolve) => {
    const worker = new Worker(new URL("./excel.worker.ts", import.meta.url), {
      type: "module",
    });
    const done = (value: Result) => {
      clearTimeout(timer);
      worker.terminate();
      resolve(value);
    };
    const timer = setTimeout(() => done({ error: "invalid" }), 15000);
    worker.onmessage = (e) => done(e.data);
    worker.onerror = () => done({ error: "invalid" });
    worker.postMessage({ file, sheet });
  });
}
