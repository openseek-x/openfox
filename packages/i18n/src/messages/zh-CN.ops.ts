/** 阶段 12e · 电脑运维：并发上限、受保护目录告警（简体中文）。由 zh-CN.ts 挂到 ops 键下（独立文件，减少并行修改冲突）。 */
export const opsZh = {
  limits: {
    title: "并发上限",
    center: "中心上限",
    local: "本机上限",
    effective: "生效",
    unreported: "—",
    hint: "生效值取中心上限与本机上限（daemon 的 --max-runs）中较小的一个",
  },
  config: {
    label: "运行配置",
    applied: "已应用 v{version}",
    restartRequired: "需重启",
    restartHint: "组织运行配置的修改需要重启这台电脑的 daemon 后生效",
    failed: "应用失败",
    error: {
      INVALID_POLICY: "这台电脑的 daemon 未能通过对组织运行配置的校验",
      ACK_FAILED: "这台电脑的 daemon 向中心确认配置版本失败，稍后会自动重试",
    },
  },
  revoke: {
    action: "撤销授权",
    title: "撤销这台电脑在当前组织的授权？",
    desc: "撤销后这台电脑不再为当前组织领取任务：排队中的运行以 AUTHORIZATION_REVOKED 失败，执行中的运行不再被中心接受。绑定的 Agent 不会被归档，电脑所有者重新把电脑加入组织后可继续使用。",
    done: "已撤销这台电脑的授权",
  },
  rotate: {
    title: "设备密钥",
    desc: "更换这台电脑连接中心使用的密钥",
    action: "轮换密钥",
    unsupported:
      "当前 daemon 不支持在线轮换，请升级 daemon 或在本机运行 openfox computer rotate",
    confirmTitle: "轮换这台电脑的密钥？",
    confirmDesc: "旧密钥在新密钥落盘后才失效，期间不会断开连接。",
    done: "密钥已轮换",
    resumed: "已完成上次未结束的密钥轮换",
    keyId: "当前密钥：{id}",
    failed: {
      KEY_ROTATION_FAILED: "密钥轮换失败，旧密钥仍然有效，请稍后重试",
      KEY_ROTATION_UNSUPPORTED:
        "这台电脑的 daemon 不支持在线轮换，请升级 daemon 或在本机运行 openfox computer rotate",
    },
  },
  orgs: {
    removedByAdmin: "该组织的管理员已移除这台电脑，需要管理员重新允许",
    title: "允许的组织",
    loadFailed: "组织列表加载失败",
    current: "当前",
    status: {
      ACTIVE: "已授权",
      REVOKED: "已撤销",
      EXPIRED: "已过期",
      removedByAdmin: "管理员已移除",
      none: "未加入",
    },
    join: "加入",
    joinLabel: "加入 {name}",
    joined: "电脑已加入 {name}",
    revoke: "撤销",
    revokeLabel: "撤销 {name} 的授权",
    revokeTitle: "撤销 {name} 的授权？",
    revokeDesc:
      "撤销后这台电脑不再为该组织领取任务：排队中的运行以 AUTHORIZATION_REVOKED 失败，执行中的运行不再被中心接受。",
    revoked: "已撤销 {name} 的授权",
    remove: "移出组织",
    removeLabel: "从 {name} 移出",
    removeTitle: "把电脑从 {name} 移出？",
    removeDesc:
      "移出后该组织里绑定这台电脑的 Agent 会被归档，进行中的运行会被取消。",
    removed: "电脑已从 {name} 移出",
  },
  removed: {
    title: "被管理员移除的电脑",
    desc: "这些电脑被本组织的所有者或管理员移除，电脑所有者不能自行加回。重新允许后，所有者可以再次把电脑加入本组织。",
    allow: "重新允许",
    allowLabel: "重新允许 {name}",
    allowed: "已重新允许，电脑所有者现在可以把它加入本组织",
    removedAt: "移除于 {time}",
    unknownOwner: "未知所有者",
  },
  protectedRoots: {
    title: "部分注册目录命中受保护目录名单",
    desc: "以下目录不能作为工作目录：",
  },
};
