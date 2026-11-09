import {
  createContext,
  useContext,
  useEffect,
  type MutableRefObject,
} from "react";
import { useLocation, useParams } from "react-router-dom";

import { clearSessionPrefix, readSession, writeSession } from "../storage";

/** 主导航的四个模块。 */
export type NavModule = "home" | "chat" | "members" | "settings";

const PREFIX = "openfox:nav:";
const MAX_LENGTH = 2048;

const storageKey = (orgId: string, module: NavModule) =>
  `${PREFIX}${orgId}:${module}`;

/**
 * 地址属于哪个模块：首页打开的对话以 rail=home 保留导航上下文；
 * 其余 chat、members、settings 各自独占前缀，
 * 组织下其余页面（收件箱、我的任务、空间、文档、Agent、市场等）都归首页。
 * 不在 /o/:orgId 下返回 null。
 */
export function moduleOf(
  orgId: string,
  pathname: string,
  search = "",
): NavModule | null {
  const base = `/o/${orgId}`;
  if (pathname !== base && !pathname.startsWith(`${base}/`)) return null;
  const rest = pathname.slice(base.length);
  if (/^\/chat(\/|$)/.test(rest))
    return new URLSearchParams(search).get("rail") === "home" ? "home" : "chat";
  if (/^\/tasks\/[^/]+$/.test(rest))
    return new URLSearchParams(search).get("rail") === "chat" ? "chat" : "home";
  if (/^\/members(\/|$)/.test(rest)) return "members";
  if (/^\/settings(\/|$)/.test(rest)) return "settings";
  return "home";
}

/** 校验一个待记住 / 待跳转的地址：只接受本组织下、属于该模块的 pathname + search。 */
export function sanitizeNavTarget(
  orgId: string,
  module: NavModule,
  value: unknown,
): string | null {
  if (typeof value !== "string" || value.length > MAX_LENGTH) return null;
  if (!value.startsWith(`/o/${orgId}/`)) return null;
  let url: URL;
  try {
    url = new URL(value, "http://nav.invalid");
  } catch {
    return null;
  }
  // 往返必须一致：拦住 hash、反斜杠、控制字符、.. 路径段等
  if (url.origin !== "http://nav.invalid") return null;
  if (url.pathname + url.search !== value) return null;
  return moduleOf(orgId, url.pathname, url.search) === module ? value : null;
}

export function readNavMemory(orgId: string, module: NavModule): string | null {
  return sanitizeNavTarget(
    orgId,
    module,
    readSession(storageKey(orgId, module)),
  );
}

/** 记录某个地址（pathname + search）为其所属模块的最近位置；非法地址忽略。 */
export function recordNav(orgId: string, target: string): void {
  const url = new URL(target, "http://nav.invalid");
  const module = moduleOf(orgId, url.pathname, url.search);
  if (!module) return;
  const clean = sanitizeNavTarget(orgId, module, target);
  if (clean) writeSession(storageKey(orgId, module), clean);
}

/** 退出登录时清空全部组织的记录。 */
export function clearNavMemory(): void {
  clearSessionPrefix(PREFIX);
}

/** 某模块主导航应跳转的地址：当前就在该模块时是当前地址（不动），否则是记住的地址或默认路径。 */
export function navTarget(
  orgId: string,
  module: NavModule,
  current: string,
  fallback: string,
): string {
  const here = new URL(current, "http://nav.invalid");
  if (moduleOf(orgId, here.pathname, here.search) === module) return current;
  return readNavMemory(orgId, module) ?? fallback;
}

/** 失效对象的回报：当前地址不应被记住，可给出去掉失效参数后的替代地址。 */
export type NavReject = { key: string; replacement: string | null };
export const NavRejectContext =
  createContext<MutableRefObject<NavReject | null> | null>(null);

/** 去掉查询串里的某个参数，返回 pathname + search。 */
export function withoutParam(
  pathname: string,
  search: string,
  name: string,
): string {
  const params = new URLSearchParams(search);
  params.delete(name);
  const rest = params.toString();
  return rest ? `${pathname}?${rest}` : pathname;
}

/** 丢弃某个地址的记录（仅当记录恰好是它）。 */
function forgetNav(orgId: string, target: string): void {
  const url = new URL(target, "http://nav.invalid");
  const module = moduleOf(orgId, url.pathname, url.search);
  if (module && readSession(storageKey(orgId, module)) === target)
    writeSession(storageKey(orgId, module), null);
}

/**
 * 页面发现当前地址指向的对象不存在 / 无权访问时调用：
 * 当前地址不再被记住；给出 replacement（去掉失效参数的地址）时改记它。
 * 同步页面（子组件 effect 先于 OrgLayout 的记录 effect）靠 ref 让 OrgLayout 跳过；
 * 异步发现（记录已写入）则直接改写。
 */
export function useRejectNavLocation(
  rejected: boolean,
  replacement: string | null = null,
): void {
  const ref = useContext(NavRejectContext);
  const { orgId = "" } = useParams();
  const location = useLocation();
  const current = location.pathname + location.search;
  useEffect(() => {
    if (!rejected) return;
    if (ref) ref.current = { key: location.key, replacement };
    forgetNav(orgId, current);
    if (replacement) recordNav(orgId, replacement);
  }, [ref, rejected, orgId, current, location.key, replacement]);
}
