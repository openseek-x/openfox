import {
  closestCorners,
  pointerWithin,
  type CollisionDetection,
} from "@dnd-kit/core";

/**
 * 跨容器拖动的落点判断：指针所在的落点优先（窄列、空分类卡用「最近角」会落到相邻容器）；
 * 指针不在任何落点上（键盘拖动）时退回最近角。看板与状态页共用。
 */
export const pointerFirstCollision: CollisionDetection = (args) => {
  const hits = pointerWithin(args);
  return hits.length > 0 ? hits : closestCorners(args);
};
