# Deployment pins

> **本文件不再手工维护版本号。**
>
> 线上实际在跑的版本记录在运维仓库的 `work/versions.json` 里，由
> `work/verify-pins.ps1` 对着服务器实测校验（容器镜像 digest、镜像
> `org.opencontainers.image.revision` label、前端 `current` 软链、线上
> `RELEASE.json` 的 commit）。任何一项与 `versions.json` 不符即 exit 1。
>
> **为什么改掉了手写表格**：从 2026-11-05 到 11-06，下面这张表在没人注意的情况下
> 落后了 7 个前端 release 和 8 个后端提交——因为它只是给人看的注释，没有程序读它。
> 一个不会被读取的版本记录，迟早会变成假信息。
>
> 因此本文件只记录**怎么部署**，不记录**正在部署什么**。本文件不含主机、端口、
> 凭据或任何秘密，可以公开。

## How the image is built and deployed

The image is built by `.github/workflows/custom-image.yml` from a commit on
`feature/logdelta-20261102` (amd64, tags `sha-<full-commit>` and `logdelta`).

Deploy by digest, not by tag:

```bash
docker pull ghcr.io/a285292107s/new-api@sha256:<digest>
```

`logdelta` is a movable tag and is **not** a safe deployment reference. Only the
digest identifies a specific image.

After deploying, the operator records the digest in the ops repo's
`versions.json` (automated by `work/deploy-backend.ps1`) and appends a line to
`out/releases.jsonl`. Do not record it here.


## Frontend direction (decided 2026-11-04)

- The user center stays on `loli.mydeepseek.top`. It is published as a static
  SPA from the sibling repo `a285292107s/newapi-user-center` via `releases/<ts>/`
  plus the `current` symlink; a frontend release does not touch the backend,
  the database or the API host.
- **No single-domain migration and no `admin.mydeepseek.top` subdomain.** The
  earlier plan (collapse to one domain + separate admin host, recorded in
  `CADDY_SINGLE_DOMAIN.md`) is retired. The verified live Caddy topology is
  kept in `deploy/Caddyfile.example` instead.
- Admin-only features live inside the user center, gated by `role >= 10`
  (`RequireAuth minRole`), rather than on a separate host. First shipped:
  `/admin/users` in release `20261104-1505`.
- Long-term goal: iterate on the user center until it **functionally replaces
  the upstream embedded frontend**. During the transition both frontends share
  one backend, one set of accounts and one quota ledger; hostnames are not part
  of that goal.

## Deployment record

> 以下是 2026-11-05 及之前的历史记录，保留作为事故追溯材料。
> **2026-11-06 起的部署记录不再写在这里**，改记在运维仓库的 `out/releases.jsonl`
> （后端由 `deploy-backend.ps1`、前端由 `publish-frontend.ps1 -Production` 自动追加）。

- `2026-11-05 14:13` (Asia/Shanghai): backend image rebuilt from `07e10ec0` and
  deployed by digest `sha256:1e73a66f…`. `/api/log/self/stat` now also returns
  `requests` / `prompt_tokens` / `completion_tokens` / `total_tokens` for the
  query range (computed in the existing indexed `logs` aggregate; `rpm`/`tpm`
  remain the last-60s values). `health=healthy`; `/api/status`, `/api/pricing`,
  `/` returned 200; three composite indexes present. Pre-deploy backups:
  database `newapi-pre-ghcr-20261105-141326.sql.gz`, compose
  `docker-compose.yml.bak-ghcr-20261105-141326`. Previous digest
  `sha256:12e7b5ed…` kept for rollback. Verified end-to-end on a local
  Postgres-seeded container: endpoint returned
  `{quota:500,requests:5,prompt_tokens:15000,completion_tokens:1500,total_tokens:16500}`.
- `2026-11-04 15:05` (Asia/Shanghai): user-center release `20261104-1505`
  deployed (commit `6eb5779`, `assets/index-BJwjqyrT.js` +
  `assets/index-0QGDq6D5.css`). Adds the admin-only `/admin/users` page
  (list/search/group filter/quota adjust) gated by `role >= 10`; the previous
  release `20261103-1426` is kept for rollback. Verified `/`, `/admin/users`,
  assets, `/api/status` at 200 and admin APIs at 401 without a token.
- `2026-11-03 22:06` (Asia/Shanghai): deployed the digest above to production.
  `health=healthy`; `/api/status`, `/api/pricing`, `/` returned 200; the new
  `groups` table was seeded with the existing groups. Pre-deploy backups on the
  server: database dump `newapi-pre-ghcr-20261103-220626.sql.gz` and compose
  `docker-compose.yml.bak-ghcr-20261103-220626`.
- Before deployment, the same image was validated on a staging container against
  a read-only copy of the production database (registry read, atomic rename,
  log endpoints).

## Source policy (since 2026-11-03)

- **The fork is the authoritative source.** All backend changes happen in
  `a285292107s/new-api`, never against upstream directly.
- `main` only mirrors upstream (`git push origin upstream/main:main`) and carries
  no custom commits.
- `feature/logdelta-20261102` is the production branch: it carries every custom
  change (log `skip_total` patch, group registry) on top of the upstream base
  recorded above.
- Upstream updates are merged deliberately: rebase/merge `upstream/main`, then
  run the full regression (`go build ./...`, `go vet ./model/ ./controller/`,
  `go test ./model/`) before an image is cut.

## Why a pair

The user-center log page polls with
`GET /api/log/self?p=1&page_size=1&skip_total=1` (a one-row probe over the last
30 minutes) and only refetches the full page when the newest request id changes.

- **Backward compatible either way**: an older backend ignores `skip_total` and
  still runs `COUNT(*)`, so the frontend keeps working against an unpatched
  backend — it just loses the cost benefit.
- **The benefit needs both**: the probe only avoids `COUNT(*)` when the backend
  implements `skip_total`.

## Release order

1. Backend first: deploy the image, wait for `healthy`, check `/api/status`.
2. Frontend second: publish the static bundle atomically
   (`releases/<ts>/` + `current` symlink).
3. After each side ships, the ops repo's `versions.json` must be updated
   (automated by `work/deploy-backend.ps1` and `work/publish-frontend.ps1`), and
   `work/verify-pins.ps1` must pass. Do not hand-edit anything here.

## Rollback

- Backend: redeploy the previous image tag/digest. Last known-good pre-group-registry
  digest: `sha256:a59c96efb51826aec1dbc62367ff9cd678e7ddbae24af997e2556f86104a89ff`
  (the group registry only adds a `groups` table via `AutoMigrate`; an older image
  simply ignores it). The official `calciumion/new-api:v1.0.0-rc.47` image also
  works — the frontend degrades gracefully to whole-page polling.
- Frontend: repoint `current` at the previous release directory.
