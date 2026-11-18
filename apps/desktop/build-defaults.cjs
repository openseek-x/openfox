"use strict";

// 打包前写入安装包的默认网页地址：resources/desktop-defaults.json。
// 地址来自环境变量 OPENFOX_DESKTOP_DEFAULT_WEB_URL，规则与设置文件的 web_url 相同（HTTP(S)，不带凭据、查询串和片段）；
// 没有设置时删除该文件，安装包就没有默认地址（首次启动显示离线页，用户在设置文件里填写）。
const fs = require("node:fs");
const path = require("node:path");
const { parseWebURL } = require("./security.cjs");

const file = path.join(__dirname, "resources", "desktop-defaults.json");
const raw = (process.env.OPENFOX_DESKTOP_DEFAULT_WEB_URL || "").trim();

if (!raw) {
  fs.rmSync(file, { force: true });
  console.log("desktop-defaults: no default web URL");
} else {
  const url = parseWebURL(raw);
  if (!url)
    throw new Error("OPENFOX_DESKTOP_DEFAULT_WEB_URL is not a valid web URL");
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, `${JSON.stringify({ web_url: url })}\n`);
  console.log(`desktop-defaults: web_url=${url}`);
}
