# Modifications

This repository is a fork of [QuantumNous/new-api](https://github.com/QuantumNous/new-api)
(AGPL-3.0). It is **not** a rebranded redistribution: the project name, the
`LICENSE`, the `NOTICE` and the required UI attribution are preserved
unchanged. Only the changes listed below are ours, marked per AGPL-3.0 §7(c).

## Base

| | |
| --- | --- |
| Upstream | `QuantumNous/new-api` |
| Base commit | `ba5f7367` (2026-11-03; includes tag `v1.0.0-rc.47`) |
| Branch carrying the changes | `feature/logdelta-20261102` |
| Production build | `40a658af` (merge of the above base), image pinned in `deploy/PINS.md` |

## Changes

Commits `f6501ec3`, `3ed38268`, `bb290749` (`feat(group): 分组注册表`):

1. **Normalized group registry.** Historically a group existed only as a bare
   string scattered across option JSON and `varchar` columns. The new `groups`
   table (`model/group.go`) gives every group a stable primary key, with
   `ratio`, `topup_ratio`, `description`, `auto_enabled` and `sort`. It reserves
   the `auto` pseudo-group and the built-in `default` group, and rejects
   reserved/duplicate names.
2. **Rename is transactional.** Phase 1 keeps the legacy string projections
   authoritative for query paths (`users.group`, `tokens.group`,
   `channels.group`, option JSON, …) and mirrors settings into the registry;
   a rename updates the registry row and every projection in one transaction.
3. **Admin API** (`controller/group.go`): `GET /api/group/registry` returns the
   registry with ratio settings, `POST /api/group/rename` renames a group; both
   sit behind `AdminAuth`.
4. **Admin UI** (`web/src/…/group-registry-card.tsx`): a settings card that
   lists the registry and performs renames; its copy is added to the seven
   frontend locale files.
5. **Tests**: `model/group_test.go` covers validation, reserved names and
   rename consistency.

Commit `feat(log): 日志接口增量能力与查询优化`:

1. **`GET /api/log/self` accepts an optional `skip_total=1|true`.**
   When set, the full-range `COUNT(*)` is skipped and `data.total` is returned as
   `-1`. Pagination, ordering, filters and the user-facing field projection are
   unchanged, and callers that do not pass the parameter keep the previous
   behaviour. Rationale: a client that only asks "is there anything newer than
   the last row I have" needs one row, and paying for `COUNT(*)` on every poll is
   the dominant server-side cost of polling a log page.

2. **`GET /api/log/self/stat` filters by `user_id` instead of `username`.**
   `username` is mutable, so a renamed account silently drifted to a different
   history. Numeric semantics are unchanged: `quota` covers the requested range,
   `rpm`/`tpm` always cover the last 60 seconds, and only consume logs count.
   It deliberately stays two queries — a single conditional-aggregate variant was
   measured slower on PostgreSQL (the planner abandons the composite index and
   does a sequential scan).

3. **Composite indexes** on `logs (user_id, created_at)`,
   `logs (username, type, created_at)` and `quota_data (user_id, created_at)`.
   On an existing large table, create them with `CREATE INDEX CONCURRENTLY`
   before upgrading so `AutoMigrate` finds them already present and does not
   block writes.

4. **Tests**: `model/log_page_test.go` (new) and one added case in
   `controller/log_export_test.go`, covering the `skip_total` contract, the
   numerical parity of the new stat query with the previous one, and the
   presence of the indexes.

## Build

No new build flags or base-image changes are required; the upstream `Dockerfile`
builds this branch as-is:

```bash
docker build -t new-api:custom .
```

The embedded admin frontend keeps upstream as-is except for the group registry
card described above. Image publishing is ours: `.github/workflows/custom-image.yml`
builds and pushes `ghcr.io/a285292107s/new-api` for this branch, and production
deploys a pinned digest recorded in `deploy/PINS.md`.

## Keeping up with upstream

This fork is product-led: upstream is a source of patches, not a branch to track
blindly. The layered policy (protocol / security / control plane), the monthly
procedure, the acceptance gate and the sync ledger live in
`deploy/UPSTREAM_SYNC.md`.

Conflicts, if any, are confined to `model/log.go`, `model/usedata.go`,
`controller/log.go` and the files listed under the group registry above.

## Attribution

The upstream `LICENSE` and `NOTICE` are kept in place, including the AGPLv3 §7
additional terms: the notice "Frontend design and development by New API
contributors." and a visible link to <https://github.com/QuantumNous/new-api>
are preserved in the user interface.
