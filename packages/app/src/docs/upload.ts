// 上传的并发控制：文件夹上传时同时最多 4 个文件在传。

export const UPLOAD_CONCURRENCY = 4;

/**
 * 按上限并发执行任务，返回失败的数量。单个任务失败不影响其他任务。
 * 任务按数组顺序启动，全部结束后才返回。
 */
export async function runLimited<T>(
  items: readonly T[],
  limit: number,
  task: (item: T) => Promise<void>,
): Promise<number> {
  let next = 0;
  let failed = 0;
  const worker = async () => {
    while (next < items.length) {
      const item = items[next++]!;
      try {
        await task(item);
      } catch {
        failed++;
      }
    }
  };
  const workers = Array.from({ length: Math.min(limit, items.length) }, () =>
    worker(),
  );
  await Promise.all(workers);
  return failed;
}
