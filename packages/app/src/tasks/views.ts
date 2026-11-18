import type { Task, TaskListQuery, TaskStatus } from "@openfox/api";

import type { DisplaySettings } from "./model";
import type { Person, TaskSource } from "./queries";

/** 看板 / 列表 / 表格共用的输入 */
export interface TaskViewProps {
  orgId: string;
  source: TaskSource;
  /** 筛选 + 显示设置组成的查询参数（不含 status 列） */
  params: TaskListQuery;
  /** 列 / 分组：按全局顺序排好的状态 */
  columns: TaskStatus[];
  /** 状态 id → 任务数 */
  counts: Map<string, number>;
  display: DisplaySettings;
  onOpen: (task: Task) => void;
  /** 在某状态下新建任务（我的任务不提供） */
  onCreate?: (statusId: string) => void;
  /** 表格内联修改处理人的候选 */
  people: Person[];
  agents: { id: string; name: string }[];
  /** 迭代 / 专项等引用 id → 显示名称，由页面从已有查询构造 */
  refNames?: ReadonlyMap<string, string>;
}
