# Deployment pins

Records which backend commit is paired with which user-center release, so the
two repositories never drift silently. No hosts, ports, credentials or secrets
belong in this file.

| Component | Repository | Pin |
| --- | --- | --- |
| Backend source | `a285292107s/new-api` (this fork) | branch `feature/logdelta-20261102` @ `40a658af` (upstream base `ba5f7367`) |
| Backend image | `ghcr.io/a285292107s/new-api` | digest `sha256:12e7b5ede2533cb32b453fd492ceae31fa187d3110c475bf0a332e26298062e3` |
| User center | `a285292107s/newapi-user-center` | commit `6eb5779` (release `20261104-1505`) |

The image is built by `.github/workflows/custom-image.yml` from the commit above
(amd64, tag `sha-<full-commit>` and `logdelta`). Deploy by digest, not by tag:
`docker pull ghcr.io/a285292107s/new-api@sha256:…`.

## Deployment record

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
3. Update the table above in the same change that ships either side.

## Rollback

- Backend: redeploy the previous image tag/digest. Last known-good pre-group-registry
  digest: `sha256:a59c96efb51826aec1dbc62367ff9cd678e7ddbae24af997e2556f86104a89ff`
  (the group registry only adds a `groups` table via `AutoMigrate`; an older image
  simply ignores it). The official `calciumion/new-api:v1.0.0-rc.47` image also
  works — the frontend degrades gracefully to whole-page polling.
- Frontend: repoint `current` at the previous release directory.
