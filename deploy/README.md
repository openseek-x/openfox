# Deployment examples

`compose.example.yml` is a local development shape only. It does not publish
the service publicly, does not create production credentials, and does not
replace a TLS reverse proxy, backup plan, or migration approval.

The production boundary is HTTPS at the reverse proxy, with the OpenFox
service and PostgreSQL on a private network. Database credentials must be
provided through a restricted file or credential store, never committed to the
repository.

Set `OPENFOX_ENV=production` explicitly in the API service environment before
exposing the reverse proxy. Provide the production HTTPS public URL and the
database and encryption key files. Startup validates the production settings
and session cookies use `Secure` in this mode.

Email notifications can be delivered more than once. If SMTP accepts a message and
the worker stops before recording completion, the recipient may get another
copy after the delivery lease expires. An expired notification can also end undelivered.

## 本地运行中心镜像

以下命令在仓库根目录执行。示例沿用 Compose 中的本地数据库凭据。目录权限设为 700，文件允许容器中的 UID 65532 读取。示例监听回环地址。

```sh
mkdir -p -m 700 deploy/secrets
chmod 700 deploy/secrets
if [ ! -e deploy/secrets/database.url ]; then
  printf '%s\n' 'postgresql://openfox_migrate:change-me-for-local-use-only@postgres/openfox?sslmode=disable' > deploy/secrets/database.url
  chmod 444 deploy/secrets/database.url
fi
docker compose -f deploy/compose.example.yml build openfox
docker compose -f deploy/compose.example.yml up -d --wait postgres
docker compose -f deploy/compose.example.yml run --rm --no-deps openfox migrate --apply
docker compose -f deploy/compose.example.yml up -d
curl --fail http://127.0.0.1:8080/ready
```

结束本地服务使用 `docker compose -f deploy/compose.example.yml down`。PostgreSQL 和产物保存在命名卷中。部署自有环境时，给运行身份单独授予读取数据库连接文件的权限，配置持久卷、HTTPS 公网地址与生产密钥。镜像默认使用 production，Compose 示例显式使用 development。

QQ 扫码需要另行提供固定版本的 Node、对应运行库和官方 SDK。默认中心镜像只携带服务端、迁移及系统证书和时区数据。[QQ 连接器部署说明](../docs/integrations/qq/README.md)

发布流水线在同一提交的质量检查通过后构建 Linux amd64 镜像。每个版本随 GitHub Release 附带 `openfox-center-<版本>-linux-amd64.tar.gz` 及其 `.sha256` 文件。将两者放在同一目录，运行 `sha256sum -c <校验文件>`，再执行 `docker load -i <镜像归档>`。导入后的标签为 `openfox-center:<版本>`，可用 `docker run --rm openfox-center:<版本> version` 核对版本。已有部署仍需显式迁移和切换服务。

## Web workbench and desktop app

`nginx-web.example.conf` serves the static build of `apps/web` and proxies
`/api/` to `openfox-server`. The desktop app does not bundle the web
workbench; like HiFox, it loads the web URL served here (set
`OPENFOX_DESKTOP_WEB_URL` or `web_url` in the desktop settings file). Build the
assets with `pnpm --filter @openfox/web build` and publish `apps/web/dist`.

## Desktop updates

同仓 `develop → master` 的发布 PR 运行静态部署测试，以及中心镜像的迁移和服务健康检查。其他 PR 还会并行运行 Go 回归和前端回归（含真实 API 浏览器测试）。每次推送到 `master`，发布工作流都会在最终提交上执行完整回归，随后验证实际打包的安装器和中心镜像。

Every push to `master`, including a merged pull request, queues a release of the
push's head commit. GitHub Actions takes the major and minor from
`apps/desktop/package.json` and uses this workflow's run number for the patch.
The runner sets that version in its checkout for the app, CLI and installers.
Reruns retain their version. After tests pass, Actions creates the `vX.Y.Z`
tag on the exact commit it built and checks any existing tag before reusing it.

GitHub Actions builds the arm64 app, verifies the DMG contains the same app as
the PKG, signs the DMG and attaches both installers and the detached signature to a draft GitHub Release. After the
SHA-256 digests match, the workflow publishes the release and then deploys
the web workbench and the update feed used by older clients.
Rerun failed jobs on the original run to reuse its verified build artifacts.

Release builds check the latest stable GitHub Release in `openseek-x/openfox`.
Sparkle-enabled clients download the DMG and its `.dmg.sig` asset through the workspace
proxy. They check both GitHub digests and verify the DMG with the embedded Ed25519
public key. The DMG is limited to 512 MiB and the download to 20 minutes.
Confirming **Install and Restart** starts Sparkle and preserves the local service.
A Mac's application-management and first-launch policies still apply.

The build reads repository variable `OPENFOX_UPDATE_PUBLIC_KEY` (32-byte raw
Ed25519 public key, Base64). Only the DMG signing step reads repository secret
`OPENFOX_UPDATE_PRIVATE_KEY` (Ed25519, PKCS#8 PEM). Configure the pair before
releasing and keep the same key across releases. A mismatch stops publication.
For the initial switch, install a Sparkle-enabled release once using its DMG
or the existing PKG update flow. Each later release can update in place.

Configure repository variable `OPENFOX_PUBLIC_URL` with the HTTPS workbench
origin. The workflow also publishes the same PKG and `desktop/latest.json` on
that origin so existing 0.1.2 installations can receive the GitHub-enabled app.

The `desktop-production` GitHub environment permits the `master` branch. It contains
`OPENFOX_DEPLOY_KEY` (secret), `OPENFOX_DEPLOY_TARGET` (user@host) and
`OPENFOX_KNOWN_HOSTS` (pinned SSH host keys). The build job has no deployment key.
On the server, install `publish-static.py` in a root-owned location and restrict
this dedicated SSH public key to the following forced command, supplying the live
web root, shared release lock and public HTTPS origin for that environment.

```text
restrict,command="/usr/local/libexec/openfox-publish-static --web-root <web-root> --lock <lock> --origin <https-origin>" ssh-ed25519 <public-key>
```

The deployment job sends the GitHub artifact's short-lived download URL over
SSH. The server fetches parallel HTTPS ranges from Actions storage, verifies
the ZIP against the SHA-256 emitted by the upload action, then checks the bundle
and package. It keeps old hashed web assets and atomically
switches the web root's `current` link. A failed public check restores the prior
target. The job logs both targets. To roll back manually, take the same lock and
atomically restore the previous symlink. Users already on the new version need
a higher-version repair release or a manual reinstall. Database and service
changes use the separate center deployment procedure.

## Explicit migrations

The server never runs migrations during `serve`. Validate the manifest with:

```sh
openfox-server migrate --check
```

Apply pending forward migrations during an approved maintenance window with a
restricted URL file:

```sh
openfox-server migrate --apply --database-url-file /run/openfox/database.url
```

The command uses a PostgreSQL advisory lock and records checksummed files in
`openfox_meta.schema_migrations`. It refuses changed or out-of-order history;
it has no destructive down-migration mode.

## Shared computers are not a sandbox

A connected computer runs Agent work through the local `openfox` daemon. Each
run gets its own working directory, but **working-directory isolation is not a
security sandbox**: runs triggered by the people a computer is shared with
execute as the local operating-system user that runs the daemon and can read
any file that user can read (and write wherever that user can write).

Operational consequences:

- Sharing a computer with a space, with the whole organization, or adding it
  to a second organization extends that local user's file access to every
  member who can then trigger runs on it. The web and desktop apps ask the
  computer owner to acknowledge this before the sharing scope is widened.
- For shared computers, run the daemon under a dedicated operating-system
  account that owns only the repositories and credentials Agents are meant to
  use. Do not run it under a personal account that holds SSH keys, cloud
  credentials, browser profiles or unrelated source trees.
- The daemon's protected-directory list and per-run directories reduce
  accidents; they are not an access-control boundary. Use operating-system
  permissions, a separate machine, a VM or a container when members must be
  isolated from each other or from the host.
- Narrowing the sharing scope or revoking a grant stops new runs from those
  members; it does not undo what earlier runs already read or changed.

## Backup and recovery

Use `backup.sh` and `verify-backup.sh` for a database archive plus persistent
artifact-volume archive. Follow `restore-runbook.md` for isolated restore and
center-epoch reconciliation. Do not use an unverified database-only dump as a
complete recovery point.

Use `release-checklist.md` to record compatibility, maintenance-window, and
rollback gates for a candidate release.
